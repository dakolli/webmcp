// Client-side half of the web-cli IPC transport: a JSON-RPC 2.0 client for the
// unix socket served by `web-cli serve`. It is a pure wire consumer — it
// imports only the wire types this package defines (plus stdlib), never rod,
// never webmcp/pkg/browsers and never webmcp/pkg/mcp (AGENTS.md CLI boundary).
// The web-cli commands use it to forward their work to the long-lived serve
// process (WEB-29); the Server half lives in server.go.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// dialTimeout bounds the initial Dial probe and each per-call connection setup.
// A dead or stale socket must fail fast so the caller can fall back to the
// in-process path (the ~500ms Ping bound lives on the caller side).
const dialTimeout = 2 * time.Second

// ServerError is a JSON-RPC 2.0 error object returned by the serve process. It
// is wrapped with %w by Client.Call so callers can unwrap it (errors.As) while
// still surfacing the server's message verbatim in Error().
type ServerError struct {
	// Method is the JSON-RPC method that failed (e.g. "page.fetch").
	Method string
	// Code is one of the Code* constants (e.g. CodeServerError = -32000).
	Code int
	// Message is the human/LLM-actionable message the server produced.
	Message string
}

// Error implements the error interface with an actionable, wrapped chain.
func (e *ServerError) Error() string {
	return fmt.Sprintf("%s: server error %d: %s", e.Method, e.Code, e.Message)
}

// Client is a JSON-RPC 2.0 client for the web-cli IPC transport. It holds no
// persistent connection: every Call opens its own short-lived unix-socket
// connection, writes exactly one request line and reads exactly one response
// line, then closes it (the serve process handles any number of sequential
// connections). Construct with Dial; release with Close (a no-op here).
type Client struct {
	// mu guards nextID so concurrent calls get distinct JSON-RPC ids.
	mu sync.Mutex
	// nextID is the JSON-RPC request id for the next Call.
	nextID int
	// path is the unix socket path the server bound (DefaultSocketPath unless
	// overridden by the global --socket flag).
	path string
}

// Dial constructs a Client for the unix socket at path, probing that a server
// is actually listening there (a stale socket from a dead serve process fails
// fast here, so the caller can fall back to the in-process path). The probe
// connection is closed immediately; per-call connections are opened by Call.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("ipc: dial %s: %w", path, err)
	}
	_ = conn.Close()
	return &Client{path: path}, nil
}

// Close releases the Client. This transport keeps no persistent socket, so
// Close is a no-op; it exists so callers can defer a uniform Close on the
// *ipc.Client they get from the detect-and-forward helper.
func (c *Client) Close() error { return nil }

// Call sends one JSON-RPC 2.0 request (with the next incrementing id) over a
// fresh connection and decodes the response. params is marshaled into the
// request's params object (nil omits params entirely); result, when non-nil,
// must be a pointer the response's result object is decoded into. A JSON-RPC
// error object from the server is returned wrapped as %w around a *ServerError
// carrying the server's code and message. The whole call honors ctx (its
// deadline, when set, becomes the connection's read/write deadline).
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	req := Request{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("ipc: encode params for %s: %w", method, err)
		}
		req.Params = b
	}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("ipc: encode request for %s: %w", method, err)
	}
	line = append(line, '\n')

	conn, err := dialConn(ctx, c.path)
	if err != nil {
		return fmt.Errorf("ipc: dial %s: %w", c.path, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if _, err := conn.Write(line); err != nil {
		return fmt.Errorf("ipc: write %s request: %w", method, err)
	}
	respLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("ipc: read %s response: %w", method, err)
	}

	// Pre-seed Result so encoding/json decodes the response's result object
	// directly into the caller's typed pointer (nil result discards it).
	var resp Response
	resp.Result = result
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return fmt.Errorf("ipc: decode %s response: %w", method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("ipc: %w", &ServerError{
			Method:  method,
			Code:    resp.Error.Code,
			Message: resp.Error.Message,
		})
	}
	return nil
}

// Ping is the liveness probe: it calls the ping method and verifies the server
// answered {ok: true}. It doubles as the stale-socket check for the
// detect-and-forward helper (which bounds it with a ~500ms timeout).
func (c *Client) Ping(ctx context.Context) error {
	var res PingResult
	if err := c.Call(ctx, MethodPing, nil, &res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("ipc: ping: unexpected result {ok: %v}", res.OK)
	}
	return nil
}

// dialConn opens a unix connection to path, honoring ctx (cancellation and
// deadline) and falling back to dialTimeout when ctx has no deadline.
func dialConn(ctx context.Context, path string) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "unix", path)
}
