// Package cmd implements the cobra commands of web-cli, the lightweight
// CLI mirror of the web-mcp MCP tools. The commands talk directly to the
// pkg/browsers core engine — never through pkg/mcp and never to go-rod — so
// the CLI is a second consumer of pkg/browsers alongside pkg/mcp/tools.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
)

// singleBrowserID is the id reported for the manager's one Browser, mirroring
// pkg/mcp/tools/browser.go. The Manager owns a single Browser (AGENTS.md
// "Browser-first paradigm"); a browser_id → Browser map is the planned growth
// path when multi-browser support lands.
const singleBrowserID = "browser-1"

// NewRootCmd constructs the web-cli root command, wiring it to the shared
// browsers.Manager and registering the browser / launch / fetch / page
// commands.
func NewRootCmd(mgr *browsers.Manager) *cobra.Command {
	root := &cobra.Command{
		Use:   "web-cli",
		Short: "Drive the web-mcp live browser engine from the command line",
	}

	// --browser-id is inherited by every subcommand (mirrors browser_create's
	// browser_id in the MCP toolset).
	root.PersistentFlags().String("browser-id", singleBrowserID,
		"id of the browser to operate on")
	// --socket is the path of the long-lived serve process's IPC socket,
	// consumed by serve itself and by the client-side command forwarding
	// (WEB-29); it must not be redefined by any subcommand.
	root.PersistentFlags().String("socket", "",
		"path to the web-cli IPC socket (default: $XDG_RUNTIME_DIR/web-mcp.sock, else ~/.web-mcp.sock)")

	browserCmd := &cobra.Command{
		Use:   "browser",
		Short: "Manage the live browser",
	}
	browserCmd.AddCommand(NewBrowserListCmd(mgr), NewBrowserCloseCmd(mgr))

	root.AddCommand(browserCmd, NewLaunchCmd(), NewFetchCmd(mgr), NewPageCmd(mgr), NewServeCmd())
	return root
}

// NewBrowserListCmd constructs the "browser list" command: it reports the
// live browser's id and its open pages (id, url), one line per page. It never
// lazily launches the browser — with no browser open it prints a friendly
// hint and exits successfully (exit 0).
func NewBrowserListCmd(mgr *browsers.Manager) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the live browser and its open pages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Forward to a live serve process when one is running (browser.list
			// never lazily launches over IPC either — the server only reports
			// an already-open browser); else fall back to the in-process path
			// below.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return browserListViaIPC(cmd, cl)
			}
			printServeHint(cmd)

			// No blocking engine call here (Manager.Browser and Browser.Pages
			// are non-blocking in-memory reads that take no context), so no
			// 30s timeout is derived for this command.
			// No lazy launch: listing must never start a browser.
			b, err := mgr.Browser()
			if err != nil {
				// No browser open yet — nothing to list.
				fmt.Printf("no browser running (%s not open); start one with 'web-cli launch'\n",
					singleBrowserID)
				return nil
			}
			fmt.Printf("browser_id: %s\n", singleBrowserID)
			for _, p := range b.Pages() {
				fmt.Printf("  %s\t%s\n", p.ID, p.URL)
			}
			return nil
		},
	}
}

// NewBrowserCloseCmd constructs the "browser close" command: it validates the
// inherited --browser-id flag (only the singleBrowserID is supported) and
// closes the manager's Browser and all of its open pages. Closing when no
// browser is open succeeds (Manager.Close is an idempotent no-op).
func NewBrowserCloseCmd(mgr *browsers.Manager) *cobra.Command {
	return &cobra.Command{
		Use:   "close",
		Short: "Close the live browser and all of its pages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := cmd.Flags().GetString("browser-id")
			if err != nil {
				return fmt.Errorf("browser close: read --browser-id: %w", err)
			}
			if id != singleBrowserID {
				return fmt.Errorf("browser close: unknown browser id %q; this CLI manages a single browser (%q)",
					id, singleBrowserID)
			}

			ctx, cancel := context.WithTimeout(cmdCtx(cmd), 30*time.Second)
			defer cancel()

			// Forward to a live serve process when one is running (closes the
			// serve process's browser, which stays alive for the next command);
			// else fall back to the in-process path below.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				if err := cl.Call(ctx, ipc.MethodBrowserClose, nil, nil); err != nil {
					return fmt.Errorf("browser close: %w", err)
				}
				fmt.Printf("browser %s closed\n", singleBrowserID)
				return nil
			}
			printServeHint(cmd)

			if err := mgr.Close(ctx); err != nil { // idempotent no-op when nothing is open
				return fmt.Errorf("browser close: %w", err)
			}
			fmt.Printf("browser %s closed\n", singleBrowserID)
			return nil
		},
	}
}

// browserListViaIPC implements `browser list` against a live serve process: it
// reports the serve process's browser id and open pages, never lazily launching
// (the server's browser.list only answers for an already-open browser). Only
// the server's genuine "no browser open" error maps to today's stdout hint with
// exit 0, byte-identical to the in-process path; any other failure (a dead or
// hung serve process, transport error, …) propagates as a real error so it is
// not silently masked as "no browser running".
func browserListViaIPC(cmd *cobra.Command, cl *ipc.Client) error {
	ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
	defer cancel()

	var res ipc.BrowserListResult
	if err := cl.Call(ctx, ipc.MethodBrowserList, nil, &res); err != nil {
		// Only the server's own -32000 "no browser open" answer maps to the
		// friendly hint + exit 0; a dead serve process, timeout or transport
		// failure is a real error and must surface loudly.
		var serverErr *ipc.ServerError
		if errors.As(err, &serverErr) &&
			serverErr.Code == ipc.CodeServerError &&
			strings.Contains(serverErr.Message, "no browser open") {
			fmt.Printf("no browser running (%s not open); start one with 'web-cli launch'\n",
				singleBrowserID)
			return nil
		}
		return fmt.Errorf("browser list: %w", err)
	}
	fmt.Printf("browser_id: %s\n", singleBrowserID)
	for _, p := range res.Pages {
		fmt.Printf("  %s\t%s\n", p.ID, p.URL)
	}
	return nil
}
