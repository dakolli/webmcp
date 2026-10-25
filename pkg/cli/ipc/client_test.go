package ipc

// Wire-contract pins for the client half: round-trips through Client.Call over
// a real unix socket, *ServerError wrapping (%w + errors.As), and the
// incrementing-id / fresh-connection-per-call contract.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestClientPingRoundTrip drives the full client/server stack: Dial + Ping
// against a Server on a temp-dir socket succeeds.
func TestClientPingRoundTrip(t *testing.T) {
	path := startServer(t, newStubServer())
	c, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial(%s): %v", path, err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestClientCallUnknownMethod pins that a -32601 server error surfaces as a
// wrapped *ServerError carrying the method name.
func TestClientCallUnknownMethod(t *testing.T) {
	path := startServer(t, newStubServer())
	c, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	err = c.Call(context.Background(), "frobnicate", nil, nil)
	var se *ServerError
	if !errors.As(err, &se) {
		t.Fatalf("Call error = %v (%T), want a wrapped *ServerError (errors.As)", err, err)
	}
	if se.Method != "frobnicate" || se.Code != CodeMethodNotFound || se.Message != `unknown method "frobnicate"` {
		t.Errorf("ServerError = %+v, want method %q code %d message %q",
			se, "frobnicate", CodeMethodNotFound, `unknown method "frobnicate"`)
	}
}

// TestClientCallBadParams pins -32602 surfacing through Client.Call for both
// the missing-url and unknown-what validation paths (both manager-free).
func TestClientCallBadParams(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		params  any
		wantMsg string
	}{
		{
			name:    "page.open missing url",
			method:  MethodPageOpen,
			params:  PageOpenParams{},
			wantMsg: `page.open: missing required param "url"`,
		},
		{
			name:    "page.fetch unknown what",
			method:  MethodPageFetch,
			params:  PageFetchParams{SessionID: "page-1", What: "bogus"},
			wantMsg: `page.fetch: unknown what="bogus" (must be one of html, text, links, meta, dom, css, js, screenshot, interactives, a11y, network, console, article, marks)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, newStubServer())
			c, err := Dial(path)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			err = c.Call(context.Background(), tt.method, tt.params, nil)
			var se *ServerError
			if !errors.As(err, &se) {
				t.Fatalf("Call error = %v (%T), want a wrapped *ServerError", err, err)
			}
			if se.Code != CodeInvalidParams {
				t.Errorf("code = %d, want %d (-32602)", se.Code, CodeInvalidParams)
			}
			if se.Message != tt.wantMsg {
				t.Errorf("message = %q, want %q", se.Message, tt.wantMsg)
			}
		})
	}
}

// TestClientCallServerErrorDecode pins that a -32000 server error decodes into
// the wrapped *ServerError with the server's code and message (browser.list
// with no browser open — the error is produced by the handler, never by a
// browser launch).
func TestClientCallServerErrorDecode(t *testing.T) {
	s := newServer(&stubbedManager{browserErr: errors.New("no browser open")})
	path := startServer(t, s)
	c, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	err = c.Call(context.Background(), MethodBrowserList, nil, nil)
	var se *ServerError
	if !errors.As(err, &se) {
		t.Fatalf("Call error = %v (%T), want a wrapped *ServerError (errors.As)", err, err)
	}
	if se.Method != MethodBrowserList {
		t.Errorf("method = %q, want %q", se.Method, MethodBrowserList)
	}
	if se.Code != CodeServerError {
		t.Errorf("code = %d, want %d (-32000)", se.Code, CodeServerError)
	}
	if want := "browser.list: no browser open; call browser.open first"; se.Message != want {
		t.Errorf("message = %q, want %q", se.Message, want)
	}
}

// TestClientCallIDsIncrementAndFreshDial pins the client contract: ids start
// at 1 and increment per call, and every call opens its OWN connection (a fake
// raw server counts both the ids and the accepted connections).
func TestClientCallIDsIncrementAndFreshDial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen fake server: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	var mu sync.Mutex
	var ids []int
	var accepts int
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			mu.Lock()
			accepts++
			mu.Unlock()
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req Request
				if err := json.Unmarshal(line, &req); err != nil {
					return
				}
				id, _ := req.ID.(float64)
				mu.Lock()
				ids = append(ids, int(id))
				mu.Unlock()
				_, _ = c.Write(fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%v,"result":{"ok":true}}`+"\n", req.ID))
			}(conn)
		}
	}()

	c, err := Dial(path) // the probe connection is the first accept
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := c.Call(context.Background(), MethodPing, nil, nil); err != nil {
			t.Fatalf("Call %d: %v", i+1, err)
		}
	}

	// The fake server records asynchronously; poll until both calls landed.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		gotIDs, gotAccepts := len(ids), accepts
		mu.Unlock()
		if gotAccepts >= 3 && gotIDs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake server saw %d accepts and %d ids (want 3 accepts, 2 ids)", gotAccepts, gotIDs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if accepts != 3 {
		t.Errorf("accepted connections = %d, want 3 (probe + one per Call — fresh dial per call)", accepts)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("request ids = %v, want [1 2]", ids)
	}
}

// TestStubHappyPaths pins the result shapes of the manager-backed handlers via
// a stub manager — none of them launch Chromium.
func TestStubHappyPaths(t *testing.T) {
	path := startServer(t, newStubServer())
	c, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	var openRes BrowserOpenResult
	if err := c.Call(ctx, MethodBrowserOpen, nil, &openRes); err != nil {
		t.Fatalf("browser.open: %v", err)
	}
	if openRes.BrowserID != browserID {
		t.Errorf("browser.open browser_id = %q, want %q", openRes.BrowserID, browserID)
	}

	var closeRes BrowserCloseResult
	if err := c.Call(ctx, MethodBrowserClose, nil, &closeRes); err != nil {
		t.Fatalf("browser.close: %v", err)
	}
	if !closeRes.OK {
		t.Error("browser.close ok = false, want true")
	}

	var listRes BrowserListResult
	if err := c.Call(ctx, MethodBrowserList, nil, &listRes); err != nil {
		t.Fatalf("browser.list: %v", err)
	}
	if listRes.BrowserID != browserID || len(listRes.Pages) != 0 {
		t.Errorf("browser.list = %+v, want browser_id %q with no pages", listRes, browserID)
	}

	var pageListRes PageListResult
	if err := c.Call(ctx, MethodPageList, nil, &pageListRes); err != nil {
		t.Fatalf("page.list: %v", err)
	}
	if len(pageListRes.Pages) != 0 {
		t.Errorf("page.list pages = %v, want empty", pageListRes.Pages)
	}
}

// TestStubBrowserFocusWirePins pins the browser.focus wire contract through
// the stub manager (no Chromium reachable): the engine's exact not-launched
// error on the default unlaunched stub, the no-lazy-launch -32000 when the
// manager's Browser() fails, a -32602 for a bogus state, and the empty-state →
// normal default (the params decode path reaches the engine instead of being
// rejected as an unknown state).
func TestStubBrowserFocusWirePins(t *testing.T) {
	t.Run("not-launched engine error on default stub", func(t *testing.T) {
		path := startServer(t, newStubServer())
		c, err := Dial(path)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		err = c.Call(context.Background(), MethodBrowserFocus, BrowserFocusParams{State: "normal"}, nil)
		var se *ServerError
		if !errors.As(err, &se) {
			t.Fatalf("Call error = %v (%T), want a wrapped *ServerError", err, err)
		}
		if se.Code != CodeServerError {
			t.Errorf("code = %d, want %d (-32000)", se.Code, CodeServerError)
		}
		if want := "browsers: focus: browser not launched"; se.Message != want {
			t.Errorf("message = %q, want %q", se.Message, want)
		}
	})

	t.Run("no-lazy-launch path with browserErr set", func(t *testing.T) {
		s := newServer(&stubbedManager{browserErr: errors.New("no browser open")})
		path := startServer(t, s)
		c, err := Dial(path)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		err = c.Call(context.Background(), MethodBrowserFocus, nil, nil)
		var se *ServerError
		if !errors.As(err, &se) {
			t.Fatalf("Call error = %v (%T), want a wrapped *ServerError", err, err)
		}
		if se.Code != CodeServerError {
			t.Errorf("code = %d, want %d (-32000)", se.Code, CodeServerError)
		}
		if want := "browser.focus: no browser open; call browser.open first"; se.Message != want {
			t.Errorf("message = %q, want %q", se.Message, want)
		}
	})

	t.Run("bogus state is invalid params", func(t *testing.T) {
		path := startServer(t, newStubServer())
		c, err := Dial(path)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		err = c.Call(context.Background(), MethodBrowserFocus, BrowserFocusParams{State: "bogus"}, nil)
		var se *ServerError
		if !errors.As(err, &se) {
			t.Fatalf("Call error = %v (%T), want a wrapped *ServerError", err, err)
		}
		if se.Code != CodeInvalidParams {
			t.Errorf("code = %d, want %d (-32602)", se.Code, CodeInvalidParams)
		}
		if want := `browser.focus: unknown state="bogus" (must be one of normal, maximized, fullscreen)`; se.Message != want {
			t.Errorf("message = %q, want %q", se.Message, want)
		}
	})

	t.Run("state omitted defaults to normal", func(t *testing.T) {
		path := startServer(t, newStubServer())
		c, err := Dial(path)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		// No state param: the handler maps empty → normal and reaches the
		// engine (which fails on the unlaunched stub) instead of rejecting the
		// params as an unknown state.
		err = c.Call(context.Background(), MethodBrowserFocus, nil, nil)
		var se *ServerError
		if !errors.As(err, &se) {
			t.Fatalf("Call error = %v (%T), want a wrapped *ServerError", err, err)
		}
		if se.Code != CodeServerError {
			t.Errorf("code = %d, want %d (-32000)", se.Code, CodeServerError)
		}
		if want := "browsers: focus: browser not launched"; se.Message != want {
			t.Errorf("message = %q, want %q", se.Message, want)
		}
	})
}

// TestStubEngineErrorSurfacing pins that engine errors from the stub (which
// has no live pages) surface as -32000 with the engine's own message — the
// same path a real engine error takes, without launching Chromium.
func TestStubEngineErrorSurfacing(t *testing.T) {
	path := startServer(t, newStubServer())
	c, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	tests := []struct {
		name    string
		method  string
		params  any
		wantMsg string
	}{
		{
			name:    "page.open on unlaunched browser",
			method:  MethodPageOpen,
			params:  PageOpenParams{URL: "https://example.com"},
			wantMsg: "browsers: browser not launched",
		},
		{
			name:    "page.close unknown page",
			method:  MethodPageClose,
			params:  PageCloseParams{SessionID: "page-99"},
			wantMsg: `browsers: page "page-99" not found`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.Call(ctx, tt.method, tt.params, nil)
			var se *ServerError
			if !errors.As(err, &se) {
				t.Fatalf("Call error = %v (%T), want a wrapped *ServerError", err, err)
			}
			if se.Code != CodeServerError {
				t.Errorf("code = %d, want %d (-32000)", se.Code, CodeServerError)
			}
			if se.Message != tt.wantMsg {
				t.Errorf("message = %q, want %q", se.Message, tt.wantMsg)
			}
		})
	}
}

// compile-time guard: stubbedManager satisfies the manager seam.
var _ manager = (*stubbedManager)(nil)
