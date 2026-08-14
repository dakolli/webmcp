package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"webmcp/pkg/browsers"
)

// maxMessageBytes caps a single newline-delimited request line at 1 MiB; a
// larger line ends the connection (bufio.Scanner cannot recover mid-line).
const maxMessageBytes = 1 << 20

// DefaultSocketPath returns the default unix socket path: $XDG_RUNTIME_DIR/
// web-mcp.sock when XDG_RUNTIME_DIR is set and the directory exists, else
// ~/.web-mcp.sock, else .web-mcp.sock in the current directory (when the home
// directory cannot be resolved). Shared with the client side (WEB-29).
func DefaultSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return filepath.Join(dir, "web-mcp.sock")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".web-mcp.sock")
	}
	return ".web-mcp.sock"
}

// Server is the long-lived JSON-RPC 2.0 server that exposes a browsers.Manager
// over a unix socket. Construct with NewServer, bind the socket with Bind, then
// serve connections until the context is cancelled or Close is called.
type Server struct {
	// mgr is the engine the JSON-RPC methods dispatch against (the internal
	// manager seam — *browsers.Manager in production, a stub in tests).
	mgr manager

	// mu guards listener, path, conns and closed.
	mu sync.Mutex
	// listener is the bound unix socket listener; nil until Bind succeeds.
	listener net.Listener
	// path is the bound socket path (removed on Close).
	path string
	// conns tracks the live connections so Close can shut them all down.
	conns map[net.Conn]struct{}
	// closed marks the server as stopped; Close is idempotent after this.
	closed bool

	// shutdownCtx is cancelled by requestShutdown (a server.shutdown request)
	// so the Serve loop stops gracefully, in addition to the caller's ctx. The
	// manager is not touched here — the serve process tears it down itself.
	shutdownCtx context.Context
	// cancelShutdown cancels shutdownCtx; see requestShutdown.
	cancelShutdown context.CancelFunc
}

// NewServer constructs a Server that dispatches JSON-RPC methods against mgr.
// The manager's lifecycle stays with the caller (`web-cli serve` constructs
// it, binds the socket and calls Shutdown on exit).
func NewServer(mgr *browsers.Manager) *Server {
	return newServer(mgr)
}

// newServer is the internal constructor shared by NewServer and the package
// tests, which inject a manager stub through the seam so no test path can
// launch Chromium.
func newServer(mgr manager) *Server {
	shutdownCtx, cancel := context.WithCancel(context.Background())
	return &Server{
		mgr:            mgr,
		conns:          make(map[net.Conn]struct{}),
		shutdownCtx:    shutdownCtx,
		cancelShutdown: cancel,
	}
}

// Bind prepares the unix socket at path and starts listening. Stale-socket
// handling: when the path already exists the server probes it with a ~1s dial
// — a live server answers and Bind fails with an actionable "already running"
// error; a dead socket (no answer) is removed and the path is reused. The
// socket is chmod 0600 so only the owning user can connect (the transport
// carries no authentication).
func (s *Server) Bind(path string) error {
	if err := cleanupStaleSocket(path); err != nil {
		return err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("ipc: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return fmt.Errorf("ipc: chmod %s: %w", path, err)
	}
	s.mu.Lock()
	s.listener = l
	s.path = path
	s.mu.Unlock()
	return nil
}

// cleanupStaleSocket removes a dead socket file at path, or fails with an
// actionable error when a live server is already listening there.
func cleanupStaleSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to clean
		}
		return fmt.Errorf("ipc: stat %s: %w", path, err)
	}
	// The path exists: probe it. A live server's listen backlog answers the
	// dial immediately; a stale socket from a dead process refuses.
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("web-cli server already running at %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("ipc: remove stale socket %s: %w", path, err)
	}
	return nil
}

// Serve runs the accept loop until ctx is cancelled or Close is called; it
// returns nil on a clean stop (spurious accept errors after Close are
// suppressed). Each accepted connection is handled in its own goroutine; per-
// page rod access is serialized by the pageops helpers' per-page locks and the
// Manager's own mutex, so no extra locking is needed beyond the conn set.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	l := s.listener
	if l == nil {
		s.mu.Unlock()
		return errors.New("ipc: server not bound; call Bind first")
	}
	if s.closed {
		s.mu.Unlock()
		return errors.New("ipc: server already closed")
	}
	s.mu.Unlock()

	// Stop the accept loop and close every connection when ctx is done
	// (SIGINT/SIGTERM cancels the signal-bound ctx in cmd/web-cli/main.go) or
	// when a server.shutdown request arrives (requestShutdown). Close is
	// idempotent, so both goroutines may race harmlessly.
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	go func() {
		<-s.shutdownCtx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if s.isClosed() {
				return nil // clean stop via Close (or ctx cancellation)
			}
			return fmt.Errorf("ipc: accept: %w", err)
		}
		s.track(conn)
		go s.serveConn(ctx, conn)
	}
}

// requestShutdown asks the serve loop to stop gracefully. It is invoked by a
// successful server.shutdown request, but only AFTER dispatch has flushed the
// response line, so the requesting client reliably reads its {ok: true} result
// before the connections are closed.
func (s *Server) requestShutdown() {
	s.cancelShutdown()
}

// Close stops the server: it closes the listener, every live connection and
// removes the socket file. It is idempotent — subsequent calls are no-ops.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	l := s.listener
	s.listener = nil
	path := s.path
	s.path = ""
	conns := s.conns
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	if l != nil {
		_ = l.Close()
	}
	for c := range conns {
		_ = c.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
	return nil
}

// serveConn reads newline-delimited requests from one connection until EOF,
// dispatching each and writing exactly one response line. A malformed JSON
// line gets a -32700 parse-error response and the loop continues; a line over
// the 1 MiB cap (bufio.Scanner error) just ends the connection.
func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.untrack(conn)
	}()

	// connCtx outlives the request so the 30s per-request timeout derives from
	// it; cancelling connCtx (ctx done) aborts any in-flight request.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue // tolerate blank lines
		}
		req, rpcErr := decodeRequest(line)
		if rpcErr != nil {
			// Malformed line: JSON-RPC parse/invalid-request error, continue.
			s.writeLine(conn, Response{JSONRPC: "2.0", Error: rpcErr})
			continue
		}
		s.dispatch(connCtx, conn, req)
	}
	// EOF (or scanner.Err() — e.g. ErrTooLong) closes the connection via defer.
}

// dispatch runs one request against the method handlers under a 30s per-call
// timeout (mirroring defaultCmdTimeout and the MCP tools), then encodes one
// response line. Notifications (nil ID) are executed but get no response.
func (s *Server) dispatch(connCtx context.Context, conn net.Conn, req Request) {
	rctx, cancel := context.WithTimeout(connCtx, 30*time.Second)
	defer cancel()

	result, rpcErr := s.handle(rctx, req)
	// A successful server.shutdown stops the serve loop only after the
	// response is flushed: the defer runs when this function returns, which is
	// after the writeLine below. Notifications (nil ID) still stop the loop —
	// the defer is registered before the early return.
	if req.Method == MethodServerShutdown && rpcErr == nil {
		defer s.requestShutdown()
	}
	if req.ID == nil {
		return // JSON-RPC notification — no response
	}
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	s.writeLine(conn, resp)
}

// decodeRequest parses one newline-delimited JSON request line. A JSON syntax
// error is a -32700 parse error; valid JSON that is not a JSON-RPC 2.0 request
// object (wrong/missing jsonrpc or missing method) is a -32600 invalid request.
func decodeRequest(line []byte) (Request, *Error) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Request{}, &Error{
			Code:    CodeParseError,
			Message: fmt.Sprintf("parse error: %v", err),
		}
	}
	if req.JSONRPC != "2.0" {
		return Request{}, &Error{
			Code:    CodeInvalidRequest,
			Message: fmt.Sprintf("invalid request: jsonrpc must be \"2.0\", got %q", req.JSONRPC),
		}
	}
	if req.Method == "" {
		return Request{}, &Error{
			Code:    CodeInvalidRequest,
			Message: "invalid request: missing method",
		}
	}
	return req, nil
}

// writeLine encodes one response as a single LF-terminated JSON line. A short
// write deadline guards against a stuck client that stopped reading (a full
// socket buffer would otherwise block this connection's goroutine forever).
func (s *Server) writeLine(conn net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		return // response types are plain values; marshal cannot fail
	}
	b = append(b, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = conn.Write(b)
}

// track adds conn to the live-conn set so Close can shut it down.
func (s *Server) track(conn net.Conn) {
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
}

// untrack removes conn from the live-conn set (its handler goroutine ended).
func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// isClosed reports whether the server has been stopped.
func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
