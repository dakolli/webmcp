// Package cmd implements the web-mcp CLI's cobra commands, talking directly to
// the pkg/browsers core engine (never through pkg/mcp). WEB-28 owns this file.
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
	"webmcp/pkg/cli/pageops"
)

// defaultCmdTimeout bounds every page subcommand so a hung page (or a
// first-run Chromium download) cannot block the CLI. Mirrors the 30s per-call
// timeout the MCP tools enforce (pkg/mcp/tools/fetch.go).
const defaultCmdTimeout = 30 * time.Second

// NewPageCmd constructs the "page" command tree — list, fetch, act and close —
// mirroring the MCP page_list / page_fetch / page_act / session_close tools
// against the same pkg/browsers engine. Bare `page` prints help.
func NewPageCmd(mgr *browsers.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "page",
		Short: "List, fetch, act on, and close live browser pages",
		Long: `Manage live browser pages (a.k.a. sessions) opened with the fetch command.

Subcommands:
  list   List the browser's open pages (id, url)
  fetch  Extract an aspect of a live page (html, dom, text, css, js, links, screenshot, meta)
  act    Perform an action on a live page (click, input, hover, scroll, select, eval, navigate, back, forward, reload, wait, set_viewport)
  close  Close an open page

Run a subcommand with --help for its flags.`,
	}
	cmd.AddCommand(
		newPageListCmd(mgr),
		newPageFetchCmd(mgr),
		newPageActCmd(mgr),
		newPageCloseCmd(mgr),
	)
	return cmd
}

// cmdCtx resolves the command's context, falling back to context.Background
// when cobra has not set one yet.
func cmdCtx(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// newPageListCmd builds `page list`: it prints one "id\turl" row per open page
// via Manager.Open -> Browser.Pages (mirroring PageList/SessionList).
func newPageListCmd(mgr *browsers.Manager) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the browser's open pages",
		Long: `Lists every open page of the browser as "id\turl" rows, so you can see
which sessions are still alive before acting on or closing them.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			// Forward to a live serve process when one is running; else fall
			// back to the in-process path below (browser dies with this
			// command — the serve hint on stderr says so).
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageListViaIPC(cmd, ctx, cl)
			}
			printServeHint(cmd)

			b, err := mgr.Open(ctx) // lazily launches (or returns) the single Browser
			if err != nil {
				return fmt.Errorf("page list: %w", err)
			}
			pages := b.Pages()
			if len(pages) == 0 {
				fmt.Println("no open pages")
				return nil
			}
			for _, p := range pages {
				fmt.Printf("%s\t%s\n", p.ID, p.URL)
			}
			return nil
		},
	}
}

// newPageFetchCmd builds `page fetch`: it extracts an aspect of a live page
// (html, text, links, meta, dom, css, js, screenshot) and prints it, truncating
// to --max-bytes on a UTF-8 rune boundary. Screenshots are the exception: the
// base64 data-URI bypasses truncation (see RunE).
func newPageFetchCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id       string
		what     string
		selector string
		target   string
		fullPage bool
		maxBytes int
	)
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch an aspect of a live page",
		Long: `Extracts an aspect of a live page identified by --id (the session id
returned by the fetch command) and prints it.

  --what html|text|links|meta|dom|css|js|screenshot
                 the aspect to extract (required): html (outerHTML), text
                 (visible text), links (anchor JSON), meta (title/url/tags
                 JSON), dom (nested DOM JSON), css/js (resource JSON list, or
                 one resource's content with --target), screenshot (base64 PNG
                 data-URI)
  --selector <css>  optional CSS selector scoping html/text
  --target <url>    fetch exactly one css/js resource's full content instead
                    of the {url,type,size} list (with --what css|js)
  --full-page       capture the entire scrollable page (screenshot only)
  --max-bytes <n>   cap output size (default 50000; screenshots are never
                    truncated)

links, meta, dom and the css/js lists print JSON, mirroring the MCP page_fetch
shapes. Screenshots print a data:image/png;base64,... data-URI.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			switch what {
			case "html", "text", "links", "meta", "dom", "css", "js", "screenshot":
			default:
				return fmt.Errorf("page fetch: unknown what=%q (must be one of html, text, links, meta, dom, css, js, screenshot)", what)
			}

			// Fail-fast switch passed: forward to a live serve process when one
			// is running; else fall back to the in-process path below.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageFetchViaIPC(cmd, ctx, cl, id, what, selector, target, fullPage, maxBytes)
			}
			printServeHint(cmd)

			p, err := mgr.Page(ctx, browsers.PageID(id)) // opens the Browser if needed; probes for liveness
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}

			var out string
			switch what {
			case "html":
				out, err = pageops.PageHTML(ctx, p, selector)
			case "text":
				out, err = pageops.PageText(ctx, p, selector)
			case "links":
				out, err = pageops.PageLinks(ctx, p)
			case "meta":
				out, err = pageops.PageMeta(ctx, p)
			case "dom":
				out, err = pageops.PageDOM(ctx, p)
			case "css":
				out, err = pageops.PageCSS(ctx, p, target)
			case "js":
				out, err = pageops.PageJS(ctx, p, target)
			case "screenshot":
				out, err = pageops.PageScreenshot(ctx, p, fullPage)
			}
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			// Screenshots bypass PageTruncate: the data-URI is binary-as-text
			// and cutting it mid-stream would produce an undecodable image.
			// Image size scales with the viewport (or full-page height), not
			// max_bytes — the 50KB truncation convention applies to text
			// extractions only. Mirrors the MCP page_fetch truncation
			// exception.
			if what == "screenshot" {
				fmt.Println(out)
				return nil
			}
			fmt.Println(pageops.PageTruncate(out, maxBytes))
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to fetch (required)")
	cmd.Flags().StringVar(&what, "what", "", "aspect to extract: html, text, links, meta, dom, css, js, screenshot (required)")
	cmd.Flags().StringVar(&selector, "selector", "", "optional CSS selector scoping html/text extraction")
	cmd.Flags().StringVar(&target, "target", "", "resource URL to fetch exactly one css/js resource's content (with --what css|js)")
	cmd.Flags().BoolVar(&fullPage, "full-page", false, "capture the entire scrollable page instead of the visible viewport (screenshot only)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", pageops.DefaultPageMaxBytes, "maximum output size in bytes (default 50000)")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("what")
	return cmd
}

// newPageActCmd builds `page act`: it performs an action on a live page —
// click, input, hover, scroll, select, eval, navigate, back, forward, reload
// and wait (mirroring the MCP page_act surface; type stays deferred, see
// RunE).
func newPageActCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id            string
		action        string
		selector      string
		value         string
		x, y          float64
		waitMode      string
		timeoutMS     int
		width, height int
	)
	cmd := &cobra.Command{
		Use:   "act",
		Short: "Perform an action on a live page",
		Long: `Performs an action on the live page identified by --id.

  --action click|input|hover|scroll|select|eval|navigate|back|forward|reload|wait|set_viewport
                 the interaction to perform (required); type is accepted but
                 not implemented yet (mirrors the MCP page_act stub)

  --selector <css>  target element (click/input/hover/select/wait interactable)
  --value <v>       payload for eval (JS function), input (text), navigate (URL),
                    select (option value)
  --x, --y <n>      scroll offsets in pixels (scroll)
  --wait <mode>     what to wait for (wait action): load, idle, stable,
                    interactable (needs --selector), ms (sleep --timeout-ms;
                    default ms)
  --width, --height <n>  target viewport size in CSS pixels (set_viewport)
  --timeout-ms <n>  per-action timeout in milliseconds (default 30000)

Interaction confirmation strings match the MCP page_act surface; eval prints
the JSON result.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Enforce an explicit per-call timeout so a wedged page (or a
			// stuck wait) cannot block the CLI: default 30s, honoring
			// --timeout-ms. The derived ctx flows through every pageops helper
			// below (mirrors the MCP page_act per-call timeout).
			timeout := defaultCmdTimeout
			if timeoutMS > 0 {
				timeout = time.Duration(timeoutMS) * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), timeout)
			defer cancel()

			switch action {
			case "click", "input", "type", "eval", "navigate", "back", "forward", "reload", "hover", "scroll", "select", "wait", "set_viewport":
			default:
				return fmt.Errorf("page act: unknown action=%q (must be one of click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport)", action)
			}

			// type stays deferred (MCP parity — the MCP page_act type action is
			// still stubbed): reject it before touching the page with the same
			// "not implemented" surface as the MCP stub.
			if action == "type" {
				return fmt.Errorf("page act type: not implemented")
			}
			if action == "set_viewport" && (width <= 0 || height <= 0) {
				return fmt.Errorf("page act: viewport width and height must be positive")
			}
			if action == "eval" && value == "" {
				return fmt.Errorf("page act: eval requires a non-empty --value (a JS function, e.g. '() => document.title')")
			}
			if action == "wait" {
				if err := pageops.ValidateWait(waitMode); err != nil {
					return fmt.Errorf("page act: %w", err)
				}
			}

			// Fail-fast switch passed: forward to a live serve process when one
			// is running; else fall back to the in-process path below.
			var vp *browsers.Viewport
			if action == "set_viewport" {
				vp = &browsers.Viewport{Width: width, Height: height}
			}
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageActViaIPC(cmd, ctx, cl, id, action, selector, value, waitMode, x, y, timeoutMS, vp)
			}
			printServeHint(cmd)

			p, err := mgr.Page(ctx, browsers.PageID(id)) // opens the Browser if needed; probes for liveness
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}

			var out string
			switch action {
			case "click":
				out, err = pageops.PageClick(ctx, p, selector)
			case "input":
				out, err = pageops.PageInput(ctx, p, selector, value)
			case "eval":
				out, err = pageops.PageEval(ctx, p, value)
			case "navigate":
				out, err = pageops.PageNavigate(ctx, p, value)
			case "back":
				out, err = pageops.PageBack(ctx, p)
			case "forward":
				out, err = pageops.PageForward(ctx, p)
			case "reload":
				out, err = pageops.PageReload(ctx, p)
			case "hover":
				out, err = pageops.PageHover(ctx, p, selector)
			case "scroll":
				out, err = pageops.PageScroll(ctx, p, x, y)
			case "select":
				out, err = pageops.PageSelect(ctx, p, selector, value)
			case "wait":
				out, err = pageops.PageWait(ctx, p, waitMode, selector, timeout)
			case "set_viewport":
				out, err = pageops.PageSetViewport(ctx, p, width, height)
			}
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to act on (required)")
	cmd.Flags().StringVar(&action, "action", "", "action: click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport (required)")
	cmd.Flags().StringVar(&selector, "selector", "", "CSS selector targeting the element (element actions)")
	cmd.Flags().StringVar(&value, "value", "", "value for eval (JS function), input (text), navigate (URL), select (option value)")
	cmd.Flags().Float64Var(&x, "x", 0, "horizontal scroll offset in pixels (scroll)")
	cmd.Flags().Float64Var(&y, "y", 0, "vertical scroll offset in pixels (scroll)")
	cmd.Flags().StringVar(&waitMode, "wait", "ms", "wait mode (wait action): load, idle, stable, interactable (needs --selector), ms (sleep --timeout-ms; default ms)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 30000, "per-action timeout in milliseconds (default 30000)")
	cmd.Flags().IntVar(&width, "width", 0, "target viewport width in CSS pixels (set_viewport action)")
	cmd.Flags().IntVar(&height, "height", 0, "target viewport height in CSS pixels (set_viewport action)")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("action")
	return cmd
}

// newPageCloseCmd builds `page close`: it resolves the page with the given id
// via Manager.Page (surfacing restart-aware errors for pre-restart sessions),
// then closes it via Manager.Open -> Browser.ClosePage (mirroring SessionClose).
func newPageCloseCmd(mgr *browsers.Manager) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "close",
		Short: "Close an open page",
		Long: `Closes the page identified by --id, releasing its browser tab. The
session id becomes invalid afterwards.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageCloseViaIPC(cmd, ctx, cl, id)
			}
			printServeHint(cmd)

			b, err := mgr.Open(ctx) // lazily launches (or returns) the single Browser
			if err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			// Resolve through the manager first so a pre-restart session id
			// surfaces the explicit "browser was restarted" error (mirrors the
			// MCP session_close tool).
			if _, err := mgr.Page(ctx, browsers.PageID(id)); err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			if err := b.ClosePage(ctx, browsers.PageID(id)); err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			fmt.Printf("session %s closed\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to close (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// pageListViaIPC implements `page list` against a live serve process: it opens
// the browser (idempotent — mirrors Manager.Open's laziness) and lists its
// pages, printing "id\turl" rows byte-identically to the in-process path.
func pageListViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client) error {
	var open ipc.BrowserOpenResult
	if err := cl.Call(ctx, ipc.MethodBrowserOpen, &ipc.BrowserOpenParams{}, &open); err != nil {
		return fmt.Errorf("page list: %w", err)
	}
	var res ipc.PageListResult
	if err := cl.Call(ctx, ipc.MethodPageList, nil, &res); err != nil {
		return fmt.Errorf("page list: %w", err)
	}
	if len(res.Pages) == 0 {
		fmt.Println("no open pages")
		return nil
	}
	for _, p := range res.Pages {
		fmt.Printf("%s\t%s\n", p.ID, p.URL)
	}
	return nil
}

// pageFetchViaIPC implements `page fetch` against a live serve process: it
// forwards the extraction (and the --target/--full-page params) to page.fetch
// (the server truncates to max_bytes itself) and prints the data
// byte-identically to the in-process path. The CLI's --max-bytes 0 means "no
// truncation" (pageops.PageTruncate returns s unchanged), which the wire
// contract cannot express (0 → the server default), so it is forwarded as an
// effectively-unbounded cap.
func pageFetchViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, id, what, selector, target string, fullPage bool, maxBytes int) error {
	if maxBytes <= 0 {
		maxBytes = int(^uint(0) >> 1) // CLI parity: 0 means "no truncation"
	}
	var res ipc.PageFetchResult
	if err := cl.Call(ctx, ipc.MethodPageFetch, &ipc.PageFetchParams{
		SessionID: id,
		What:      what,
		Selector:  selector,
		Target:    target,
		FullPage:  fullPage,
		MaxBytes:  maxBytes,
	}, &res); err != nil {
		return fmt.Errorf("page fetch: %w", err)
	}
	fmt.Println(res.Data)
	return nil
}

// pageActViaIPC implements `page act` against a live serve process: it
// forwards the action (with its selector/value/x/y/timeout/wait/viewport
// params) to page.act and prints the confirmation byte-identically to the
// in-process path.
func pageActViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, id, action, selector, value, waitMode string, x, y float64, timeoutMS int, vp *browsers.Viewport) error {
	var res ipc.PageActResult
	if err := cl.Call(ctx, ipc.MethodPageAct, &ipc.PageActParams{
		SessionID: id,
		Action:    action,
		Selector:  selector,
		Value:     value,
		X:         x,
		Y:         y,
		TimeoutMS: timeoutMS,
		WaitMode:  waitMode,
		Viewport:  vp,
	}, &res); err != nil {
		return fmt.Errorf("page act: %w", err)
	}
	fmt.Println(res.Data)
	return nil
}

// pageCloseViaIPC implements `page close` against a live serve process: it
// forwards the close to page.close and prints the confirmation
// byte-identically to the in-process path.
func pageCloseViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, id string) error {
	if err := cl.Call(ctx, ipc.MethodPageClose, &ipc.PageCloseParams{SessionID: id}, nil); err != nil {
		return fmt.Errorf("page close: %w", err)
	}
	fmt.Printf("session %s closed\n", id)
	return nil
}
