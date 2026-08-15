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
// Page creation is additionally serialized by createMu so the failed-create
// orphan cleanup (closeOrphanedTab) can identify the tabs it owns.
type Browser struct {
	// mu guards the pages map and the rod browser pointer.
	mu sync.Mutex
	// createMu serializes page creation (the Target.createTarget + orphan
	// cleanup window in createPage), so closeOrphanedTab never races a
	// concurrent NewPage. Taken after mu, never while holding it.
	createMu sync.Mutex
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
	// baseCtx is the manager-owned, process-lifetime context the rod browser's
	// default ctx is bound to (WEB-59): a transient tool-call ctx can never
	// poison it, so canceling a request aborts a call but never the browser
	// itself. Every engine rod call chains its own per-call ctx via
	// .Context(callCtx); derived timeouts (closes, orphan cleanup) hang off
	// this base so teardown outlives the request that triggered it.
	baseCtx context.Context
}

// NewBrowser constructs a Browser ready to be launched. baseCtx is the
// manager-owned, process-lifetime context the rod browser's default ctx will
// be bound to at Launch (never a transient tool-call ctx — see WEB-59);
// headless selects headless Chromium (headful is the default); stealth enables
// go-rod/stealth on every new page. No browser process is started until Launch.
func NewBrowser(baseCtx context.Context, headless, stealth bool) *Browser {
	return &Browser{
		pages:    make(map[PageID]*Page),
		headless: headless,
		stealth:  stealth,
		baseCtx:  baseCtx,
	}
}

// Launch connects the Browser to a Chromium instance via the go-rod launcher.
// Chromium runs headful by default (`.Headless(false)`) unless the Browser was
// constructed with headless=true; NoSandbox is always set, as required in
// containers. Launch is idempotent: it is a no-op once the browser is up.
//
// The rod browser's DEFAULT ctx is bound to the manager-owned baseCtx the
// Browser was constructed with — never to the caller's ctx — so a transient
// tool-call ctx cannot poison the browser (WEB-58: a poisoned default ctx
// leaked one ~100 MB renderer per fetch_page and made browser_close report
// "context canceled"). Every engine rod call afterwards chains its own
// per-call ctx via .Context(callCtx) instead. The launch-time operations
// (first-run Chromium download, early bail) stay bounded by the caller's ctx.
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

	// Pages launch PLAIN by default (per user decision 2026-08-14): no
	// window-size / device-scale / emulation launcher flags here. Screen-size
	// emulation is page-scoped and opt-in — a caller passes an explicit
	// viewport to NewPage, or applies a go-rod devices.X preset via
	// Page.Emulate after creation. A bare launch gets rod's own defaults.
	// The launcher is ctx-bound (l.Context) so a hung first-run Chromium
	// download honors the caller's deadline instead of running on a background
	// ctx.
	l := launcher.New().NoSandbox(true).Headless(b.headless).Context(ctx) // headful by default
	u, err := l.Launch()                                                  // error form; no Must*
	if err != nil {
		return fmt.Errorf("browsers: launch chromium: %w", err)
	}
	// The Chromium process is now live (WEB-60): if anything below fails — a
	// caller ctx that expired during launch, or a failed websocket connect —
	// kill it best-effort so a failed launch never orphans a background
	// browser process.
	connected := false
	defer func() {
		if !connected {
			l.Kill() // best-effort; kills the just-launched Chromium process
		}
	}()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browsers: launch browser: %w", err)
	}

	// The rod browser's default ctx is the manager-owned baseCtx, not the
	// caller's tool-call ctx: a canceled request must abort calls, never the
	// browser itself (WEB-59). The websocket dial inside Connect is a fast
	// localhost op; the launcher above is where real blocking (first-run
	// download) happens, and it stays caller-bounded.
	rb := rod.New().Context(b.baseCtx).ControlURL(u)
	if err := rb.Connect(); err != nil {
		return fmt.Errorf("browsers: connect: %w", err)
	}
	connected = true // Chromium is wired up; the launch is a success

	b.mu.Lock()
	b.rod = rb
	b.mu.Unlock()
	return nil
}

// NewPage opens a new tab in the Browser, navigates it to url (when non-empty)
// and registers it in the pages map under a fresh PageID. Stealth is applied
// to every new page by default and skipped only when the Browser was
// constructed with stealth=false. The page launches PLAIN — no screen-size
// override of any kind unless the caller explicitly passes one — and every
// optional override is applied BEFORE navigating, so it governs the load
// itself (no re-navigate needed), in this order:
//
//  1. viewport (when vp is non-nil): a MANUAL width × height override via
//     Page.SetViewport (Emulation.setDeviceMetricsOverride, ctx-bound, with
//     matching screen metrics); nil leaves rod's own defaults in place.
//  2. device (when device is non-empty): a go-rod device preset via
//     Page.Emulate — the primary, opt-in emulation mechanism (device UA +
//     metrics + touch). An unknown name fails with the pinned
//     "unknown device %q" error; a non-empty preset also makes the manual
//     viewport (1) moot for metrics, but both steps run in order.
//  3. userAgent (when userAgent is non-empty): an explicit User-Agent
//     override via rod's SetUserAgent, applied after any device preset so an
//     explicit UA wins over the preset's UA.
//
// Empty strings skip their step, so a plain call (nil viewport, empty device,
// empty userAgent) makes ZERO emulation calls on the default path. Any failure
// closes the orphaned tab (no leak) and wraps the error with %w: a create
// failure additionally triggers closeOrphanedTab, because rod's own
// leak-prevention defer only covers post-create failures (WEB-58/WEB-59).
func (b *Browser) NewPage(
	ctx context.Context,
	url string,
	vp *Viewport,
	device,
	userAgent string,
) (*Page, error) {
	b.mu.Lock()
	rb := b.rod
	b.mu.Unlock()
	if rb == nil {
		return nil, fmt.Errorf("browsers: browser not launched")
	}

	// Chain the per-call ctx explicitly: the rod browser's default ctx is the
	// manager-owned baseCtx (WEB-59), so a stale/poisoned default is never
	// inherited here, and the create-target CDP round trip is bounded by the
	// tool call.
	p, err := b.createPage(rb.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("browsers: new page: %w", err)
	}

	// Wrap the rod handle in the engine Page so every operation below goes
	// through the engine seams (Page.SetViewport owns the page lock, the
	// nil-Rod guard and the "browsers: set viewport" error wrap). The page is
	// not yet registered in the pages map, so no other goroutine can see it;
	// ID, URL and lastAccess are filled at registration below.
	page := &Page{Rod: p}

	// Apply a manual viewport override in the same pre-registration window as
	// Navigate: the fresh page is not yet in the pages map, so no other
	// goroutine can see it and no new lock is needed — Page.SetViewport takes
	// it. The call is ctx-bound so a hung CDP call cannot outlive the tool
	// call (the previous review flagged the un-bound SetViewport). The
	// override replaces both the emulated viewport and the screen metrics
	// (proto ScreenWidth/ScreenHeight are *int); nil = plain launch (rod
	// defaults, no manual screen-size override — device presets via
	// Page.Emulate are the opt-in path).
	if vp != nil {
		if err := page.SetViewport(ctx, vp); err != nil {
			b.closeUnregisteredPage(p) // do not leak the orphaned tab (mirror the navigate failure path)
			return nil, err
		}
	}

	// Apply a device preset pre-navigation via the engine seam (Page.Emulate
	// owns the page lock and the nil-Rod guard and resolves the name
	// case-insensitively, failing with the pinned "unknown device %q" error).
	// Same pre-registration window as the viewport branch above: the page is
	// not yet in the pages map, so no other goroutine can see it.
	if device != "" {
		if err := page.Emulate(ctx, device); err != nil {
			b.closeUnregisteredPage(p) // do not leak the orphaned tab
			return nil, err
		}
	}

	// Apply an explicit UA override pre-navigation via rod's own SetUserAgent
	// (the same helper Page.Emulate uses internally for the preset UA; an
	// explicit userAgent thus wins over a device preset's UA). ctx-bound so a
	// hung CDP call cannot outlive the tool call. In the pre-registration
	// window no lock is needed — no other goroutine can see the page yet.
	if userAgent != "" {
		if err := p.Context(ctx).SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: userAgent}); err != nil {
			b.closeUnregisteredPage(p) // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: set user agent: %w", err)
		}
	}

	if url != "" {
		// Bound to ctx so a hung navigation cannot outlive the tool call.
		pc := p.Context(ctx)
		if err := pc.Navigate(url); err != nil {
			b.closeUnregisteredPage(p) // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: navigate: %w", err)
		}
		if err := pc.WaitLoad(); err != nil {
			b.closeUnregisteredPage(p) // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: wait load: %w", err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	page.ID = PageID(fmt.Sprintf("page-%d", b.nextID))
	page.URL = url
	page.lastAccess = time.Now()
	b.pages[page.ID] = page
	return page, nil
}

// createPage creates a new rod tab, applying go-rod/stealth by default (skipped
// only when the Browser was constructed with stealth=false). rb is a
// ctx-chained rod browser clone whose ctx bounds the create (the per-call tool
// ctx — never the poisoned default). Creation is serialized by b.createMu so a
// failed create's orphan cleanup (closeOrphanedTab) can identify the tabs it
// owns without racing a concurrent NewPage.
func (b *Browser) createPage(rb *rod.Browser) (*rod.Page, error) {
	b.createMu.Lock()
	defer b.createMu.Unlock()

	var p *rod.Page
	var err error
	if b.stealth {
		p, err = stealth.Page(rb) // stealth on by default (error form)
	} else {
		p, err = rb.Page(proto.TargetCreateTarget{})
	}
	if err != nil {
		// rod's Browser.Page registers its leak-prevention close-target defer
		// only AFTER Target.createTarget succeeds (rod/browser.go:195-200): a
		// create that failed after the CDP write was delivered (response wait
		// aborted at lib/cdp/client.go:100-110, e.g. the per-call ctx was
		// canceled mid-call) leaks the freshly created about:blank tab. Close
		// it best-effort before surfacing the error.
		b.closeOrphanedTab(rb)
		return nil, err
	}
	return p, nil
}

// closeOrphanedTab is a best-effort cleanup for a page-create failure (see
// createPage): it closes every about:blank PAGE target that is not a registered
// page's tab. Called only under b.createMu, so no concurrent NewPage can
// interleave — the tabs it finds are the failed create's own orphan — and the
// registration membership check protects pages an agent deliberately parked at
// about:blank. The listing/close runs on a fresh timeout off the manager-owned
// baseCtx because the per-call ctx is precisely what may be canceled here.
// Best-effort by design: a wedged CDP connection degrades to the same orphan
// the bug already produced, never a crash.
func (b *Browser) closeOrphanedTab(rb *rod.Browser) {
	// Protect registered pages parked at about:blank (deliberate agent
	// navigations): their TargetIDs are never closed by this cleanup.
	b.mu.Lock()
	protected := make(map[proto.TargetTargetID]struct{}, len(b.pages))
	for _, p := range b.pages {
		p.mu.Lock()
		if p.Rod != nil {
			protected[p.Rod.TargetID] = struct{}{}
		}
		p.mu.Unlock()
	}
	b.mu.Unlock()

	closeCtx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
	defer cancel()
	res, err := proto.TargetGetTargets{}.Call(rb.Context(closeCtx))
	if err != nil {
		return
	}
	for _, ti := range res.TargetInfos {
		if ti.Type != proto.TargetTargetInfoTypePage || ti.URL != "about:blank" {
			continue
		}
		if _, ok := protected[ti.TargetID]; ok {
			continue
		}
		_, _ = proto.TargetCloseTarget{TargetID: ti.TargetID}.Call(rb.Context(closeCtx))
	}
}

// closeUnregisteredPage best-effort closes a freshly created page whose session
// was never registered, bounding the close off the manager-owned baseCtx so a
// canceled per-call ctx cannot skip the close (WEB-59). The page's default ctx
// IS the per-call tool ctx it was created under (inherited via rb.Context(ctx)
// → stealth.Page/PageFromTarget): rod's Page.Close issues proto.PageClose on
// p.GetContext(), so with a canceled ctx the cdp client returns ctx.Err()
// immediately and the close is a NO-OP — the tab and its ~100 MB renderer leak
// until browser_close (WEB-58). A fresh closePageTimeout derived from the
// baseCtx — which outlives any single request — guarantees the close is
// actually delivered. Best-effort by design, like closeOrphanedTab.
func (b *Browser) closeUnregisteredPage(p *rod.Page) {
	closeCtx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
	defer cancel()
	_ = p.Context(closeCtx).Close()
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
// wmctrl/xdotool): it resolves the OS window of the window containing any
// live page's devtools target (all pages live in the same window) with rod's
// Page.GetWindow, then applies the requested window state with rod's
// Page.SetWindow(bounds). The default state (normal) restores a minimized
// window and re-asserts its current bounds — the standard best-effort raise;
// maximize/fullscreen are state-only, because the proto doc forbids combining
// those states with left/top/width/height.
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
	// Collect EVERY live page handle under the locks: map iteration is
	// unordered, and a crashed tab whose rod handle survived (the WEB-47 case)
	// would fail GetWindow with "No target with given id found". The
	// candidates are tried in order below until one resolves the OS window, so
	// a single dead tab cannot block the focus. Holding a *Page is safe: every
	// access below happens under the page's own lock, under which the eviction
	// path nils p.Rod.
	var pages []*Page
	for _, p := range b.pages {
		p.mu.Lock()
		if p.Rod != nil {
			pages = append(pages, p)
		}
		p.mu.Unlock()
	}
	b.mu.Unlock()
	if len(pages) == 0 {
		return fmt.Errorf("browsers: focus: no pages open; open a page first")
	}

	// Bound to the caller's ctx (rod's ctx-aware form; no Must*). The first
	// candidate that resolves the OS window wins; a dead target's failure is
	// skipped so a healthy tab still resolves the window (all pages share the
	// same browser window).
	var win *Page
	var winBounds *proto.BrowserBounds
	var lastErr error
	for _, p := range pages {
		p.mu.Lock()
		if p.Rod == nil {
			lastErr = fmt.Errorf("browsers: focus: page rod handle is nil")
			p.mu.Unlock()
			continue
		}
		bounds, err := p.Rod.Context(ctx).GetWindow()
		p.mu.Unlock()
		if err != nil {
			lastErr = err
			continue
		}
		win = p
		winBounds = bounds
		break
	}
	if win == nil {
		return fmt.Errorf("browsers: focus: no live page window: %w", lastErr)
	}

	win.mu.Lock()
	defer win.mu.Unlock()
	if win.Rod == nil {
		return fmt.Errorf("browsers: focus: page rod handle is nil")
	}
	page := win.Rod.Context(ctx)

	switch state {
	case WindowStateNormal:
		// Re-asserting the returned bounds (position/size echoed from the get
		// result) is the standard best-effort raise. Bounds fields may be nil
		// (e.g. minimized); nil fields are left unchanged per the proto doc.
		bounds := &proto.BrowserBounds{WindowState: proto.BrowserWindowStateNormal}
		if winBounds != nil {
			bounds.Left = winBounds.Left
			bounds.Top = winBounds.Top
			bounds.Width = winBounds.Width
			bounds.Height = winBounds.Height
		}
		if err := page.SetWindow(bounds); err != nil {
			return fmt.Errorf("browsers: focus: set window bounds: %w", err)
		}
		return nil
	case WindowStateMaximized:
		// State-only: the proto doc forbids combining maximized with
		// left/top/width/height.
		if err := page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateMaximized}); err != nil {
			return fmt.Errorf("browsers: focus: set window state: %w", err)
		}
		return nil
	case WindowStateFullscreen:
		// State-only, same proto constraint as maximized.
		if err := page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateFullscreen}); err != nil {
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
// and releasing its browser tab. The session is evicted unconditionally — even
// if the underlying tab close fails — so a wedged/dead CDP connection cannot
// leave a phantom session that session_list keeps reporting (WEB-60 MED-1).
// The rod handle is captured and nilled under the locks (mirroring
// Browser.Close) and closed strictly afterwards, outside any lock, so a slow
// close cannot block other browser operations. The close runs on a fresh
// timeout derived from the manager-owned baseCtx — never the caller's ctx — so
// a mid-close request abort cannot surface "context canceled" for a teardown
// that already succeeded (the caller's ctx is kept only for API symmetry with
// the other lifecycle methods).
func (b *Browser) ClosePage(ctx context.Context, id PageID) error {
	b.mu.Lock()
	p, ok := b.pages[id]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("browsers: page %q not found", id)
	}
	// Evict unconditionally: capture the handle and nil p.Rod under the
	// per-page mutex (Browser.Close pattern), then drop the map entry, all
	// while holding the browser mutex. A wedged close must not keep the
	// session visible to session_list; the handle is closed outside, below.
	p.mu.Lock()
	handle := p.Rod
	p.Rod = nil
	delete(b.pages, id)
	p.mu.Unlock()
	b.mu.Unlock()

	if handle == nil {
		return nil
	}
	closeCtx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
	defer cancel()
	if err := closeRodPage(closeCtx, handle); err != nil {
		return fmt.Errorf("browsers: close page %q: %w", id, err)
	}
	return nil
}

// Close closes the Browser and all of its live pages, releasing the underlying
// Chromium process. After Close the Browser must not be reused.
//
// Every live page handle and the rod browser are captured under the locks and
// closed strictly afterwards, outside any lock (mirroring the sweeper's
// capture-under-lock/close-outside pattern): the closes are network round
// trips that would block every other browser operation if done under b.mu, and
// an in-flight tool call on a page can race the close (closeRodPage's
// panic-recover absorbs that). Each close runs on a fresh timeout derived from
// the manager-owned baseCtx — never the caller's ctx — so a canceled request
// ctx cannot abort a teardown that already succeeded (HIGH-2: browser_close
// reported "context canceled" despite successful Chromium teardown).
func (b *Browser) Close(ctx context.Context) error {
	// Capture the live page handles (nil-ing p.Rod so no later close can
	// double-close) and the rod browser under the locks.
	b.mu.Lock()
	var handles []*rod.Page
	for _, p := range b.pages {
		p.mu.Lock()
		if p.Rod != nil {
			handles = append(handles, p.Rod)
			p.Rod = nil
		}
		p.mu.Unlock()
	}
	b.pages = make(map[PageID]*Page)
	rb := b.rod
	b.rod = nil
	b.mu.Unlock()

	// Close the captured pages outside the locks, each bounded by a fresh
	// per-close timeout off the manager-owned baseCtx (the caller's ctx may
	// already be canceled — it must not abort teardown). Best-effort per-page,
	// like the sweeper: a wedged tab cannot block the rest of the teardown.
	for _, h := range handles {
		closeCtx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
		_ = closeRodPage(closeCtx, h)
		cancel()
	}

	if rb == nil {
		return nil
	}
	closeCtx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
	defer cancel()
	return rb.Context(closeCtx).Close() // error form; no Must*
}
