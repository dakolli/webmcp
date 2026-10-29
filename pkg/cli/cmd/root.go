// Package cmd implements the cobra commands of web-cli, the lightweight
// CLI mirror of the web-mcp MCP tools. The commands talk directly to the
// pkg/browsers core engine — never through pkg/mcp and never to go-rod — so
// the CLI is a second consumer of pkg/browsers alongside pkg/mcp/tools.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/ipc"
	"github.com/dakolli/webmcp/pkg/config"
)

// singleBrowserID is the id reported for the manager's one Browser, mirroring
// pkg/mcp/tools/browser.go. The Manager owns a single Browser (AGENTS.md
// "Browser-first paradigm"); a browser_id → Browser map is the planned growth
// path when multi-browser support lands.
const singleBrowserID = "browser-1"

// explicitBrowserID returns the --browser-id value only when the user set the
// flag, so an omitted flag keeps the command's default target instead of
// silently pinning browser-1.
func explicitBrowserID(cmd *cobra.Command) string {
	if !cmd.Flags().Changed("browser-id") {
		return ""
	}
	id, err := cmd.Flags().GetString("browser-id")
	if err != nil {
		return ""
	}
	return id
}

// reportBrowserTarget writes the pinned browser id to stderr, leaving stdout
// byte-stable for scripts and JSON consumers.
func reportBrowserTarget(cmd *cobra.Command, id string) {
	if id == "" {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "browser_id: %s (--browser-id)\n", id)
}

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
		"id of the browser to operate on (must already be live for fetch/page commands)")
	// --socket is the path of the long-lived serve process's IPC socket,
	// consumed by serve itself and by the client-side command forwarding
	// (WEB-29); it must not be redefined by any subcommand.
	root.PersistentFlags().String("socket", "",
		"path to the browser IPC socket (default: $XDG_RUNTIME_DIR/web-mcp.sock, else ~/.web-mcp.sock)")

	browserCmd := &cobra.Command{
		Use:   "browser",
		Short: "Manage the live browser",
		Long: `Manage live browsers: list them with their owner/profile lifecycle, close one
or all (optionally purging a profile or pruning orphan ephemeral profiles), or
raise a browser window to the foreground. Mirrors the MCP browser_create,
browser_list and browser_close tools.`,
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
		NewSkillCmd(),
		NewMcpCmd(),
		NewConfigCmd(),
		NewCaidoCmd(),
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
		Short: "List live browsers with their lifecycle state and pages",
		Long: `Shows every live browser with its lifecycle header (owner, persist, ttl),
its profile directory, and its open pages as "id<TAB>url" rows.
Never launches a browser: with nothing running it prints a friendly hint and
exits 0, so listing is safe in scripts.`,
		Example: `  browser list                      # browsers, owners, persist/ttl, profile dirs and pages
  browser close --prune             # close all and clean orphan ephemeral profiles`,
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
			ids := make([]string, 0, len(active))
			for id := range active {
				ids = append(ids, string(id))
			}
			sort.Strings(ids)
			entries := make([]ipc.BrowserEntry, 0, len(ids))
			for _, id := range ids {
				b := active[browsers.BrowserID(id)]
				info := b.Info()
				entries = append(entries, ipc.BrowserEntry{
					BrowserID:   string(info.BrowserID),
					Owner:       info.Owner,
					Bin:         info.Bin,
					ProfileDir:  info.ProfileDir,
					Persist:     info.Persist,
					Ephemeral:   info.Ephemeral,
					CreatedAt:   info.CreatedAt,
					IdleFor:     info.IdleFor,
					TTL:         info.TTL,
					Scope:       info.Scope,
					ScopeStrict: info.ScopeStrict,
					Pages:       b.Pages(),
				})
			}
			printBrowserEntries(cmd.OutOrStdout(), entries)
			return nil
		},
	}
}

// NewBrowserCloseCmd constructs the "browser close" command: it closes the
// target browser and all of its pages, optionally deleting its profile
// (--purge) or closing every browser and removing orphan ephemeral profiles
// (--prune). Closing when no browser is open succeeds (idempotent no-op).
func NewBrowserCloseCmd(mgr *browsers.Manager) *cobra.Command {
	var purge, prune bool
	cmd := &cobra.Command{
		Use:   "close [id]",
		Short: "Close a browser and all of its pages",
		Long: `Closes a browser and every page in it. Closing when nothing is open
succeeds silently (idempotent, exit 0). Note: this closes the browser, not the
serve daemon — stop that with 'web-cli serve stop'.

  --purge  delete the closed browser's profile directory (and its logins); the
           directory must resolve under ~/.config/browser-mcp/profiles
  --prune  close every browser, then delete leftover profiles under
           profiles/.ephemeral that no live browser owns; named persistent
           profiles are never deleted`,
		Example: `  web-cli browser close                    # close the default browser (browser-1)
  web-cli browser close browser-2          # close one browser
  web-cli browser close browser-2 --purge  # close it and delete its profile
  web-cli browser close --prune            # close all, clean orphan ephemeral profiles`,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("browser-id")
			idExplicit := cmd.Flags().Changed("browser-id")
			if len(args) > 0 {
				id = args[0]
				idExplicit = true
			}
			if err := validateCloseCleanup(idExplicit, purge, prune); err != nil {
				return fmt.Errorf("browser close: %w", err)
			}

			ctx, cancel := context.WithTimeout(cmdCtx(cmd), 30*time.Second)
			defer cancel()

			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				params := ipc.BrowserCloseParams{BrowserID: id, Purge: purge, Prune: prune}
				if prune {
					params.BrowserID = ""
				}
				var res ipc.BrowserCloseResult
				if err := cl.Call(ctx, ipc.MethodBrowserClose, params, &res); err != nil {
					return fmt.Errorf("browser close: %w", err)
				}
				if res.Message != "" {
					fmt.Println(res.Message)
				} else if id != "" {
					fmt.Printf("browser %s closed\n", id)
				} else {
					fmt.Println("all browsers closed")
				}
				return nil
			}
			printServeHint(cmd)

			switch {
			case prune:
				removed, err := mgr.PruneProfiles(ctx)
				if err != nil {
					return fmt.Errorf("browser close: %w", err)
				}
				fmt.Printf("all browsers closed; pruned %d orphan ephemeral profile(s)\n", len(removed))
			case purge:
				if err := mgr.PurgeProfile(ctx, browsers.BrowserID(id)); err != nil {
					return fmt.Errorf("browser close: %w", err)
				}
				fmt.Printf("browser %s closed and its profile deleted\n", id)
			default:
				if err := mgr.CloseBrowser(ctx, browsers.BrowserID(id)); err != nil {
					return fmt.Errorf("browser close: %w", err)
				}
				if id != "" {
					fmt.Printf("browser %s closed\n", id)
				} else {
					fmt.Println("all browsers closed")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "delete the closed browser's profile directory (config-dir guarded)")
	cmd.Flags().BoolVar(&prune, "prune", false, "close all browsers and delete orphan ephemeral profiles")
	return cmd
}

// validateCloseCleanup applies browser_close's purge/prune rules: the flags are
// mutually exclusive, prune takes no browser id, and purge needs one.
func validateCloseCleanup(idExplicit, purge, prune bool) error {
	switch {
	case purge && prune:
		return errors.New("purge and prune are mutually exclusive — pass purge with a browser_id, or prune alone")
	case prune && idExplicit:
		return errors.New("prune takes no browser_id — it closes all browsers and cleans orphan ephemeral profiles; use purge with browser_id to delete one profile")
	case purge && !idExplicit:
		return errors.New("purge requires a browser_id — pass the browser whose profile to delete, or use prune to clean orphan ephemeral profiles")
	}
	return nil
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
		printBrowserEntries(cmd.OutOrStdout(), res.Browsers)
	} else {
		fmt.Printf("browser_id: %s\n", res.BrowserID)
		for _, p := range res.Pages {
			fmt.Printf("  %s\t%s\n", p.ID, p.URL)
		}
	}
	return nil
}

// printBrowserEntries renders live browsers in the human-readable list shape:
// a lifecycle header (owner, persist, ttl) per browser, an indented profile_dir
// row, then its pages.
func printBrowserEntries(w io.Writer, entries []ipc.BrowserEntry) {
	for _, b := range entries {
		scope := b.Scope
		if scope == "" {
			scope = browsers.ScopeOpen
		}
		fmt.Fprintf(w, "browser_id: %s (owner: %s, persist: %t, ttl: %s, scope: %s, scope_strict: %t",
			b.BrowserID, b.Owner, b.Persist, b.TTL, scope, b.ScopeStrict)
		if b.Bin != "" {
			fmt.Fprintf(w, ", bin: %s", b.Bin)
		}
		fmt.Fprintln(w, ")")
		if b.ProfileDir != "" {
			fmt.Fprintf(w, "  profile_dir: %s\n", b.ProfileDir)
		}
		for _, p := range b.Pages {
			fmt.Fprintf(w, "  %s\t%s\n", p.ID, p.URL)
		}
	}
}

// NewBrowserCreateCmd constructs the "browser create" command: launches a new
// browser process with optional custom binary (--bin), profile (--profile),
// persistence (--persist), owner label (--owner), window position
// (--position), headless mode (--headless), and a per-host rate budget
// (--rps/--burst).
func NewBrowserCreateCmd(mgr *browsers.Manager) *cobra.Command {
	var bin, profile, position, proxy, proxyBypass, owner string
	var headers, scope, chromiumArgs []string
	var persist, ignoreCerts, headless, scopeStrict bool
	var rps float64
	var burst int
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create and launch a new browser instance",
		Long: `Launches a new browser process with optional custom binary (--bin), profile
(--profile), owner label (--owner), and window position (--position
left|right|max|normal).

A bare profile name resolves under ~/.config/browser-mcp/profiles; an absolute
path is used as given; relative paths are rejected. Browsers are ephemeral by
default: the profile is deleted on close. Pass --persist to keep a named
profile (and its logins) after close. The plain human 'browser serve' daemon
keeps its lazily launched browser-1 persistent.

--owner labels the browser and joins the create key: the same owner with the
same options reuses one browser, a different owner gets a separate one.

--headless launches Chromium with no window. It joins the create key too, so
--headless and a plain create are different browsers. The CLI default stays
headful; pass --headless explicitly (the serve daemon has its own global
--headless).

--proxy sends traffic through an intercept proxy such as Burp, Caido or ZAP:
accept host:port or scheme://host:port, for example 127.0.0.1:8080 or
http://127.0.0.1:8080. --proxy-bypass lists comma-separated hosts that skip it.
--ignore-certs accepts the proxy's MITM certificate and is independent of
--proxy. Proxy credentials never go on the command line: put them in
~/.config/browser-mcp/proxy.json (mode 0600), keyed by proxy host:port.

--header NAME=VALUE (repeatable) sends an identity header with every request from
every page of this browser, the first navigation included. Both flags are
repeatable: --header X-Bug-Bounty=me --header X-Team=red.

--scope HOST (repeatable) sets the request allowlist: a bare host covers its
subdomains, '*.example.com' works, 'host:port' is accepted, and '*' allows all.
No --scope means no limit; --scope-strict with no --scope denies every request.
Scope covers navigation, replay, every redirect hop and intercepted
subresources: an out-of-scope request is refused before it is sent.

--rps and --burst set the per-host request budget. --rps is the sustained
requests per second (default 5) and --burst is the token bucket depth (default
10). Every engine-issued request waits for a token; a request whose deadline
cannot fit the wait fails as a timeout naming the host and the remaining wait.

--chromium-arg SWITCH (repeatable) adds a Chromium command-line switch to this
browser, appended to the built-in quiet set and [browser] chromium_flags from
webmcp.toml. Pass --chromium-arg=--disable-gpu or --chromium-arg --disable-gpu.`,
		Example: `  browser create --owner scraper-a
  browser create --headless
  browser create --persist --profile audit
  browser create --proxy http://127.0.0.1:8080 --ignore-certs
  browser create --proxy 127.0.0.1:8080 --proxy-bypass localhost,*.internal
  browser create --header X-Bug-Bounty=me --scope example.com --scope '*.iana.org'
  browser create --scope example.com --rps 2 --burst 4
  browser create --chromium-arg=--disable-gpu`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), 30*time.Second)
			defer cancel()

			owner, err := ipc.NormalizeOwner(owner)
			if err != nil {
				return fmt.Errorf("browser create: %w", err)
			}
			if owner != "" {
				ctx = browsers.WithExplicitOwner(ctx, owner)
			}
			headerMap, err := parseHeaderFlags(headers)
			if err != nil {
				return fmt.Errorf("browser create: %w", err)
			}
			browser, err := config.ResolveBrowser(chromiumArgs)
			if err != nil {
				return fmt.Errorf("browser create: %w", err)
			}

			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				params := ipc.BrowserOpenParams{
					Bin:         bin,
					Profile:     profile,
					Position:    position,
					Proxy:       proxy,
					ProxyBypass: proxyBypass,
					Owner:       owner,
					Headers:     headerMap,
					Scope:       scope,
				}
				if cmd.Flags().Changed("persist") {
					params.Persist = &persist
				}
				if cmd.Flags().Changed("ignore-certs") {
					params.IgnoreCerts = &ignoreCerts
				}
				if cmd.Flags().Changed("headless") {
					params.Headless = &headless
				}
				if cmd.Flags().Changed("scope-strict") {
					params.ScopeStrict = &scopeStrict
				}
				if cmd.Flags().Changed("rps") {
					params.RPS = &rps
				}
				if cmd.Flags().Changed("burst") {
					params.Burst = &burst
				}
				var res ipc.BrowserOpenResult
				if err := cl.Call(ctx, ipc.MethodBrowserOpen, params, &res); err != nil {
					return fmt.Errorf("browser create: %w", err)
				}
				fmt.Printf("browser_id: %s (owner: %s, persist: %t, ttl: %s, scope: %s, scope_strict: %t, limits: %g/%d)\n",
					res.BrowserID, res.Owner, res.Persist, res.TTL, res.Scope, res.ScopeStrict,
					res.Limits.RPS, res.Limits.Burst)
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
			if proxy != "" {
				opts = append(opts, browsers.WithProxy(proxy))
			}
			if proxyBypass != "" {
				opts = append(opts, browsers.WithProxyBypass(proxyBypass))
			}
			if cmd.Flags().Changed("ignore-certs") {
				opts = append(opts, browsers.WithIgnoreCerts(ignoreCerts))
			}
			if cmd.Flags().Changed("headless") {
				opts = append(opts, browsers.WithHeadless(headless))
			}
			if len(headerMap) > 0 {
				opts = append(opts, browsers.WithExtraHeaders(headerMap))
			}
			opts = append(opts, browsers.WithChromiumFlags(browser.ChromiumFlags))
			if len(scope) > 0 {
				opts = append(opts, browsers.WithScope(scope))
			}
			if cmd.Flags().Changed("scope-strict") {
				opts = append(opts, browsers.WithScopeStrict(scopeStrict))
			}
			if cmd.Flags().Changed("rps") || cmd.Flags().Changed("burst") {
				limit := browsers.RateLimit{
					RPS:   browsers.DefaultRateRPS,
					Burst: browsers.DefaultRateBurst,
				}
				if cmd.Flags().Changed("rps") {
					limit.RPS = rps
				}
				if cmd.Flags().Changed("burst") {
					limit.Burst = burst
				}
				if err := limit.Validate(); err != nil {
					return fmt.Errorf("browser create: %w", err)
				}
				opts = append(opts, browsers.WithRateLimit(limit))
			}
			id, b, err := mgr.CreateBrowser(ctx, opts...)
			if err != nil {
				return fmt.Errorf("browser create: %w", err)
			}
			info := b.Info()
			details := []string{
				fmt.Sprintf("owner: %s", info.Owner),
				fmt.Sprintf("persist: %t", info.Persist),
				fmt.Sprintf("ttl: %s", info.TTL),
				fmt.Sprintf("scope: %s", info.Scope),
				fmt.Sprintf("scope_strict: %t", info.ScopeStrict),
				fmt.Sprintf("limits: %g/%d", info.Limits.RPS, info.Limits.Burst),
			}
			if info.Bin != "" {
				details = append(details, "bin: "+info.Bin)
			}
			if info.ProfileDir != "" {
				details = append(details, "profile: "+info.ProfileDir)
			}
			if b.Position() != "" {
				details = append(details, "position: "+b.Position())
			}
			fmt.Printf("browser_id: %s (%s)\n", id, strings.Join(details, ", "))
			return nil
		},
	}
	cmd.Flags().StringVar(&bin, "bin", "", "custom browser binary executable path or command")
	cmd.Flags().StringVar(&profile, "profile", "", "profile name resolved under the config dir, or an absolute directory path")
	cmd.Flags().BoolVar(&persist, "persist", false, "keep the profile (and its logins) after close; browsers are otherwise ephemeral")
	cmd.Flags().StringVar(&owner, "owner", "", "owner label (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-') that joins the create key")
	cmd.Flags().StringVar(&position, "position", "", "window position split screen preset: left, right, max, normal")
	cmd.Flags().StringVar(&proxy, "proxy", "", "upstream proxy host:port or scheme://host:port (e.g. 127.0.0.1:8080)")
	cmd.Flags().StringVar(&proxyBypass, "proxy-bypass", "", "comma-separated hosts that skip the proxy (e.g. localhost,*.internal)")
	cmd.Flags().BoolVar(&ignoreCerts, "ignore-certs", false, "accept invalid TLS certificates (e.g. an intercept proxy's MITM CA); independent of --proxy")
	cmd.Flags().BoolVar(&headless, "headless", false, "launch Chromium with no window (joins the create key; default is headful)")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "request header NAME=VALUE sent with every request from every page (repeatable)")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "request allowlist entry: host (covers subdomains), '*.example.com', 'host:port' or '*' (repeatable; empty = no limit)")
	cmd.Flags().BoolVar(&scopeStrict, "scope-strict", false, "deny every request when --scope is empty")
	cmd.Flags().Float64Var(&rps, "rps", browsers.DefaultRateRPS, "per-host sustained request rate in requests per second (default 5)")
	cmd.Flags().IntVar(&burst, "burst", browsers.DefaultRateBurst, "per-host token bucket depth, i.e. requests allowed at once before the rps pace applies (default 10)")
	cmd.Flags().StringArrayVar(&chromiumArgs, "chromium-arg", nil, "extra Chromium switch, e.g. --chromium-arg=--disable-gpu (repeatable; appended to the resolved [browser] set)")
	return cmd
}

// parseHeaderFlags converts repeatable --header NAME=VALUE flags into a header
// map, rejecting a flag that is not in NAME=VALUE form.
func parseHeaderFlags(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	headers := make(map[string]string, len(flags))
	for _, flag := range flags {
		name, value, ok := strings.Cut(flag, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid --header %q — use NAME=VALUE, e.g. X-Bug-Bounty=your-handle", flag)
		}
		headers[name] = value
	}
	return headers, nil
}
