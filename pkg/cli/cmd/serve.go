package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
)

// NewServeCmd constructs the long-lived "serve" command: it owns the browser
// for the process's lifetime and exposes it over a local JSON-RPC 2.0 unix
// socket, so the other web-cli commands (and any local client) can drive the
// SAME live browser across invocations — page ids minted here persist.
//
// Unlike the other commands it deliberately takes NO injected *browsers.Manager:
// serve must construct its own manager with construction-time options
// (--headless, --stealth — browsers.NewManager(WithHeadless, WithStealth)), so
// the root-injected manager (constructed with engine defaults) cannot be reused.
func NewServeCmd() *cobra.Command {
	var headless, stealth bool

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the live browser over a local IPC socket",
		Long: `Serve starts the shared browser and keeps it running in the foreground,
exposing it over a local JSON-RPC 2.0 unix socket (newline-delimited JSON).
The headful Chromium window stays open until you press Ctrl-C; the other
web-cli commands and local clients connect to the socket to drive the SAME
live browser, so pages persist across invocations.

  --headless   run Chromium headless (default: headful)
  --stealth    apply go-rod/stealth to every page (default: on)
  --socket     path of the IPC socket (default: $XDG_RUNTIME_DIR/web-mcp.sock,
               else ~/.web-mcp.sock)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmdCtx(cmd) // the signal-bound ctx from cmd/web-cli/main.go

			// Resolve the socket path: --socket flag, else the default.
			path, err := cmd.Flags().GetString("socket")
			if err != nil {
				return fmt.Errorf("serve: read --socket: %w", err)
			}
			if path == "" {
				path = ipc.DefaultSocketPath()
			}

			// Construct the manager with the requested construction-time
			// options (headful + stealth are the engine defaults).
			mgr := browsers.NewManager(
				browsers.WithHeadless(headless),
				browsers.WithStealth(stealth),
			)
			// Graceful teardown of the browser and all of its pages on
			// SIGINT/SIGTERM: the signal-bound ctx is cancelled by main.go, then
			// Shutdown closes the browser and every page.
			defer mgr.Shutdown(ctx)

			// Bind the socket BEFORE launching the browser: when another serve
			// process already holds the socket, Bind fails cheaply with the
			// "already running" error instead of paying for the first-run
			// Chromium download (~100MB) and browser launch below.
			srv := ipc.NewServer(mgr) // construction only — no I/O
			if err := srv.Bind(path); err != nil {
				return fmt.Errorf("serve: %w", err)
			}

			// First run downloads Chromium (~100MB); ctx bounds the launch.
			if _, err := mgr.Open(ctx); err != nil {
				return fmt.Errorf("serve: %w", err)
			}

			cmd.Printf("browser_id: %s\n", singleBrowserID)
			cmd.Printf("socket: %s\n", path)
			cmd.Printf("press Ctrl-C to stop\n")

			// Blocks until SIGINT/SIGTERM (ctx cancellation) or an unrecoverable
			// server error.
			if err := srv.Serve(ctx); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			// Serve already shut the listener/conns down on ctx cancellation;
			// Close is idempotent and also removes the socket file.
			_ = srv.Close()
			return nil
		},
	}

	serveCmd.Flags().BoolVar(&headless, "headless", false,
		"run the browser headless instead of headful (default: headful)")
	serveCmd.Flags().BoolVar(&stealth, "stealth", true,
		"apply go-rod/stealth to every page (default: on)")
	return serveCmd
}
