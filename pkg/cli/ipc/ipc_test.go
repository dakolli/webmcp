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
}

// Open implements manager.
func (s *stubbedManager) Open(context.Context) (*browsers.Browser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.browser, nil
}

// Close implements manager.
func (s *stubbedManager) Close(context.Context) error { return s.closeErr }

// Browser implements manager.
func (s *stubbedManager) Browser() (*browsers.Browser, error) {
	if s.browserErr != nil {
		return nil, s.browserErr
	}
	return s.browser, nil
}

// Page implements manager.
func (s *stubbedManager) Page(ctx context.Context, id browsers.PageID) (*browsers.Page, error) {
	return s.browser.Page(ctx, id)
}

// newStubServer returns a Server wired to a stub manager whose Browser is
// unlaunched and empty (no pages) — safe for any wire-contract test.
func newStubServer() *Server {
	return newServer(&stubbedManager{browser: browsers.NewBrowser(false, false)})
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
