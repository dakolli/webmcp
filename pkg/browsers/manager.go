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
	"sync"
	"time"

	"github.com/go-rod/rod"
)

// defaultTTL is the per-page idle timeout applied by NewManager when no
// WithTTL option is given. Pages that go unused for longer than the TTL are
// closed by the manager's background sweeper.
const defaultTTL = 15 * time.Minute

// closePageTimeout bounds each individual page close performed by the
// background sweeper, so a single hung tab cannot stall the sweep loop.
const closePageTimeout = 30 * time.Second

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
	// mu guards browser, shutdown, and the sweeper stop channel.
	mu sync.Mutex
	// browser is the single Browser owned by this manager; nil until first Open.
	browser *Browser
	// headless controls launcher headless mode (default: headful).
	headless bool
	// stealth controls per-page stealth (default: on).
	stealth bool
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
}

// NewManager constructs a Manager with the given options applied over sensible
// defaults: headful Chromium, stealth on for every page, and a 15-minute
// per-page idle TTL enforced by a background sweeper. The Browser itself is NOT
// launched here — that happens lazily on the first Open call.
func NewManager(opts ...Option) *Manager {
	m := &Manager{
		stealth:       true,
		ttl:           defaultTTL,
		sweepInterval: sweepIntervalForTTL(defaultTTL),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
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

// Open lazily launches the manager's single Browser on first use and returns
// it; subsequent calls return the already-open Browser. The Browser runs
// headful by default (unless WithHeadless(true) was set) with stealth applied
// to every new page (unless WithStealth(false) was set).
//
// Open is safe for concurrent use: when two goroutines race the first launch,
// exactly one Browser survives — the loser closes its own just-launched
// Browser (best-effort, error form) and every caller receives the winner, so
// no Chromium instance is orphaned.
func (m *Manager) Open(ctx context.Context) (*Browser, error) {
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil, errors.New("browsers: manager has been shut down")
	}
	if m.browser != nil {
		b := m.browser
		m.mu.Unlock()
		return b, nil
	}
	m.mu.Unlock()

	b := NewBrowser(m.headless, m.stealth)
	if err := b.Launch(ctx); err != nil {
		return nil, fmt.Errorf("browsers: launch browser: %w", err)
	}

	m.mu.Lock()
	if m.shutdown {
		// Shutdown raced in while we were launching: do not register the fresh
		// browser — close it so nothing leaks, and surface the shutdown error.
		m.mu.Unlock()
		_ = b.Close(ctx)
		return nil, errors.New("browsers: manager has been shut down")
	}
	if winner := m.browser; winner != nil {
		// Another goroutine won the race and registered its Browser while we
		// were launching. Close our just-launched Browser (best-effort, error
		// form) so no Chromium is orphaned, and hand out the winner. The
		// sweeper is left untouched: the winner's insert started it exactly
		// once, and the loser must not stop it.
		m.mu.Unlock()
		_ = b.Close(ctx)
		return winner, nil
	}
	m.browser = b
	if m.ttl > 0 {
		m.startSweeperLocked()
	}
	m.mu.Unlock()
	return b, nil
}

// Browser returns the manager's live Browser, or an error when no Browser has
// been opened yet (call Open first).
func (m *Manager) Browser() (*Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browser == nil {
		return nil, errors.New("browsers: no browser open; call Manager.Open first")
	}
	return m.browser, nil
}

// Close closes the manager's Browser and all of its pages, so a fresh Browser
// can be opened with a later Open call. It stops the background sweeper first
// (idempotently — a second Close is a no-op when nothing is open); a later
// Open restarts a fresh sweeper. It is a no-op when no Browser is open.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.stopSweeperLocked()
	b := m.browser
	m.browser = nil
	m.mu.Unlock()

	if b == nil {
		return nil
	}
	return b.Close(ctx)
}

// Shutdown is the graceful-teardown entry point, intended to be called from
// main() when the server shuts down: it stops the background sweeper, closes
// the Browser and all of its pages, and marks the manager as terminated. After
// Shutdown the manager must not be reused — further Open calls fail.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shutdown = true
	m.stopSweeperLocked()
	b := m.browser
	m.browser = nil
	m.mu.Unlock()

	if b == nil {
		return nil
	}
	return b.Close(ctx)
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
	b := m.browser
	ttl := m.ttl
	m.mu.Unlock()
	if b == nil || ttl <= 0 {
		return
	}

	// Snapshot the idle candidates under the browser mutex. Every successful
	// Browser.Page lookup touches the page under this same mutex, so a page
	// looked up before this sweep tick reads as fresh and is spared.
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
		// Re-check under the browser mutex that the page is still stale: a
		// lookup may have touched it after the snapshot. If it is, capture its
		// rod handle, remove it from the map, and nil p.Rod while still holding
		// the locks — a later lookup then sees "page not found" instead of
		// racing the close. The handle is closed below, outside the locks.
		var handle *rod.Page
		b.mu.Lock()
		p, ok := b.pages[id]
		if ok {
			p.mu.Lock()
			if time.Since(p.lastAccess) > ttl {
				handle = p.Rod
				p.Rod = nil
				delete(b.pages, id)
			}
			p.mu.Unlock()
		}
		b.mu.Unlock()
		if handle == nil {
			continue // re-touched after the snapshot, or already gone
		}

		closeCtx, cancel := context.WithTimeout(ctx, closePageTimeout)
		err := closeRodPage(closeCtx, handle)
		cancel()
		if err != nil {
			// Best-effort by design: log once and move on. The page's map slot
			// and rod handle are already reclaimed, so a tab that keeps failing
			// to close is logged once (never once per sweep tick) through the
			// injected logger (no-op by default).
			m.logger.Warn("browsers: sweep: close page failed",
				"page", id, "error", err)
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
