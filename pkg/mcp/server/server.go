// Package server implements the web-mcp MCP-over-SSE HTTP server as a library.
// An injected *browsers.Manager backs every registered tool, and the listen
// address is configurable, so multiple instances — the standalone cmd/mcp
// binary and the `web-cli serve mcp` daemon — can coexist without colliding on
// a hardcoded port. The package translates JSON-RPC calls into the pkg/mcp/tools
// registry and never touches go-rod directly: all browser work goes through
// pkg/browsers (AGENTS.md boundary).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
	"webmcp/pkg/mcp/tools"
)

// shutdownTimeout bounds the HTTP server drain performed on context
// cancellation, so attached SSE clients cannot block process exit forever.
const shutdownTimeout = 5 * time.Second

// Server is an MCP-over-SSE HTTP server backed by a browsers.Manager. Construct
// with New; the manager's lifecycle stays with the caller (cmd/mcp/main.go and
// the `web-cli serve mcp` loop both construct the manager, serve, and call
// mgr.Shutdown on exit).
type Server struct {
	// mgr is the engine backing every registered tool.
	mgr *browsers.Manager
	// addr is the HTTP listen address (e.g. ":4099").
	addr string
	// registry holds the MCP tools, all wired to mgr.
	registry *tools.Registry
	// httpSrv is the underlying HTTP server; constructed in New.
	httpSrv *http.Server
	// ln is an optional pre-bound listener. When set (via SetListener), Serve
	// serves on it instead of binding addr itself, so a caller can surface a
	// bind failure synchronously before any serving goroutine starts.
	ln net.Listener

	// sessionsMu guards sessions and labels: the SSE session_id → outbound
	// message channel map (an SSE stream buffers responses for its client) and
	// the session_id → client label map captured at initialize.
	sessionsMu sync.Mutex
	sessions   map[string]chan []byte
	labels     map[string]string
}

// New constructs an MCP Server listening on addr (e.g. ":4099") that drives the
// injected manager's live browser. Nothing is served until Serve is called.
func New(mgr *browsers.Manager, addr string) *Server {
	s := &Server{
		mgr:      mgr,
		addr:     addr,
		registry: registerAll(mgr),
		sessions: make(map[string]chan []byte),
		labels:   make(map[string]string),
	}
	s.httpSrv = &http.Server{Addr: addr, Handler: s.Handler()}
	return s
}

// SetListener injects a pre-bound net.Listener that Serve uses instead of
// binding the configured address itself. This lets a caller detect a bind
// failure synchronously (net.Listen returns the error) before starting any
// serving goroutine — e.g. `web-cli serve mcp` binds the MCP port up front
// and falls back to a warning instead of an async log. When no listener is
// set, Serve binds the configured address itself.
func (s *Server) SetListener(ln net.Listener) {
	s.ln = ln
}

// Handler returns the HTTP handler exposing the MCP routes: /sse (the
// Server-Sent Events stream), /message (JSON-RPC POST) and / (diagnostics).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", s.handleSSE)
	mux.HandleFunc("/message", s.handleMessage)
	mux.HandleFunc("/", s.handleHome)
	return mux
}

// Serve runs the HTTP server until ctx is cancelled (SIGINT/SIGTERM, or the
// serve loop stopping in the serve-mcp daemon); it returns nil on a graceful
// stop and a wrapped error on failure. When a listener was injected via
// SetListener it serves on that listener (returning http.ErrServerClosed on a
// graceful shutdown, same as ListenAndServe); otherwise it binds the
// configured address itself. Attached SSE clients are drained with a bounded
// shutdown; if the drain times out the remaining connections are force-closed
// so the process can exit.
func (s *Server) Serve(ctx context.Context) error {
	// Stop the HTTP server when ctx is cancelled: the background goroutine
	// drains it (http.Server.Shutdown), which makes Serve/ListenAndServe
	// return http.ErrServerClosed.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			// The graceful drain hit its deadline — typically attached SSE
			// clients that never idle out. Force-drop the remaining active
			// connections so the process can exit promptly; the browser
			// teardown (the caller's mgr.Shutdown) still runs.
			log.Printf("web-mcp: http shutdown timed out: %v; forcing connections closed", err)
			if closeErr := s.httpSrv.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				log.Printf("web-mcp: http force-close: %v", closeErr)
			}
		}
	}()

	log.Printf("web-mcp Server listening on http://0.0.0.0%s", s.addr)
	if s.ln != nil {
		// Serve on the pre-bound listener (web-cli serve mcp): the caller
		// already owns the bind, so a port conflict surfaced synchronously
		// instead of as an async log after launch.
		if err := s.httpSrv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("web-mcp: server aborted: %w", err)
		}
		return nil
	}
	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web-mcp: server aborted: %w", err)
	}
	return nil
}

// registerAll wires every MCP tool to the given manager, so a page opened by
// fetch_page can be read by page_fetch and driven by page_act, and a single
// Browser (one Chromium instance) backs all sessions.
func registerAll(mgr *browsers.Manager) *tools.Registry {
	r := tools.NewEmptyRegistry()
	r.Register(tools.NewFetch(mgr))         // fetch_page
	r.Register(tools.NewPage(mgr))          // page_fetch
	r.Register(tools.NewAct(mgr))           // page_act
	r.Register(tools.NewSessionClose(mgr))  // session_close
	r.Register(tools.NewSessionList(mgr))   // session_list
	r.Register(tools.NewBrowserCreate(mgr)) // browser_create
	r.Register(tools.NewBrowserList(mgr))   // browser_list
	r.Register(tools.NewBrowserClose(mgr))  // browser_close
	r.Register(tools.NewPageList(mgr))      // page_list

	r.RegisterPrompt(tools.NewFetchAndSummarizePrompt())        // fetch_and_summarize
	r.RegisterPrompt(tools.NewInspectPageDecompositionPrompt()) // inspect_page_decomposition
	r.RegisterPrompt(tools.NewFormSearchAndSubmitPrompt())      // form_search_and_submit
	r.RegisterPrompt(tools.NewBrowserSetupAndEmulatePrompt())   // browser_setup_and_emulate
	r.RegisterPrompt(tools.NewManageSessionLifecyclePrompt())   // manage_session_lifecycle
	return r
}

// handleHome serves a simple diagnostics text page, or routes to SSE/JSON-RPC
// when appropriate based on request method and Accept headers.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodPost {
		s.handleMessage(w, r)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.handleSSE(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "web-mcp Server is active on port %s.\nUse /sse (SSE Connection) and /message (POST requests) for MCP operations.\n", s.addr)
}

// handleSSE establishes the Server-Sent Events stream (or delegates POST requests to handleMessage).
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == http.MethodPost {
		s.handleMessage(w, r)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
	sessionChan := make(chan []byte, 100)

	s.sessionsMu.Lock()
	s.sessions[sessionID] = sessionChan
	s.sessionsMu.Unlock()

	defer func() {
		s.sessionsMu.Lock()
		delete(s.sessions, sessionID)
		delete(s.labels, sessionID)
		s.sessionsMu.Unlock()
		close(sessionChan)
		log.Printf("Session %s disconnected", sessionID)
	}()

	log.Printf("Session %s connected", sessionID)

	// Instruct client where to POST payload messages
	fmt.Fprintf(w, "event: endpoint\ndata: /message?session_id=%s\n\n", sessionID)
	flusher.Flush()

	for {
		select {
		case msg, open := <-sessionChan:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleMessage processes incoming JSON-RPC calls.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req models.JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.sendErrorDirectly(w, nil, -32700, "Parse error")
		return
	}

	// A JSON-RPC notification is a request without an id (JSON-RPC 2.0 §2.3):
	// the server MUST NOT reply to it. Silently accept it instead of routing
	// it into processRequest, which would emit a spurious -32601 response.
	if req.ID == nil {
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	resp := s.processRequest(withSSESession(r.Context(), sessionID), req)
	respBytes, err := json.Marshal(resp)
	if err != nil {
		s.sendErrorDirectly(w, req.ID, -32603, "Internal marshalling error")
		return
	}

	var sessionChan chan []byte

	if sessionID != "" {
		s.sessionsMu.Lock()
		sessionChan = s.sessions[sessionID]
		s.sessionsMu.Unlock()
	}

	if sessionChan != nil {
		sessionChan <- respBytes
		w.WriteHeader(http.StatusOK)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}
}

// sendErrorDirectly writes a JSON-RPC error response straight to the wire,
// bypassing the session channel (used for parse/marshal failures that occur
// before a session could be resolved).
func (s *Server) sendErrorDirectly(w http.ResponseWriter, id interface{}, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	resp := models.JSONRPCResponse{
		Jsonrpc: "2.0",
		ID:      id,
		Error: models.JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
	bytes, _ := json.Marshal(resp)
	w.Write(bytes)
}

// sseSessionCtxKey carries the SSE connection id on a request context so the
// owner identity can be injected before tools run.
type sseSessionCtxKey struct{}

// withSSESession records the SSE connection id on ctx. An empty id (direct POST
// with no session) leaves ctx untouched, so the owner falls back to unowned.
func withSSESession(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sseSessionCtxKey{}, id)
}

// sseSessionFrom returns the SSE connection id recorded on ctx, or "".
func sseSessionFrom(ctx context.Context) string {
	id, _ := ctx.Value(sseSessionCtxKey{}).(string)
	return id
}

// rememberClientLabel records the client name a session announced at initialize.
func (s *Server) rememberClientLabel(sessionID, name string) {
	if sessionID == "" || name == "" {
		return
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.labels == nil {
		s.labels = make(map[string]string)
	}
	s.labels[sessionID] = name
}

// clientLabel returns the display label for a session: the client name when it
// announced one, else a short synthetic label derived from the session id.
func (s *Server) clientLabel(sessionID string) string {
	s.sessionsMu.Lock()
	name := s.labels[sessionID]
	s.sessionsMu.Unlock()
	if name != "" {
		return name
	}
	short := sessionID
	if len(short) > 6 {
		short = short[len(short)-6:]
	}
	return "client-" + short
}

// processRequest runs one JSON-RPC request against the tool registry: it
// injects the caller's owner identity, handles the MCP handshake (initialize),
// the tool inventory (tools/list) and tool invocations (tools/call), returning a
// JSON-RPC response.
func (s *Server) processRequest(ctx context.Context, req models.JSONRPCRequest) models.JSONRPCResponse {
	resp := models.JSONRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID,
	}

	if sessionID := sseSessionFrom(ctx); sessionID != "" {
		ctx = browsers.WithOwnerKey(ctx, sessionID)
		ctx = browsers.WithOwnerLabel(ctx, s.clientLabel(sessionID))
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err == nil {
				s.rememberClientLabel(sseSessionFrom(ctx), params.ClientInfo.Name)
			}
		}
		resp.Result = models.InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo: models.ServerInfo{
				Name:    "web-mcp",
				Version: "0.1.0",
			},
			Capabilities: models.ServerCapabilities{
				Tools:   make(map[string]interface{}),
				Prompts: make(map[string]interface{}),
			},
			Instructions: "web-mcp provides stateful Chromium browser automation tools (fetch_page, page_fetch, page_act, session_close, browser_create, etc.) supporting element clicks, text input, key presses/combos (press_key), coordinate mouse movement/clicks (click_at, hover_at), drag-and-drop (drag_and_drop), and file uploads (upload_file). Predefined task templates are available via the prompts/list and prompts/get MCP protocol endpoints to guide common workflows such as page exploration, form filling, and content extraction.",
		}

	case "tools/list":
		resp.Result = models.ListToolsResult{
			Tools: s.registry.List(),
		}

	case "tools/call":
		var callParams models.CallToolRequest
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			resp.Error = models.JSONRPCError{Code: -32602, Message: "Invalid parameters"}
			return resp
		}
		resultText, err := s.registry.Execute(ctx, callParams.Name, callParams.Arguments)
		if err != nil {
			resp.Error = models.JSONRPCError{Code: -32603, Message: err.Error()}
			return resp
		}
		resp.Result = models.CallToolResult{
			Content: []models.ContentBlock{
				{
					Type: "text",
					Text: resultText,
				},
			},
		}

	case "prompts/list":
		resp.Result = models.ListPromptsResult{
			Prompts: s.registry.ListPrompts(),
		}

	case "prompts/get":
		var getParams models.GetPromptRequest
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &getParams); err != nil {
				resp.Error = models.JSONRPCError{Code: -32602, Message: "Invalid parameters"}
				return resp
			}
		}
		if getParams.Name == "" {
			resp.Error = models.JSONRPCError{Code: -32602, Message: "Missing prompt name"}
			return resp
		}
		promptRes, err := s.registry.GetPrompt(getParams.Name, getParams.Arguments)
		if err != nil {
			resp.Error = models.JSONRPCError{Code: -32603, Message: err.Error()}
			return resp
		}
		resp.Result = promptRes

	default:
		resp.Error = models.JSONRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	return resp
}
