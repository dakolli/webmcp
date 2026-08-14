package browsers

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// Browser owns a single live Chromium instance (a *rod.Browser) and the map of
// its live Pages (browser tabs, a.k.a. MCP sessions).
//
// Thread safety: the pages map is guarded by the Browser mutex; each Page
// carries its own mutex because rod Pages are not thread-safe (see session.go).
type Browser struct {
	// mu guards the pages map and the rod browser pointer.
	mu sync.Mutex
	// rod is the live go-rod browser handle; nil until Launch succeeds.
	rod *rod.Browser
	// pages maps page IDs to their live pages.
	pages map[PageID]*Page
	// nextID is the counter used to mint fresh PageIDs in NewPage.
	nextID int
	// headless selects headless Chromium at launch time (default: headful).
	headless bool
	// stealth applies go-rod/stealth to every new page (default: on).
	stealth bool
}

// NewBrowser constructs a Browser ready to be launched. headless selects
// headless Chromium (headful is the default); stealth enables go-rod/stealth
// on every new page. No browser process is started until Launch.
func NewBrowser(headless, stealth bool) *Browser {
	return &Browser{
		pages:    make(map[PageID]*Page),
		headless: headless,
		stealth:  stealth,
	}
}

// Launch connects the Browser to a Chromium instance via the go-rod launcher.
// Chromium runs headful by default (`.Headless(false)`) unless the Browser was
// constructed with headless=true; NoSandbox is always set, as required in
// containers. Launch is idempotent: it is a no-op once the browser is up.
func (b *Browser) Launch(ctx context.Context) error {
	b.mu.Lock()
	if b.rod != nil {
		b.mu.Unlock()
		return nil // idempotent: already launched
	}
	b.mu.Unlock()

	// Bail out early when the caller's context is already done; the launcher
	// and connect steps below are local but can still block (e.g. first run
	// downloading Chromium), so honor ctx between steps.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browsers: launch browser: %w", err)
	}

	l := launcher.New().NoSandbox(true).Headless(b.headless) // headful by default
	u, err := l.Launch()                                     // error form; no Must*
	if err != nil {
		return fmt.Errorf("browsers: launch chromium: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browsers: launch browser: %w", err)
	}

	rb := rod.New().ControlURL(u)
	if err := rb.Connect(); err != nil {
		return fmt.Errorf("browsers: connect: %w", err)
	}

	b.mu.Lock()
	b.rod = rb
	b.mu.Unlock()
	return nil
}

// NewPage opens a new tab in the Browser, navigates it to url (when non-empty)
// and registers it in the pages map under a fresh PageID. Stealth is applied
// to every new page by default and skipped only when the Browser was
// constructed with stealth=false.
func (b *Browser) NewPage(ctx context.Context, url string) (*Page, error) {
	b.mu.Lock()
	if b.rod == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("browsers: browser not launched")
	}
	b.mu.Unlock()

	var p *rod.Page
	var err error
	if b.stealth {
		p, err = stealth.Page(b.rod) // stealth on by default (error form)
	} else {
		p, err = b.rod.Page(proto.TargetCreateTarget{})
	}
	if err != nil {
		return nil, fmt.Errorf("browsers: new page: %w", err)
	}

	if url != "" {
		// Bound to ctx so a hung navigation cannot outlive the tool call.
		pc := p.Context(ctx)
		if err := pc.Navigate(url); err != nil {
			_ = p.Close() // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: navigate: %w", err)
		}
		if err := pc.WaitLoad(); err != nil {
			_ = p.Close() // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: wait load: %w", err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	page := &Page{
		ID:         PageID(fmt.Sprintf("page-%d", b.nextID)),
		URL:        url,
		Rod:        p,
		lastAccess: time.Now(),
	}
	b.pages[page.ID] = page
	return page, nil
}

// Page returns the live page with the given ID, or an error when no page with
// that ID is open (e.g. it was closed or swept). Every successful lookup
// verifies the tab is still alive (a single crashed tab keeps its map entry —
// the rod handle object survives — while its CDP target is gone) and refreshes
// the page's last-access time (under the same browser mutex the sweeper
// iterates under), so the manager's TTL sweeper never closes a page that is in
// use.
//
// The liveness probe is a network call (Target.getTargetInfo via p.Rod.Info),
// so it runs outside b.mu and only under the per-page mutex, bound to a short
// timeout: a dead websocket fails the probe instead of blocking. On probe
// failure the dead entry is evicted race-safely (WEB-39 pattern): the handle
// is captured and nilled under the locks and closed strictly afterwards, so a
// concurrent sweepOnce or Page lookup never double-closes or panics.
func (b *Browser) Page(ctx context.Context, id PageID) (*Page, error) {
	b.mu.Lock()
	p, ok := b.pages[id]
	if ok {
		p.touch()
	}
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("browsers: page %q not found", id)
	}

	if err := probePage(p); err != nil {
		// The tab is dead: evict it (WEB-39 TOCTOU-safe capture pattern).
		var handle *rod.Page
		b.mu.Lock()
		if cur, ok := b.pages[id]; ok && cur == p {
			p.mu.Lock()
			handle = p.Rod
			p.Rod = nil
			delete(b.pages, id)
			p.mu.Unlock()
		}
		b.mu.Unlock()
		if handle != nil {
			// Close the captured handle outside the locks, via closeRodPage
			// (the sweeper's path): it honors a bound close ctx and converts a
			// concurrent-close panic into an error — an in-flight tool call on
			// the same tab (goroutine-per-request MCP callers) can race this
			// close. Best-effort: the target is already gone.
			closeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			_ = closeRodPage(closeCtx, handle)
		}
		return nil, fmt.Errorf("browsers: page %q not found", id)
	}
	p.touch()
	return p, nil
}

// probePage verifies the page's CDP target is still alive with an Info() round
// trip, the call that surfaces -32602 "No target with given id found" when the
// tab crashed while its rod handle survived. It is a network call, so it must
// never run under b.mu (or m.mu) — only under the page's own mutex, consistent
// with the tools holding p.Lock() during their rod calls — and it is bound to
// a short timeout so a dead websocket fails the probe instead of blocking.
//
// The probe is deliberately NOT derived from the caller's ctx (mirroring
// probeBrowser): rod cannot distinguish a caller-cancelled ctx from a dead
// websocket, so a caller whose ctx expired would wrongly evict a healthy tab.
// The probe always runs on a fixed 2s timeout off context.Background.
func probePage(p *Page) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("browsers: page rod handle is nil")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	_, err := p.Rod.Context(probeCtx).Info()
	return err
}

// WindowState is the desired OS window state for Browser.Focus. The engine
// owns the vocabulary and maps it to proto.BrowserWindowState internally, so
// the IPC/CLI layers never import rod.
type WindowState string

const (
	// WindowStateNormal restores a minimized window and re-asserts its bounds
	// as the standard best-effort raise. This is the default.
	WindowStateNormal WindowState = "normal"
	// WindowStateMaximized maximizes the browser window (a stronger raise on
	// window managers that re-layout the window on top).
	WindowStateMaximized WindowState = "maximized"
	// WindowStateFullscreen makes the browser window fullscreen.
	WindowStateFullscreen WindowState = "fullscreen"
)

// Focus raises the Browser's OS window to the foreground via pure CDP (no
// wmctrl/xdotool): it resolves the OS window id of the window containing any
// live page's devtools target (all pages live in the same window) with
// proto.BrowserGetWindowForTarget, then applies the requested window state
// with proto.BrowserSetWindowBounds. The default state (normal) restores a
// minimized window and re-asserts its current bounds — the standard
// best-effort raise; maximize/fullscreen are state-only, because the proto doc
// forbids combining those states with left/top/width/height.
//
// Focus is NOT a launch trigger and never closes the browser: with no browser
// launched it fails (mirroring the NewPage guard), and with no live page it
// fails — the --no-startup-window case, where Chromium may have no window
// until a page opens.
func (b *Browser) Focus(ctx context.Context, state WindowState) error {
	// Fail fast on a bogus state before any network round trip (the IPC/CLI
	// layers validate too; this guards direct engine callers).
	switch state {
	case WindowStateNormal, WindowStateMaximized, WindowStateFullscreen:
	default:
		return fmt.Errorf("browsers: focus: unknown window state %q (must be one of normal, maximized, fullscreen)", state)
	}

	b.mu.Lock()
	rb := b.rod
	if rb == nil {
		b.mu.Unlock()
		return fmt.Errorf("browsers: focus: browser not launched")
	}
	// Collect EVERY live page's target id under the locks: map iteration is
	// unordered, and a crashed tab whose rod handle survived (the WEB-47 case)
	// would fail BrowserGetWindowForTarget with "No target with given id
	// found". The candidates are tried in order below until one resolves the
	// OS window, so a single dead tab cannot block the focus.
	var pageIDs []proto.TargetTargetID
	for _, p := range b.pages {
		p.mu.Lock()
		if p.Rod != nil {
			pageIDs = append(pageIDs, p.Rod.TargetID) // immutable after page creation
		}
		p.mu.Unlock()
	}
	b.mu.Unlock()
	if len(pageIDs) == 0 {
		return fmt.Errorf("browsers: focus: no pages open; open a page first")
	}

	// Bound to the caller's ctx (rod's ctx-aware form; no Must*). The first
	// candidate that resolves the OS window wins; a dead target's failure is
	// skipped so a healthy tab still resolves the window (all pages share the
	// same browser window).
	var res *proto.BrowserGetWindowForTargetResult
	var lastErr error
	for _, pageID := range pageIDs {
		r, err := proto.BrowserGetWindowForTarget{TargetID: pageID}.Call(rb.Context(ctx))
		if err != nil {
			lastErr = err
			continue
		}
		res = r
		break
	}
	if res == nil {
		return fmt.Errorf("browsers: focus: no live page window: %w", lastErr)
	}

	switch state {
	case WindowStateNormal:
		// Re-asserting the returned bounds (position/size echoed from the get
		// result) is the standard best-effort raise. Bounds fields may be nil
		// (e.g. minimized); nil fields are left unchanged per the proto doc.
		bounds := &proto.BrowserBounds{WindowState: proto.BrowserWindowStateNormal}
		if res.Bounds != nil {
			bounds.Left = res.Bounds.Left
			bounds.Top = res.Bounds.Top
			bounds.Width = res.Bounds.Width
			bounds.Height = res.Bounds.Height
		}
		if err := (proto.BrowserSetWindowBounds{WindowID: res.WindowID, Bounds: bounds}).Call(rb.Context(ctx)); err != nil {
			return fmt.Errorf("browsers: focus: set window bounds: %w", err)
		}
		return nil
	case WindowStateMaximized:
		// State-only: the proto doc forbids combining maximized with
		// left/top/width/height.
		if err := (proto.BrowserSetWindowBounds{WindowID: res.WindowID, Bounds: &proto.BrowserBounds{WindowState: proto.BrowserWindowStateMaximized}}).Call(rb.Context(ctx)); err != nil {
			return fmt.Errorf("browsers: focus: set window state: %w", err)
		}
		return nil
	case WindowStateFullscreen:
		// State-only, same proto constraint as maximized.
		if err := (proto.BrowserSetWindowBounds{WindowID: res.WindowID, Bounds: &proto.BrowserBounds{WindowState: proto.BrowserWindowStateFullscreen}}).Call(rb.Context(ctx)); err != nil {
			return fmt.Errorf("browsers: focus: set window state: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("browsers: focus: unknown window state %q (must be one of normal, maximized, fullscreen)", state)
	}
}

// Pages returns a read-only snapshot of the Browser's live pages (id + url),
// safe to marshal and iterate; it never exposes the live *rod.Page handles.
func (b *Browser) Pages() []PageInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	infos := make([]PageInfo, 0, len(b.pages))
	for _, p := range b.pages {
		infos = append(infos, PageInfo{ID: p.ID, URL: p.URL})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

// ClosePage closes the page with the given ID, removing it from the pages map
// and releasing its browser tab.
func (b *Browser) ClosePage(ctx context.Context, id PageID) error {
	b.mu.Lock()
	p, ok := b.pages[id]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("browsers: page %q not found", id)
	}

	if err := p.Close(ctx); err != nil {
		return fmt.Errorf("browsers: close page %q: %w", id, err)
	}

	b.mu.Lock()
	delete(b.pages, id)
	b.mu.Unlock()
	return nil
}

// Close closes the Browser and all of its live pages, releasing the underlying
// Chromium process. After Close the Browser must not be reused.
func (b *Browser) Close(ctx context.Context) error {
	b.mu.Lock()
	for _, p := range b.pages {
		_ = p.Close(ctx) // best-effort per-page close
	}
	b.pages = make(map[PageID]*Page)
	rb := b.rod
	b.rod = nil
	b.mu.Unlock()

	if rb == nil {
		return nil
	}
	return rb.Close() // error form; no Must*
}
