// Package main implements a Model Context Protocol (MCP) server over SSE.
// The server exposes browser tools (browser_create, browser_list,
// browser_close, page_list, fetch_page, page_fetch, page_act,
// session_close, session_list) that give LLM agents a live, stateful browser
// session via go-rod.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
	"webmcp/pkg/mcp/tools"
)

const port = ":4099"

// shutdownTimeout bounds the HTTP and browser-manager teardown performed on
// SIGINT/SIGTERM, so a hung Chromium cannot block process exit indefinitely.
const shutdownTimeout = 5 * time.Second

// mgr is the single browsers.Manager backing every registered tool. It is
// package-level so main can tear it down gracefully (closing Chromium and all
// live pages) when the server shuts down.
var mgr = browsers.NewManager()

var (
	sessionsMu sync.Mutex
	sessions   = make(map[string]chan []byte)
	registry   = func() *tools.Registry {
		// Every tool must share the SAME browsers.Manager (mgr) so that a page
		// opened by fetch_page can be read by page_fetch and driven by page_act,
		// and so a single Browser (one Chromium instance) backs all sessions.
		//
		// Configure the manager at construction, e.g.:
		//
		//   mgr := browsers.NewManager(            // optionally:
		//       browsers.WithHeadless(true),       //   headless Chromium (default headful)
		//       browsers.WithStealth(false),       //   disable go-rod/stealth (default on)
		//       browsers.WithTTL(15*time.Minute),  //   page idle TTL (default 15m)
		//   )
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
		return r
	}()
)

func main() {
	http.HandleFunc("/sse", handleSSE)
	http.HandleFunc("/message", handleMessage)
	http.HandleFunc("/", handleHome)

	// Stop the HTTP server when SIGINT or SIGTERM arrives: the background
	// goroutine drains it (srv.Shutdown), which makes ListenAndServe return
	// http.ErrServerClosed and main fall through to the browser teardown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: port}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// The graceful drain hit its deadline — typically attached SSE
			// clients that never idle out. Force-drop the remaining active
			// connections so the process can exit promptly; the browser
			// teardown below still runs.
			log.Printf("web-mcp: http shutdown timed out: %v; forcing connections closed", err)
			if closeErr := srv.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				log.Printf("web-mcp: http force-close: %v", closeErr)
			}
		}
	}()

	log.Printf("web-mcp Server listening on http://0.0.0.0%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("web-mcp: server aborted: %v", err)
	}

	// Graceful browser teardown: close Chromium and all live pages so nothing
	// leaks when the process exits. Bounded so a hung page cannot block exit.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		log.Printf("web-mcp: browser shutdown: %v", err)
	}
	log.Print("web-mcp: shut down cleanly")
}

// handleHome serves a simple diagnostics text page.
func handleHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "web-mcp Server is active on port %s.\nUse /sse (SSE Connection) and /message (POST requests) for MCP operations.\n", port)
}

// handleSSE establishes the Server-Sent Events stream.
func handleSSE(w http.ResponseWriter, r *http.Request) {
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

	sessionsMu.Lock()
	sessions[sessionID] = sessionChan
	sessionsMu.Unlock()

	defer func() {
		sessionsMu.Lock()
		delete(sessions, sessionID)
		sessionsMu.Unlock()
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
func handleMessage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

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
		sendErrorDirectly(w, nil, -32700, "Parse error")
		return
	}

	resp := processRequest(r.Context(), req)
	respBytes, err := json.Marshal(resp)
	if err != nil {
		sendErrorDirectly(w, req.ID, -32603, "Internal marshalling error")
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	var sessionChan chan []byte

	if sessionID != "" {
		sessionsMu.Lock()
		sessionChan = sessions[sessionID]
		sessionsMu.Unlock()
	}

	if sessionChan != nil {
		sessionChan <- respBytes
		w.WriteHeader(http.StatusOK)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}
}

func sendErrorDirectly(w http.ResponseWriter, id interface{}, code int, message string) {
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

func processRequest(ctx context.Context, req models.JSONRPCRequest) models.JSONRPCResponse {
	resp := models.JSONRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID,
	}

	switch req.Method {
	case "initialize":
		resp.Result = models.InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo: models.ServerInfo{
				Name:    "web-mcp",
				Version: "0.1.0",
			},
			Capabilities: models.ServerCapabilities{
				Tools: make(map[string]interface{}),
			},
		}

	case "tools/list":
		resp.Result = models.ListToolsResult{
			Tools: registry.List(),
		}

	case "tools/call":
		var callParams models.CallToolRequest
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			resp.Error = models.JSONRPCError{Code: -32602, Message: "Invalid parameters"}
			return resp
		}
		resultText, err := registry.Execute(ctx, callParams.Name, callParams.Arguments)
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

	default:
		resp.Error = models.JSONRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	return resp
}
