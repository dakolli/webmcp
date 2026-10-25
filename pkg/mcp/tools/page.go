package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/pageops"
	"webmcp/pkg/mcp/models"
)

// defaultMaxBytes caps how much of any extraction is returned to the LLM in one
// call. Huge pages are dehydrated to a first slice; the agent can re-request
// with a narrower selector or a higher max_bytes.
const defaultMaxBytes = 50_000

// defaultPageTimeout bounds the whole page_fetch call when the caller does not
// provide one: a hung page (or a wedged CDP connection) must not block the
// server. Every extraction inherits this bound via p.Rod.Context(ctx).
const defaultPageTimeout = 30 * time.Second

// fullPageScreenshotTimeout bounds a full_page screenshot capture. A full-page
// capture scrolls, composites and encodes a potentially very tall image, which
// can exceed the 30s per-call default on heavy pages; this dedicated budget is
// decoupled from the caller's deadline (via context.WithoutCancel) so a heavy
// capture gets room, while a genuinely wedged page still fails at 90s (no
// infinite block). Viewport-only captures keep the 30s default.
const fullPageScreenshotTimeout = 90 * time.Second

// Page implements the "page_fetch" tool: it reads any aspect of a LIVE page
// that was previously opened with fetch_page. The session_id from fetch_page
// identifies which browser tab to read; what selects the aspect:
//
//	html | dom | text | css | js | links | screenshot | meta | interactives | a11y |
//	network | console | article | marks
//
// The "dom" output is the page's DOM re-nested from the flattened Documents
// array + Strings table produced by CaptureDOMSnapshot into a compact,
// LLM-friendly tree (iframes, shadow DOM and all). "css"/"js" enumerate the
// page's resource tree and fetch the matching resources. "screenshot" returns
// a base64 PNG data-URI. "interactives" returns a dense action map of every
// visible, non-inert clickable/typeable element (with stable handles for
// page_act). "a11y" returns the flattened accessibility tree — computed
// roles, accessible names, values and state flags — with handles on
// DOM-backed nodes. "article" returns the readable body as markdown.
// "marks" returns numbered overlay boxes plus the annotated screenshot.
// Both bypass max_bytes truncation.
type Page struct {
	// mgr is the shared browsers.Manager owning the single Browser and its
	// live pages (sessions).
	mgr *browsers.Manager
}

// NewPage constructs the page_fetch tool wired to the given browsers.Manager.
func NewPage(mgr *browsers.Manager) *Page {
	return &Page{mgr: mgr}
}

// Name returns the MCP tool name "page_fetch".
func (t *Page) Name() string {
	return "page_fetch"
}

// Description returns a customer/LLM-facing description of page_fetch.
func (t *Page) Description() string {
	return "Reads an aspect of a live page that was opened with fetch_page: " +
		"html, dom (full flattened DOM snapshot, LLM-friendly), text, css, js, " +
		"links, screenshot (base64 PNG data-URI), interactives (dense action " +
		"map of every visible clickable/typeable element with stable handles " +
		"for page_act), a11y (flattened accessibility tree: computed " +
		"role/name/value/state flags plus depth, with handles on DOM-backed " +
		"nodes usable as page_act's handle; root frame only), network and " +
		"console (best-effort request and console logs as " +
		"{\"entries\":[...],\"count\",\"dropped\",\"truncated\"} holding the " +
		"newest 100 buffered entries; capture is cumulative from page creation, " +
		"so filter client-side or call page_act action clear_logs right before " +
		"the step whose traffic/logs you want to observe), article (the readable " +
		"article body as markdown with title, byline, excerpt, word count and " +
		"truncated flag), marks (numbered set-of-marks boxes plus an annotated " +
		"viewport screenshot whose x/y coordinates feed page_act click_at), or " +
		"meta. Pass the session_id " +
		"returned by fetch_page plus what=<aspect>; use selector to scope " +
		"extraction, target to fetch exactly one css/js resource's full content " +
		"(instead of the resource list), full_page to capture the whole " +
		"scrollable page in a screenshot (default: visible viewport; full-page " +
		"captures of tall/heavy pages get an extended internal budget of ~90s " +
		"and may still time out on extreme pages), and max_bytes to cap the " +
		"response size. Each aspect is capped at a fixed 30s per-call timeout " +
		"(full-page screenshots ~90s); timeout_ms is not supported on this tool. " +
		"Example: { \"session_id\": \"page-1\", \"what\": \"dom\", \"max_bytes\": 8000 }."
}

// InputSchema returns the JSON Schema for page_fetch's arguments: a required
// session_id and what, plus optional selector, target, max_bytes and full_page.
func (t *Page) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by fetch_page for the page to read.",
			},
			"what": map[string]any{
				"type":        "string",
				"enum":        []string{"html", "dom", "text", "css", "js", "links", "screenshot", "meta", "interactives", "a11y", "network", "console", "article", "marks"},
				"description": "The aspect of the page to extract: html (outerHTML), dom (flattened DOM snapshot; every element node carries a g<document>-b<backendNodeId> handle usable as page_act's handle arg; the document token makes handles from a previous document stale after navigation), text (visible text), css (stylesheet resources, including inline <style> blocks reported with type inline-style), js (script resources), links (anchor hrefs, each with a handle where one matched), screenshot (base64 PNG data-URI; full_page captures the whole scrollable page), interactives (dense action map of every visible clickable/typeable element — with stable handles usable as page_act's handle arg; whole-page by design, the selector arg is ignored), a11y (flattened accessibility tree in DFS order: computed role/name/value plus the state flags checked, expanded, selected, disabled, required, pressed, invalid, focused, and depth — e.g. what: \"a11y\" → {\"nodes\":[{\"role\":\"button\",\"name\":\"Sign in\",\"handle\":\"g3k9x2ab-b6\",…}],\"count\":N,\"truncated\":M}; DOM-backed nodes carry a handle usable as page_act's handle arg; root frame only — iframes are separate trees; selector/target/full_page are ignored), network/console (bounded log envelope: what: \"network\" → {\"entries\":[{\"method\":\"GET\",\"url\":\"…\",\"status\":200,…}],\"count\":N,\"dropped\":D,\"truncated\":M}; entries are the newest 100 buffered, oldest first, and dropped counts true ring overflow; capture is cumulative from page creation — filter client-side or call page_act action clear_logs right before the step under observation; selector/target/full_page are ignored), article (the readable article body as markdown: {title, byline?, excerpt?, text, words, truncated, url} — the densest article/main root with page chrome removed and headings, paragraphs, lists, code, quotes, links, images and tables rendered; text is capped at 40000 runes and truncated reports the cut; prefer it over text for prose — selector/target/full_page are ignored; e.g. what: \"article\" → {\"title\":\"Example\",\"byline\":\"Ada\",\"text\":\"# Heading …\",\"words\":812,\"truncated\":false,\"url\":\"https://example.com/a\"}; example: {\"session_id\":\"page-1\",\"what\":\"article\"}), marks (numbered set-of-marks: {marks:[{index, role, text?, x, y, width, height}], screenshot, viewport_width, viewport_height, truncated} — one numbered box per visible interactive element plus the annotated viewport screenshot as a base64 PNG data-URI; x/y/width/height are viewport CSS px and x/y can be passed straight to page_act click_at; capped at the first 60 marks with truncated counting the rest — selector/target/full_page are ignored; e.g. what: \"marks\" → {\"marks\":[{\"index\":1,\"role\":\"a\",\"text\":\"Sign in\",\"x\":120.5,\"y\":48,\"width\":88,\"height\":32}],\"screenshot\":\"data:image/png;base64,…\",\"viewport_width\":1280,\"viewport_height\":800,\"truncated\":0}; example: {\"session_id\":\"page-1\",\"what\":\"marks\"}), meta (title/url/description). Both article and marks bypass max_bytes truncation.",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "Optional CSS selector scoping the extraction (html/text).",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"default":     defaultMaxBytes,
				"description": "Maximum response size in bytes (default 50000).",
			},
			"full_page": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "Capture the entire scrollable page instead of the visible viewport. Tall/heavy pages get an extended internal capture budget (~90s) and may still time out on extreme pages.",
			},
			"target": map[string]any{
				"type":        "string",
				"description": "Resource URL to fetch exactly one css/js resource (instead of listing). Use with what=css|js.",
			},
		},
		Required: []string{"session_id", "what"},
	}
}

// pageFetchArgs is the strict decoding target for page_fetch arguments.
type pageFetchArgs struct {
	// SessionID identifies the live page/session to read (required).
	SessionID browsers.PageID `json:"session_id"`
	// What is the aspect to extract (required).
	What string `json:"what"`
	// Selector optionally scopes the extraction to a matching element.
	Selector string `json:"selector"`
	// MaxBytes caps the response size (default defaultMaxBytes).
	MaxBytes int `json:"max_bytes"`
	// FullPage captures the entire scrollable page instead of the visible
	// viewport (screenshot only; zero value = viewport-only capture).
	FullPage bool `json:"full_page"`
	// Target is a resource URL that, when set, makes what=css|js fetch exactly
	// that one resource's content instead of listing the page's resources.
	Target string `json:"target"`
}

// Execute parses the page_fetch arguments, resolves the session and dispatches
// to the per-aspect extraction helper, truncating the result to max_bytes.
func (t *Page) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a pageFetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("page_fetch: invalid arguments: %w", err)
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("page_fetch: missing required argument %q", "session_id")
	}
	if a.What == "" {
		return "", fmt.Errorf("page_fetch: missing required argument %q", "what")
	}
	if a.MaxBytes <= 0 {
		a.MaxBytes = defaultMaxBytes
	}

	// Enforce an explicit per-call timeout so a hung page (or a wedged CDP
	// connection) cannot block the server. The bounded ctx flows into every
	// p.Rod.Context(ctx) call below, so cancellation aborts the extraction.
	ctx, cancel := context.WithTimeout(ctx, defaultPageTimeout)
	defer cancel()

	p, err := t.mgr.Page(ctx, a.SessionID) // opens the Browser if needed; probes for liveness
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}

	var out string
	switch a.What {
	case "html":
		out, err = t.extractHTML(ctx, p, a.Selector)
	case "dom":
		out, err = t.extractDOMSnapshot(ctx, p)
	case "text":
		out, err = t.extractText(ctx, p, a.Selector)
	case "css":
		out, err = t.extractCSS(ctx, p, a.Target)
	case "js":
		out, err = t.extractJS(ctx, p, a.Target)
	case "links":
		out, err = t.extractLinks(ctx, p)
	case "screenshot":
		out, err = t.extractScreenshot(ctx, p, a.FullPage)
	case "meta":
		out, err = t.extractMeta(ctx, p)
	case "interactives":
		out, err = t.extractInteractives(ctx, p)
	case "a11y":
		out, err = t.extractA11y(ctx, p)
	case "network":
		out, err = t.extractNetwork(ctx, p)
	case "console":
		out, err = t.extractConsole(ctx, p)
	case "article":
		out, err = t.extractArticle(ctx, p)
	case "marks":
		out, err = t.extractMarks(ctx, p)
	default:
		return "", fmt.Errorf("page_fetch: unknown what=%q (must be one of html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, console, article, marks)", a.What)
	}
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}
	// Screenshots bypass truncate: the data-URI is binary-as-text and cutting
	// it mid-stream would produce an undecodable image. Image size scales with
	// the viewport (or full-page height), not max_bytes — the 50KB truncation
	// convention applies to text extractions only. Interactives, a11y, network
	// and console bypass it too: all four are machine-parseable JSON envelopes
	// whose designed incompleteness signal is their "truncated" field (the
	// MaxInteractives / MaxA11yNodes / MaxLogEmit cap) plus, for the logs, the
	// "dropped" ring-overflow counter — not a byte cut. Truncating would emit
	// invalid JSON plus a misleading "narrow the selector" hint. Article and
	// marks bypass it for the same reason: article self-caps at
	// browsers.MaxArticleRunes and reports it through "truncated", while marks
	// carries an annotated screenshot data-URI that a byte cut would corrupt.
	if a.What == "screenshot" || a.What == "interactives" || a.What == "a11y" ||
		a.What == "network" || a.What == "console" || a.What == "article" ||
		a.What == "marks" {
		return out, nil
	}
	switch a.What {
	case "dom", "links", "meta":
		return truncateJSON(out, a.MaxBytes), nil
	}
	return t.truncate(out, a.MaxBytes), nil
}

// extractHTML returns the page's outerHTML, or of the element matched by
// selector when one is given. Backed by p.Rod.HTML() / element HTML().
func (t *Page) extractHTML(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

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

// extractText returns the visible text of the page (or of the element matched
// by selector). Backed by Eval over document.body.innerText / element.innerText.
func (t *Page) extractText(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

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

// extractLinks returns the page's anchor links as a JSON array of
// {text, href, handle?} entries, deduplicated by href (first occurrence wins).
// Backed by the shared pageops.PageLinks implementation so the MCP, CLI and IPC
// surfaces stay byte-identical.
func (t *Page) extractLinks(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageLinks(ctx, p)
}

// extractDOMSnapshot returns the page's DOM as a compact, LLM-friendly JSON
// tree in which every element node carries a g<document>-b<backendNodeId> handle.
// Backed by
// the shared pageops.PageDOM implementation so the MCP, CLI and IPC surfaces
// stay byte-identical.
func (t *Page) extractDOMSnapshot(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageDOM(ctx, p)
}

// extractCSS returns the page's stylesheet resources: a JSON list of
// {url, type, size} entries, or — when target is non-empty — the full content
// of exactly that one stylesheet. Backed by proto.PageGetResourceTree
// enumeration + p.Rod.GetResource(url).
func (t *Page) extractCSS(ctx context.Context, p *browsers.Page, target string) (string, error) {
	return t.extractResources(ctx, p, target, "css", proto.NetworkResourceTypeStylesheet)
}

// extractJS returns the page's script resources: a JSON list of
// {url, type, size} entries, or — when target is non-empty — the full content
// of exactly that one script. Backed by proto.PageGetResourceTree enumeration
// + p.Rod.GetResource(url).
func (t *Page) extractJS(ctx context.Context, p *browsers.Page, target string) (string, error) {
	return t.extractResources(ctx, p, target, "js", proto.NetworkResourceTypeScript)
}

// extractResources is the shared backend of extractCSS and extractJS. It
// enumerates the page's resources of the given kind by walking the CDP
// resource tree recursively (iframes/subframes included) and returns either:
//
//   - with target set: the content of exactly that resource, fetched via
//     p.Rod.GetResource(url) and prefixed with a small // url= / // bytes=
//     header, or an LLM-actionable error when the resource is unknown; or
//   - without target: the JSON list of matching resources, capped downstream
//     by truncate (re-request with what=css&target=URL for full content).
//
// what is the tool's own what value for the kind ("css" or "js"); it is used
// in the hint so the suggested re-request uses a valid enum value (the CDP
// resource type string, e.g. "Stylesheet", is not part of the what enum).
//
// The per-page mutex is held for the whole extraction because rod pages are
// not thread-safe.
func (t *Page) extractResources(ctx context.Context, p *browsers.Page, target, what string, kind proto.NetworkResourceType) (string, error) {
	p.Lock()
	defer p.Unlock()

	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}
	// Bind the ctx-bounded clone once and reuse it for the tree call and the
	// GetResource fetch, so the whole extraction honors the Execute context.
	page := p.Rod.Context(ctx)

	res, err := proto.PageGetResourceTree{}.Call(page)
	if err != nil {
		return "", fmt.Errorf("enumerate resource tree: %w", err)
	}

	var resources []*proto.PageFrameResource
	collectResources(res.FrameTree, kind, &resources)

	if target != "" {
		return t.fetchResource(page, resources, target)
	}

	list := make([]resourceEntry, 0, len(resources))
	for _, r := range resources {
		list = append(list, resourceEntry{URL: r.URL, Type: string(r.Type), Size: r.ContentSize})
	}
	if kind == proto.NetworkResourceTypeStylesheet {
		inline, err := collectInlineStyles(page)
		if err != nil {
			return "", fmt.Errorf("read inline styles: %w", err)
		}
		list = append(list, inline...)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("encode resource list: %w", err)
	}
	// The list is capped by truncate in Execute; the hint tells the agent how
	// to fetch a single resource's full content (AGENTS.md dehydration
	// convention: "first 50KB; use what=css&target=URL to fetch more").
	return string(b) + "\n// hint: to fetch one resource's full content, re-request with what=" +
		what + "&target=<url>", nil
}

// collectResources walks the resource tree depth-first, appending every
// resource of the given kind that actually loaded (Failed and Canceled both
// false) to out. ChildFrames are visited recursively so iframe/subframe
// resources are included.
func collectResources(tree *proto.PageFrameResourceTree, kind proto.NetworkResourceType, out *[]*proto.PageFrameResource) {
	if tree == nil {
		return
	}
	for _, r := range tree.Resources {
		if r.Type == kind && !r.Failed && !r.Canceled {
			*out = append(*out, r)
		}
	}
	for _, child := range tree.ChildFrames {
		collectResources(child, kind, out)
	}
}

// collectInlineStyles returns one resource entry per inline <style> block so a
// page with only inline CSS reports its styles instead of an empty list.
func collectInlineStyles(page *rod.Page) ([]resourceEntry, error) {
	res, err := page.Eval(`() => [...document.querySelectorAll('style')].map(s => s.textContent || '')`)
	if err != nil {
		return nil, err
	}
	var bodies []string
	if err := res.Value.Unmarshal(&bodies); err != nil {
		return nil, fmt.Errorf("decode inline styles: %w", err)
	}
	entries := make([]resourceEntry, 0, len(bodies))
	for i, body := range bodies {
		size := float64(len(body))
		entries = append(entries, resourceEntry{
			URL:  fmt.Sprintf("inline-style[%d]", i+1),
			Type: "inline-style",
			Size: &size,
		})
	}
	return entries, nil
}

// fetchResource returns the content of the resource whose URL equals target,
// preferring a match from the enumerated (already-loaded) resources but
// falling back to a direct GetResource attempt so an unknown-but-fetchable URL
// still resolves. The content is prefixed with a small // url= / // bytes=
// header and capped downstream by truncate.
func (t *Page) fetchResource(page *rod.Page, resources []*proto.PageFrameResource, target string) (string, error) {
	for _, r := range resources {
		if r.URL == target {
			b, err := page.GetResource(r.URL)
			if err != nil {
				return "", fmt.Errorf("fetch resource %q: %w", target, err)
			}
			return formatResource(target, len(b), b), nil
		}
	}
	b, err := page.GetResource(target)
	if err != nil {
		return "", fmt.Errorf("resource %q not found in the page's resource tree: %w", target, err)
	}
	return formatResource(target, len(b), b), nil
}

// formatResource prefixes fetched resource bytes with a header identifying the
// URL and byte size, so the LLM knows which resource the content belongs to.
func formatResource(url string, size int, b []byte) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "// url=%s\n// bytes=%d\n", url, size)
	buf.Write(b)
	return buf.String()
}

// resourceEntry is one css/js resource in the enumeration list returned by
// extractResources.
type resourceEntry struct {
	// URL is the absolute resource URL.
	URL string `json:"url"`
	// Type is the resource type as reported by the browser ("Stylesheet" or
	// "Script").
	Type string `json:"type"`
	// Size is the content size in bytes reported by the browser, or null when
	// the browser did not report one. Resources are never fetched just to size
	// them.
	Size *float64 `json:"size"`
}

// extractScreenshot returns a PNG screenshot of the page as a base64 data-URI
// ("data:image/png;base64,…") that MCP clients can render directly. When
// fullPage is true the entire scrollable page is captured; otherwise only the
// visible viewport. Backed by p.Rod.Screenshot(fullPage, nil) — a nil request
// yields CDP defaults (PNG). The output deliberately bypasses truncate in
// Execute: image size scales with the viewport, not max_bytes, and cutting a
// data-URI mid-stream would corrupt it.
func (t *Page) extractScreenshot(ctx context.Context, p *browsers.Page, fullPage bool) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	// A full-page capture scrolls the whole page, composites the tiles and
	// encodes a large PNG, so a tall/heavy page can exceed the 30s per-call
	// budget. Give it a dedicated, longer bound decoupled from the caller's
	// deadline: context.WithoutCancel keeps the value chain but drops the 30s
	// cancellation, and WithTimeout caps the capture at
	// fullPageScreenshotTimeout. Viewport-only captures keep the caller's ctx
	// and its 30s bound.
	captureCtx := ctx
	if fullPage {
		var cancel context.CancelFunc
		captureCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), fullPageScreenshotTimeout)
		defer cancel()
	}

	b, err := p.Rod.Context(captureCtx).Screenshot(fullPage, nil)
	if err != nil {
		return "", fmt.Errorf("capture screenshot (full_page=%t): %w", fullPage, err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b), nil
}

// extractMeta returns the page's meta info (title, url, meta tags) as a JSON
// object. Backed by p.Rod.Info() plus Eval collecting every <meta> tag.
func (t *Page) extractMeta(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

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

	var tags []metaTag
	if err := res.Value.Unmarshal(&tags); err != nil {
		return "", fmt.Errorf("decode meta tags: %w", err)
	}

	out := pageMeta{Title: info.Title, URL: info.URL, Meta: tags}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode page meta: %w", err)
	}
	return string(b), nil
}

// extractInteractives returns the page's interactive elements as a dense,
// LLM-friendly action map: every visible, non-inert clickable/typeable element
// with a stable handle, role, text, href, input type and center x/y — the
// whole-page snapshot an agent uses to act immediately (page_act
// { handle: ... }) without selector guesswork. It is a thin wrapper over the
// engine method Page.Interactives (pkg/browsers): that method owns the page
// lock and the nil-Rod guard, so this helper deliberately does NOT lock, and
// the result's json tags ARE the wire shape, so marshaling it directly keeps
// the MCP and CLI surfaces identical by construction.
func (t *Page) extractInteractives(ctx context.Context, p *browsers.Page) (string, error) {
	res, err := p.Interactives(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode interactives: %w", err)
	}
	return string(b), nil
}

// extractA11y returns the page's flattened accessibility tree as a JSON
// envelope: computed roles, accessible names, values and curated state flags,
// with depth and a handle on every DOM-backed node. It is a thin wrapper over
// pageops.PageA11y — the engine method Page.A11yNodes owns the page lock and
// the nil-Rod guard, so this helper deliberately does NOT lock, and marshaling
// happens in pageops so the MCP, CLI and IPC surfaces stay byte-identical.
func (t *Page) extractA11y(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageA11y(ctx, p)
}

// extractNetwork returns the newest buffered network entries as the bounded log
// envelope. It is a thin wrapper over pageops.PageNetwork so the MCP, CLI and
// IPC surfaces stay byte-identical.
func (t *Page) extractNetwork(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageNetwork(ctx, p)
}

// extractConsole returns the newest buffered console entries as the bounded log
// envelope. It is a thin wrapper over pageops.PageConsole so the MCP, CLI and
// IPC surfaces stay byte-identical.
func (t *Page) extractConsole(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageConsole(ctx, p)
}

// extractArticle returns the page's readable body as markdown plus its metadata,
// JSON-encoded. It is a thin wrapper over pageops.PageArticle so the MCP, CLI
// and IPC surfaces stay byte-identical; the engine owns the rune cap.
func (t *Page) extractArticle(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageArticle(ctx, p)
}

// extractMarks returns the numbered set-of-marks table and its annotated
// viewport screenshot, JSON-encoded. It is a thin wrapper over
// pageops.PageMarks so the MCP, CLI and IPC surfaces stay byte-identical.
func (t *Page) extractMarks(ctx context.Context, p *browsers.Page) (string, error) {
	return pageops.PageMarks(ctx, p)
}

// pageMeta is the JSON shape returned by extractMeta: the page title and URL
// plus the collected <meta> tags.
type pageMeta struct {
	// Title is the page's document title.
	Title string `json:"title"`
	// URL is the page's current URL.
	URL string `json:"url"`
	// Meta is the ordered list of <meta> tags.
	Meta []metaTag `json:"meta"`
}

// metaTag is one <meta> tag flattened to its meaningful attributes. Attributes
// absent on the tag are dropped via omitempty.
type metaTag struct {
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

// truncate caps s at maxBytes bytes, cutting on a UTF-8 rune boundary so the
// returned text never contains a half-decoded character. A non-positive
// maxBytes returns s unchanged. A truncation note names the applied limit so
// the caller knows the output was dehydrated.
func (t *Page) truncate(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	for len(b) > maxBytes && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes]) + fmt.Sprintf(
		"\n… (truncated at max_bytes=%d bytes; raise max_bytes — dom/links/meta do not use selector)",
		maxBytes,
	)
}

// truncateJSON caps a JSON extraction at maxBytes while keeping the result
// parseable. Array roots are cut at element boundaries and returned in a
// truncated envelope; object roots keep their fields, shrink the first array
// field, and gain a truncated flag.
func truncateJSON(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	var root any
	if err := json.Unmarshal([]byte(s), &root); err != nil {
		return s
	}
	switch v := root.(type) {
	case []any:
		return truncateJSONArray(v, maxBytes)
	case map[string]any:
		return truncateJSONObject(v, maxBytes)
	default:
		return s
	}
}

// truncateJSONArray keeps the largest prefix of items that fits in an envelope
// carrying the truncation flag and the omitted count.
func truncateJSONArray(items []any, maxBytes int) string {
	lo, hi := 0, len(items)
	best := ""
	for lo <= hi {
		mid := (lo + hi) / 2
		b, err := json.Marshal(map[string]any{
			"items":     items[:mid],
			"omitted":   len(items) - mid,
			"truncated": true,
			"max_bytes": maxBytes,
		})
		if err == nil && len(b) <= maxBytes {
			best = string(b)
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	if best != "" {
		return best
	}
	b, err := json.Marshal(map[string]any{"truncated": true, "max_bytes": maxBytes})
	if err != nil || len(b) > maxBytes {
		return fmt.Sprintf(`{"truncated":true,"max_bytes":%d}`, maxBytes)
	}
	return string(b)
}

// truncateJSONObject keeps the object's fields, trims its first array field to
// the largest prefix that fits, and reports the truncation explicitly.
func truncateJSONObject(obj map[string]any, maxBytes int) string {
	out := make(map[string]any, len(obj)+3)
	for k, v := range obj {
		out[k] = v
	}
	out["truncated"] = true
	out["max_bytes"] = maxBytes
	return fitJSONObject(out, "", maxBytes)
}

// fitJSONObject binary-searches the length of the preferred array field until
// the encoded object fits maxBytes, falling back to a minimal envelope.
func fitJSONObject(out map[string]any, key string, maxBytes int) string {
	if key == "" {
		key, _ = firstArrayField(out)
	}
	if key == "" {
		if b, err := json.Marshal(out); err == nil && len(b) <= maxBytes {
			return string(b)
		}
		b, err := json.Marshal(map[string]any{"truncated": true, "max_bytes": maxBytes})
		if err != nil || len(b) > maxBytes {
			return fmt.Sprintf(`{"truncated":true,"max_bytes":%d}`, maxBytes)
		}
		return string(b)
	}
	items, _ := out[key].([]any)
	lo, hi := 0, len(items)
	best := ""
	for lo <= hi {
		mid := (lo + hi) / 2
		out[key] = items[:mid]
		out["omitted"] = len(items) - mid
		b, err := json.Marshal(out)
		if err == nil && len(b) <= maxBytes {
			best = string(b)
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	if best != "" {
		return best
	}
	out[key] = items[:0]
	out["omitted"] = len(items)
	b, err := json.Marshal(out)
	if err == nil && len(b) <= maxBytes {
		return string(b)
	}
	b, err = json.Marshal(map[string]any{"truncated": true, "max_bytes": maxBytes})
	if err != nil || len(b) > maxBytes {
		return fmt.Sprintf(`{"truncated":true,"max_bytes":%d}`, maxBytes)
	}
	return string(b)
}

// firstArrayField returns the first array field in sorted key order, preferring
// well-known collection fields so the choice stays deterministic.
func firstArrayField(obj map[string]any) (string, []any) {
	for _, key := range []string{"meta", "items", "nodes", "entries", "links", "children"} {
		if items, ok := obj[key].([]any); ok {
			return key, items
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if items, ok := obj[k].([]any); ok {
			return k, items
		}
	}
	return "", nil
}

// Ensure Page satisfies the Tool interface at compile time.
var _ Tool = (*Page)(nil)

// InspectPageDecompositionPrompt is a co-located prompt definition for page_fetch.
type InspectPageDecompositionPrompt struct{}

// NewInspectPageDecompositionPrompt constructs the inspect_page_decomposition prompt template.
func NewInspectPageDecompositionPrompt() *InspectPageDecompositionPrompt {
	return &InspectPageDecompositionPrompt{}
}

// Name returns the prompt template name "inspect_page_decomposition".
func (p *InspectPageDecompositionPrompt) Name() string { return "inspect_page_decomposition" }

// Description returns a description of the prompt template.
func (p *InspectPageDecompositionPrompt) Description() string {
	return "Provides structured guidance to decompose a live session into metadata, navigation links, and DOM tree snapshot."
}

// Arguments returns the parameter list accepted by the prompt template.
func (p *InspectPageDecompositionPrompt) Arguments() []models.PromptArgument {
	return []models.PromptArgument{
		{Name: "session_id", Description: "ID of the active page session to decompose", Required: true},
	}
}

// GetMessages builds the prompt messages payload for the given argument map.
func (p *InspectPageDecompositionPrompt) GetMessages(args map[string]string) ([]models.PromptMessage, error) {
	sessionID := args["session_id"]
	if sessionID == "" {
		sessionID = "page-1"
	}
	text := fmt.Sprintf("Please inspect and decompose the live session %q:\n"+
		"1. Call page_fetch with session_id: %q and what: \"meta\" to confirm title and canonical URL.\n"+
		"2. Call page_fetch with session_id: %q and what: \"links\" (max_bytes: 20000) to map navigation destinations.\n"+
		"3. Call page_fetch with session_id: %q and what: \"dom\" (scoped via selector if needed) to analyze layout tree.\n"+
		"4. Present the structured page decomposition.", sessionID, sessionID, sessionID, sessionID)

	return []models.PromptMessage{
		{
			Role:    "user",
			Content: models.TextContent{Type: "text", Text: text},
		},
	}, nil
}

// Ensure InspectPageDecompositionPrompt satisfies the Prompt interface at compile time.
var _ Prompt = (*InspectPageDecompositionPrompt)(nil)
