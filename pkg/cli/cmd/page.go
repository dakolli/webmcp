// Package cmd implements the web-mcp CLI's cobra commands, talking directly to
// the pkg/browsers core engine (never through pkg/mcp). WEB-28 owns this file.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
)

// defaultCmdTimeout bounds every page subcommand so a hung page (or a
// first-run Chromium download) cannot block the CLI. Mirrors the 30s per-call
// timeout the MCP tools enforce (pkg/mcp/tools/fetch.go).
const defaultCmdTimeout = 30 * time.Second

// defaultPageMaxBytes caps how much of any page fetch extraction is printed in
// one call (the --max-bytes default). Huge pages are dehydrated to a first
// slice; the user can re-run with a narrower selector or a higher --max-bytes.
const defaultPageMaxBytes = 50_000

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
				out, err = pageHTML(ctx, p, selector)
			case "text":
				out, err = pageText(ctx, p, selector)
			case "links":
				out, err = pageLinks(ctx, p)
			case "meta":
				out, err = pageMeta(ctx, p)
			}
			if err != nil {
				return fmt.Errorf("page fetch: %w", err)
			}
			fmt.Println(pageTruncate(out, maxBytes))
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page/session id to fetch (required)")
	cmd.Flags().StringVar(&what, "what", "", "aspect to extract: html, text, links, meta (required)")
	cmd.Flags().StringVar(&selector, "selector", "", "optional CSS selector scoping html/text extraction")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", defaultPageMaxBytes, "maximum output size in bytes (default 50000)")
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

			b, err := mgr.Open(ctx) // lazily launches (or returns) the single Browser
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}
			p, err := b.Page(browsers.PageID(id))
			if err != nil {
				return fmt.Errorf("page act: %w", err)
			}

			out, err := pageEval(ctx, p, value)
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

// pageHTML returns the page's outerHTML, or of the element matched by selector
// when one is given. Backed by p.Rod.HTML() / element HTML() — mirrors the MCP
// extractHTML.
func pageHTML(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	p.Lock()
	defer p.Unlock()

	if selector == "" {
		html, err := p.Rod.Context(ctx).HTML()
		if err != nil {
			return "", fmt.Errorf("read page html: %w", err)
		}
		return html, nil
	}

	el, err := p.Rod.Context(ctx).Element(selector) // CSS; retries until found
	if err != nil {
		return "", fmt.Errorf("no element matched selector %q: %w", selector, err)
	}
	html, err := el.HTML()
	if err != nil {
		return "", fmt.Errorf("read html of element %q: %w", selector, err)
	}
	return html, nil
}

// pageText returns the visible text of the page (or of the element matched by
// selector). Backed by Eval over document.body.innerText / element.innerText —
// mirrors the MCP extractText.
func pageText(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	p.Lock()
	defer p.Unlock()

	js := `() => document.body ? document.body.innerText : ''`
	if selector != "" {
		// JSON-quote the selector so it is safe to embed in the JS source
		// (quotes, backslashes and control characters are escaped).
		q, err := json.Marshal(selector)
		if err != nil {
			return "", fmt.Errorf("encode selector %q: %w", selector, err)
		}
		js = `() => { const el = document.querySelector(` + string(q) + `); return el ? el.innerText : null }`
	}

	res, err := p.Rod.Context(ctx).Eval(js)
	if err != nil {
		return "", fmt.Errorf("evaluate innerText: %w", err)
	}
	// The selector-scoped Eval resolves to null when nothing matches; the
	// whole-document Eval always yields a string. Nil() must be checked before
	// Str(), because gson Str() renders a null as "<nil>".
	if res.Value.Nil() {
		return "", fmt.Errorf("no element matched selector %q", selector)
	}
	return res.Value.Str(), nil
}

// pageLinks returns the page's anchor links as a JSON array of {text, href},
// deduplicated by href (first occurrence wins) — mirrors the MCP extractLinks
// shape.
func pageLinks(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()

	res, err := p.Rod.Context(ctx).Eval(
		`() => [...document.querySelectorAll('a[href]')].map(a => ({text: a.innerText.trim(), href: a.href}))`)
	if err != nil {
		return "", fmt.Errorf("evaluate links: %w", err)
	}

	var links []pageLink
	if err := res.Value.Unmarshal(&links); err != nil {
		return "", fmt.Errorf("decode links: %w", err)
	}

	// Dedupe by href, first occurrence wins (the JS Map alternative would do
	// the same; doing it Go-side keeps the JS simple).
	seen := make(map[string]bool, len(links))
	dedup := make([]pageLink, 0, len(links))
	for _, l := range links {
		if seen[l.Href] {
			continue
		}
		seen[l.Href] = true
		dedup = append(dedup, l)
	}

	b, err := json.Marshal(dedup)
	if err != nil {
		return "", fmt.Errorf("encode links: %w", err)
	}
	return string(b), nil
}

// pageMeta returns the page's meta info (title, url, meta tags) as a JSON
// object — mirrors the MCP extractMeta shape. Backed by p.Rod.Info() plus Eval
// collecting every <meta> tag.
func pageMeta(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()

	info, err := p.Rod.Context(ctx).Info()
	if err != nil {
		return "", fmt.Errorf("read page info: %w", err)
	}

	// Each meta tag is flattened to its meaningful attributes; absent ones
	// (getAttribute returned null) are dropped Go-side via omitempty.
	res, err := p.Rod.Context(ctx).Eval(
		`() => [...document.querySelectorAll('meta')].map(m => ({name: m.getAttribute('name'), ` +
			`property: m.getAttribute('property'), 'http-equiv': m.getAttribute('http-equiv'), ` +
			`content: m.getAttribute('content'), charset: m.getAttribute('charset')}))`)
	if err != nil {
		return "", fmt.Errorf("evaluate meta tags: %w", err)
	}

	var tags []pageMetaTag
	if err := res.Value.Unmarshal(&tags); err != nil {
		return "", fmt.Errorf("decode meta tags: %w", err)
	}

	out := pageMetaResult{Title: info.Title, URL: info.URL, Meta: tags}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode page meta: %w", err)
	}
	return string(b), nil
}

// pageEval runs a JavaScript function in the page — the primary escape hatch
// for any page manipulation — and returns the JSON result. Backed by
// p.Rod.Context(ctx).Eval(js); js must be a JS function definition, e.g.
// "() => document.title". Mirrors the Eval pattern proven in extractText.
func pageEval(ctx context.Context, p *browsers.Page, js string) (string, error) {
	p.Lock()
	defer p.Unlock()

	res, err := p.Rod.Context(ctx).Eval(js) // Eval only accepts a JS function definition
	if err != nil {
		return "", fmt.Errorf("evaluate js: %w", err)
	}
	return res.Value.JSON("", ""), nil // gson marshals to compact JSON text
}

// pageLink is one anchor link collected by pageLinks: its visible text and its
// absolute href.
type pageLink struct {
	// Text is the anchor's visible text, trimmed of surrounding whitespace.
	Text string `json:"text"`
	// Href is the anchor's absolute URL (resolved against the document base).
	Href string `json:"href"`
}

// pageMetaResult is the JSON shape returned by pageMeta: the page title and URL
// plus the collected <meta> tags.
type pageMetaResult struct {
	// Title is the page's document title.
	Title string `json:"title"`
	// URL is the page's current URL.
	URL string `json:"url"`
	// Meta is the ordered list of <meta> tags.
	Meta []pageMetaTag `json:"meta"`
}

// pageMetaTag is one <meta> tag flattened to its meaningful attributes.
// Attributes absent on the tag are dropped via omitempty.
type pageMetaTag struct {
	// Name is the tag's name attribute (e.g. "viewport", "description").
	Name string `json:"name,omitempty"`
	// Property is the tag's property attribute (e.g. OpenGraph "og:title").
	Property string `json:"property,omitempty"`
	// HTTPEquiv is the tag's http-equiv attribute (e.g. "refresh").
	HTTPEquiv string `json:"http-equiv,omitempty"`
	// Content is the tag's content attribute.
	Content string `json:"content,omitempty"`
	// Charset is the tag's charset attribute.
	Charset string `json:"charset,omitempty"`
}

// pageTruncate caps s at maxBytes bytes, cutting on a UTF-8 rune boundary so
// the printed text never contains a half-decoded character. A non-positive
// maxBytes returns s unchanged. A truncation note is appended so the user knows
// the output was dehydrated. Mirrors the MCP page_fetch truncate.
func pageTruncate(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	for len(b) > maxBytes && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes]) + "\n… (output truncated — raise --max-bytes or narrow the selector)"
}
