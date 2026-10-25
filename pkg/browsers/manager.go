// Package browsers implements the core browser engine that powers the MCP
// tools. A Manager owns a single live Browser (one Chromium instance via
// go-rod), and each Browser owns its live Pages (browser tabs, a.k.a. MCP
// sessions). This package is rod-only: it owns all go-rod/Chromium logic and
// never imports pkg/mcp; the MCP layer translates calls into this package.
package browsers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
)

// BrowserID uniquely identifies a live Browser instance (e.g. "browser-1", "browser-2").
type BrowserID string

// ErrUnknownBrowser reports that a browser id does not match a live browser.
var ErrUnknownBrowser = errors.New("unknown browser")

// defaultTTL is the per-page idle timeout applied by NewManager when no
// WithTTL option is given. Pages that go unused for longer than the TTL are
// closed by the manager's background sweeper.
const defaultTTL = 15 * time.Minute

// closePageTimeout bounds each individual page close performed by the
// background sweeper, so a single hung tab cannot stall the sweep loop.
const closePageTimeout = 30 * time.Second

// teardownBudget bounds the WHOLE Browser.Close teardown (all per-page closes
// plus the browser close, sharing one deadline) and each ClosePage call: N
// stuck pages × a per-close timeout would blow any single call deadline, which
// is why probe-based self-heal appeared hung (WEB-72). 10s keeps the entire
// teardown inside a 30s tool-call deadline with room for the manager's 2s
// liveness probes. Sweeper per-page closes keep closePageTimeout — the sweep is
// background and never holds a request deadline.
const teardownBudget = 10 * time.Second

// probeTimeout bounds every liveness probe (browser-level and per-page): rod
// has no auto-reconnect, so on a dead CDP websocket a Call blocks until its
// ctx deadline. Probes therefore always derive a short timeout off the caller's
// ctx, failing the probe instead of hanging the caller.
const probeTimeout = 2 * time.Second

// Option configures a Manager at construction time. Apply via
// NewManager(WithHeadless(...), WithStealth(...), ...).
type Option func(*Manager)

// WithHeadless controls whether the launcher runs Chromium in headless mode.
// The default is headful (headless=false); pass WithHeadless(true) for
// headless Chromium.
func WithHeadless(headless bool) Option {
	return func(m *Manager) { m.headless = headless }
}

// WithStealth controls whether go-rod/stealth is applied to every new page.
// Stealth is on by default; pass WithStealth(false) to disable it.
func WithStealth(stealth bool) Option {
	return func(m *Manager) { m.stealth = stealth }
}

// WithBin configures a custom browser binary executable path (e.g. "/usr/bin/brave-browser",
// "brave", "google-chrome", or "chrome"). When empty, the launcher defaults to the
// downloaded Chromium binary.
func WithBin(binPath string) Option {
	return func(m *Manager) { m.binPath = binPath }
}

// WithProfileDir configures a custom user data profile directory path for the browser.
// When empty, the launcher creates a temporary user profile directory for the session.
func WithProfileDir(profileDir string) Option {
	return func(m *Manager) { m.profileDir = profileDir }
}

// WithPosition configures the initial window placement ("left", "right", "max", "normal").
func WithPosition(position string) Option {
	return func(m *Manager) { m.position = position }
}

// WithTTL sets the per-page idle timeout enforced by the manager's background
// sweeper: pages that go unused for longer than ttl are closed and removed,
// reclaiming leaked tabs. The default is 15 minutes; the sweep interval is
// derived from the TTL (see sweepIntervalForTTL). A non-positive ttl disables
// sweeping entirely — pages are never reclaimed and no sweeper goroutine is
// started at all.
func WithTTL(ttl time.Duration) Option {
	return func(m *Manager) { m.ttl = ttl }
}

// WithLogger sets the logger used for the manager's best-effort background
// logging (currently the sweeper's page-close failures). The default is a
// no-op logger that discards everything, keeping pkg/browsers a quiet library
// unless the caller opts in; pass WithLogger(slog.Default()) to route to the
// process logger. A nil logger is ignored (the default no-op is kept).
func WithLogger(logger *slog.Logger) Option {
	return func(m *Manager) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// Manager owns the single Browser that all MCP sessions live in.
//
// Lifecycle:
//
//   - The Browser is launched lazily on the first Open call (via the go-rod
//     launcher, headful by default) and kept for the life of the manager.
//   - Close tears the Browser down so a fresh one can be opened later;
//     Shutdown is the terminal variant — after Shutdown the manager must not
//     be reused.
//   - The manager currently holds ONE browser field, keeping the model simple;
//     a browser_id → Browser map is the planned growth path when multi-browser
//     support lands (AGENTS.md "Browser-first paradigm").
type Manager struct {
	// mu guards browsers map, shutdown, and the sweeper stop channel.
	mu sync.Mutex
	// browsers maps browser IDs (e.g. "browser-1", "browser-2") to live Browser instances.
	browsers map[BrowserID]*Browser
	// nextBrowserID is the counter used to mint fresh BrowserIDs.
	nextBrowserID int
	// relaunched is set when the manager observed a Browser's Chromium
	// process die and (re)started it.
	relaunched bool
	// headless controls default launcher headless mode (default: headful).
	headless bool
	// stealth controls per-page stealth (default: on).
	stealth bool
	// binPath specifies a custom default browser executable path (e.g. Brave, Chrome).
	binPath string
	// profileDir specifies a custom default user profile directory path.
	profileDir string
	// position specifies default window placement ("left", "right", "max", "normal").
	position string
	// shutdown marks the manager as terminated; Open rejects further calls.
	shutdown bool
	// ttl is the per-page idle timeout enforced by the background sweeper
	// (default: 15m; a non-positive value disables sweeping).
	ttl time.Duration
	// sweepInterval is how often the background sweeper runs; derived from ttl
	// at construction (see sweepIntervalForTTL).
	sweepInterval time.Duration
	// sweepStop, when non-nil, is the stop channel of a running sweeper
	// goroutine; nil means no sweeper is running. Guarded by mu.
	sweepStop chan struct{}
	// logger is the sink for the manager's best-effort background logging
	// (e.g. sweeper close failures). Defaults to a no-op discard logger;
	// inject one with WithLogger. Read-only after construction, so it is safe
	// to use without holding mu.
	logger *slog.Logger
	// baseCtx is the manager-owned, process-lifetime context every Browser's
	// rod default ctx is bound to (WEB-59): canceled only by Shutdown, so no
	// transient tool-call ctx can ever poison the browser (a poisoned default
	// ctx leaked ~100 MB per fetch_page and made closes report "context
	// canceled"). Relaunches after Close reuse it, so a fresh Browser is never
	// re-poisoned.
	baseCtx context.Context
	// cancelBase releases baseCtx; called exactly once by Shutdown.
	cancelBase context.CancelFunc
}

// NewManager constructs a Manager with the given options applied over sensible
// defaults: headful Chromium, stealth on for every page, and a 15-minute
// per-page idle TTL enforced by a background sweeper. Browsers are launched
// lazily or created on demand via CreateBrowser / Open.
func NewManager(opts ...Option) *Manager {
	// The manager-owned lifetime ctx: every Browser's rod default ctx hangs
	// off it (WEB-59), so no tool-call ctx can ever poison the browser. It is
	// canceled only by Shutdown — Close keeps it alive so a relaunch after
	// Close is never re-poisoned.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	m := &Manager{
		browsers:      make(map[BrowserID]*Browser),
		nextBrowserID: 1,
		stealth:       true,
		ttl:           defaultTTL,
		sweepInterval: sweepIntervalForTTL(defaultTTL),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		baseCtx:       baseCtx,
		cancelBase:    cancelBase,
	}
	for _, o := range opts {
		o(m)
	}
	// Re-derive the sweep interval after options so a custom WithTTL also gets
	// a proportional polling cadence.
	m.sweepInterval = sweepIntervalForTTL(m.ttl)
	return m
}

// sweepIntervalForTTL derives the background sweep interval from a TTL: half
// the TTL, floored at 1s so even a very short TTL yields a sane polling
// cadence.
func sweepIntervalForTTL(ttl time.Duration) time.Duration {
	interval := ttl / 2
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

// DefaultBrowserID is the default BrowserID used when none is specified.
const DefaultBrowserID BrowserID = "browser-1"

// CreateBrowser creates and launches a new Browser instance with custom options
// (e.g. WithBin, WithProfileDir, WithHeadless, WithStealth). It assigns a fresh
// BrowserID (e.g. "browser-1", "browser-2") and registers it in the manager.
func (m *Manager) CreateBrowser(ctx context.Context, opts ...Option) (BrowserID, *Browser, error) {
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return "", nil, errors.New("browsers: manager has been shut down")
	}

	// Idempotent default browser reuse when no custom options provided
	if len(opts) == 0 {
		if b, ok := m.browsers[DefaultBrowserID]; ok && b.IsAlive(ctx) {
			m.mu.Unlock()
			return DefaultBrowserID, b, nil
		}
	}

	id := BrowserID(fmt.Sprintf("browser-%d", m.nextBrowserID))
	m.nextBrowserID++

	// Copy manager defaults
	headless := m.headless
	stealth := m.stealth
	binPath := m.binPath
	profileDir := m.profileDir
	position := m.position

	// Apply per-browser options if provided
	tempMgr := &Manager{
		headless:   headless,
		stealth:    stealth,
		binPath:    binPath,
		profileDir: profileDir,
		position:   position,
	}
	for _, o := range opts {
		o(tempMgr)
	}

	// Avoid Chromium SingletonLock profile dir collisions across distinct browser instances:
	// browser-1 holds the main persistent profile directory (~/.config/browser-mcp/profiles/...).
	// Any secondary browser created while the main profile is in use uses an ephemeral
	// profile (profileDir = "") so it auto-deletes on exit and never bloats the profiles directory.
	for _, existing := range m.browsers {
		if existing.IsAlive(ctx) && existing.profileDir != "" && existing.profileDir == tempMgr.profileDir {
			tempMgr.profileDir = ""
			break
		}
	}
	m.mu.Unlock()

	b := NewBrowser(id, m.baseCtx, tempMgr.headless, tempMgr.stealth, tempMgr.binPath, tempMgr.profileDir, tempMgr.position)
	if err := b.Launch(ctx); err != nil {
		return "", nil, fmt.Errorf("browsers: launch browser %s: %w", id, err)
	}

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		_ = b.Close(ctx)
		return "", nil, errors.New("browsers: manager has been shut down")
	}
	m.browsers[id] = b
	if m.ttl > 0 {
		m.startSweeperLocked()
	}
	m.mu.Unlock()

	return id, b, nil
}

// Open lazily opens or returns the default browser ("browser-1").
func (m *Manager) Open(ctx context.Context) (*Browser, error) {
	return m.OpenBrowser(ctx, DefaultBrowserID)
}

// OpenBrowser lazily opens or returns a Browser with the specified ID.
// If id is empty, DefaultBrowserID ("browser-1") is used.
func (m *Manager) OpenBrowser(ctx context.Context, id BrowserID) (*Browser, error) {
	if id == "" {
		id = DefaultBrowserID
	}

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil, errors.New("browsers: manager has been shut down")
	}

	if b, ok := m.browsers[id]; ok {
		m.mu.Unlock()
		if err := probeBrowser(b); err == nil {
			return b, nil
		}
		// Stale browser process died: remove and relaunch below
		m.mu.Lock()
		delete(m.browsers, id)
		m.relaunched = true
		m.mu.Unlock()
		_ = b.Close(ctx)
	} else {
		m.mu.Unlock()
	}

	// Create and launch the target browser
	b := NewBrowser(id, m.baseCtx, m.headless, m.stealth, m.binPath, m.profileDir)
	if err := b.Launch(ctx); err != nil {
		return nil, fmt.Errorf("browsers: launch browser %s: %w", id, err)
	}

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		_ = b.Close(ctx)
		return nil, errors.New("browsers: manager has been shut down")
	}
	if winner, ok := m.browsers[id]; ok {
		// Race condition loss: keep winner
		m.mu.Unlock()
		_ = b.Close(ctx)
		return winner, nil
	}
	m.browsers[id] = b
	if m.ttl > 0 {
		m.startSweeperLocked()
	}
	m.mu.Unlock()
	return b, nil
}

// Browser returns the live Browser with the given ID, or the default/first open
// Browser when id is empty. Does NOT lazily launch a new browser.
func (m *Manager) Browser(id BrowserID) (*Browser, error) {
	m.mu.Lock()
	var target *Browser
	if id == "" {
		if b, ok := m.browsers[DefaultBrowserID]; ok {
			target = b
		} else {
			for _, b := range m.browsers {
				target = b
				break
			}
		}
	} else {
		target = m.browsers[id]
	}
	running := m.runningIDsLocked()
	m.mu.Unlock()

	if target == nil {
		if id == "" {
			return nil, fmt.Errorf("no browser running (create one with browser_create): %w", ErrUnknownBrowser)
		}
		return nil, fmt.Errorf("browser %q not found (running: %s): %w", id, running, ErrUnknownBrowser)
	}

	if err := probeBrowser(target); err != nil {
		m.mu.Lock()
		delete(m.browsers, target.id)
		m.relaunched = true
		if len(m.browsers) == 0 {
			m.stopSweeperLocked()
		}
		m.mu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		_ = target.Close(closeCtx)
		return nil, fmt.Errorf("browsers: browser %s died", target.id)
	}
	return target, nil
}
func probeBrowser(b *Browser) error {
	b.mu.Lock()
	rb := b.rod
	b.mu.Unlock()
	if rb == nil {
		return errors.New("browsers: browser not launched")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	_, err := rb.Context(probeCtx).Version()
	return err
}

// Browsers returns a snapshot map of all currently open Browser instances.
func (m *Manager) Browsers() map[BrowserID]*Browser {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[BrowserID]*Browser, len(m.browsers))
	for id, b := range m.browsers {
		result[id] = b
	}
	return result
}

// runningIDsLocked returns a sorted comma-separated list of live browser ids,
// or "none" when nothing is running. The caller must hold m.mu.
func (m *Manager) runningIDsLocked() string {
	ids := make([]string, 0, len(m.browsers))
	for id := range m.browsers {
		ids = append(ids, string(id))
	}
	if len(ids) == 0 {
		return "none"
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// Page resolves the live page with the given PageID across all active browsers.
func (m *Manager) Page(ctx context.Context, id PageID) (*Page, error) {
	m.mu.Lock()
	browsers := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		browsers = append(browsers, b)
	}
	m.mu.Unlock()

	if len(browsers) == 0 {
		b, err := m.Open(ctx)
		if err != nil {
			return nil, err
		}
		browsers = append(browsers, b)
	}

	for _, b := range browsers {
		if p, err := b.Page(ctx, id); err == nil {
			return p, nil
		}
	}

	m.mu.Lock()
	relaunched := m.relaunched
	m.mu.Unlock()
	if relaunched {
		return nil, fmt.Errorf("session %q no longer exists: browser was restarted; call fetch_page to re-open URLs", id)
	}
	return nil, fmt.Errorf("session %q not found", id)
}

// CloseBrowser closes a specific Browser by BrowserID and all of its pages.
// If id is empty, it closes ALL open browsers.
func (m *Manager) CloseBrowser(ctx context.Context, id BrowserID) error {
	m.mu.Lock()
	if id == "" {
		m.stopSweeperLocked()
		targets := m.browsers
		m.browsers = make(map[BrowserID]*Browser)
		m.mu.Unlock()
		var firstErr error
		for _, b := range targets {
			if err := b.Close(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	b, ok := m.browsers[id]
	if !ok {
		running := m.runningIDsLocked()
		m.mu.Unlock()
		return fmt.Errorf("browser %q not found (running: %s): %w", id, running, ErrUnknownBrowser)
	}
	delete(m.browsers, id)
	if len(m.browsers) == 0 {
		m.stopSweeperLocked()
	}
	m.mu.Unlock()

	return b.Close(ctx)
}

// Close closes all open Browser instances and their pages.
func (m *Manager) Close(ctx context.Context) error {
	return m.CloseBrowser(ctx, "")
}

// Shutdown gracefully closes all browsers and cancels the manager context.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shutdown = true
	m.mu.Unlock()

	err := m.CloseBrowser(ctx, "")
	m.cancelBase()
	return err
}

// startSweeperLocked launches the background TTL sweep goroutine unless one is
// already running. It must be called with m.mu held. With a non-positive TTL
// sweeping is disabled and no goroutine is started at all.
func (m *Manager) startSweeperLocked() {
	if m.ttl <= 0 {
		return // sweeping disabled: no ticker goroutine at all
	}
	if m.sweepStop != nil {
		return // a sweeper is already running
	}
	stop := make(chan struct{})
	m.sweepStop = stop
	go m.sweep(stop)
}

// stopSweeperLocked stops a running TTL sweep goroutine. It is idempotent — a
// second call is a no-op — and must be called with m.mu held. The sweeper
// observes the closed stop channel on its next iteration and exits, so no
// goroutine is leaked.
func (m *Manager) stopSweeperLocked() {
	if m.sweepStop == nil {
		return
	}
	close(m.sweepStop)
	m.sweepStop = nil
}

// sweep runs the background TTL sweep loop until stop is closed: on every
// tick it closes pages that have been idle for longer than the manager's TTL.
func (m *Manager) sweep(stop <-chan struct{}) {
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.sweepOnce(context.Background())
		}
	}
}

// sweepOnce closes every live page that has been idle longer than the
// manager's TTL. Each candidate is removed from the pages map — and its rod
// handle captured and nilled — while still holding the browser and page locks,
// so the actual tab close happens strictly afterwards, outside any lock: a
// Browser.Page lookup racing the sweep either sees the page (and touches it,
// sparing it) or correctly reports "page not found" — never a page whose tab is
// mid-close. Each close is best-effort, bounded by closePageTimeout, and never
// retried, so a wedged tab cannot stall the sweep loop or resurface. ctx is the
// sweep's base context (typically context.Background); per-close timeouts are
// derived from it.
func (m *Manager) sweepOnce(ctx context.Context) {
	m.mu.Lock()
	browsers := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		browsers = append(browsers, b)
	}
	ttl := m.ttl
	m.mu.Unlock()
	if len(browsers) == 0 || ttl <= 0 {
		return
	}

	for _, b := range browsers {
		// Snapshot the idle candidates under the browser mutex.
		b.mu.Lock()
		var idle []PageID
		now := time.Now()
		for id, p := range b.pages {
			p.mu.Lock()
			idleSince := p.lastAccess
			p.mu.Unlock()
			if now.Sub(idleSince) > ttl {
				idle = append(idle, id)
			}
		}
		b.mu.Unlock()

		for _, id := range idle {
			var handle *rod.Page
			b.mu.Lock()
			p, ok := b.pages[id]
			if ok {
				p.mu.Lock()
				if time.Since(p.lastAccess) > ttl {
					p.stopObservation()
					handle = p.Rod
					p.Rod = nil
					delete(b.pages, id)
				}
				p.mu.Unlock()
			}
			b.mu.Unlock()
			if handle == nil {
				continue
			}

			closeCtx, cancel := context.WithTimeout(ctx, closePageTimeout)
			err := closeRodPage(closeCtx, handle)
			cancel()
			if err != nil {
				m.logger.Warn("sweeper: failed to close idle page",
					slog.String("page_id", string(id)),
					slog.String("browser_id", string(b.id)),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// closeRodPage closes a *rod.Page handle captured by the sweeper. rod's Close
// has no ctx-aware form, so a ctx-bound clone honors the caller's timeout
// (closePageTimeout). The recover is a safety net for the sweeper path: the
// handle's page may be raced by an in-flight tool call that still holds a
// reference, and a concurrent close can surface as a panic; converting it to an
// error keeps the sweep loop alive.
func closeRodPage(ctx context.Context, handle *rod.Page) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("browsers: close page: panic: %v", r)
		}
	}()
	return handle.Context(ctx).Close()
}
