package ipc

// Wire-contract pins for the server half: JSON-RPC 2.0 framing (error codes,
// notification suppression, scanner cap, malformed-line continuation), the
// Bind/Close/Serve lifecycle, and DefaultSocketPath resolution.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServerPingRoundTrip pins the exact ping wire shape: a request id 1 ping
// answers {"jsonrpc":"2.0","id":1,"result":{"ok":true}} with no error object.
func TestServerPingRoundTrip(t *testing.T) {
	path := startServer(t, newStubServer())
	conn := dialRaw(t, path)
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	line := readLineFrom(t, r)

	want := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	if got := string(line[:len(line)-1]); got != want {
		t.Errorf("ping response line = %s, want %s", got, want)
	}

	var resp Response
	resp.Result = &PingResult{}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode ping response: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("ping response has error: %+v", resp.Error)
	}
	if pr, ok := resp.Result.(*PingResult); !ok || !pr.OK {
		t.Errorf("ping result = %v, want {ok: true}", resp.Result)
	}
}

// TestServerUnknownMethod pins the -32601 method-not-found response: it names
// the method and echoes the request id.
func TestServerUnknownMethod(t *testing.T) {
	path := startServer(t, newStubServer())
	conn := dialRaw(t, path)
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":7,"method":"frobnicate"}` + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line := readLineFrom(t, r)

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected an error object, got result")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("error code = %d, want %d (-32601)", resp.Error.Code, CodeMethodNotFound)
	}
	if resp.Error.Message != `unknown method "frobnicate"` {
		t.Errorf("error message = %q, want %q", resp.Error.Message, `unknown method "frobnicate"`)
	}
	if id, ok := resp.ID.(float64); !ok || id != 7 {
		t.Errorf("response id = %v, want 7", resp.ID)
	}
}

// TestServerMalformedJSONContinues pins the parse-error contract: a malformed
// line gets a -32700 response with a null id, and the connection STAYS alive —
// a second, valid request on the same connection still succeeds.
func TestServerMalformedJSONContinues(t *testing.T) {
	path := startServer(t, newStubServer())
	conn := dialRaw(t, path)
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	first := readLineFrom(t, r)
	var parseErr Response
	if err := json.Unmarshal(first, &parseErr); err != nil {
		t.Fatalf("decode parse-error response: %v", err)
	}
	if parseErr.Error == nil || parseErr.Error.Code != CodeParseError {
		t.Errorf("first response error = %+v, want code %d (-32700)", parseErr.Error, CodeParseError)
	}
	if parseErr.ID != nil {
		t.Errorf("parse-error response id = %v, want null", parseErr.ID)
	}

	// The connection must still be usable: a valid ping follows the error.
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")); err != nil {
		t.Fatalf("write valid request after parse error: %v", err)
	}
	second := readLineFrom(t, r)
	if !bytes.Contains(second, []byte(`"result":{"ok":true}`)) {
		t.Errorf("second response = %s, want a successful ping result", second)
	}
}

// TestServerNonObjectJSONIsParseError documents the accepted behavior for
// valid-JSON-but-non-object lines (e.g. [] or 42): they are treated as parse
// errors (-32700) today. This is a documented, acceptable edge — not a
// -32600, because the value cannot be decoded into a Request object.
func TestServerNonObjectJSONIsParseError(t *testing.T) {
	for _, line := range []string{"[]\n", "42\n"} {
		t.Run(strings.TrimSpace(line), func(t *testing.T) {
			path := startServer(t, newStubServer())
			conn := dialRaw(t, path)
			r := bufio.NewReader(conn)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

			if _, err := conn.Write([]byte(line)); err != nil {
				t.Fatalf("write %q: %v", line, err)
			}
			resp := readLineFrom(t, r)
			var parsed Response
			if err := json.Unmarshal(resp, &parsed); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if parsed.Error == nil || parsed.Error.Code != CodeParseError {
				t.Errorf("error = %+v, want code %d (-32700) for non-object JSON %q", parsed.Error, CodeParseError, line)
			}
		})
	}
}

// TestServerNotificationNoResponse pins JSON-RPC notification semantics: a
// request with no id is executed but produces NO response line — the read
// side observes a timeout, not a line and not a connection close.
func TestServerNotificationNoResponse(t *testing.T) {
	path := startServer(t, newStubServer())
	conn := dialRaw(t, path)
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))

	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","method":"ping"}` + "\n")); err != nil {
		t.Fatalf("write notification: %v", err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("notification produced a response line: %q", buf[:n])
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("expected a read timeout (no response line), got %v", err)
	}
}

// TestServerOversizedLineClosesConnection pins the scanner cap: a request line
// over maxMessageBytes (1 MiB) cannot be recovered mid-line, so the server
// ends the connection (the read side sees EOF/reset instead of a response).
func TestServerOversizedLineClosesConnection(t *testing.T) {
	path := startServer(t, newStubServer())
	conn := dialRaw(t, path)

	line := append(bytes.Repeat([]byte{'a'}, maxMessageBytes+64), '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(line); err != nil && !isClosedConnErr(err) {
		t.Fatalf("write oversized line: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if n != 0 {
		t.Fatalf("read %d bytes, want none (the connection should be closed)", n)
	}
	if !isClosedConnErr(err) {
		t.Fatalf("read error = %v, want a closed-connection error (io.EOF or a reset), not a timeout", err)
	}
}

// isClosedConnErr reports whether err indicates the peer closed the
// connection (EOF or a reset), as opposed to a timeout or other failure.
func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	return true
}

// TestServerBadParams pins the -32602 invalid-params responses for page.open:
// both a missing required field and a type-mismatched field name the offending
// field in the message. All are validated before any manager involvement.
func TestServerBadParams(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string // substring the -32602 message must contain
	}{
		{
			name: "page.open missing url",
			line: `{"jsonrpc":"2.0","id":1,"method":"page.open","params":{}}`,
			want: `page.open: missing required param "url"`,
		},
		{
			name: "page.open url type mismatch",
			line: `{"jsonrpc":"2.0","id":1,"method":"page.open","params":{"url":42}}`,
			want: "url", // encoding/json names the struct field in the decode error
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, newStubServer())
			conn := dialRaw(t, path)
			r := bufio.NewReader(conn)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

			if _, err := conn.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("write request: %v", err)
			}
			line := readLineFrom(t, r)
			var resp Response
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
				t.Fatalf("error = %+v, want code %d (-32602)", resp.Error, CodeInvalidParams)
			}
			if !strings.Contains(resp.Error.Message, tt.want) {
				t.Errorf("error message = %q, want it to contain %q", resp.Error.Message, tt.want)
			}
		})
	}
}

// TestServerBadParamsPageFetchUnknownWhat pins the -32602 error for an unknown
// page.fetch extractor — again validated before any manager involvement.
func TestServerBadParamsPageFetchUnknownWhat(t *testing.T) {
	path := startServer(t, newStubServer())
	conn := dialRaw(t, path)
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	req := `{"jsonrpc":"2.0","id":1,"method":"page.fetch","params":{"session_id":"page-1","what":"bogus"}}` + "\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line := readLineFrom(t, r)
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v, want code %d (-32602)", resp.Error, CodeInvalidParams)
	}
	if want := `page.fetch: unknown what="bogus" (must be one of html, text, links, meta, dom, css, js, screenshot, interactives, a11y, network, console, body, article, marks, security, storage)`; resp.Error.Message != want {
		t.Errorf("error message = %q, want %q", resp.Error.Message, want)
	}
}

// TestServerPageFetchAcceptsSharedAspects pins that newer extractors pass the
// shared what validation: the stub has no live page, so the call must fail later
// with a -32000 engine error, never -32602.
func TestServerPageFetchAcceptsSharedAspects(t *testing.T) {
	path := startServer(t, newStubServer())

	for _, what := range []string{"article", "marks", "body", "security"} {
		t.Run(what, func(t *testing.T) {
			conn := dialRaw(t, path)
			r := bufio.NewReader(conn)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

			req := `{"jsonrpc":"2.0","id":1,"method":"page.fetch","params":{"session_id":"page-1","what":"` + what + `"}}` + "\n"
			if _, err := conn.Write([]byte(req)); err != nil {
				t.Fatalf("write request: %v", err)
			}
			line := readLineFrom(t, r)
			var resp Response
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Error == nil {
				t.Fatalf("error = nil, want the stub's missing-page error")
			}
			if resp.Error.Code != CodeServerError {
				t.Fatalf("code = %d (%q), want %d (-32000); what=%q must pass validation", resp.Error.Code, resp.Error.Message, CodeServerError, what)
			}
			if strings.Contains(resp.Error.Message, "unknown what") {
				t.Errorf("message = %q, want the aspect rejected only after validation", resp.Error.Message)
			}
		})
	}
}

// TestBindAlreadyRunning pins the live-socket guard: Bind on a path a live
// server already listens on fails with the actionable "already running" error.
func TestBindAlreadyRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.sock")
	s1 := newStubServer()
	if err := s1.Bind(path); err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	t.Cleanup(func() { _ = s1.Close() })

	s2 := newStubServer()
	err := s2.Bind(path)
	if err == nil {
		t.Fatal("Bind on a live socket succeeded, want \"already running\" error")
	}
	if want := "web-cli server already running at " + path; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	_ = s2.Close()
}

// TestBindStaleSocketAutoClean pins the stale-socket recovery: a socket file
// left behind by a dead process is removed and the path is reused.
func TestBindStaleSocketAutoClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.sock")
	// Create a socket file with no live listener — the classic stale-socket
	// shape. Go's UnixListener unlinks on Close by default, so disable that
	// to leave the file behind, exactly as a crashed process would.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file should exist after listener close: %v", err)
	}

	s := newStubServer()
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind on stale socket should auto-clean and succeed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("socket file should exist after Bind: %v", err)
	}
	_ = s.Close()
}

// TestBindSocketMode pins the 0600 socket permission (the transport carries
// no authentication, so only the owning user may connect).
func TestBindSocketMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode.sock")
	s := newStubServer()
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
}

// TestCloseIdempotent pins the Close lifecycle: the socket file is removed on
// the first call and a second call is a silent no-op.
func TestCloseIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close.sock")
	s := newStubServer()
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file should exist after Bind: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket file should be removed after Close (stat err = %v)", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close must be a no-op, got %v", err)
	}
}

// TestServeCtxCancel pins that Serve returns nil on ctx cancellation with no
// spurious errors.
func TestServeCtxCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.sock")
	s := newStubServer()
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after ctx cancel = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after ctx cancellation")
	}
}

// TestDefaultSocketPath pins the path resolution: XDG_RUNTIME_DIR (existing
// dir) wins, otherwise ~/.web-mcp.sock.
func TestDefaultSocketPath(t *testing.T) {
	t.Run("XDG_RUNTIME_DIR set", func(t *testing.T) {
		dir := t.TempDir() // existing directory
		t.Setenv("XDG_RUNTIME_DIR", dir)
		if got, want := DefaultSocketPath(), filepath.Join(dir, "web-mcp.sock"); got != want {
			t.Errorf("DefaultSocketPath() = %q, want %q", got, want)
		}
	})
	t.Run("XDG_RUNTIME_DIR unset", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "")
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("no resolvable home directory in this environment")
		}
		if got, want := DefaultSocketPath(), filepath.Join(home, ".web-mcp.sock"); got != want {
			t.Errorf("DefaultSocketPath() = %q, want %q", got, want)
		}
	})
}
