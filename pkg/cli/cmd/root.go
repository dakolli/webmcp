// Package cmd implements the cobra commands of webmcp-cli, the lightweight
// CLI mirror of the web-mcp MCP tools. The commands talk directly to the
// pkg/browsers core engine — never through pkg/mcp and never to go-rod — so
// the CLI is a second consumer of pkg/browsers alongside pkg/mcp/tools.
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
)

// singleBrowserID is the id reported for the manager's one Browser, mirroring
// pkg/mcp/tools/browser.go. The Manager owns a single Browser (AGENTS.md
// "Browser-first paradigm"); a browser_id → Browser map is the planned growth
// path when multi-browser support lands.
const singleBrowserID = "browser-1"

// NewRootCmd constructs the webmcp-cli root command, wiring it to the shared
// browsers.Manager and registering the browser / launch / fetch / page
// commands.
func NewRootCmd(mgr *browsers.Manager) *cobra.Command {
	root := &cobra.Command{
		Use:   "webmcp-cli",
		Short: "Drive the web-mcp live browser engine from the command line",
	}

	// --browser-id is inherited by every subcommand (mirrors browser_create's
	// browser_id in the MCP toolset).
	root.PersistentFlags().String("browser-id", singleBrowserID,
		"id of the browser to operate on")

	browserCmd := &cobra.Command{
		Use:   "browser",
		Short: "Manage the live browser",
	}
	browserCmd.AddCommand(NewBrowserListCmd(mgr), NewBrowserCloseCmd(mgr))

	root.AddCommand(browserCmd, NewLaunchCmd(mgr), NewFetchCmd(mgr), NewPageCmd(mgr))
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
			// No blocking engine call here (Manager.Browser and Browser.Pages
			// are non-blocking in-memory reads that take no context), so no
			// 30s timeout is derived for this command.
			// No lazy launch: listing must never start a browser.
			b, err := mgr.Browser()
			if err != nil {
				// No browser open yet — nothing to list.
				fmt.Printf("no browser running (%s not open); start one with 'webmcp-cli launch'\n",
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

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			if err := mgr.Close(ctx); err != nil { // idempotent no-op when nothing is open
				return fmt.Errorf("browser close: %w", err)
			}
			fmt.Printf("browser %s closed\n", singleBrowserID)
			return nil
		},
	}
}
