package ipc

// Shared test scaffolding for the ipc wire-contract suite: the manager stub
// behind the internal manager seam, plus helpers to bind/serve a Server on a
// temp-dir unix socket and speak raw JSON-RPC lines to it.

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"testing"

	"webmcp/pkg/browsers"
)

// stubbedManager implements the manager seam with an in-memory, unlaunched
// *browsers.Browser (browsers.NewBrowser allocates only a pages map; the rod
// handle stays nil). Every handler path that only reads engine state
// round-trips, and every path that needs a live page fails with the engine's
// own "not launched"/"not found" errors — the stub structurally cannot launch
// Chromium, so the suite satisfies "no test reaches a real browser".
type stubbedManager struct {
	// browser is what Open and Browser return when no error is set.
	browser *browsers.Browser
	// openErr, closeErr and browserErr force the corresponding method to fail.
	openErr    error
	closeErr   error
	browserErr error
	// purgeErr/pruneErr force the cleanup methods to fail; purged records the
	// ids purge was called with and pruneN is the removed-directory count.
	purgeErr error
	pruneErr error
	purged   []browsers.BrowserID
	pruneN   int
}

// Open implements manager.
func (s *stubbedManager) Open(context.Context) (*browsers.Browser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.browser, nil
}

func (s *stubbedManager) OpenBrowser(ctx context.Context, id browsers.BrowserID) (*browsers.Browser, error) {
	return s.Open(ctx)
}

func (s *stubbedManager) CreateBrowser(ctx context.Context, opts ...browsers.Option) (browsers.BrowserID, *browsers.Browser, error) {
	if s.openErr != nil {
		return "", nil, s.openErr
	}
	return "browser-1", s.browser, nil
}

// Close implements manager.
func (s *stubbedManager) Close(context.Context) error { return s.closeErr }

func (s *stubbedManager) CloseBrowser(ctx context.Context, id browsers.BrowserID) error {
	return s.closeErr
}

func (s *stubbedManager) PurgeProfile(ctx context.Context, id browsers.BrowserID) error {
	if s.purgeErr != nil {
		return s.purgeErr
	}
	s.purged = append(s.purged, id)
	return s.closeErr
}

func (s *stubbedManager) PruneProfiles(context.Context) ([]string, error) {
	if s.pruneErr != nil {
		return nil, s.pruneErr
	}
	removed := make([]string, s.pruneN)
	for i := range removed {
		removed[i] = "removed"
	}
	return removed, s.closeErr
}

// Browser implements manager.
func (s *stubbedManager) Browser(id browsers.BrowserID) (*browsers.Browser, error) {
	if s.browserErr != nil {
		return nil, s.browserErr
	}
	return s.browser, nil
}

func (s *stubbedManager) Browsers() map[browsers.BrowserID]*browsers.Browser {
	if s.browser != nil {
		return map[browsers.BrowserID]*browsers.Browser{"browser-1": s.browser}
	}
	return nil
}

// Page implements manager.
func (s *stubbedManager) Page(ctx context.Context, id browsers.PageID) (*browsers.Page, error) {
	return s.browser.Page(ctx, id)
}

// CheckPageScope implements manager: the stub has no allowlist.
func (s *stubbedManager) CheckPageScope(browsers.PageID, string) error { return nil }

// newStubServer returns a Server wired to a stub manager whose Browser is
// unlaunched and empty (no pages) — safe for any wire-contract test.
func newStubServer() *Server {
	// context.Background() as the manager-owned base ctx: the stub browser is
	// never launched, so the ctx is only read by Close-timeout derivation on
	// paths that no-op on a nil rod.
	return newServer(&stubbedManager{browser: browsers.NewBrowser("browser-1", context.Background(), false, false, "", "")})
}

// startServer binds s at a fresh temp-dir socket, serves it in a goroutine and
// returns the socket path. The server is stopped (ctx cancel + Serve return)
// by the test's cleanup; tests never touch the default socket path.
func startServer(t *testing.T, s *Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sock")
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind(%s): %v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve returned %v, want nil on the clean stop", err)
		}
	})
	return path
}

// dialRaw opens a raw unix connection to the server so a test can assert on
// the exact framing rather than going through Client.
func dialRaw(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readLineFrom reads one newline-terminated response line from r, failing the
// test on any error (including a spurious connection close or timeout).
func readLineFrom(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response line: %v", err)
	}
	return line
}
