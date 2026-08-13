// Command webmcp-cli is the lightweight CLI mirror of the web-mcp MCP tools:
// it drives the same pkg/browsers core engine from the command line, so we
// (the developers) can experiment interactively with the exact functionality
// the agent gets over MCP. It talks directly to pkg/browsers — never through
// pkg/mcp — mirroring the core-vs-MCP boundary kept by pkg/mcp/tools.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/cmd"
)

func main() {
	// Cancel ctx on SIGINT/SIGTERM so a hung engine call cannot outlive the
	// process; ExecuteContext plumbs it into every cobra command.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The shared manager: headful Chromium, stealth on — the engine defaults.
	// Constructed once and injected into the root command so every subcommand
	// operates on the SAME browser (mirrors cmd/mcp/main.go's registry wiring).
	mgr := browsers.NewManager()
	defer mgr.Shutdown(context.Background()) // graceful teardown (best-effort)

	if err := cmd.NewRootCmd(mgr).ExecuteContext(ctx); err != nil {
		log.Fatal(err)
	}
}
