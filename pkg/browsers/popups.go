package browsers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// maxPagesPerBrowser caps concurrent live pages (tabs, popups included) in one
// Chromium process. A new create fails with pageLimitError once the cap is
// reached; a popup that would exceed it is closed immediately.
const maxPagesPerBrowser = 20

// popupSettleBudget bounds how long WaitOpen waits for a fresh popup to leave
// about:blank before reporting its current URL.
const popupSettleBudget = 2 * time.Second

// popupSettlePoll is the interval between live URL reads while a popup settles.
const popupSettlePoll = 50 * time.Millisecond

// Popup identifies a page created by another page (a target="_blank" link or
// window.open) and is returned by page_act's wait_open action.
type Popup struct {
	// SessionID is the page id minted for the new tab.
	SessionID PageID `json:"session_id"`
	// URL is the new tab's live URL, or its creation URL while it is still
	// about:blank.
	URL string `json:"url"`
	// Opener is the page id that opened it.
	Opener PageID `json:"opener"`
}

// popupTracker records the targets a root browser wrapped and broadcasts each
// newly registered popup to waiters. All fields are guarded by the root
// browser's mutex.
type popupTracker struct {
	// targets maps a CDP target id to the page that wraps it, for normal pages
	// and popups alike, so an opener target can be resolved to its page id.
	targets map[proto.TargetTargetID]*Page
	// notify is closed and replaced whenever a popup registers, waking every
	// wait_open blocked on the previous channel.
	notify chan struct{}
	// stop cancels the browser-level target subscription.
	stop context.CancelFunc
	// capErr is the last page-cap refusal, reported by WaitOpen when a popup
	// could not be registered.
	capErr error
	// closed reports that the browser was torn down, so WaitOpen returns at
	// once instead of blocking until its timeout.
	closed bool
}

// startTargetTracking subscribes the root browser to Target.targetCreated so
// popups opened by any of its pages are wrapped and registered. It is a no-op
// on a clone or an already subscribed browser.
func (b *Browser) startTargetTracking() {
	if b.parent != nil {
		return
	}
	b.mu.Lock()
	if b.popups.stop != nil || b.rod == nil {
		b.mu.Unlock()
		return
	}
	if b.popups.targets == nil {
		b.popups.targets = make(map[proto.TargetTargetID]*Page)
	}
	b.popups.closed = false
	rb := b.rod
	ctx, cancel := context.WithCancel(b.baseCtx)
	b.popups.stop = cancel
	b.mu.Unlock()

	go b.watchTargets(ctx, rb)
}

// watchTargets reads the browser-wide event stream and registers every new
// page target that has an opener. Frames and opener-less tabs (the ones NewPage
// creates itself) are ignored.
func (b *Browser) watchTargets(ctx context.Context, rb *rod.Browser) {
	events := rb.Context(ctx).Event()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-events:
			if !ok {
				return
			}
			var created proto.TargetTargetCreated
			if !msg.Load(&created) || created.TargetInfo == nil {
				continue
			}
			if created.TargetInfo.Type != proto.TargetTargetInfoTypePage {
				continue
			}
			if created.TargetInfo.OpenerID == "" {
				continue
			}
			b.registerPopup(rb, created.TargetInfo)
		}
	}
}

// registerPopup wraps a created target as a page, registers it under a fresh
// page id, and wakes WaitOpen. A target that would exceed the per-browser cap
// is closed and the cap error is recorded for the waiter.
func (b *Browser) registerPopup(rb *rod.Browser, info *proto.TargetTargetInfo) {
	root := b.root()

	root.mu.Lock()
	if root.popups.targets == nil {
		root.mu.Unlock()
		return
	}
	if _, dup := root.popups.targets[info.TargetID]; dup {
		root.mu.Unlock()
		return
	}
	if err := root.pageLimitLocked(); err != nil {
		root.recordPopupCapLocked(err)
		root.mu.Unlock()
		closeTarget(rb, info.TargetID)
		return
	}
	root.mu.Unlock()

	rodPage, err := rb.PageFromTarget(info.TargetID)
	if err != nil {
		return
	}

	p := &Page{ID: root.mintPageID(), Rod: rodPage, URL: info.URL, browser: root}
	p.startObservation()
	p.startDialogs()

	registered, err := root.registerPage(info, p)
	if err != nil {
		root.recordPopupCapError(err)
	}
	if !registered {
		root.discardPage(p)
	}
}

// registerPage inserts an already-wrapped popup into the browser that owns its
// incognito context and records its target id. It reports false when the target
// is already registered or the browser is full.
func (b *Browser) registerPage(info *proto.TargetTargetInfo, p *Page) (bool, error) {
	root := b.root()

	root.mu.Lock()
	if root.popups.targets == nil {
		root.popups.targets = make(map[proto.TargetTargetID]*Page)
	}
	if _, dup := root.popups.targets[info.TargetID]; dup {
		root.mu.Unlock()
		return false, nil
	}
	if err := root.pageLimitLocked(); err != nil {
		root.mu.Unlock()
		return false, err
	}
	if opener := root.popups.targets[info.OpenerID]; opener != nil && p.Opener == "" {
		p.Opener = opener.ID
	}
	owner := root.ownerForContextLocked(info.BrowserContextID)
	if owner == nil {
		owner = root
	}
	if owner != root {
		owner.mu.Lock()
	}
	owner.pages[p.ID] = p
	if owner != root {
		owner.mu.Unlock()
	}
	root.popups.targets[info.TargetID] = p
	root.popups.capErr = nil
	root.wakePopupsLocked()
	root.mu.Unlock()
	root.touch()
	return true, nil
}

// recordPopupCapLocked stores the cap refusal and wakes the waiters. The caller
// must hold the root browser mutex.
func (b *Browser) recordPopupCapLocked(err error) {
	b.popups.capErr = err
	b.wakePopupsLocked()
}

// recordPopupCapError stores the cap refusal and wakes the waiters.
func (b *Browser) recordPopupCapError(err error) {
	root := b.root()
	root.mu.Lock()
	root.recordPopupCapLocked(err)
	root.mu.Unlock()
}

// trackTarget records an already-registered page under its CDP target id so a
// later popup can resolve its opener.
func (b *Browser) trackTarget(p *Page, targetID proto.TargetTargetID) {
	root := b.root()
	root.mu.Lock()
	if root.popups.targets != nil {
		root.popups.targets[targetID] = p
	}
	root.mu.Unlock()
}

// forgetTarget drops every target mapping that points at p.
func (b *Browser) forgetTarget(p *Page) {
	root := b.root()
	root.mu.Lock()
	for targetID, tracked := range root.popups.targets {
		if tracked == p {
			delete(root.popups.targets, targetID)
		}
	}
	root.mu.Unlock()
}

// stopTargetTrackingLocked marks the tracker closed so waiters return, drops the
// target map and clears the stop hook. The caller must hold the browser mutex
// and run the captured stop hook outside it.
func (b *Browser) stopTargetTrackingLocked() {
	b.popups.closed = true
	b.popups.stop = nil
	b.popups.targets = nil
	b.wakePopupsLocked()
}

// wakePopupsLocked closes the current notify channel so every blocked WaitOpen
// re-scans. The caller must hold the browser mutex.
func (b *Browser) wakePopupsLocked() {
	if b.popups.notify != nil {
		close(b.popups.notify)
		b.popups.notify = nil
	}
}

// popupNotifyLocked returns the channel the next WaitOpen sleeps on, creating
// it lazily. The caller must hold the browser mutex.
func (b *Browser) popupNotifyLocked() chan struct{} {
	if b.popups.notify == nil {
		b.popups.notify = make(chan struct{})
	}
	return b.popups.notify
}

// claimPopupLocked returns the first unclaimed popup opened by opener and marks
// it claimed. The caller must hold the root browser mutex.
func (b *Browser) claimPopupLocked(opener PageID) *Page {
	if p := claimPopupIn(b.pages, opener); p != nil {
		return p
	}
	for _, child := range b.identities {
		child.mu.Lock()
		p := claimPopupIn(child.pages, opener)
		child.mu.Unlock()
		if p != nil {
			return p
		}
	}
	return nil
}

// claimPopupIn returns one unclaimed popup of opener from pages and marks it
// claimed. The caller must hold the map owner's mutex.
func claimPopupIn(pages map[PageID]*Page, opener PageID) *Page {
	for _, p := range pages {
		if p.Opener == opener && !p.popupClaimed {
			p.popupClaimed = true
			return p
		}
	}
	return nil
}

// pageCountLocked returns the live page count across the root and its
// identities. The caller must hold the root browser mutex.
func (b *Browser) pageCountLocked() int {
	count := len(b.pages)
	for _, child := range b.identities {
		child.mu.Lock()
		count += len(child.pages)
		child.mu.Unlock()
	}
	return count
}

// pageLimitLocked reports the cap error when the browser is full. The caller
// must hold the root browser mutex.
func (b *Browser) pageLimitLocked() error {
	count := b.pageCountLocked()
	if count >= maxPagesPerBrowser {
		return pageLimitError(b.id, count)
	}
	return nil
}

// checkPageCap reports the page-cap error when a create should be refused.
func (b *Browser) checkPageCap() error {
	root := b.root()
	root.mu.Lock()
	defer root.mu.Unlock()
	return root.pageLimitLocked()
}

// PageLimitError reports that a browser already holds its maximum number of
// live pages. It is a distinct type so the result envelope classifies it as
// invalid_args instead of the generic page_closed branch.
type PageLimitError struct {
	// Browser is the browser whose cap was reached.
	Browser BrowserID
	// Count is the live page count at the refusal.
	Count int
	// Limit is the configured per-browser page cap.
	Limit int
}

// Error renders the pinned cap sentence: it names the browser, the live page
// count and the limit, and says how to free a slot with session_close.
func (e *PageLimitError) Error() string {
	return fmt.Sprintf(
		"browsers: page limit reached — browser %s already has %d live pages (max %d); close one with session_close before opening another",
		e.Browser,
		e.Count,
		e.Limit,
	)
}

// pageLimitError builds the typed cap error for a browser at the limit.
func pageLimitError(id BrowserID, count int) error {
	return &PageLimitError{Browser: id, Count: count, Limit: maxPagesPerBrowser}
}

// popupTree returns id and every popup opened by it, recursively, in pre-order.
func (b *Browser) popupTree(id PageID) []PageID {
	root := b.root()
	root.mu.Lock()
	defer root.mu.Unlock()

	seen := make(map[PageID]bool)
	var out []PageID
	var walk func(PageID)
	walk = func(cur PageID) {
		if seen[cur] {
			return
		}
		seen[cur] = true
		out = append(out, cur)

		var kids []PageID
		for _, p := range root.pages {
			if p.Opener == cur {
				kids = append(kids, p.ID)
			}
		}
		for _, child := range root.identities {
			child.mu.Lock()
			for _, p := range child.pages {
				if p.Opener == cur {
					kids = append(kids, p.ID)
				}
			}
			child.mu.Unlock()
		}
		for _, kid := range kids {
			walk(kid)
		}
	}
	walk(id)
	return out
}

// withPopupTree expands ids with the popups they opened, so a sweep that closes
// an idle opener also closes its orphaned targets.
func (b *Browser) withPopupTree(ids []PageID) []PageID {
	seen := make(map[PageID]bool, len(ids))
	out := make([]PageID, 0, len(ids))
	for _, id := range ids {
		for _, pid := range b.popupTree(id) {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

// ownerForContextLocked returns the browser whose incognito context matches
// ctxID, or nil when no live identity owns it. The caller must hold the root
// browser mutex.
func (b *Browser) ownerForContextLocked(ctxID proto.BrowserBrowserContextID) *Browser {
	if b.rod != nil && b.rod.BrowserContextID == ctxID {
		return b
	}
	for _, child := range b.identities {
		child.mu.Lock()
		match := child.rod != nil && child.rod.BrowserContextID == ctxID
		child.mu.Unlock()
		if match {
			return child
		}
	}
	return nil
}

// closeTarget closes a just-created target that was refused registration.
func closeTarget(rb *rod.Browser, id proto.TargetTargetID) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	_, _ = proto.TargetCloseTarget{TargetID: id}.Call(rb.Context(ctx))
}

// discardPage releases a wrapped page that never reached the registry.
func (b *Browser) discardPage(p *Page) {
	p.stopDialogs()
	p.stopObservation()
	if p.Rod == nil {
		return
	}
	ctx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
	defer cancel()
	_ = closeRodPage(ctx, p.Rod)
}

// WaitOpen blocks until opener opens a page (a target="_blank" link or
// window.open) and returns its id and live URL. A popup registered before the
// call is returned immediately; each popup is reported once.
func (b *Browser) WaitOpen(ctx context.Context, opener PageID) (Popup, error) {
	root := b.root()
	for {
		root.mu.Lock()
		if root.popups.closed {
			root.mu.Unlock()
			return Popup{}, fmt.Errorf("browsers: wait_open: browser %s is closed", root.id)
		}
		if p := root.claimPopupLocked(opener); p != nil {
			root.mu.Unlock()
			return Popup{
				SessionID: p.ID,
				URL:       root.waitPopupURL(ctx, p),
				Opener:    p.Opener,
			}, nil
		}
		notify := root.popupNotifyLocked()
		root.mu.Unlock()

		select {
		case <-ctx.Done():
			root.mu.Lock()
			capErr := root.popups.capErr
			root.mu.Unlock()
			if capErr != nil {
				return Popup{}, capErr
			}
			return Popup{}, fmt.Errorf(
				"browsers: wait_open: no new page opened by %s within the timeout — click the target=\"_blank\" link or call window.open, then retry",
				opener,
			)
		case <-notify:
			root.mu.Lock()
			capErr := root.popups.capErr
			root.mu.Unlock()
			if capErr != nil {
				return Popup{}, capErr
			}
		}
	}
}

// WaitOpen resolves the opener page through the manager's qualifier-aware
// lookup, then blocks for a page it opens.
func (m *Manager) WaitOpen(
	ctx context.Context,
	opener PageID,
	browserID BrowserID,
	owner string,
) (Popup, error) {
	b, err := m.resolvePage(opener, browserID, owner)
	if err != nil {
		return Popup{}, err
	}
	return b.WaitOpen(ctx, opener)
}

// waitPopupURL reads the popup's live URL, giving it a short budget to leave
// about:blank after creation.
func (b *Browser) waitPopupURL(ctx context.Context, p *Page) string {
	if p.Rod == nil {
		return p.URL
	}
	deadline := time.Now().Add(popupSettleBudget)
	for {
		if url, ok := livePageURL(ctx, p); ok && url != "" && url != "about:blank" {
			return url
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return p.URL
		}
		select {
		case <-ctx.Done():
			return p.URL
		case <-time.After(popupSettlePoll):
		}
	}
}

// livePageURL reads a page's current URL without waiting on its operation lock.
func livePageURL(ctx context.Context, p *Page) (string, bool) {
	unlock, ok := p.TryLock()
	if !ok {
		return "", false
	}
	defer unlock()
	if p.Rod == nil {
		return "", false
	}
	url, _, err := p.readInfo(ctx)
	if err != nil {
		return "", false
	}
	return url, true
}
