// Command mcp is the standalone web-mcp MCP-over-SSE server: a thin wrapper
// over pkg/mcp/server that parses --port, constructs the browsers.Manager and
// serves until SIGINT/SIGTERM. The same library backs `web-cli serve mcp`, so
// the daemon and this binary share one code path (WEB-42).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/server"
)

func main() {
	port := flag.Int("port", 4099, "HTTP port for the MCP/SSE server")
	flag.Parse()

	// The shared manager: headful Chromium, stealth on — the engine defaults.
	// Constructed here and injected into the MCP server so the tools own one
	// live browser for the process's lifetime.
	mgr := browsers.NewManager()
	// Bounded so a hung page cannot block process exit.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer mgr.Shutdown(shutdownCtx) // graceful teardown (best-effort)

	// Stop the HTTP server when SIGINT or SIGTERM arrives: the signal-bound
	// ctx makes server.Serve drain the server and return.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(mgr, fmt.Sprintf(":%d", *port))
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
	log.Print("web-mcp: shut down cleanly")
}
