package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
)

// browserID is the id reported for the manager's single Browser. The Manager
// owns one Browser and does not expose an id, so this mirrors the MCP
// single-browser id (pkg/mcp/tools/browser.go singleBrowserID).
const browserID = "browser-1"

// NewLaunchCmd constructs the "launch" command: it lazily starts the manager's
// single Browser (one live Chromium instance) via Manager.Open and prints its
// browser_id. It is the CLI equivalent of the MCP browser_create tool.
func NewLaunchCmd(mgr *browsers.Manager) *cobra.Command {
	var headless, stealth bool

	launchCmd := &cobra.Command{
		Use:   "launch",
		Short: "Launch the shared browser",
		Long: "Launch lazily starts the manager's single headful Chromium browser " +
			"and prints its browser_id. The browser stays alive until the manager " +
			"closes it; pages opened afterwards (fetch/page commands) live inside it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// headless/stealth are parsed for CLI parity with browser_create but
			// are NOT applied: they are construction-time Manager options and
			// Manager.Open takes no per-open options.
			// TODO(web-mcp): map headless/stealth to per-open Manager options once supported.
			_ = headless
			_ = stealth

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			if _, err := mgr.Open(ctx); err != nil {
				return fmt.Errorf("launch: %w", err)
			}
			cmd.Printf("browser_id: %s\n", browserID)
			return nil
		},
	}

	launchCmd.Flags().BoolVar(&headless, "headless", false, "run the browser headless instead of headful (reserved: follows the manager configuration for now)")
	launchCmd.Flags().BoolVar(&stealth, "stealth", true, "apply go-rod/stealth to every page (reserved: follows the manager configuration for now)")

	return launchCmd
}
