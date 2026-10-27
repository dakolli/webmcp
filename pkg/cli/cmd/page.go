// Package cmd implements the web-mcp CLI's cobra commands, talking directly to
// the pkg/browsers core engine (never through pkg/mcp). WEB-28 owns this file.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/ipc"
	"github.com/dakolli/webmcp/pkg/cli/pageops"
	"github.com/dakolli/webmcp/pkg/har"
)

const (
	// defaultCmdTimeout bounds every page subcommand so a hung page (or a
	// first-run Chromium download) cannot block the CLI. Mirrors the 30s
	// per-call timeout the MCP tools enforce (pkg/mcp/tools/fetch.go).
	defaultCmdTimeout = 30 * time.Second
	// maxPageTimeoutMS caps page fetch's --timeout-ms so a caller cannot pin a
	// call open indefinitely.
	maxPageTimeoutMS = 120_000
	// harUncapped is the HAR encoder's sentinel for "export every buffered
	// entry": the document's comment reads "Cap: none" and truncated stays 0.
	harUncapped = -1
)

// rawActionsEnabled reports whether WEB_MCP_RAW_ACTIONS is set in this CLI
// process. A serve daemon reads its own environment, so the client must read
// the hatch itself and forward the intent over the socket.
func rawActionsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(browsers.RawActionsEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// pageFetchTimeout resolves page fetch's --timeout-ms: absent (0 or negative)
// uses the 30s default and a supplied value is clamped to 1ms..120s.
func pageFetchTimeout(ms int) time.Duration {
	if ms <= 0 {
		return defaultCmdTimeout
	}
	if ms > maxPageTimeoutMS {
		return time.Duration(maxPageTimeoutMS) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

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
  fetch  Extract an aspect of a live page (html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, console, body, article, tables, forms, marks, security, storage, sinks, postmessage, har); a URL opens a new page first
  act    Perform an action on a live page (click, input, hover, scroll, select, eval, navigate, back, forward, reload, wait, set_viewport, emulate, clear_logs, sinks_install, sinks_clear, replay, replay_fuzz, inject_canary, wait_open, auth_save, auth_restore, auth_list, poc_bundle)
  auth   Save, restore or list named auth snapshots (counts only, never values)
  close  Close an open page

Run a subcommand with --help for its flags and examples.`,
		Example: `  web-cli page list                  # see your open pages (id + url)
  web-cli page fetch links           # extract links from the single open page
  web-cli page fetch https://x.com text   # open + read in one step (no prior fetch)
  web-cli page act click "#search"   # click on the single open page
  web-cli page close                 # close the single open page`,
		PersistentPreRunE: silenceRuntimeUsage,
	}
	cmd.AddCommand(
		newPageListCmd(mgr),
		newPageFetchCmd(mgr),
		newPageActCmd(mgr),
		newPageAuthCmd(mgr),
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

// silenceRuntimeUsage stops cobra printing the whole usage block after a page
// command fails at runtime: usage only helps for a malformed invocation, which
// cobra rejects before RunE runs.
func silenceRuntimeUsage(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	return nil
}

// reportFallbackBrowserID writes the resolved browser id to stderr only when it
// came from the inherited --browser-id flag; an explicit --browser already names
// the target, so echoing it is noise on a successful run.
func reportFallbackBrowserID(cmd *cobra.Command, browserID string) {
	if cmd.Flags().Changed("browser") {
		return
	}
	reportBrowserTarget(cmd, browserID)
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

			entries, err := browserEntries(ctx, mgr, nil, "page list")
			if err != nil {
				return err
			}
			var pages []browsers.PageInfo
			if browserID == "" {
				for _, entry := range entries {
					pages = append(pages, entry.Pages...)
				}
			} else {
				entry, err := selectBrowserEntry(entries, browserID, "page list")
				if err != nil {
					return err
				}
				pages = entry.Pages
			}
			reportFallbackBrowserID(cmd, browserID)
			if len(pages) == 0 {
				fmt.Println("no open pages")
				return nil
			}
			for _, p := range pages {
				fmt.Println(pageListRow(p))
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
		browserID     string
		owner         string
		what          string
		url           string
		selector      string
		target        string
		fullPage      bool
		maxBytes      int
		includeValues bool
		identity      string
		timeoutMS     int
		outPath       string
	)
	cmd := &cobra.Command{
		Use:   "fetch [id|url] [what]",
		Short: "Extract an aspect of a live page",
		Long: `Extracts an aspect of a live page (html, text, links, meta, dom, css,
js, screenshot, interactives, a11y, network, console, body, article, tables,
forms, marks, security, storage, sinks, postmessage, har) and prints it. The page id is optional when
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
          screenshot, interactives, a11y, network, console, body, article, tables,
          forms, marks, security, storage, sinks, postmessage, har (default: text)

  --id <session>     the page/session id (the positional [id] wins when both
                     are given; never treated as a URL)
  --what <what>      the aspect to extract (the positional [what] wins when
                     both are given; default text)
  --selector <css>   optional CSS selector scoping html/text/interactives extraction
  --target <url>     fetch exactly one css/js resource's full content instead
                     of the {url,type,size} list (with --what css|js); with
                     --what body, select the entry by seq or URL substring
  --full-page        capture the entire scrollable page (screenshot only)
  --include-values   with --what storage, print real cookie and storage values
                     instead of <redacted> (exposes live session tokens)
  --max-bytes <n>    cap output size (default 50000; screenshots,
                     interactives, a11y, network, console, body, article, tables,
                     forms, marks, security, sinks and postmessage are never truncated)
  --out <path>       write the HAR body to this file instead of stdout (har only;
                     the parent directory must already exist, and the file is
                     mode 0600). Without --max-bytes, or with --max-bytes 0, the
                     CLI asks for the uncapped export: the file holds every
                     observed entry and its comment reads "Cap: none".
  --timeout-ms <n>   per-call timeout in milliseconds (default 30000, clamped
                     to 1..120000); a full-page screenshot keeps a 90s floor
                     unless you ask for more
  --identity <name>  open the URL in an incognito identity inside the target
                     browser (default "default"): a new name is created on
                     first use, and identities keep separate cookies/storage
                     in one Chromium process. Only valid with a URL.
  --browser <id>     resolve the page inside that browser only; use it when
                     more than one browser is live. Wins over --owner.
  --owner <label>    restrict the lookup to browsers with that owner (1-64
                     chars: A-Z, a-z, 0-9, '.', '_', '-'). A page held by
                     another owner is refused, never returned.

links, meta, dom, interactives, a11y, network, console, body, article, tables,
forms, marks, security, sinks and postmessage print JSON, mirroring the MCP page_fetch shapes. article is the readable prose body as
markdown ({title, byline?, excerpt?, text, words, truncated, url}); prefer it
over text when you want the article rather than the whole page. marks is the
numbered overlay ({marks:[{index, role, text?, x, y, width, height}], screenshot,
viewport_width, viewport_height, truncated}) whose x/y feed 'page act click_at'.
security is the passive audit ({checks:[{id, status, evidence, detail}], count})
covering response headers, CSP (header or meta), cookie flags, mixed content,
subresource integrity, form targets and CORS. storage is a read-only snapshot of
the browser context ({cookies, local_storage, session_storage, indexed_db,
caches} with a *_truncated counter per section); every value is <redacted>
unless --include-values is set, which exposes live session tokens. sinks is the
source-to-sink tracer ring ({installed, hits:[{source, sink, context, value,
value_cut, stack, selector, ts}], dropped, truncated}); arm it with
'page act sinks_install' and reset it with 'page act sinks_clear'. postmessage is
the postMessage recorder ring ({installed, messages:[{origin, source_kind, data,
data_type, ts, stack}], dropped, truncated, frames:[{url, probed}]}); arm it with
'page act postmessage_install', reset it with 'page act postmessage_clear' or
--target clear, and send a message with 'page act post_message'. Messages are
aggregated across up to 20 frames and data is clipped at 500 runes.
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
  web-cli page fetch last screenshot --full-page --timeout-ms 60000  # give a heavy capture 60s
  web-cli page fetch last a11y                          # semantic roles + names + handles
  web-cli page fetch last network  # last 100 network requests + dropped count
  web-cli page fetch last body --target /api/orders  # newest matching API body
  web-cli page fetch last security                      # passive headers/CSP/cookie/mixed-content audit
  web-cli page fetch last storage                       # cookie flags + storage keys, values redacted
  web-cli page fetch last storage --include-values      # real values: exposes live session tokens
  web-cli page fetch last sinks                         # source-to-sink hits after 'page act sinks_install'
  web-cli page fetch last har --out traffic.har         # write HAR 1.2 to a file (import into Caido/Burp)
  web-cli page fetch https://hackernews.com links       # open + extract in one step`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			timeout := pageFetchTimeout(timeoutMS)
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), timeout)
			defer cancel()

			if browserID == "" {
				browserID = explicitBrowserID(cmd)
			}
			normalized, err := ipc.NormalizeOwner(owner)
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			owner = normalized
			if owner != "" {
				ctx = browsers.WithExplicitOwner(ctx, owner)
			}
			id, what, url = parseFetchArgs(args, id, what)

			if url != "" {
				if err := pageops.ValidateURL(url); err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
			}
			if err := pageops.ValidateAspect(what); err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			if what == "har" && maxBytes <= 0 {
				maxBytes = harUncapped
			}
			if outPath != "" {
				if what != "har" {
					return fmt.Errorf("--out needs the har aspect (got %q)", what)
				}
				if err := validateOutDir(outPath); err != nil {
					return err
				}
				if !cmd.Flags().Changed("max-bytes") {
					maxBytes = harUncapped
				}
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
						if err := cl.Call(ctx, ipc.MethodBrowserOpen, &ipc.BrowserOpenParams{Owner: owner}, &open); err != nil {
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
					reportFallbackBrowserID(cmd, browserID)
					body, err := pageFetchViaIPC(ctx, cl, summary.SessionID, browserID, owner, what, selector, target, fullPage, maxBytes, int(timeout/time.Millisecond), includeValues)
					if err != nil {
						return err
					}
					return emitFetch(body, outPath)
				}
				rid, err := resolvePageID(ctx, mgr, cl, browserID, owner, id, "page fetch")
				if err != nil {
					return err
				}
				reportFallbackBrowserID(cmd, browserID)
				body, err := pageFetchViaIPC(ctx, cl, rid, browserID, owner, what, selector, target, fullPage, maxBytes, int(timeout/time.Millisecond), includeValues)
				if err != nil {
					return err
				}
				return emitFetch(body, outPath)
			}
			printServeHint(cmd)

			var p *browsers.Page
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
				reportFallbackBrowserID(cmd, browserID)
				p, err = b.NewPage(ctx, url, nil, "", "") // new tab; plain launch; stealth applied by default
				if err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
				if err := pageops.WaitForPage(ctx, p, "idle"); err != nil {
					return fmt.Errorf("page fetch: %w", err)
				}
			} else {
				var rid string
				rid, err = resolvePageID(ctx, mgr, nil, browserID, owner, id, "page fetch")
				if err != nil {
					return err
				}
				reportFallbackBrowserID(cmd, browserID)
				p, err = mgr.Page(
					ctx,
					browsers.PageID(rid),
					browsers.BrowserID(browserID),
					owner,
				)
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
			return emitFetch(out, outPath)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to fetch (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&what, "what", "", "aspect to extract: html, text, links, meta, dom, css, js, screenshot, interactives, a11y, network, console, body, article, tables, forms, marks, security, storage, sinks, postmessage, har (default text)")
	cmd.Flags().StringVar(&selector, "selector", "", "optional CSS selector scoping html/text/interactives extraction")
	cmd.Flags().StringVar(&target, "target", "", "resource URL for one css/js resource, or seq/URL substring selecting one network body (with --what body)")
	cmd.Flags().BoolVar(&fullPage, "full-page", false, "capture the entire scrollable page instead of the visible viewport (screenshot only)")
	cmd.Flags().BoolVar(&includeValues, "include-values", false, "with --what storage, print real cookie and localStorage/sessionStorage values instead of <redacted> (exposes live tokens)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", pageops.DefaultPageMaxBytes, "maximum output size in bytes (default 50000)")
	cmd.Flags().StringVar(&outPath, "out", "", "write the HAR body to this file instead of stdout (har aspect only; the parent directory must exist; mode 0600)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 30000, "per-call timeout in milliseconds (default 30000, clamped to 1..120000)")
	cmd.Flags().StringVar(&identity, "identity", "", "incognito identity for a URL open (default \"default\"); a new name is created on first use, cookies/storage stay separate")
	cmd.Flags().StringVar(&browserID, "browser", "", "browser_id to resolve the page inside that browser only (use when more than one browser is live)")
	cmd.Flags().StringVar(&owner, "owner", "", "owner label restricting the lookup to browsers with that owner (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-')")
	return cmd
}

// newPageActCmd builds `page act`: it performs an action on a live page —
// click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file,
// hover, scroll, select, eval, navigate, back, forward, reload, wait, set_viewport,
// emulate, clear_logs, sinks_install, sinks_clear, replay, replay_fuzz,
// inject_canary, postmessage_install, postmessage_clear, post_message,
// wait_open and poc_bundle (mirroring the MCP page_act surface).
func newPageActCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id             string
		browserID      string
		owner          string
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
		origin         string
		paths          []string
		targets        []string
		rules          []browsers.InterceptRule
		waitMode       string
		timeoutMS      int
		width, height  int
		device         string
		target         string
		method         string
		headers        []string
		pointFlags     []string
		payloadFlags   []string
		strategy       string
		points         []browsers.FuzzPoint
		payloads       []string
		site           string
		name           string
		force          bool
		maxBytes       int64
		raw            bool
	)
	cmd := &cobra.Command{
		Use:   "act [id] [action] [payload...]",
		Short: "Perform an action on a live page",
		Long: `Performs an action on a live page (click, input, type, press_key, click_at,
hover_at, drag_and_drop, upload_file, hover, scroll, select, eval, navigate, back,
forward, reload, wait, set_viewport, emulate, clear_logs, sinks_install,
sinks_clear, replay, replay_fuzz, inject_canary, postmessage_install, postmessage_clear,
post_message, wait_open, intercept_add, intercept_list, intercept_clear,
auth_save, auth_restore, auth_list, poc_bundle) and prints the confirmation. The page id
is optional when only one page is open; 'last' means the most recently opened page;
a bare number resolves to page-N.
Positional payloads fill each action's fields — flags remain as fallbacks.
--browser resolves the page inside that browser only; --owner restricts the
lookup to browsers with that owner (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-').
With more than one live browser pass one of them; a page held by another owner
is refused, never acted on.
Element actions also accept --handle from page fetch interactives/dom/links/a11y, e.g.
--handle g3k9x2ab-b6; when both --handle and --selector are given, the handle wins.
A handle is scoped to the document that minted it: after a navigation, reload or
redirect it is rejected as stale, so re-fetch before acting.
Pointer and keyboard actions (click, click_at, input, hover, hover_at,
press_key) are humanized by default: the pointer steps to the target with small
jitter and a randomized 100-500 ms pause precedes and follows the action. The
pause is skipped when it would not fit --timeout-ms, so the action never
overruns its deadline. Pass --raw, set WEB_MCP_RAW_ACTIONS=1, or start the
serve daemon with raw actions enabled to make them instant. eval and navigate
never pause.

--timeout-ms bounds the action itself, exactly as timeout_ms does over MCP.
Behind a running serve process the socket read gets a separate grace, which only
prevents a false transport timeout; it never extends the action.

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
  sinks_install (none)
  sinks_clear  (none)
  replay       [target]   (target via --target; overrides via --method, --header, --value, --max-bytes)
  replay_fuzz  [target]   (matrix via --points, --payloads, --strategy; alias: replay-fuzz)
  inject_canary [canary]  (optional custom value; targets via --targets)
  postmessage_install (none)
  postmessage_clear   (none)
  post_message  [value]   (target frame via --target-selector, default top; origin via --origin)
  wait_open    (none)     (block until this page opens a popup; --timeout-ms bounds the wait)
  intercept_add [rules]   (JSON array of {id,url_pattern,method,action,headers,status,body,delay_ms})
  intercept_list (none)
  intercept_clear (none)
  back/forward/reload    (none)
  type         [selector] [text]

clear_logs empties the network, console and websocket buffers and prints
{"network":N,"console":M,"websocket":K}.
Logs are cumulative from page creation: clear right before the step whose traffic/logs
you want to observe, then run 'page fetch last network|console|websocket' and filter client-side.
sinks_install arms the source-to-sink tracer and prints {"installed":true}; it changes
page behavior, so capture stays opt-in. sinks_clear empties the tracer ring and prints
{"cleared":N}; read the hits with 'page fetch <id> sinks'. inject_canary injects a
unique wm-canary-<8 hex> value into query/hash/postmessage/name (all four by
default; narrow with --targets, repeatable or comma-separated) and prints
{canary, injected, hits, installed}. It requires 'page act sinks_install' first
and works across at most 20 frames. postmessage_install arms the postMessage
recorder: one capture-phase listener per window and iframe, without wrapping
addEventListener, so page listeners keep their identity; it prints
{"installed":true}. postmessage_clear empties every recorder ring and prints
{"cleared":N}; read the log with 'page fetch <id> postmessage', or pass
--target clear for the same {cleared} envelope. post_message delivers a value to
window.top (--target-selector top or omitted) or to the contentWindow of the
iframe named by --target-selector, using --origin as the target origin (default
"*"), arms the recorder first, and prints {origin,target}. A value that is a
complete JSON object or array is delivered as that structure; anything else is
delivered as a string. wait_open blocks until this page opens a popup (a
target="_blank" link or window.open) and prints {session_id,url,opener};
--timeout-ms bounds the wait, and a popup opened before the call returns at
once. replay resends one captured network entry through a Go HTTP client,
outside the browser: it bypasses CORS and carries the page's cookies for the
target host without writing any back. --target takes the entry's seq number or
a unique substring of its request URL (read the ring with
'page fetch <id> network'); --method, --header NAME=VALUE and --value override
the captured method, headers and body, and an omitted --value keeps the captured
body. The result is the same compact JSON the MCP page_act replay action returns
({status, headers, body|base64, bytes, truncated, body_redactions, ms, request,
chain}) with sensitive headers redacted and the body capped at 256 KB.
replay_fuzz (alias replay-fuzz) runs a bounded matrix of variants over one
captured network entry: --target uses replay's seq-or-URL-substring rule,
--points is repeatable (param:location, e.g. q:query, or a JSON
{"param":...,"location":...} object; location is query, body, header or
cookie), --payloads is repeatable (the same raw strings replay sends), and
--strategy is sniper (one point at a time, points*payloads requests; default),
pitchfork (nth point with nth payload) or clusterbomb (every combination,
payloads^points). The size is computed and refused before any request: at most
100 matrix requests (the baseline probe is extra), 20 points, 50 payloads and
200 runes each, with 5 requests in flight and 10s each. It sends one unmodified
baseline first and prints {runs:[{seq,payload,point,status,ms,bytes,body_cut,
body_redactions}], summary:{baseline:{status,bytes,ms}, anomalies:[{seq,rule,
why}]}}; the rules are status_changed, size_delta (>10% either way) and
time_delta (>3x either way). Example: 3 points with 4 payloads under sniper
sends 12 requests; 5 points with 3 payloads under clusterbomb computes 243 and
is refused with that size and the 100 bound.
intercept_add arms a page-scoped in-flight request interceptor: rules is a JSON
array of {id, url_pattern, method, action, headers, status, body, delay_ms} where
action is block, allow, rewrite or fulfill, url_pattern is a wildcard glob like
*api*, and a request matching no rule continues immediately. Interception is
opt-in, changes page behaviour and disables caching; max 50 rules per page.
intercept_list prints the page's rules with sensitive header values redacted;
intercept_clear removes every rule, continues any paused request and disables
interception; rules never outlive the page. auth_save captures the live page's cookies for --site plus local storage, session
storage and IndexedDB database/store names, and persists them under --name; it
returns counts only and never prints a cookie or storage value. Snapshots are
stored unencrypted in the local 0600 state database, so treat that file as a
credential. auth_restore writes the named snapshot into the page's browser
context; a stored user agent that differs from the live page is refused with
both agents named unless --force is set. Restoring into a fresh identity writes
only that identity's browser context. auth_list lists saved snapshots as
{site,name,cookie_count,origin_count,user_agent,updated_at} with no values, and
needs no page.`,
		Example: `  web-cli page act click "#submit"                          # click on the single open page
  web-cli page act 12 input "#search" "pizza"               # type into a search box
  web-cli page act 12 press_key "Enter"                     # press Enter key
  web-cli page act 12 click_at 100 200                     # click at x=100 y=200
  web-cli page act 12 navigate https://news.ycombinator.com # go somewhere
  web-cli page act last eval '() => document.title'         # run JS on the latest page
  web-cli page act last replay /api/user --method PUT --value '{"id":2}'  # replay with an edited body
  web-cli page act last replay --target /api/user --header X-Env=staging  # header override, same target
  web-cli page act last replay-fuzz /api/search --points q:query --payloads a --payloads b  # 2 sniper runs
  web-cli page act last replay-fuzz /api/user --points id:body --payloads 1 --payloads 2 --strategy clusterbomb
  web-cli page act last clear_logs  # reset all three buffers, print {network,console,websocket} counts
  web-cli page act last sinks_install  # arm the source-to-sink tracer
  web-cli page act last sinks_clear    # empty the tracer ring, print {cleared}
  web-cli page act last inject_canary  # inject into all four sources, print sink hits
  web-cli page act last inject_canary --targets hash  # hash only, no reload
  web-cli page act last postmessage_install  # arm the postMessage recorder
  web-cli page act last post_message '{"t":"wm"}'  # send to window.top, read with 'page fetch last postmessage'
  web-cli page act last post_message '{"t":"wm"}' --target-selector "iframe#widget" --origin https://widget.example
  web-cli page act last postmessage_clear  # empty every recorder ring, print {cleared}
  web-cli page act last wait_open  # block until the page opens a popup, print {session_id,url,opener}`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Positional args win over the --flags; payload counts are
			// validated here, before any browser work.
			parsed, err := parseActArgs(args, actArgFlags{
				id: id, action: action, selector: selector, handle: handle, value: value, key: key,
				button: button, clickCount: clickCount, targetSelector: targetSelector, origin: origin,
				paths: paths, targets: targets, waitMode: waitMode, device: device, x: x, y: y,
				targetX: targetX, targetY: targetY, width: width, height: height,
				target: target, method: method, headers: headers, maxBytes: maxBytes,
				points: pointFlags, payloads: payloadFlags, strategy: strategy,
				site: site, name: name, force: force,
				valueSet: cmd.Flags().Changed("value"),
			})
			if err != nil {
				return err
			}
			id, action, selector, handle = parsed.id, parsed.action, parsed.selector, parsed.handle
			value, key, button, clickCount = parsed.value, parsed.key, parsed.button, parsed.clickCount
			targetSelector, origin, paths, waitMode, device = parsed.targetSelector, parsed.origin, parsed.paths, parsed.waitMode, parsed.device
			targets = parsed.targets
			rules = parsed.rules
			x, y, targetX, targetY = parsed.x, parsed.y, parsed.targetX, parsed.targetY
			width, height = parsed.width, parsed.height
			target, method, headers, maxBytes = parsed.target, parsed.method, parsed.headers, parsed.maxBytes
			points, payloads, strategy = parsed.points, parsed.payloads, parsed.strategy
			site, name, force = parsed.site, parsed.name, parsed.force
			valueSet := parsed.valueSet

			actionTimeout := ipc.PageActTimeout(timeoutMS)
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), ipc.PageActBudget(action, waitMode, timeoutMS))
			defer cancel()
			rawIntent := raw || rawActionsEnabled()
			if rawIntent {
				ctx = browsers.WithRawCall(ctx)
			}

			if !ipc.ValidPageActAction(action) {
				return fmt.Errorf("page act: unknown action=%q (must be one of %s)", action, ipc.PageActActionList())
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
			if action == "post_message" && value == "" {
				return fmt.Errorf("page act: post_message requires a payload (e.g. 'page act last post_message '{\"t\":\"wm\"}''")
			}
			if action == "wait" {
				if err := pageops.ValidateWait(waitMode); err != nil {
					return fmt.Errorf("page act: %w", err)
				}
			}
			if action == "replay" {
				if target == "" {
					return fmt.Errorf("page act: replay requires target=<seq> or target=<url substring> from page_fetch what=network")
				}
				if maxBytes < 0 {
					return fmt.Errorf("page act: replay max_bytes must be >= 0")
				}
			}
			if action == "replay_fuzz" {
				if target == "" {
					return fmt.Errorf("page act: replay_fuzz requires target=<seq> or target=<url substring> from page_fetch what=network")
				}
				if maxBytes < 0 {
					return fmt.Errorf("page act: replay_fuzz max_bytes must be >= 0")
				}
			}
			if action == "auth_save" || action == "auth_restore" {
				if site == "" {
					return fmt.Errorf("page act: %s requires site (the host, e.g. example.org)", action)
				}
				if name == "" {
					return fmt.Errorf("page act: %s requires name (the snapshot name, e.g. main)", action)
				}
			}
			if action == "intercept_add" && len(rules) == 0 {
				return fmt.Errorf(`page act: intercept_add requires rules (e.g. [{"url_pattern":"*api*","action":"block"}])`)
			}
			if action == "poc_bundle" {
				if value == "" {
					return fmt.Errorf("page act: poc_bundle requires value=<finding title>")
				}
				if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
					return fmt.Errorf("page act: poc_bundle requires paths=[<absolute output directory>]")
				}
			}
			headerMap, err := parseHeaderFlags(headers)
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}

			if action == "auth_list" {
				if cl := dialServe(cmd); cl != nil {
					defer cl.Close()
					return pageActViaIPC(ctx, cl, &ipc.PageActParams{Action: action, Site: site})
				}
				out, err := pageops.PageAuthList(ctx, site)
				if err != nil {
					return fmt.Errorf("page act: %w", err)
				}
				fmt.Println(out)
				return nil
			}

			var vp *browsers.Viewport
			if action == "set_viewport" {
				vp = &browsers.Viewport{Width: width, Height: height}
			}
			if browserID == "" {
				browserID = explicitBrowserID(cmd)
			}
			normalized, err := ipc.NormalizeOwner(owner)
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			owner = normalized
			if owner != "" {
				ctx = browsers.WithExplicitOwner(ctx, owner)
			}
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				readCtx, cancelRead := context.WithTimeout(cmdCtx(cmd), ipc.PageActReadTimeout(actionTimeout))
				defer cancelRead()
				rid, err := resolvePageID(readCtx, mgr, cl, browserID, owner, id, "page act")
				if err != nil {
					return err
				}
				reportFallbackBrowserID(cmd, browserID)
				return pageActViaIPC(readCtx, cl, &ipc.PageActParams{
					SessionID:      rid,
					BrowserID:      browserID,
					Owner:          owner,
					Action:         action,
					Selector:       selector,
					Handle:         handle,
					Value:          value,
					ValueSet:       valueSet,
					Key:            key,
					Button:         button,
					ClickCount:     clickCount,
					X:              x,
					Y:              y,
					TargetX:        targetX,
					TargetY:        targetY,
					TargetSelector: targetSelector,
					Origin:         origin,
					Target:         target,
					Method:         method,
					Headers:        headerMap,
					Points:         points,
					Payloads:       payloads,
					Strategy:       browsers.FuzzStrategy(strategy),
					Site:           site,
					Name:           name,
					Force:          force,
					MaxBytes:       maxBytes,
					Paths:          paths,
					Targets:        targets,
					Rules:          rules,
					Raw:            rawIntent,
					TimeoutMS:      int(actionTimeout / time.Millisecond),
					WaitMode:       waitMode,
					Viewport:       vp,
					Device:         device,
				})
			}
			printServeHint(cmd)

			rid, err := resolvePageID(ctx, mgr, nil, browserID, owner, id, "page act")
			if err != nil {
				return err
			}
			reportFallbackBrowserID(cmd, browserID)
			p, err := mgr.Page(
				ctx,
				browsers.PageID(rid),
				browsers.BrowserID(browserID),
				owner,
			)
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
				out, err = pageops.PageWait(ctx, p, waitMode, handle, selector, actionTimeout)
			case "set_viewport":
				out, err = pageops.PageSetViewport(ctx, p, width, height)
			case "emulate":
				out, err = pageops.PageEmulate(ctx, p, device)
			case "clear_logs":
				out, err = pageops.PageClearCapture(p)
			case "sinks_install":
				out, err = pageops.PageSinksInstall(ctx, p)
			case "sinks_clear":
				out, err = pageops.PageSinksClear(ctx, p)
			case "replay":
				out, err = pageops.PageReplay(ctx, p, pageops.ReplayRequest{
					Target:   target,
					Method:   method,
					Headers:  headerMap,
					Body:     value,
					BodySet:  valueSet,
					MaxBytes: maxBytes,
					CheckScope: func(rawURL string) error {
						return mgr.CheckPageScope(browsers.PageID(rid), rawURL)
					},
				})
			case "replay_fuzz":
				var fuzz browsers.FuzzResult
				fuzz, err = p.ReplayFuzz(ctx, parsed.fuzzSpec(func(rawURL string) error {
					return mgr.CheckPageScope(browsers.PageID(rid), rawURL)
				}))
				if err == nil {
					var data []byte
					data, err = json.Marshal(fuzz)
					out = string(data)
				}
			case "inject_canary":
				out, err = pageops.PageCanaryInject(ctx, p, targets, value)
			case "postmessage_install":
				out, err = pageops.PagePostMessageInstall(ctx, p)
			case "postmessage_clear":
				out, err = pageops.PagePostMessageClear(ctx, p)
			case "post_message":
				out, err = pageops.PagePostMessageSend(ctx, p, value, targetSelector, origin)
			case "wait_open":
				var popup browsers.Popup
				popup, err = mgr.WaitOpen(ctx, browsers.PageID(rid), browsers.BrowserID(browserID), owner)
				if err == nil {
					var data []byte
					data, err = json.Marshal(popup)
					out = string(data)
				}
			case "auth_save":
				out, err = pageops.PageAuthSave(ctx, p, site, name)
			case "auth_restore":
				out, err = pageops.PageAuthRestore(ctx, p, site, name, force)
			case "intercept_add":
				out, err = pageops.PageInterceptAdd(ctx, p, rules)
			case "intercept_list":
				out, err = pageops.PageInterceptList(ctx, p)
			case "intercept_clear":
				out, err = pageops.PageInterceptClear(ctx, p)
			case "poc_bundle":
				out, err = pageops.PagePocBundle(ctx, p, value, paths, target)
			}
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			if stateErr == nil {
				after := pageops.PageStateAfterAct(ctx, p, action, before)
				out += pageops.ActStateDeltaFor(action, before, after)
			}
			emitActOutput(action, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to act on (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&action, "action", "", "action: click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs, sinks_install, sinks_clear, replay, replay_fuzz, inject_canary, postmessage_install, postmessage_clear, post_message, wait_open, intercept_add, intercept_list, intercept_clear, auth_save, auth_restore, auth_list, poc_bundle")
	cmd.Flags().StringVar(&selector, "selector", "", "CSS selector targeting the element (element actions)")
	cmd.Flags().StringVar(&handle, "handle", "", "element handle from page fetch (g<document>-b<backendNodeId>, scoped to its document); wins over --selector (element actions)")
	cmd.Flags().StringVar(&value, "value", "", "value for eval (JS function), input (text), type (text), navigate (URL), select (option value), or inject_canary (custom canary)")
	cmd.Flags().StringVar(&key, "key", "", "key or key combo for press_key action (e.g. Enter, Escape, Control+A)")
	cmd.Flags().StringVar(&button, "button", "left", "mouse button for click and click_at: left, right, middle")
	cmd.Flags().IntVar(&clickCount, "click-count", 1, "click count for click and click_at (1 for single click, 2 for double click)")
	cmd.Flags().Float64Var(&x, "x", 0, "horizontal coordinate in pixels (scroll, click_at, hover_at, drag_and_drop)")
	cmd.Flags().Float64Var(&y, "y", 0, "vertical coordinate in pixels (scroll, click_at, hover_at, drag_and_drop)")
	cmd.Flags().Float64Var(&targetX, "target-x", 0, "target horizontal coordinate in pixels (drag_and_drop)")
	cmd.Flags().Float64Var(&targetY, "target-y", 0, "target vertical coordinate in pixels (drag_and_drop)")
	cmd.Flags().StringVar(&targetSelector, "target-selector", "", "target element selector (drag_and_drop), or destination frame for post_message: \"top\" (default) or a CSS selector naming an iframe")
	cmd.Flags().StringVar(&origin, "origin", "*", "target origin for post_message (default \"*\"); pass the receiver's origin to restrict delivery")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "file paths for upload_file action")
	cmd.Flags().StringArrayVar(&paths, "paths", nil, "absolute output directory for poc_bundle (paths[0]); also accepted as an alias for upload_file paths")
	cmd.Flags().StringArrayVar(&targets, "targets", nil, "canary injection points for inject_canary: query, hash, postmessage, name (repeatable or comma-separated; default all four)")
	cmd.Flags().StringVar(&target, "target", "", "replay target: a seq number or a substring of the captured request URL (read the ring with 'page fetch <id> network')")
	cmd.Flags().StringVar(&method, "method", "", "HTTP method override for replay (e.g. PUT, PATCH); omit to keep the captured method")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "replay header override NAME=VALUE (repeatable); omitted headers keep their captured values")
	cmd.Flags().StringArrayVar(&pointFlags, "points", nil, "replay_fuzz injection point, repeatable: param:location (e.g. q:query) or a JSON {\"param\":...,\"location\":...} object; location is query, body, header or cookie")
	cmd.Flags().StringArrayVar(&payloadFlags, "payloads", nil, "replay_fuzz payload value (repeatable); the same raw strings replay sends as a body/value")
	cmd.Flags().StringVar(&strategy, "strategy", "sniper", "replay_fuzz strategy: sniper (one point at a time), pitchfork (nth point with nth payload) or clusterbomb (every combination)")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 0, "cap the replay response body in bytes (0 = the engine's 256 KB ceiling); with replay_fuzz it caps each run body")
	cmd.Flags().StringVar(&waitMode, "wait", "ms", "wait mode (wait action): load, idle, stable, interactable (needs --selector or --handle), ms (sleep --timeout-ms; default ms)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 30000, "action deadline in milliseconds (default 30000); it bounds the action, and a separate socket-read grace only prevents false transport errors")
	cmd.Flags().BoolVar(&raw, "raw", false, "skip humanized pointer/keyboard timing for this call (instant moves, no pre/post pause); the same switch is available process-wide via WEB_MCP_RAW_ACTIONS=1")
	cmd.Flags().StringVar(&browserID, "browser", "", "browser_id to resolve the page inside that browser only (use when more than one browser is live)")
	cmd.Flags().StringVar(&owner, "owner", "", "owner label restricting the lookup to browsers with that owner (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-')")
	cmd.Flags().IntVar(&width, "width", 0, "target viewport width in CSS pixels (set_viewport action)")
	cmd.Flags().IntVar(&height, "height", 0, "target viewport height in CSS pixels (set_viewport action)")
	cmd.Flags().StringVar(&device, "device", "", "go-rod device preset to apply (emulate action), e.g. IPhoneX, IPadPro")
	cmd.Flags().StringVar(&site, "site", "", "auth snapshot site/host, e.g. example.org (auth_save, auth_restore; optional filter for auth_list)")
	cmd.Flags().StringVar(&name, "name", "", "auth snapshot name, unique per site (auth_save, auth_restore)")
	cmd.Flags().BoolVar(&force, "force", false, "auth_restore: restore even when the live page user agent differs from the stored one")
	return cmd
}

// newPageAuthCmd builds the `page auth` tree — save, restore and list — over
// named auth snapshots. Every subcommand reuses the page act auth_save /
// auth_restore / auth_list implementation, so the CLI, MCP and IPC surfaces
// print byte-identical results.
func newPageAuthCmd(mgr *browsers.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Save and restore named auth snapshots",
		Long: `Saves and restores a session's auth state under a name, in the local state
database. A snapshot holds cookies for one host set plus local storage, session
storage and IndexedDB database/store names, and survives a daemon restart.

Snapshots are stored unencrypted in a 0600 database, so treat that file as a
credential; no result, error or log line prints a cookie or storage value.
auth_restore writes cookies first and then storage for the live origin; a
stored user agent that differs from the live page is refused with both agents
named unless --force is set. Restoring into a fresh identity never touches the
other identities.

  save <site> <name>     capture the single open page's auth state
  restore <site> <name>  write the snapshot into the single open page
  list [site]            list saved snapshots (counts only, never values)`,
		Example: `  browser page auth save example.org main
  browser page auth list
  browser page auth restore example.org main
  browser page auth restore example.org main --force`,
	}
	cmd.AddCommand(
		newPageAuthSaveCmd(mgr),
		newPageAuthRestoreCmd(mgr),
		newPageAuthListCmd(),
	)
	return cmd
}

// newPageAuthSaveCmd builds `page auth save <site> <name>`.
func newPageAuthSaveCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id        string
		browserID string
		owner     string
	)
	cmd := &cobra.Command{
		Use:   "save <site> <name>",
		Short: "Capture the open page's auth state under a name",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPageAuth(cmd, mgr, "auth_save", args[0], args[1], false, id, browserID, owner)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&browserID, "browser", "", "browser_id to resolve the page inside that browser only")
	cmd.Flags().StringVar(&owner, "owner", "", "owner label restricting the lookup to browsers with that owner")
	return cmd
}

// newPageAuthRestoreCmd builds `page auth restore <site> <name>`.
func newPageAuthRestoreCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id        string
		browserID string
		owner     string
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "restore <site> <name>",
		Short: "Write a saved auth snapshot into the open page",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPageAuth(cmd, mgr, "auth_restore", args[0], args[1], force, id, browserID, owner)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&browserID, "browser", "", "browser_id to resolve the page inside that browser only")
	cmd.Flags().StringVar(&owner, "owner", "", "owner label restricting the lookup to browsers with that owner")
	cmd.Flags().BoolVar(&force, "force", false, "restore even when the live page user agent differs from the stored one")
	return cmd
}

// newPageAuthListCmd builds `page auth list [site]`.
func newPageAuthListCmd() *cobra.Command {
	var site string
	cmd := &cobra.Command{
		Use:   "list [site]",
		Short: "List saved auth snapshots (counts only)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				site = args[0]
			}
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return pageActViaIPC(ctx, cl, &ipc.PageActParams{Action: "auth_list", Site: site})
			}
			out, err := pageops.PageAuthList(ctx, site)
			if err != nil {
				return fmt.Errorf("page auth: %w", err)
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "optional site/host filter")
	return cmd
}

// runPageAuth resolves one live page and runs an auth save or restore through
// the daemon when one is reachable, else in-process.
func runPageAuth(
	cmd *cobra.Command,
	mgr *browsers.Manager,
	action, site, name string,
	force bool,
	id, browserID, owner string,
) error {
	ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
	defer cancel()
	if browserID == "" {
		browserID = explicitBrowserID(cmd)
	}
	normalized, err := ipc.NormalizeOwner(owner)
	if err != nil {
		return fmt.Errorf("page auth: %w", err)
	}
	owner = normalized
	if owner != "" {
		ctx = browsers.WithExplicitOwner(ctx, owner)
	}
	if cl := dialServe(cmd); cl != nil {
		defer cl.Close()
		rid, err := resolvePageID(ctx, mgr, cl, browserID, owner, id, "page auth")
		if err != nil {
			return err
		}
		return pageActViaIPC(ctx, cl, &ipc.PageActParams{
			SessionID: rid,
			BrowserID: browserID,
			Owner:     owner,
			Action:    action,
			Site:      site,
			Name:      name,
			Force:     force,
		})
	}
	printServeHint(cmd)
	rid, err := resolvePageID(ctx, mgr, nil, browserID, owner, id, "page auth")
	if err != nil {
		return err
	}
	p, err := mgr.Page(
		ctx,
		browsers.PageID(rid),
		browsers.BrowserID(browserID),
		owner,
	)
	if err != nil {
		return fmt.Errorf("page auth: %w", err)
	}
	var out string
	switch action {
	case "auth_save":
		out, err = pageops.PageAuthSave(ctx, p, site, name)
	case "auth_restore":
		out, err = pageops.PageAuthRestore(ctx, p, site, name, force)
	}
	if err != nil {
		return fmt.Errorf("page auth: %w", err)
	}
	fmt.Println(out)
	return nil
}

// newPageCloseCmd builds `page close`: it resolves the page with the same
// qualifier-aware lookup as page fetch/act, then closes it through the browser
// that holds it (mirroring the MCP session_close tool).
func newPageCloseCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		id        string
		browserID string
		owner     string
	)
	cmd := &cobra.Command{
		Use:   "close [id]",
		Short: "Close an open page",
		Long: `Closes the page with the given id, releasing its browser tab. The id is
optional when only one page is open; 'last' means the most recently opened
page; a bare number resolves to page-N. With more than one live browser pass
--browser or --owner; a page held by another owner is refused, never closed.
The session id becomes invalid afterwards.`,
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

			if browserID == "" {
				browserID = explicitBrowserID(cmd)
			}
			normalized, err := ipc.NormalizeOwner(owner)
			if err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			owner = normalized
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				rid, err := resolvePageID(ctx, mgr, cl, browserID, owner, id, "page close")
				if err != nil {
					return err
				}
				reportFallbackBrowserID(cmd, browserID)
				return pageCloseViaIPC(cmd, ctx, cl, rid, browserID, owner)
			}
			printServeHint(cmd)

			rid, err := resolvePageID(ctx, mgr, nil, browserID, owner, id, "page close")
			if err != nil {
				return err
			}
			reportFallbackBrowserID(cmd, browserID)

			if _, err := mgr.ClosePage(
				ctx,
				browsers.PageID(rid),
				browsers.BrowserID(browserID),
				owner,
			); err != nil {
				return fmt.Errorf("page close: %w", err)
			}
			fmt.Printf("session %s closed\n", rid)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to close (optional with one page open; 'last'; or a bare number)")
	cmd.Flags().StringVar(&browserID, "browser", "", "browser_id to resolve the page inside that browser only (use when more than one browser is live)")
	cmd.Flags().StringVar(&owner, "owner", "", "owner label restricting the lookup to browsers with that owner (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-')")
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
		reportFallbackBrowserID(cmd, browserID)
	}
	if len(pages) == 0 {
		fmt.Println("no open pages")
		return nil
	}
	for _, p := range pages {
		fmt.Println(pageListRow(p))
	}
	return nil
}

// pageListRow renders one page listing row: its id and live URL, plus the
// opener id when the page is a popup. Both the in-process and daemon page list
// paths print it, and page-id resolution errors reuse it for their page rows.
func pageListRow(p browsers.PageInfo) string {
	row := fmt.Sprintf("%s\t%s", p.ID, p.URL)
	if p.Opener != "" {
		row += "\topener=" + string(p.Opener)
	}
	return row
}

// pageFetchViaIPC implements `page fetch` against a live serve process: it
// forwards the extraction (and the --target/--full-page/--timeout-ms params)
// to page.fetch (the server truncates to max_bytes itself) and returns the data
// byte-identically to the in-process path. The CLI's --max-bytes 0 means "no
// truncation" (pageops.PageTruncate returns s unchanged), which the wire
// contract cannot express (0 → the server default), so it is forwarded as an
// effectively-unbounded cap. har keeps its own negative sentinel instead, so the
// daemon hands it to the encoder unchanged and the document reports "Cap: none".
func pageFetchViaIPC(
	ctx context.Context,
	cl *ipc.Client,
	id, browserID, owner, what, selector, target string,
	fullPage bool,
	maxBytes int,
	timeoutMS int,
	includeValues bool,
) (string, error) {
	if maxBytes <= 0 && what != "har" {
		maxBytes = int(^uint(0) >> 1) // CLI parity: 0 means "no truncation"
	}
	var res ipc.PageFetchResult
	if err := cl.Call(ctx, ipc.MethodPageFetch, &ipc.PageFetchParams{
		SessionID:     id,
		BrowserID:     browserID,
		Owner:         owner,
		What:          what,
		Selector:      selector,
		Target:        target,
		FullPage:      fullPage,
		MaxBytes:      maxBytes,
		TimeoutMS:     timeoutMS,
		IncludeValues: includeValues,
	}, &res); err != nil {
		return "", fmt.Errorf("page fetch: %w", err)
	}
	return res.Data, nil
}

// emitFetch prints one fetched aspect body, or writes it to the --out file when
// that flag is set. The caller validates --out as har-only before any work.
func emitFetch(body, outPath string) error {
	if outPath == "" {
		fmt.Println(body)
		return nil
	}
	line, err := writeHAR(outPath, body)
	if err != nil {
		return err
	}
	fmt.Println(line)
	return nil
}

// writeHAR decodes the received HAR document, writes it to path with mode 0600
// and returns the "wrote <path> (<n> entries)" line. It never creates the parent
// directory and never prints the body.
func writeHAR(path, body string) (string, error) {
	var archive har.Archive
	if err := json.Unmarshal([]byte(body), &archive); err != nil {
		return "", fmt.Errorf("--out: decoding HAR: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("--out: writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("--out: setting mode on %s: %w", path, err)
	}
	line := fmt.Sprintf("wrote %s (%d entries)", path, len(archive.Log.Entries))
	if archive.Log.Truncated > 0 {
		line += " (truncated)"
	}
	return line, nil
}

// validateOutDir fails when the --out parent directory does not exist. It never
// creates one.
func validateOutDir(path string) error {
	dir := filepath.Dir(path)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("--out: directory %s does not exist", dir)
	}
	return nil
}

// pageActViaIPC implements `page act` against a live serve process: it forwards
// the fully mapped action parameters to page.act and prints the confirmation
// byte-identically to the in-process path.
func pageActViaIPC(
	ctx context.Context,
	cl *ipc.Client,
	params *ipc.PageActParams,
) error {
	var res ipc.PageActResult
	if err := cl.Call(ctx, ipc.MethodPageAct, params, &res); err != nil {
		return fmt.Errorf("page act: %w", err)
	}
	emitActOutput(params.Action, res.Data)
	return nil
}

// emitActOutput prints an action's result. poc_bundle renders its directory and
// one line per file instead of the raw JSON envelope; every other action keeps
// the plain result line.
func emitActOutput(action, out string) {
	if action != "poc_bundle" {
		fmt.Println(out)
		return
	}
	var res pageops.PocBundleResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		fmt.Println(out)
		return
	}
	fmt.Println(res.Directory)
	for _, f := range res.Files {
		fmt.Printf("%s\t%d bytes\n", f.Name, f.Bytes)
	}
	for _, o := range res.Omitted {
		fmt.Printf("omitted\t%s\t%s\n", o.Name, o.Reason)
	}
}

// pageCloseViaIPC implements `page close` against a live serve process: it
// forwards the close (and the optional owning browser id) to page.close and
// prints the confirmation byte-identically to the in-process path.
func pageCloseViaIPC(
	cmd *cobra.Command,
	ctx context.Context,
	cl *ipc.Client,
	id, browserID, owner string,
) error {
	params := &ipc.PageCloseParams{SessionID: id, BrowserID: browserID, Owner: owner}
	if err := cl.Call(ctx, ipc.MethodPageClose, params, nil); err != nil {
		return fmt.Errorf("page close: %w", err)
	}
	fmt.Printf("session %s closed\n", id)
	return nil
}
