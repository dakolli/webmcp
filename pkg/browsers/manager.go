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
	"sync"
)

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
	// mu guards browser and shutdown.
	mu sync.Mutex
	// browser is the single Browser owned by this manager; nil until first Open.
	browser *Browser
	// headless controls launcher headless mode (default: headful).
	headless bool
	// stealth controls per-page stealth (default: on).
	stealth bool
	// shutdown marks the manager as terminated; Open rejects further calls.
	shutdown bool
}

// NewManager constructs a Manager with the given options applied over sensible
// defaults: headful Chromium and stealth on for every page. The Browser itself
// is NOT launched here — that happens lazily on the first Open call.
func NewManager(opts ...Option) *Manager {
	m := &Manager{stealth: true}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Open lazily launches the manager's single Browser on first use and returns
// it; subsequent calls return the already-open Browser. The Browser runs
// headful by default (unless WithHeadless(true) was set) with stealth applied
// to every new page (unless WithStealth(false) was set).
func (m *Manager) Open(ctx context.Context) (*Browser, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   m.mu.Lock()
	//   if m.shutdown { m.mu.Unlock(); return nil, errors.New("browsers: manager has been shut down") }
	//   if m.browser != nil { b := m.browser; m.mu.Unlock(); return b, nil }
	//   m.mu.Unlock()
	//
	//   b := NewBrowser(m.headless, m.stealth)
	//   if err := b.Launch(ctx); err != nil {
	//       return nil, fmt.Errorf("browsers: launch browser: %w", err)
	//   }
	//
	//   m.mu.Lock()
	//   if m.shutdown { m.mu.Unlock(); _ = b.Close(ctx); return nil, errors.New("browsers: manager has been shut down") }
	//   m.browser = b
	//   m.mu.Unlock()
	//   return b, nil
	return nil, fmt.Errorf("browsers.manager.Open: not implemented")
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
// can be opened with a later Open call. It is a no-op when no Browser is open.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	b := m.browser
	m.browser = nil
	m.mu.Unlock()

	if b == nil {
		return nil
	}
	return b.Close(ctx)
}

// Shutdown is the graceful-teardown entry point, intended to be called from
// main() when the server shuts down: it closes the Browser and all of its
// pages and marks the manager as terminated. After Shutdown the manager must
// not be reused — further Open calls fail.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shutdown = true
	b := m.browser
	m.browser = nil
	m.mu.Unlock()

	if b == nil {
		return nil
	}
	return b.Close(ctx)
}
