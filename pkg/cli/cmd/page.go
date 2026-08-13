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
  fetch  Extract an aspect of a live page (html, text, links, meta)
  act    Perform an action on a live page (eval implemented; rest pending)
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
// (html, text, links, meta — implemented; dom, css, js, screenshot — deferred)
// and prints it, truncating to --max-bytes on a UTF-8 rune boundary.
func newPageFetchCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id       string
		what     string
		selector string
		maxBytes int
	)
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch an aspect of a live page",
		Long: `Extracts an aspect of a live page identified by --id (the session id
returned by the fetch command) and prints it.

  --what html|text|links|meta  the aspect to extract (required; dom, css, js
                               and screenshot are not yet implemented — see
                               WEB-18/19/20)
  --selector <css>             optional CSS selector scoping html/text
  --max-bytes <n>              cap output size (default 50000)

links and meta print JSON, mirroring the MCP page_fetch shapes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			// Fail fast on the deferred extractors (and unknown values) so the
			// user is told the extractor itself is unimplemented, not that the
			// page lookup failed.
			switch what {
			case "html", "text", "links", "meta":
			case "dom", "css", "js", "screenshot":
				return fmt.Errorf("page fetch %s: not implemented (engine lacks it; see WEB-18/19/20)", what)
			default:
				return fmt.Errorf("page fetch: unknown what=%q (must be one of html, text, links, meta, dom, css, js, screenshot)", what)
			}

			// Fail-fast switch passed: forward to a live serve process when one
			// is running (only html|text|links|meta ever leave the client — the
			// deferred extractors returned above stay in-process and verbatim);
			// else fall back to the in-process path below.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageFetchViaIPC(cmd, ctx, cl, id, what, selector, maxBytes)
			}
			printServeHint(cmd)

			b, err := mgr.Open(ctx) // lazily launches (or returns) the single Browser
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			p, err := b.Page(browsers.PageID(id))
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
			}
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			fmt.Println(pageops.PageTruncate(out, maxBytes))
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to fetch (required)")
	cmd.Flags().StringVar(&what, "what", "", "aspect to extract: html, text, links, meta (required)")
	cmd.Flags().StringVar(&selector, "selector", "", "optional CSS selector scoping html/text extraction")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", pageops.DefaultPageMaxBytes, "maximum output size in bytes (default 50000)")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("what")
	return cmd
}

// newPageActCmd builds `page act`: it performs an action on a live page. Only
// eval is implemented (JS via Page.Rod.Context(ctx).Eval); the other eleven
// actions are deferred until the engine interaction methods land (WEB-22/23).
func newPageActCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id        string
		action    string
		selector  string
		value     string
		x, y      float64
		timeoutMS int
	)
	cmd := &cobra.Command{
		Use:   "act",
		Short: "Perform an action on a live page",
		Long: `Performs an action on the live page identified by --id.

  --action eval  run a JavaScript function (e.g. '() => document.title') and
                 print the JSON result (implemented)
  --action click|input|type|navigate|back|forward|reload|hover|scroll|select|wait
                 accepted but not implemented yet — deferred to a follow-up
                 CLI ticket (engine interaction methods pending WEB-22/WEB-23)

  --selector <css>  target element (element actions)
  --value <v>       payload for eval (JS function), input/type (text), navigate (URL)
  --x, --y <n>      scroll offsets in pixels (scroll)
  --timeout-ms <n>  per-action timeout in milliseconds (default 30000)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			switch action {
			case "click", "input", "type", "eval", "navigate", "back", "forward", "reload", "hover", "scroll", "select", "wait":
			default:
				return fmt.Errorf("page act: unknown action=%q (must be one of click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait)", action)
			}

			// Fail fast on the deferred actions so the user is told the action
			// itself is unimplemented, not that the page lookup failed.
			if action != "eval" {
				return fmt.Errorf("page act %s: not implemented — deferred to a follow-up CLI ticket (engine interaction methods pending WEB-22/WEB-23)", action)
			}
			if value == "" {
				return fmt.Errorf("page act: eval requires a non-empty --value (a JS function, e.g. '() => document.title')")
			}

			// Fail-fast switch passed: forward to a live serve process when one
			// is running (only action=eval ever leaves the client — the other
			// eleven actions returned above stay in-process and verbatim); else
			// fall back to the in-process path below.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageActViaIPC(cmd, ctx, cl, id, value)
			}
			printServeHint(cmd)

			b, err := mgr.Open(ctx) // lazily launches (or returns) the single Browser
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			p, err := b.Page(browsers.PageID(id))
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}

			out, err := pageops.PageEval(ctx, p, value)
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to act on (required)")
	cmd.Flags().StringVar(&action, "action", "", "action: click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait (required)")
	cmd.Flags().StringVar(&selector, "selector", "", "CSS selector targeting the element (element actions)")
	cmd.Flags().StringVar(&value, "value", "", "value for eval (JS function), input/type (text), navigate (URL)")
	cmd.Flags().Float64Var(&x, "x", 0, "horizontal scroll offset in pixels (scroll)")
	cmd.Flags().Float64Var(&y, "y", 0, "vertical scroll offset in pixels (scroll)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 30000, "per-action timeout in milliseconds (default 30000)")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("action")
	return cmd
}

// newPageCloseCmd builds `page close`: it closes the page with the given id via
// Manager.Open -> Browser.ClosePage (mirroring SessionClose).
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
// forwards the extraction to page.fetch (the server truncates to max_bytes
// itself) and prints the data byte-identically to the in-process path. The
// CLI's --max-bytes 0 means "no truncation" (pageops.PageTruncate returns s
// unchanged), which the wire contract cannot express (0 → the server default),
// so it is forwarded as an effectively-unbounded cap.
func pageFetchViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, id, what, selector string, maxBytes int) error {
	if maxBytes <= 0 {
		maxBytes = int(^uint(0) >> 1) // CLI parity: 0 means "no truncation"
	}
	var res ipc.PageFetchResult
	if err := cl.Call(ctx, ipc.MethodPageFetch, &ipc.PageFetchParams{
		SessionID: id,
		What:      what,
		Selector:  selector,
		MaxBytes:  maxBytes,
	}, &res); err != nil {
		return fmt.Errorf("page fetch: %w", err)
	}
	fmt.Println(res.Data)
	return nil
}

// pageActViaIPC implements `page act` (eval) against a live serve process: it
// forwards the eval to page.act and prints the JSON result byte-identically to
// the in-process path.
func pageActViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, id, value string) error {
	var res ipc.PageActResult
	if err := cl.Call(ctx, ipc.MethodPageAct, &ipc.PageActParams{
		SessionID: id,
		Action:    "eval",
		Value:     value,
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
