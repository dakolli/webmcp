// Package cmd implements the web-mcp CLI's cobra commands, talking directly to
// the pkg/browsers core engine (never through pkg/mcp). WEB-28 owns this file.
package cmd

import (
	"context"
	"fmt"
	"strings"
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
With one page open the page id is optional everywhere; 'last' means the most
recently opened page and a bare number resolves to page-N (see each
subcommand's --help).

Typical workflow: 'web-cli fetch <url>' opens a page and prints its session id
(page-1); 'web-cli page list' shows the open pages (id + url); 'web-cli page
fetch [id] [what]' reads an aspect of a live page; 'web-cli page act [id]
[action] [payload...]' drives it; 'web-cli page close [id]' closes it. 'web-cli
page fetch <url>' skips the separate fetch step: it opens the URL in a new page
first and reads the aspect in one go.

Every page command honors the global --browser-id: when it is set, page ids
resolve only against that browser and an unknown id fails with the live ids.

Subcommands:
  list   List the browser's open pages (id, url)
  fetch  Extract an aspect of a live page (html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, console, body, article, marks, security); a URL opens a new page first
  act    Perform an action on a live page (click, input, hover, scroll, select, eval, navigate, back, forward, reload, wait, set_viewport, emulate, clear_logs)
  close  Close an open page

Run a subcommand with --help for its flags and examples.`,
		Example: `  web-cli page list                  # see your open pages (id + url)
  web-cli page fetch links           # extract links from the single open page
  web-cli page fetch https://x.com text   # open + read in one step (no prior fetch)
  web-cli page act click "#search"   # click on the single open page
  web-cli page close                 # close the single open page`,
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
		Example: `  web-cli page list    # one "id<TAB>url" row per open page`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			browserID := explicitBrowserID(cmd)
			// Forward to a live serve process when one is running; else fall
			// back to the in-process path below (browser dies with this
			// command — the serve hint on stderr says so).
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageListViaIPC(cmd, ctx, cl, browserID)
			}
			printServeHint(cmd)

			pages, err := browserPages(ctx, mgr, nil, browserID, "page list")
			if err != nil {
				return err
			}
			reportBrowserTarget(cmd, browserID)
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

// newPageFetchCmd builds `page fetch`: it reads any aspect in the shared pageops
// registry and prints it, truncating to --max-bytes on a UTF-8 rune boundary
// except for the aspects whose reader caps its own output.
func newPageFetchCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id            string
		what          string
		url           string
		selector      string
		target        string
		fullPage      bool
		maxBytes      int
		includeValues bool
		identity      string
	)
	cmd := &cobra.Command{
		Use:   "fetch [id|url] [what]",
		Short: "Extract an aspect of a live page",
		Long: `Extracts an aspect of a live page (html, text, links, meta, dom, css,
js, screenshot, interactives, a11y, network, console, body, article, marks,
security, storage) and prints it. The page id is optional when
only one page is open; 'last' means the most recently opened page; a bare number
resolves to page-N. A URL in the [id] slot opens a NEW page for it first —
creating the browser if needed, exactly like 'web-cli fetch <url>' — and then
extracts the aspect from that fresh page (one step, no separate "open first"
dance).

  [id]    the page/session id to read (optional with one page; 'last'; or a
          bare number, e.g. 12 → page-12)
  [url]   an absolute URL (contains "://") to open in a new page first, then
          extract the aspect — mirrors 'web-cli fetch <url>' (wait: idle)
  [what]  the aspect to extract: html, text, links, meta, dom, css, js,
          screenshot, interactives, a11y, network, console, body, article, marks,
          security, storage (default: text)

  --id <session>     the page/session id (the positional [id] wins when both
                     are given; never treated as a URL)
  --what <what>      the aspect to extract (the positional [what] wins when
                     both are given; default text)
  --selector <css>   optional CSS selector scoping html/text extraction
  --target <url>     fetch exactly one css/js resource's full content instead
                     of the {url,type,size} list (with --what css|js); with
                     --what body, select the entry by seq or URL substring
  --full-page        capture the entire scrollable page (screenshot only)
  --include-values   with --what storage, print real cookie and storage values
                     instead of <redacted> (exposes live session tokens)
  --max-bytes <n>    cap output size (default 50000; screenshots,
                     interactives, a11y, network, console, body, article,
                     marks and security are never truncated)
  --identity <name>  open the URL in an incognito identity inside the target
                     browser (default "default"): a new name is created on
                     first use, and identities keep separate cookies/storage
                     in one Chromium process. Only valid with a URL.

links, meta, dom, interactives, a11y, network, console, body, article, marks and
security print JSON, mirroring the MCP page_fetch shapes. article is the readable prose body as
markdown ({title, byline?, excerpt?, text, words, truncated, url}); prefer it
over text when you want the article rather than the whole page. marks is the
numbered overlay ({marks:[{index, role, text?, x, y, width, height}], screenshot,
viewport_width, viewport_height, truncated}) whose x/y feed 'page act click_at'.
security is the passive audit ({checks:[{id, status, evidence, detail}], count})
covering response headers, CSP (header or meta), cookie flags, mixed content,
subresource integrity, form targets and CORS. storage is a read-only snapshot of
the browser context ({cookies, local_storage, session_storage, indexed_db,
caches} with a *_truncated counter per section); every value is <redacted>
unless --include-values is set, which exposes live session tokens.
Screenshots print a data:image/png;base64,... data-URI. network/console emit
{entries,count,dropped,truncated} over the newest 100 buffered entries; capture is
cumulative from page creation, so clear with 'page act clear_logs' right before the
step you want to observe, then filter client-side. body fetches the browser's
stored response body for one entry ({seq,url,content_type,bytes,truncated,
body|base64}); target selects it by seq or URL substring (newest match wins), the
payload is capped at 256 KB and binary bodies come back as base64.`,
		Example: `  web-cli page fetch links                              # single open page → its links
  web-cli page fetch 12 dom                             # page-12's DOM tree
  web-cli page fetch 12 text --selector ".article"      # scoped text extraction
  web-cli page fetch 12 article                         # readable body as markdown
  web-cli page fetch 12 marks                           # numbered boxes + annotated screenshot
  web-cli page fetch last screenshot --full-page        # full-page capture of the latest page
  web-cli page fetch last a11y                          # semantic roles + names + handles
  web-cli page fetch last network  # last 100 network requests + dropped count
  web-cli page fetch last body --target /api/orders  # newest matching API body
  web-cli page fetch last security                      # passive headers/CSP/cookie/mixed-content audit
  web-cli page fetch last storage                       # cookie flags + storage keys, values redacted
  web-cli page fetch last storage --include-values      # real values: exposes live session tokens
  web-cli page fetch https://hackernews.com links       # open + extract in one step`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			browserID := explicitBrowserID(cmd)
			id, what, url = parseFetchArgs(args, id, what)

			if url != "" {
				if err := pageops.ValidateURL(url); err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
			}
			if err := pageops.ValidateAspect(what); err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			identity = strings.TrimSpace(identity)
			if identity != "" {
				if url == "" {
					return fmt.Errorf("page fetch: --identity only applies when opening a URL — omit it to read an existing page")
				}
				if err := browsers.ValidateIdentityName(identity); err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
			}

			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				if url != "" {
					if browserID == "" {
						var open ipc.BrowserOpenResult
						if err := cl.Call(ctx, ipc.MethodBrowserOpen, &ipc.BrowserOpenParams{}, &open); err != nil {
							return fmt.Errorf("page fetch: %w", err)
						}
					} else if err := requireLiveBrowser(ctx, cl, browserID, "page fetch"); err != nil {
						return err
					}
					var summary ipc.PageOpenResult
					if err := cl.Call(ctx, ipc.MethodPageOpen, &ipc.PageOpenParams{
						URL:       url,
						Wait:      "idle",
						BrowserID: browserID,
						Identity:  identity,
					}, &summary); err != nil {
						return fmt.Errorf("page fetch: %w", err)
					}
					reportBrowserTarget(cmd, browserID)
					return pageFetchViaIPC(cmd, ctx, cl, summary.SessionID, what, selector, target, fullPage, maxBytes, includeValues)
				}
				rid, err := resolvePageID(ctx, cmd, mgr, cl, browserID, id, "page fetch")
				if err != nil {
					return err
				}
				reportBrowserTarget(cmd, browserID)
				return pageFetchViaIPC(cmd, ctx, cl, rid, what, selector, target, fullPage, maxBytes, includeValues)
			}
			printServeHint(cmd)

			var p *browsers.Page
			var err error
			if url != "" {
				var b *browsers.Browser
				b, err = openTargetBrowser(ctx, mgr, browserID, "page fetch")
				if err != nil {
					return err
				}
				if identity != "" && identity != browsers.DefaultIdentity {
					b, err = b.NewIdentity(identity)
					if err != nil {
						return fmt.Errorf("page fetch: %w", err)
					}
				}
				reportBrowserTarget(cmd, browserID)
				p, err = b.NewPage(ctx, url, nil, "", "") // new tab; plain launch; stealth applied by default
				if err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
				if err := pageops.WaitForPage(ctx, p, "idle"); err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
			} else {
				var rid string
				rid, err = resolvePageID(ctx, cmd, mgr, nil, browserID, id, "page fetch")
				if err != nil {
					return err
				}
				reportBrowserTarget(cmd, browserID)
				p, err = mgr.Page(ctx, browsers.PageID(rid)) // opens the Browser if needed; probes for liveness
				if err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
			}

			out, err := pageops.PageAspect(ctx, p, pageops.AspectQuery{
				What:          what,
				Selector:      selector,
				Target:        target,
				FullPage:      fullPage,
				IncludeValues: includeValues,
				MaxBytes:      maxBytes,
			})
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to fetch (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&what, "what", "", "aspect to extract: html, text, links, meta, dom, css, js, screenshot, interactives, a11y, network, console, body, article, marks, security, storage (default text)")
	cmd.Flags().StringVar(&selector, "selector", "", "optional CSS selector scoping html/text extraction")
	cmd.Flags().StringVar(&target, "target", "", "resource URL for one css/js resource, or seq/URL substring selecting one network body (with --what body)")
	cmd.Flags().BoolVar(&fullPage, "full-page", false, "capture the entire scrollable page instead of the visible viewport (screenshot only)")
	cmd.Flags().BoolVar(&includeValues, "include-values", false, "with --what storage, print real cookie and localStorage/sessionStorage values instead of <redacted> (exposes live tokens)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", pageops.DefaultPageMaxBytes, "maximum output size in bytes (default 50000)")
	cmd.Flags().StringVar(&identity, "identity", "", "incognito identity for a URL open (default \"default\"); a new name is created on first use, cookies/storage stay separate")
	return cmd
}

// newPageActCmd builds `page act`: it performs an action on a live page —
// click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file,
// hover, scroll, select, eval, navigate, back, forward, reload, wait, set_viewport,
// emulate and clear_logs (mirroring the MCP page_act surface).
func newPageActCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id             string
		action         string
		selector       string
		handle         string
		value          string
		key            string
		button         string
		clickCount     int
		x, y           float64
		targetX        float64
		targetY        float64
		targetSelector string
		paths          []string
		waitMode       string
		timeoutMS      int
		width, height  int
		device         string
	)
	cmd := &cobra.Command{
		Use:   "act [id] [action] [payload...]",
		Short: "Perform an action on a live page",
		Long: `Performs an action on a live page (click, input, type, press_key, click_at,
hover_at, drag_and_drop, upload_file, hover, scroll, select, eval, navigate, back,
forward, reload, wait, set_viewport, emulate, clear_logs) and prints the confirmation. The page id
is optional when only one page is open; 'last' means the most recently opened page;
a bare number resolves to page-N.
Positional payloads fill each action's fields — flags remain as fallbacks.
Element actions also accept --handle from page fetch interactives/dom/links/a11y, e.g.
--handle g3k9x2ab-b6; when both --handle and --selector are given, the handle wins.
A handle is scoped to the document that minted it: after a navigation, reload or
redirect it is rejected as stale, so re-fetch before acting.

  [id] [action] [payload...]   e.g. "12 click \"#submit\"", "last eval '() => document.title'"

  action       positional payloads (in order)
  click        [selector]
  hover        [selector]
  select       [selector] [value]
  input        [selector] [text]
  press_key    [key]
  click_at     [x] [y]
  hover_at     [x] [y]
  drag_and_drop [from_x] [from_y] [to_x] [to_y]
  upload_file  [selector] [path...]
  navigate     [url]
  eval         [js]
  scroll       [x] [y]
  wait         [mode] (optional; default ms)
  set_viewport [w] [h]
  emulate      [device]
  clear_logs   (none)
  back/forward/reload    (none)
  type         [selector] [text]

clear_logs empties the network and console buffers and prints {"network":N,"console":M}.
Logs are cumulative from page creation: clear right before the step whose traffic/logs
you want to observe, then run 'page fetch last network|console' and filter client-side.`,
		Example: `  web-cli page act click "#submit"                          # click on the single open page
  web-cli page act 12 input "#search" "pizza"               # type into a search box
  web-cli page act 12 press_key "Enter"                     # press Enter key
  web-cli page act 12 click_at 100 200                     # click at x=100 y=200
  web-cli page act 12 navigate https://news.ycombinator.com # go somewhere
  web-cli page act last eval '() => document.title'         # run JS on the latest page
  web-cli page act last clear_logs  # reset both buffers, print {network,console} counts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Positional args win over the --flags; payload counts are
			// validated here, before any browser work.
			parsed, err := parseActArgs(args, actArgFlags{
				id: id, action: action, selector: selector, handle: handle, value: value, key: key,
				button: button, clickCount: clickCount, targetSelector: targetSelector,
				paths: paths, waitMode: waitMode, device: device, x: x, y: y,
				targetX: targetX, targetY: targetY, width: width, height: height,
			})
			if err != nil {
				return err
			}
			id, action, selector, handle = parsed.id, parsed.action, parsed.selector, parsed.handle
			value, key, button, clickCount = parsed.value, parsed.key, parsed.button, parsed.clickCount
			targetSelector, paths, waitMode, device = parsed.targetSelector, parsed.paths, parsed.waitMode, parsed.device
			x, y, targetX, targetY = parsed.x, parsed.y, parsed.targetX, parsed.targetY
			width, height = parsed.width, parsed.height

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
			case "click", "input", "type", "press_key", "click_at", "hover_at", "drag_and_drop", "upload_file", "eval", "navigate", "back", "forward", "reload", "hover", "scroll", "select", "wait", "set_viewport", "emulate", "clear_logs":
			default:
				return fmt.Errorf("page act: unknown action=%q (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs)", action)
			}

			if action == "set_viewport" && (width <= 0 || height <= 0) {
				return fmt.Errorf("page act: viewport width and height must be positive")
			}
			if action == "emulate" {
				if device == "" {
					return fmt.Errorf("page act: emulate requires a device")
				}
				if err := browsers.ValidateDeviceName(device); err != nil {
					return fmt.Errorf("page act: %w", err)
				}
			}
			if action == "eval" && value == "" {
				return fmt.Errorf("page act: eval requires a non-empty --value (a JS function, e.g. '() => document.title')")
			}
			if action == "wait" {
				if err := pageops.ValidateWait(waitMode); err != nil {
					return fmt.Errorf("page act: %w", err)
				}
			}

			var vp *browsers.Viewport
			if action == "set_viewport" {
				vp = &browsers.Viewport{Width: width, Height: height}
			}
			browserID := explicitBrowserID(cmd)
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				rid, err := resolvePageID(ctx, cmd, mgr, cl, browserID, id, "page act")
				if err != nil {
					return err
				}
				reportBrowserTarget(cmd, browserID)
				return pageActViaIPC(cmd, ctx, cl, rid, action, handle, selector, value, key, button, clickCount, x, y, targetX, targetY, targetSelector, paths, waitMode, timeoutMS, vp, device)
			}
			printServeHint(cmd)

			rid, err := resolvePageID(ctx, cmd, mgr, nil, browserID, id, "page act")
			if err != nil {
				return err
			}
			reportBrowserTarget(cmd, browserID)
			p, err := mgr.Page(ctx, browsers.PageID(rid)) // opens the Browser if needed; probes for liveness
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}

			before, stateErr := pageops.PageStateOf(ctx, p)

			var out string
			switch action {
			case "click":
				out, err = pageops.PageClickOpts(ctx, p, handle, selector, button, clickCount)
			case "input":
				out, err = pageops.PageInput(ctx, p, handle, selector, value)
			case "type":
				out, err = pageops.PageType(ctx, p, handle, selector, value)
			case "press_key":
				keyStr := key
				if keyStr == "" {
					keyStr = value
				}
				out, err = pageops.PagePressKey(ctx, p, handle, selector, keyStr)
			case "click_at":
				out, err = pageops.PageClickAt(ctx, p, x, y, button, clickCount)
			case "hover_at":
				out, err = pageops.PageHoverAt(ctx, p, x, y)
			case "drag_and_drop":
				out, err = pageops.PageDragAndDrop(ctx, p, x, y, targetX, targetY, selector, targetSelector)
			case "upload_file":
				pList := paths
				if len(pList) == 0 && value != "" {
					pList = []string{value}
				}
				out, err = pageops.PageUploadFile(ctx, p, handle, selector, pList)
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
				out, err = pageops.PageHover(ctx, p, handle, selector)
			case "scroll":
				out, err = pageops.PageScroll(ctx, p, x, y)
			case "select":
				out, err = pageops.PageSelect(ctx, p, handle, selector, value)
			case "wait":
				out, err = pageops.PageWait(ctx, p, waitMode, handle, selector, timeout)
			case "set_viewport":
				out, err = pageops.PageSetViewport(ctx, p, width, height)
			case "emulate":
				out, err = pageops.PageEmulate(ctx, p, device)
			case "clear_logs":
				out, err = pageops.PageClearLogs(p)
			}
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			if stateErr == nil {
				after := pageops.PageStateAfterAct(ctx, p, action, before)
				out += pageops.ActStateDeltaFor(action, before, after)
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to act on (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&action, "action", "", "action: click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs")
	cmd.Flags().StringVar(&selector, "selector", "", "CSS selector targeting the element (element actions)")
	cmd.Flags().StringVar(&handle, "handle", "", "element handle from page fetch (g<document>-b<backendNodeId>, scoped to its document); wins over --selector (element actions)")
	cmd.Flags().StringVar(&value, "value", "", "value for eval (JS function), input (text), type (text), navigate (URL), select (option value)")
	cmd.Flags().StringVar(&key, "key", "", "key or key combo for press_key action (e.g. Enter, Escape, Control+A)")
	cmd.Flags().StringVar(&button, "button", "left", "mouse button for click and click_at: left, right, middle")
	cmd.Flags().IntVar(&clickCount, "click-count", 1, "click count for click and click_at (1 for single click, 2 for double click)")
	cmd.Flags().Float64Var(&x, "x", 0, "horizontal coordinate in pixels (scroll, click_at, hover_at, drag_and_drop)")
	cmd.Flags().Float64Var(&y, "y", 0, "vertical coordinate in pixels (scroll, click_at, hover_at, drag_and_drop)")
	cmd.Flags().Float64Var(&targetX, "target-x", 0, "target horizontal coordinate in pixels (drag_and_drop)")
	cmd.Flags().Float64Var(&targetY, "target-y", 0, "target vertical coordinate in pixels (drag_and_drop)")
	cmd.Flags().StringVar(&targetSelector, "target-selector", "", "target element selector (drag_and_drop)")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "file paths for upload_file action")
	cmd.Flags().StringVar(&waitMode, "wait", "ms", "wait mode (wait action): load, idle, stable, interactable (needs --selector or --handle), ms (sleep --timeout-ms; default ms)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 30000, "per-action timeout in milliseconds (default 30000)")
	cmd.Flags().IntVar(&width, "width", 0, "target viewport width in CSS pixels (set_viewport action)")
	cmd.Flags().IntVar(&height, "height", 0, "target viewport height in CSS pixels (set_viewport action)")
	cmd.Flags().StringVar(&device, "device", "", "go-rod device preset to apply (emulate action), e.g. IPhoneX, IPadPro")
	return cmd
}

// newPageCloseCmd builds `page close`: it resolves the page with the given id
// via Manager.Page (surfacing restart-aware errors for pre-restart sessions),
// then closes it via Manager.Open -> Browser.ClosePage (mirroring SessionClose).
func newPageCloseCmd(mgr *browsers.Manager) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "close [id]",
		Short: "Close an open page",
		Long: `Closes the page with the given id, releasing its browser tab. The id is
optional when only one page is open; 'last' means the most recently opened
page; a bare number resolves to page-N. The session id becomes invalid
afterwards.`,
		Example: `  web-cli page close     # close the single open page
  web-cli page close 12  # close page-12
  web-cli page close last`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				id = args[0] // the positional [id] wins over --id
			}
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			browserID := explicitBrowserID(cmd)
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				rid, err := resolvePageID(ctx, cmd, mgr, cl, browserID, id, "page close")
				if err != nil {
					return err
				}
				reportBrowserTarget(cmd, browserID)
				return pageCloseViaIPC(cmd, ctx, cl, rid, browserID)
			}
			printServeHint(cmd)

			rid, err := resolvePageID(ctx, cmd, mgr, nil, browserID, id, "page close")
			if err != nil {
				return err
			}
			reportBrowserTarget(cmd, browserID)

			b, err := openTargetBrowser(ctx, mgr, browserID, "page close")
			if err != nil {
				return err
			}
			// Resolve through the manager first so a pre-restart session id
			// surfaces the explicit "browser was restarted" error (mirrors the
			// MCP session_close tool).
			if _, err := mgr.Page(ctx, browsers.PageID(rid)); err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			if err := b.ClosePage(ctx, browsers.PageID(rid)); err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			fmt.Printf("session %s closed\n", rid)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to close (optional with one page open; 'last'; or a bare number)")
	return cmd
}

// pageListViaIPC implements `page list` against a live serve process: it asks
// for the daemon's live browsers via browser.list (which, like the CLI `browser
// list` command, never lazily launches a browser) and prints "id\turl" rows
// byte-identically to the in-process path. A pinned browserID narrows the rows
// to that browser and fails with the live ids when it is not running. The
// server's own "no browser open" answer maps to the friendly "no open pages"
// output with exit 0 (mirroring browserListViaIPC); any other failure
// propagates as a real error.
func pageListViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, browserID string) error {
	res, err := browserSnapshotViaIPC(ctx, cl, "page list")
	if err != nil {
		return err
	}
	pages := res.Pages
	if browserID != "" {
		entry, err := selectBrowserEntry(res.Browsers, browserID, "page list")
		if err != nil {
			return err
		}
		pages = entry.Pages
		reportBrowserTarget(cmd, browserID)
	}
	if len(pages) == 0 {
		fmt.Println("no open pages")
		return nil
	}
	for _, p := range pages {
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
func pageFetchViaIPC(
	cmd *cobra.Command,
	ctx context.Context,
	cl *ipc.Client,
	id, what, selector, target string,
	fullPage bool,
	maxBytes int,
	includeValues bool,
) error {
	if maxBytes <= 0 {
		maxBytes = int(^uint(0) >> 1) // CLI parity: 0 means "no truncation"
	}
	var res ipc.PageFetchResult
	if err := cl.Call(ctx, ipc.MethodPageFetch, &ipc.PageFetchParams{
		SessionID:     id,
		What:          what,
		Selector:      selector,
		Target:        target,
		FullPage:      fullPage,
		MaxBytes:      maxBytes,
		IncludeValues: includeValues,
	}, &res); err != nil {
		return fmt.Errorf("page fetch: %w", err)
	}
	fmt.Println(res.Data)
	return nil
}

// pageActViaIPC implements `page act` against a live serve process: it
// forwards the action (with its handle/selector/value/key/button/click_count/
// x/y/target_x/target_y/target_selector/paths/timeout/wait/viewport/device
// params) to page.act and prints the confirmation byte-identically to the
// in-process path.
func pageActViaIPC(
	cmd *cobra.Command,
	ctx context.Context,
	cl *ipc.Client,
	id, action, handle, selector, value, key, button string,
	clickCount int,
	x, y, targetX, targetY float64,
	targetSelector string,
	paths []string,
	waitMode string,
	timeoutMS int,
	vp *browsers.Viewport,
	device string,
) error {
	var res ipc.PageActResult
	if err := cl.Call(ctx, ipc.MethodPageAct, &ipc.PageActParams{
		SessionID:      id,
		Action:         action,
		Selector:       selector,
		Handle:         handle,
		Value:          value,
		Key:            key,
		Button:         button,
		ClickCount:     clickCount,
		X:              x,
		Y:              y,
		TargetX:        targetX,
		TargetY:        targetY,
		TargetSelector: targetSelector,
		Paths:          paths,
		TimeoutMS:      timeoutMS,
		WaitMode:       waitMode,
		Viewport:       vp,
		Device:         device,
	}, &res); err != nil {
		return fmt.Errorf("page act: %w", err)
	}
	fmt.Println(res.Data)
	return nil
}

// pageCloseViaIPC implements `page close` against a live serve process: it
// forwards the close (and the optional owning browser id) to page.close and
// prints the confirmation byte-identically to the in-process path.
func pageCloseViaIPC(
	cmd *cobra.Command,
	ctx context.Context,
	cl *ipc.Client,
	id, browserID string,
) error {
	params := &ipc.PageCloseParams{SessionID: id, BrowserID: browserID}
	if err := cl.Call(ctx, ipc.MethodPageClose, params, nil); err != nil {
		return fmt.Errorf("page close: %w", err)
	}
	fmt.Printf("session %s closed\n", id)
	return nil
}
