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
// browsers.Manager and registering the browser / fetch / page commands and the
// serve tree (serve, serve stop, serve mcp).
func NewRootCmd(mgr *browsers.Manager) *cobra.Command {
	root := &cobra.Command{
		Use:   "web-cli",
		Short: "Drive the web-mcp live browser engine from the command line",
		Long: `Drive the web-mcp live browser engine from the command line: open URLs in a
real Chromium browser, read any aspect of the live page, and interact with it
— the human-side knob over the same engine the MCP tools expose to LLM agents.

Typical workflow: 'web-cli serve' starts the shared browser as a background
daemon (optional — fetch launches it lazily); 'web-cli fetch <url>' opens a
page and prints its session id (page-1); 'web-cli page fetch/act' read and
drive the live page; 'web-cli page close' and 'web-cli serve stop' clean up.
With one page open the page id is optional; 'last' means the most recently
opened page; a bare number resolves to page-N.`,
		Example: `  # Start the shared browser as a background daemon (optional: fetch launches it lazily)
  web-cli serve

  # Open a URL — prints the session id (page-1) you use with the page commands
  web-cli fetch https://news.ycombinator.com

  # Read and drive the live page. With one page open, the id is optional.
  web-cli page list
  web-cli page fetch links
  web-cli page act click "#search"

  # Clean up
  web-cli page close
  web-cli serve stop`,
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
		Long: `Manage the live browser: list its open pages, close it (and every page in
it), or raise its OS window to the foreground. The CLI manages a single
browser (browser-1), mirroring the MCP browser_create/list/close tools.`,
		Example: `  web-cli browser list              # show the live browser and its open pages (id, url)
  web-cli browser close             # close the browser and every page in it
  web-cli browser focus             # raise the browser window (best-effort)
  web-cli browser focus --state maximize`,
	}
	browserCmd.AddCommand(
		NewBrowserListCmd(mgr),
		NewBrowserCloseCmd(mgr),
		NewBrowserFocusCmd(mgr),
	)

	root.AddCommand(
		browserCmd,
		NewFetchCmd(mgr),
		NewPageCmd(mgr),
		NewServeCmd(),
	)
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
		Long: `Shows the live browser's id and its open pages as "id<TAB>url" rows.
Never launches the browser: with nothing running it prints a friendly hint and
exits 0, so listing is safe in scripts.`,
		Example: `  web-cli browser list              # show the live browser and its open pages (id, url)
  web-cli browser close             # close the browser and every page in it
  web-cli browser focus             # raise the browser window (best-effort)
  web-cli browser focus --state maximize`,
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
				fmt.Printf("no browser running (%s not open); start one with 'web-cli serve'\n",
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
		Long: `Closes the browser and every page in it. Closing when nothing is open
succeeds silently (idempotent, exit 0). Note: this closes the browser, not the
serve daemon — stop that with 'web-cli serve stop'.`,
		Example: `  web-cli browser list              # show the live browser and its open pages (id, url)
  web-cli browser close             # close the browser and every page in it
  web-cli browser focus             # raise the browser window (best-effort)
  web-cli browser focus --state maximize`,
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

// NewBrowserFocusCmd constructs the "browser focus" command: it validates the
// inherited --browser-id flag (only the singleBrowserID is supported), resolves
// the live browser's OS window via pure CDP through the rod handle and raises
// it (best-effort — see the honest limitation in Short). --state selects the
// target window state (default normal; "maximize" is the user-facing alias for
// the engine value "maximized"). Focus is never a launch trigger: with no
// browser open (or no page open) it prints the same friendly hint as
// `browser list`, mirroring the no-lazy-launch doctrine.
func NewBrowserFocusCmd(mgr *browsers.Manager) *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "focus",
		Short: "Raise the live browser window to the foreground (best-effort)",
		Long: `Raises/restores the live browser's OS window via pure CDP
(Browser.getWindowForTarget + Browser.setWindowBounds) — no wmctrl/xdotool.
The default state (normal) restores a minimized window and re-asserts its
bounds as the standard best-effort raise; --state maximize or fullscreen are
stronger (they force the window manager to re-layout the window on top).

Honest limitation: CDP Browser.setWindowBounds does not guarantee z-order
raise on every window manager — re-asserting normal bounds is the standard
best-effort; --state maximize is the stronger fallback.

Focus never launches a browser: it operates on an already-running one.`,
		Example: `  web-cli browser list              # show the live browser and its open pages (id, url)
  web-cli browser close             # close the browser and every page in it
  web-cli browser focus             # raise the browser window (best-effort)
  web-cli browser focus --state maximize`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := cmd.Flags().GetString("browser-id")
			if err != nil {
				return fmt.Errorf("browser focus: read --browser-id: %w", err)
			}
			if id != singleBrowserID {
				return fmt.Errorf("browser focus: unknown browser id %q; this CLI manages a single browser (%q)",
					id, singleBrowserID)
			}
			// Normalize the documented CLI vocabulary ("maximize" is the
			// user-facing alias for the engine constant "maximized") and
			// reject anything else with the documented value list.
			ws, err := normalizeFocusState(state)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmdCtx(cmd), 30*time.Second)
			defer cancel()

			// Forward to a live serve process when one is running (raises the
			// serve process's browser window, which stays alive for the next
			// command); else fall back to the in-process path below.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				var res ipc.BrowserFocusResult
				if err := cl.Call(ctx, ipc.MethodBrowserFocus, &ipc.BrowserFocusParams{State: string(ws)}, &res); err != nil {
					// Only the server's own -32000 "no browser open" answer
					// maps to the friendly hint + exit 0 (byte-identical to
					// the in-process path, mirroring browserListViaIPC); a
					// dead serve process, timeout, transport failure or engine
					// "no pages open" error is a real error and must surface
					// loudly.
					var serverErr *ipc.ServerError
					if errors.As(err, &serverErr) &&
						serverErr.Code == ipc.CodeServerError &&
						strings.Contains(serverErr.Message, "no browser open") {
						fmt.Printf("no browser running (%s not open); start one with 'web-cli serve'\n",
							singleBrowserID)
						return nil
					}
					return fmt.Errorf("browser focus: %w", err)
				}
				fmt.Printf("browser %s focused (state: %s)\n", singleBrowserID, state)
				return nil
			}
			printServeHint(cmd)

			// No lazy launch: focus is a command on an already-running browser.
			b, err := mgr.Browser()
			if err != nil {
				// No browser open — nothing to focus; never start one.
				fmt.Printf("no browser running (%s not open); start one with 'web-cli serve'\n",
					singleBrowserID)
				return nil
			}
			if err := b.Focus(ctx, ws); err != nil {
				return fmt.Errorf("browser focus: %w", err)
			}
			fmt.Printf("browser %s focused (state: %s)\n", singleBrowserID, state)
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", string(browsers.WindowStateNormal),
		"target window state: normal (restore/raise), maximize, or fullscreen")
	return cmd
}

// normalizeFocusState maps the documented CLI --state vocabulary to the
// engine's WindowState values: "maximize" is accepted as the user-facing alias
// for the engine constant "maximized" (the error message and flag help list the
// CLI vocabulary "maximize, fullscreen, normal"); any other value fails. The
// engine value "maximized" itself passes through unchanged, so both spellings
// work.
func normalizeFocusState(state string) (browsers.WindowState, error) {
	if state == "maximize" {
		state = string(browsers.WindowStateMaximized) // user-facing alias
	}
	switch browsers.WindowState(state) {
	case browsers.WindowStateNormal, browsers.WindowStateMaximized, browsers.WindowStateFullscreen:
		return browsers.WindowState(state), nil
	default:
		return "", fmt.Errorf("browser focus: unknown state %q (must be one of maximize, fullscreen, normal)", state)
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
			fmt.Printf("no browser running (%s not open); start one with 'web-cli serve'\n",
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
