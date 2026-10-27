// Command web-cli is the lightweight CLI mirror of the web-mcp MCP tools:
// it drives the same pkg/browsers core engine from the command line, so we
// (the developers) can experiment interactively with the exact functionality
// the agent gets over MCP. It talks directly to pkg/browsers — never through
// pkg/mcp — mirroring the core-vs-MCP boundary kept by pkg/mcp/tools.
//
// Example usage (human-facing):
//
//	# 1. Start the shared browser as a background daemon (optional — commands
//	#    launch the browser lazily). fetch/page commands then persist across
//	#    invocations over the IPC socket.
//	web-cli serve
//
//	# 2. Open a URL. The printed session id (page-1) is what you pass to the
//	#    page commands — with one page open, you can omit it entirely.
//	web-cli fetch https://news.ycombinator.com
//
//	# 3. Read and drive the live page. 'last' means the most recently opened
//	#    page; bare numbers resolve to page-N.
//	web-cli page list
//	web-cli page fetch links
//	web-cli page act click "#search"
//	web-cli page fetch 12 text
//
//	# 4. Close the page, the browser, and the daemon.
//	web-cli page close
//	web-cli serve stop
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/cmd"
	"webmcp/pkg/config"
)

func main() {
	// Cancel ctx on SIGINT/SIGTERM so a hung engine call cannot outlive the
	// process; ExecuteContext plumbs it into every cobra command.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The shared manager: headful Chromium, stealth on, ephemeral profiles by
	// default (a caller opts into persistence), and the configured browser idle
	// TTL. Constructed once and injected into the root command so every
	// subcommand operates on the SAME browser (mirrors cmd/mcp/main.go's
	// registry wiring).
	ttl := browsers.DefaultBrowserTTL
	if cfg, err := config.LoadConfig(); err == nil {
		ttl = cfg.BrowserIdleTTL(ttl)
	}
	mgr := browsers.NewManager(
		browsers.WithPersistentDefault(false),
		browsers.WithBrowserTTL(ttl),
		browsers.WithLogger(browsers.NewLogger(os.Stderr, nil)),
	)
	defer mgr.Shutdown(context.Background()) // graceful teardown (best-effort)

	if err := cmd.NewRootCmd(mgr).ExecuteContext(ctx); err != nil {
		log.Fatal(err)
	}
}
