// Command mcp is the standalone web-mcp MCP-over-SSE server: a thin wrapper
// over pkg/mcp/server that parses --port, constructs the browsers.Manager and
// serves until SIGINT/SIGTERM. The same library backs `web-cli serve mcp`, so
// the daemon and this binary share one code path (WEB-42).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/config"
	"webmcp/pkg/mcp/server"
)

func main() {
	// Keep the default in sync with web-cli's --port default
	// (pkg/cli/cmd.defaultServeMcpPort, WEB-43): the const cannot cross the
	// package boundary, so the two defaults must stay in lockstep by hand.
	port := flag.Int("port", 4099, "HTTP port for the MCP/SSE server")
	bind := flag.String("bind", server.DefaultBind,
		"host/interface to bind (default 127.0.0.1; 0.0.0.0 exposes the unauthenticated server to the network)")
	flag.Parse()

	addr, err := server.ListenAddress(*bind, *port)
	if err != nil {
		log.Fatalf("mcp: %v", err)
	}

	// The shared manager: headful Chromium, stealth on — the engine defaults.
	// Constructed here and injected into the MCP server so the tools own one
	// live browser for the process's lifetime. This binary drives agents only,
	// so browsers are ephemeral unless a caller passes persist, and idle
	// browsers are swept after the configured TTL.
	browserTTL := browsers.DefaultBrowserTTL
	if cfg, err := config.LoadConfig(); err == nil {
		browserTTL = cfg.BrowserIdleTTL(browserTTL)
	}
	mgr := browsers.NewManager(
		browsers.WithPersistentDefault(false),
		browsers.WithBrowserTTL(browserTTL),
	)
	// Bounded so a hung page cannot block process exit.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer mgr.Shutdown(shutdownCtx) // graceful teardown (best-effort)

	// Stop the HTTP server when SIGINT or SIGTERM arrives: the signal-bound
	// ctx makes server.Serve drain the server and return.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(mgr, addr)
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
	log.Print("web-mcp: shut down cleanly")
}
