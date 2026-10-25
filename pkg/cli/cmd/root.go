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
		Use:   "browser",
		Short: "Drive the web-mcp live browser engine from the command line",
		Long: `Drive the web-mcp live browser engine from the command line: open URLs in a
real Chromium browser, read any aspect of the live page, and interact with it
— the human-side knob over the same engine the MCP tools expose to LLM agents.

Typical workflow: 'browser serve' starts the shared browser as a background
daemon (optional — fetch launches it lazily); 'browser fetch <url>' opens a
page and prints its session id (page-1); 'browser page fetch/act' read and
drive the live page; 'browser page close' and 'browser serve stop' clean up.
With one page open the page id is optional; 'last' means the most recently
opened page; a bare number resolves to page-N.`,
		Example: `  # Start the shared browser as a background daemon (optional: fetch launches it lazily)
  browser serve

  # Open a URL — prints the session id (page-1) you use with the page commands
  browser fetch https://news.ycombinator.com

  # Read and drive the live page. With one page open, the id is optional.
  browser page list
  browser page fetch links
  browser page act click "#search"

  # Clean up
  browser page close
  browser serve stop`,
	}

	// --browser-id is inherited by every subcommand (mirrors browser_create's
	// browser_id in the MCP toolset).
	root.PersistentFlags().String("browser-id", singleBrowserID,
		"id of the browser to operate on")
	// --socket is the path of the long-lived serve process's IPC socket,
	// consumed by serve itself and by the client-side command forwarding
	// (WEB-29); it must not be redefined by any subcommand.
	root.PersistentFlags().String("socket", "",
		"path to the browser IPC socket (default: $XDG_RUNTIME_DIR/web-mcp.sock, else ~/.web-mcp.sock)")

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
		NewBrowserCreateCmd(mgr),
		NewBrowserListCmd(mgr),
		NewBrowserCloseCmd(mgr),
		NewBrowserFocusCmd(mgr),
	)

	root.AddCommand(
		browserCmd,
		NewBrowserCreateCmd(mgr),
		NewBrowserListCmd(mgr),
		NewBrowserCloseCmd(mgr),
		NewBrowserFocusCmd(mgr),
		NewFetchCmd(mgr),
		NewPageCmd(mgr),
		NewServeCmd(),
		NewServeStopCmd(),
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

			active := mgr.Browsers()
			if len(active) == 0 {
				fmt.Println("no browsers running; start one with 'browser serve' or 'browser fetch <url>'")
				return nil
			}
			for id, b := range active {
				info := ""
				if b.BinPath() != "" {
					info += fmt.Sprintf(" (bin: %s)", b.BinPath())
				}
				if b.ProfileDir() != "" {
					info += fmt.Sprintf(" (profile: %s)", b.ProfileDir())
				}
				fmt.Printf("browser_id: %s%s\n", id, info)
				for _, p := range b.Pages() {
					fmt.Printf("  %s\t%s\n", p.ID, p.URL)
				}
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
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("browser-id")
			if len(args) > 0 {
				id = args[0]
			}

			ctx, cancel := context.WithTimeout(cmdCtx(cmd), 30*time.Second)
			defer cancel()

			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				params := map[string]string{}
				if id != "" {
					params["browser_id"] = id
				}
				if err := cl.Call(ctx, ipc.MethodBrowserClose, params, nil); err != nil {
					return fmt.Errorf("browser close: %w", err)
				}
				if id != "" {
					fmt.Printf("browser %s closed\n", id)
				} else {
					fmt.Println("all browsers closed")
				}
				return nil
			}
			printServeHint(cmd)

			if err := mgr.CloseBrowser(ctx, browsers.BrowserID(id)); err != nil {
				return fmt.Errorf("browser close: %w", err)
			}
			if id != "" {
				fmt.Printf("browser %s closed\n", id)
			} else {
				fmt.Println("all browsers closed")
			}
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
			b, err := mgr.Browser(browsers.BrowserID(id))
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
		var serverErr *ipc.ServerError
		if errors.As(err, &serverErr) &&
			serverErr.Code == ipc.CodeServerError &&
			strings.Contains(serverErr.Message, "no browser open") {
			fmt.Println("no browsers running; start one with 'browser serve' or 'browser fetch <url>'")
			return nil
		}
		return fmt.Errorf("browser list: %w", err)
	}
	if len(res.Browsers) > 0 {
		for _, b := range res.Browsers {
			info := ""
			if b.Bin != "" {
				info += fmt.Sprintf(" (bin: %s)", b.Bin)
			}
			if b.ProfileDir != "" {
				info += fmt.Sprintf(" (profile: %s)", b.ProfileDir)
			}
			fmt.Printf("browser_id: %s%s\n", b.BrowserID, info)
			for _, p := range b.Pages {
				fmt.Printf("  %s\t%s\n", p.ID, p.URL)
			}
		}
	} else {
		fmt.Printf("browser_id: %s\n", res.BrowserID)
		for _, p := range res.Pages {
			fmt.Printf("  %s\t%s\n", p.ID, p.URL)
		}
	}
	return nil
}

// NewBrowserCreateCmd constructs the "browser create" command: launches a new
// browser process with optional custom binary (--bin), profile (--profile),
// persistence (--persist), and window position (--position).
func NewBrowserCreateCmd(mgr *browsers.Manager) *cobra.Command {
	var bin, profile, position string
	var persist bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create and launch a new browser instance",
		Long: `Launches a new browser process with optional custom binary (--bin), profile
(--profile), and window position (--position left|right|max|normal).

A bare profile name resolves under ~/.config/browser-mcp/profiles; an absolute
path is used as given; relative paths are rejected. Secondary browsers default
to an ephemeral profile that is deleted on close. Pass --persist to keep a named
profile (and its logins) after close. browser-1 with no explicit profile is
always persistent.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), 30*time.Second)
			defer cancel()

			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				params := map[string]any{
					"bin":      bin,
					"profile":  profile,
					"position": position,
				}
				if cmd.Flags().Changed("persist") {
					params["persist"] = persist
				}
				var res struct {
					BrowserID string `json:"browser_id"`
				}
				if err := cl.Call(ctx, ipc.MethodBrowserOpen, params, &res); err != nil {
					return fmt.Errorf("browser create: %w", err)
				}
				fmt.Printf("browser_id: %s\n", res.BrowserID)
				return nil
			}
			printServeHint(cmd)

			var opts []browsers.Option
			if bin != "" {
				opts = append(opts, browsers.WithBin(bin))
			}
			if profile != "" {
				opts = append(opts, browsers.WithProfileName(profile))
			}
			if cmd.Flags().Changed("persist") {
				opts = append(opts, browsers.WithPersist(persist))
			}
			if position != "" {
				opts = append(opts, browsers.WithPosition(position))
			}
			id, b, err := mgr.CreateBrowser(ctx, opts...)
			if err != nil {
				return fmt.Errorf("browser create: %w", err)
			}
			info := ""
			if b.BinPath() != "" {
				info += fmt.Sprintf(" (bin: %s)", b.BinPath())
			}
			if b.ProfileDir() != "" {
				info += fmt.Sprintf(" (profile: %s)", b.ProfileDir())
			}
			if b.Position() != "" {
				info += fmt.Sprintf(" (position: %s)", b.Position())
			}
			fmt.Printf("browser_id: %s%s\n", id, info)
			return nil
		},
	}
	cmd.Flags().StringVar(&bin, "bin", "", "custom browser binary executable path or command")
	cmd.Flags().StringVar(&profile, "profile", "", "profile name resolved under the config dir, or an absolute directory path")
	cmd.Flags().BoolVar(&persist, "persist", false, "keep the profile (and its logins) after close; secondary browsers are otherwise ephemeral")
	cmd.Flags().StringVar(&position, "position", "", "window position split screen preset: left, right, max, normal")
	return cmd
}
