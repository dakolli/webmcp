// Package pageops holds the cobra-free page helpers shared by the web-cli
// commands (pkg/cli/cmd) and the IPC server (pkg/cli/ipc): fetching page
// summaries, extracting page aspects (html, dom, text, css, js, links,
// screenshot, meta, interactives), performing page actions (click, input,
// hover, scroll, select, eval, navigate, back, forward, reload, wait) and
// truncating output.
// The helpers own the per-page Lock/Unlock discipline required because rod
// Pages are not thread-safe (pkg/browsers session.go) — callers must not lock
// themselves. Only this package drives rod on the CLI side; cmd/ipc never
// import rod-family packages (AGENTS.md CLI boundary).
package pageops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/browsers"
)

// DefaultPageMaxBytes caps how much of any page fetch extraction is returned
// in one call (the CLI --max-bytes default and the IPC page.fetch default).
// Huge pages are dehydrated to a first slice; the caller can re-run with a
// narrower selector or a higher max_bytes. Screenshots and interactives are
// the documented exceptions: the data-URI (screenshot) and the
// machine-parseable JSON envelope with its own "truncated" field
// (interactives) bypass PageTruncate entirely.
const DefaultPageMaxBytes = 50_000

// defaultActTimeout bounds the page_act-style helpers' "ms" wait-mode sleep
// when the caller passes no explicit timeout (mirroring defaultActTimeout in
// pkg/mcp/tools/act.go). Callers bound the ctx themselves (defaultCmdTimeout /
// the IPC per-call timeout); this fallback only sizes the sleep.
const defaultActTimeout = 30 * time.Second

// ValidateURL checks that s is an absolute http(s) URL the browser can
// navigate to: it must parse, be absolute and carry a host.
func ValidateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", s, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("invalid url %q: must be an absolute http(s) URL", s)
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("invalid url %q: scheme %q not supported (must be http or https)", s, u.Scheme)
	}
}

// ValidateWait checks that the wait strategy is one of the shared readiness
// vocabulary: idle, load, stable (used by fetch and page.open) plus the
// page_act-only interactable and ms modes (used by the wait action's --wait).
func ValidateWait(waitFor string) error {
	switch waitFor {
	case "idle", "load", "stable", "interactable", "ms":
		return nil
	default:
		return fmt.Errorf("invalid --wait %q (must be one of idle, load, stable, interactable, ms)", waitFor)
	}
}

// ParseViewport parses a "WxH" viewport string (e.g. "1440x900") into a
// browsers.Viewport. An empty or absent flag yields nil — a plain launch with
// rod's own defaults, NO override (the explicit-override-only doctrine: pages
// launch plain by default; device presets via Page.Emulate are the primary
// opt-in mechanism). Any malformed or non-positive value fails with the exact
// pinned error so the CLI and IPC layers share one parse surface (mirrors the
// ValidateURL/ValidateWait pattern).
func ParseViewport(s string) (*browsers.Viewport, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	return &browsers.Viewport{Width: w, Height: h}, nil
}

// WaitForPage blocks until the page satisfies the requested readiness level.
// It maps "load" to WaitLoad, "idle" to WaitRequestIdle, and "stable" to
// WaitDOMStable. All rod page calls are serialized with the per-page lock. The
// page_act-only "interactable" and "ms" modes are handled by PageWait, not
// here (they need a selector and a timeout respectively).
func WaitForPage(ctx context.Context, p *browsers.Page, waitFor string) error {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}

	pc := p.Rod.Context(ctx) // bound to ctx so cancellation aborts a stuck wait
	switch waitFor {
	case "idle":
		// WaitRequestIdle RETURNS a blocking func() — call it, do not return it.
		wait := pc.WaitRequestIdle(500*time.Millisecond, nil, nil, nil)
		wait()
		return nil
	case "load":
		return pc.WaitLoad()
	case "stable":
		return pc.WaitDOMStable(500*time.Millisecond, 0.05)
	default:
		return fmt.Errorf("invalid wait option %q", waitFor)
	}
}

// FetchSummary is the output shape of opening a page, identical between the
// CLI fetch command's human-readable/--json forms and the IPC page.open
// result. It carries the session_id (usable with the page command / page.*
// IPC methods), page metadata and compact size/timing hints — never the full
// HTML (first-50KB rule).
type FetchSummary struct {
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	TextSnippet string `json:"text_snippet"`
	HTMLBytes   int    `json:"html_bytes"`
	LoadMs      int64  `json:"load_ms"`
}

// PageSummary renders a compact summary of a live page: the page ID, its title
// and URL (via p.Rod.Info()), plus a short text snippet and byte-size hints.
// loadStarted anchors the load_ms timing (captured around open → navigate →
// wait).
func PageSummary(ctx context.Context, p *browsers.Page, loadStarted time.Time) (FetchSummary, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return FetchSummary{}, fmt.Errorf("page session is closed")
	}

	info, err := p.Rod.Context(ctx).Info() // title + url
	if err != nil {
		return FetchSummary{}, fmt.Errorf("page info: %w", err)
	}

	html, err := p.Rod.Context(ctx).HTML() // byte-size hint only; not printed in full
	if err != nil {
		return FetchSummary{}, fmt.Errorf("read html: %w", err)
	}

	text, err := p.Rod.Context(ctx).Eval(`() => document.body.innerText.slice(0, 500)`)
	if err != nil {
		return FetchSummary{}, fmt.Errorf("read text: %w", err)
	}

	return FetchSummary{
		SessionID:   string(p.ID),
		Title:       info.Title,
		URL:         info.URL,
		TextSnippet: text.Value.Str(),
		HTMLBytes:   len(html),
		LoadMs:      time.Since(loadStarted).Milliseconds(),
	}, nil
}

// PageHTML returns the page's outerHTML, or of the element matched by selector
// when one is given. Backed by p.Rod.HTML() / element HTML() — mirrors the MCP
// extractHTML.
func PageHTML(ctx context.Context, p *browsers.Page, selector string) (string, error) {
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

// PageText returns the visible text of the page (or of the element matched by
// selector). Backed by Eval over document.body.innerText / element.innerText —
// mirrors the MCP extractText.
func PageText(ctx context.Context, p *browsers.Page, selector string) (string, error) {
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

// PageLinks returns the page's anchor links as a JSON array of {text, href},
// deduplicated by href (first occurrence wins). Non-navigable hrefs
// (javascript:/mailto:/data:/tel:, empty, exactly "#") and text-less anchors
// are dropped; remaining hrefs are entity-decoded — mirrors the MCP
// extractLinks shape and behavior.
func PageLinks(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	// Text comes from textContent (rendering-independent: captures anchors in
	// CSS-hidden dropdown panels that innerText reports as ""), normalized to
	// single spaces, with an accessible-name fallback chain (aria-label →
	// title → img[alt]) for anchors with no text node. Anchors with no text
	// and no fallback are dropped right here (no "text":"" on the wire, ever).
	res, err := p.Rod.Context(ctx).Eval(
		`() => [...document.querySelectorAll('a[href]')].map(a => {
  const text = (a.textContent || '').replace(/\s+/g, ' ').trim()
    || (a.getAttribute('aria-label') || '').trim()
    || (a.title || '').trim()
    || ((a.querySelector('img') || {}).alt || '').trim();
  return { text, href: a.href };
}).filter(l => l.text !== '')`)
	if err != nil {
		return "", fmt.Errorf("evaluate links: %w", err)
	}

	var links []PageLink
	if err := res.Value.Unmarshal(&links); err != nil {
		return "", fmt.Errorf("decode links: %w", err)
	}

	// Filter, decode and dedupe in one pass — dedupe AFTER filtering so junk
	// schemes never occupy the seen map. Drop non-navigable hrefs and any
	// text-less leftover (the Eval already skips those; this guard keeps the
	// no-empty-text invariant at the output boundary), unescape entity
	// references a page may have set literally from JS ("&amp;" — harmless on
	// already-decoded hrefs), then keep the first occurrence per href.
	seen := make(map[string]bool, len(links))
	dedup := make([]PageLink, 0, len(links))
	for _, l := range links {
		l.Href = html.UnescapeString(l.Href)
		if l.Text == "" || !navigableHref(l.Href) || seen[l.Href] {
			continue
		}
		seen[l.Href] = true
		dedup = append(dedup, l)
	}

	// Encode without HTML-safe escaping so "&" renders literally on the links
	// surface (json.Marshal would emit \u0026); every other extractor keeps
	// the default escaping.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(dedup); err != nil {
		return "", fmt.Errorf("encode links: %w", err)
	}
	// json.Encoder appends a trailing newline that json.Marshal did not —
	// trim it so the output shape is unchanged.
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// navigableHref reports whether an href is one the browser can navigate to:
// non-empty, not exactly "#", and not a non-navigable pseudo-scheme
// (javascript:, mailto:, data:, tel: — compared case-insensitively). Relative
// URLs (already resolved absolute by a.href) and same-page fragment links like
// "#skip" pass through.
func navigableHref(href string) bool {
	if href == "" || href == "#" {
		return false
	}
	switch strings.ToLower(strings.SplitN(href, ":", 2)[0]) {
	case "javascript", "mailto", "data", "tel":
		return false
	}
	return true
}

// PageMeta returns the page's meta info (title, url, meta tags) as a JSON
// object — mirrors the MCP extractMeta shape. Backed by p.Rod.Info() plus Eval
// collecting every <meta> tag.
func PageMeta(ctx context.Context, p *browsers.Page) (string, error) {
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

	var tags []PageMetaTag
	if err := res.Value.Unmarshal(&tags); err != nil {
		return "", fmt.Errorf("decode meta tags: %w", err)
	}

	out := PageMetaResult{Title: info.Title, URL: info.URL, Meta: tags}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode page meta: %w", err)
	}
	return string(b), nil
}

// PageInteractives returns the page's interactive elements as a dense JSON
// action map: every visible, non-inert clickable/typeable element with a
// stable handle, role, text, href, input type and center x/y — the whole-page
// snapshot used to act immediately (page act { handle: ... }) without selector
// guesswork. Mirrors the MCP page_fetch what=interactives shape exactly.
//
// Deliberately does NOT lock: the engine method Page.Interactives takes the
// page lock itself and owns the nil-Rod guard (mirroring PageSetViewport/
// PageEmulate's engine-seam pattern) — an exception to the pageops
// self-locking convention, so callers must not pre-lock either.
func PageInteractives(ctx context.Context, p *browsers.Page) (string, error) {
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

// PageScreenshot returns a PNG screenshot of the page as a base64 data-URI
// ("data:image/png;base64,…") that consumers can render directly. When fullPage
// is true the entire scrollable page is captured; otherwise only the visible
// viewport. Backed by p.Rod.Screenshot(fullPage, nil) — a nil request yields
// CDP defaults (PNG). The output deliberately bypasses PageTruncate at the call
// sites: image size scales with the viewport, not max_bytes, and cutting a
// data-URI mid-stream would corrupt it. Mirrors the MCP extractScreenshot.
func PageScreenshot(ctx context.Context, p *browsers.Page, fullPage bool) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	b, err := p.Rod.Context(ctx).Screenshot(fullPage, nil)
	if err != nil {
		return "", fmt.Errorf("capture screenshot (full_page=%t): %w", fullPage, err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b), nil
}

// PageCSS returns the page's stylesheet resources: a JSON list of
// {url, type, size} entries, or — when target is non-empty — the full content
// of exactly that one stylesheet. Mirrors the MCP extractCSS.
func PageCSS(ctx context.Context, p *browsers.Page, target string) (string, error) {
	return PageResources(ctx, p, target, "css", proto.NetworkResourceTypeStylesheet)
}

// PageJS returns the page's script resources: a JSON list of {url, type, size}
// entries, or — when target is non-empty — the full content of exactly that one
// script. Mirrors the MCP extractJS.
func PageJS(ctx context.Context, p *browsers.Page, target string) (string, error) {
	return PageResources(ctx, p, target, "js", proto.NetworkResourceTypeScript)
}

// PageResources is the shared backend of PageCSS and PageJS. It enumerates the
// page's resources of the given kind by walking the CDP resource tree
// recursively (iframes/subframes included) and returns either:
//
//   - with target set: the content of exactly that resource, fetched via
//     p.Rod.GetResource(url) and prefixed with a small // url= / // bytes=
//     header, or an LLM-actionable error when the resource is unknown; or
//   - without target: the JSON list of matching resources, capped downstream
//     by PageTruncate (re-request with --what css|js --target <url> for full
//     content).
//
// what is the tool's own what value for the kind ("css" or "js"); it is used
// in the hint so the suggested re-request uses a valid enum value (the CDP
// resource type string, e.g. "Stylesheet", is not part of the what enum).
//
// The per-page mutex is held for the whole extraction because rod pages are
// not thread-safe. Mirrors the MCP extractResources.
func PageResources(ctx context.Context, p *browsers.Page, target, what string, kind proto.NetworkResourceType) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	// Bind the ctx-bounded clone once and reuse it for the tree call and the
	// GetResource fetch, so the whole extraction honors the caller's context.
	page := p.Rod.Context(ctx)

	res, err := proto.PageGetResourceTree{}.Call(page)
	if err != nil {
		return "", fmt.Errorf("enumerate resource tree: %w", err)
	}

	var resources []*proto.PageFrameResource
	collectResources(res.FrameTree, kind, &resources)

	if target != "" {
		return fetchResource(page, resources, target)
	}

	list := make([]resourceEntry, 0, len(resources))
	for _, r := range resources {
		list = append(list, resourceEntry{URL: r.URL, Type: string(r.Type), Size: r.ContentSize})
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("encode resource list: %w", err)
	}
	// The list is capped by PageTruncate at the call sites; the hint tells the
	// caller how to fetch a single resource's full content (AGENTS.md
	// dehydration convention: "first 50KB; use what=css&target=URL to fetch
	// more").
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

// fetchResource returns the content of the resource whose URL equals target,
// preferring a match from the enumerated (already-loaded) resources but
// falling back to a direct GetResource attempt so an unknown-but-fetchable URL
// still resolves. The content is prefixed with a small // url= / // bytes=
// header and capped downstream by PageTruncate.
func fetchResource(page *rod.Page, resources []*proto.PageFrameResource, target string) (string, error) {
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
// URL and byte size, so the caller knows which resource the content belongs to.
func formatResource(url string, size int, b []byte) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "// url=%s\n// bytes=%d\n", url, size)
	buf.Write(b)
	return buf.String()
}

// resourceEntry is one css/js resource in the enumeration list returned by
// PageResources.
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

// PageDOM returns the page's DOM as a compact, LLM-friendly JSON tree.
// CaptureDOMSnapshot yields a flattened Documents array (index 0 = root
// document, later indexes = iframe/imported documents) plus a shared Strings
// table; this helper re-nests each document's parallel-array node tables by
// ParentIndex and resolves every string index through the table, so the reader
// sees a real tree (iframes, template contents and flattened shadow DOM
// included) instead of raw CDP arrays. Backed by p.Rod.CaptureDOMSnapshot();
// mirrors the MCP extractDOMSnapshot shape exactly.
func PageDOM(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	snap, err := p.Rod.Context(ctx).CaptureDOMSnapshot()
	if err != nil {
		return "", fmt.Errorf("capture DOM snapshot: %w", err)
	}

	docs := buildDOMDocuments(snap)
	b, err := json.Marshal(docs)
	if err != nil {
		return "", fmt.Errorf("encode DOM snapshot: %w", err)
	}
	return string(b), nil
}

// domDocument is the JSON shape of one document in the DOM snapshot output:
// its context (title, url, frame id) plus the re-nested element tree.
type domDocument struct {
	// Title is the document's title.
	Title string `json:"title"`
	// URL is the document's URL.
	URL string `json:"url"`
	// FrameID is the CDP frame id this document belongs to.
	FrameID string `json:"frame_id,omitempty"`
	// Root is the nested tree root (#document).
	Root *domNode `json:"root"`
}

// domNode is one node in the re-nested DOM tree.
type domNode struct {
	// Tag is the lowercased element name ("div"), or a pseudo-name for
	// non-elements ("#document", "#text", "#comment").
	Tag string `json:"tag"`
	// ID is the element's id attribute, hoisted out of Attrs.
	ID string `json:"id,omitempty"`
	// Class is the element's class attribute, whitespace-normalized, hoisted
	// out of Attrs.
	Class string `json:"class,omitempty"`
	// Attrs holds the element's remaining attributes.
	Attrs map[string]string `json:"attrs,omitempty"`
	// Text is the node's text: the nodeValue for text nodes, the textarea/
	// input value or the layout text for elements.
	Text string `json:"text,omitempty"`
	// Visible is true when the node has a layout box (absolute bounds), i.e.
	// it is rendered rather than display:none or detached. Absent when false.
	Visible bool `json:"visible,omitempty"`
	// Bounds is the node's absolute position bounding box as reported by CDP
	// (an array of floats), when the node has one.
	Bounds []float64 `json:"bounds,omitempty"`
	// FrameURL is set on frame-owner elements (iframe/frame/object) that CDP
	// reports as containing another document; the child document itself is
	// also emitted as its own entry in the Documents array.
	FrameURL string `json:"frame_url,omitempty"`
	// Children are the node's element/text/comment children in document order.
	Children []*domNode `json:"children,omitempty"`
}

// buildDOMDocuments converts every document in the snapshot into a nested tree
// and attaches iframe child documents to their frame-owner nodes. Each
// document is emitted with its title/url/frame id context so no document is
// lost, and frame owners carry the child document's URL inline.
func buildDOMDocuments(snap *proto.DOMSnapshotCaptureSnapshotResult) []domDocument {
	docs := make([]domDocument, len(snap.Documents))
	nodesByDoc := make([]map[int]*domNode, len(snap.Documents))
	for i, d := range snap.Documents {
		root, byIndex := buildDOMTree(snap.Strings, d)
		nodesByDoc[i] = byIndex
		docs[i] = domDocument{
			Title:   str(snap.Strings, d.Title),
			URL:     str(snap.Strings, d.DocumentURL),
			FrameID: str(snap.Strings, d.FrameID),
			Root:    root,
		}
	}

	// Second pass: a node whose ContentDocumentIndex rare data points at
	// another document is a frame owner — annotate it with the child
	// document's URL so the reader can jump from the iframe to its content.
	for i, d := range snap.Documents {
		byIndex := nodesByDoc[i]
		if byIndex == nil || d.Nodes == nil || d.Nodes.ContentDocumentIndex == nil {
			continue
		}
		for k, nodeIdx := range d.Nodes.ContentDocumentIndex.Index {
			childIdx := d.Nodes.ContentDocumentIndex.Value[k]
			if childIdx < 0 || childIdx >= len(docs) || childIdx == i {
				continue
			}
			if node := byIndex[nodeIdx]; node != nil {
				node.FrameURL = docs[childIdx].URL
			}
		}
	}
	return docs
}

// buildDOMTree re-nests one document's flat parallel-array node table into a
// tree rooted at the node with parent -1, and returns the root plus a flat
// node-index → *domNode map (used to attach iframe documents afterwards).
// Whitespace-only text nodes and the inline bodies of <script>/<style>
// elements are elided to keep the output compact (script/style sources are
// available via --what js/css instead).
func buildDOMTree(strs []string, doc *proto.DOMSnapshotDocumentSnapshot) (*domNode, map[int]*domNode) {
	n := doc.Nodes
	if n == nil || len(n.ParentIndex) == 0 {
		return nil, nil
	}

	layout := layoutIndex(doc.Layout)
	byIndex := make(map[int]*domNode, len(n.ParentIndex))

	// Pass 1: populate every node from its parallel-array rows.
	for i := range n.ParentIndex {
		nd := &domNode{Tag: nodeTag(strs, n, i)}
		if i < len(n.NodeType) {
			switch n.NodeType[i] {
			case 1: // element: hoist id/class, keep other attributes
				nd.ID, nd.Class, nd.Attrs = nodeAttrs(strs, n.Attributes, i)
				nd.Text = rareString(strs, n.TextValue, i)
				if nd.Text == "" {
					nd.Text = rareString(strs, n.InputValue, i)
				}
			case 3: // text: the nodeValue is the text itself
				nd.Text = str(strs, n.NodeValue[i])
			}
		}
		if li, ok := layout[i]; ok {
			if li < len(doc.Layout.Bounds) && len(doc.Layout.Bounds[li]) > 0 {
				nd.Visible = true
				nd.Bounds = doc.Layout.Bounds[li]
			}
			if nd.Text == "" && li < len(doc.Layout.Text) {
				nd.Text = str(strs, doc.Layout.Text[li])
			}
		}
		byIndex[i] = nd
	}

	// Pass 2: attach children by ParentIndex, in document order.
	var root *domNode
	for i := range n.ParentIndex {
		nd := byIndex[i]
		parent := n.ParentIndex[i]
		if parent < 0 || parent >= len(n.ParentIndex) || parent == i {
			root = nd
			continue
		}
		// Elide whitespace-only text nodes (pretty-printed markup noise) and
		// the bodies of script/style elements (sources via --what js/css).
		if i < len(n.NodeType) && n.NodeType[i] == 3 && strings.TrimSpace(nd.Text) == "" {
			continue
		}
		pn := byIndex[parent]
		if pn == nil {
			root = nd
			continue
		}
		if pn.Tag == "script" || pn.Tag == "style" {
			continue
		}
		pn.Children = append(pn.Children, nd)
	}
	return root, byIndex
}

// layoutIndex maps node-tree index → layout-tree entry for O(1) lookups.
func layoutIndex(layout *proto.DOMSnapshotLayoutTreeSnapshot) map[int]int {
	if layout == nil {
		return nil
	}
	m := make(map[int]int, len(layout.NodeIndex))
	for i, node := range layout.NodeIndex {
		m[node] = i
	}
	return m
}

// nodeTag resolves a node's tag name: the nodeName lowercased for elements
// ("DIV" → "div"), or the raw pseudo-name for non-elements ("#document",
// "#text", "#comment").
func nodeTag(strs []string, n *proto.DOMSnapshotNodeTreeSnapshot, i int) string {
	if i >= len(n.NodeName) {
		return ""
	}
	name := str(strs, n.NodeName[i])
	if name == "" || strings.HasPrefix(name, "#") {
		return name
	}
	return strings.ToLower(name)
}

// nodeAttrs parses the flattened name,value attribute pairs of element node i,
// hoisting id and class out and returning the remaining attributes. Unnamed or
// unvalued trailing pairs are ignored.
func nodeAttrs(strs []string, attrs []proto.DOMSnapshotArrayOfStrings, i int) (id, class string, rest map[string]string) {
	if i >= len(attrs) {
		return "", "", nil
	}
	pairs := attrs[i]
	for k := 0; k+1 < len(pairs); k += 2 {
		name := str(strs, pairs[k])
		value := str(strs, pairs[k+1])
		switch name {
		case "id":
			id = value
		case "class":
			class = strings.Join(strings.Fields(value), " ")
		case "":
			// unnamed attribute pair — skip
		default:
			if rest == nil {
				rest = make(map[string]string, 4)
			}
			rest[name] = value
		}
	}
	return id, class, rest
}

// str resolves a string-table index, returning "" for negative or
// out-of-range indexes (a node may simply have no value).
func str(strs []string, idx proto.DOMSnapshotStringIndex) string {
	if idx < 0 || int(idx) >= len(strs) {
		return ""
	}
	return strs[idx]
}

// rareString returns the string-table value of the rare-data entry for node
// index i, or "" when the table has no entry for it. Rare data is small, so a
// linear scan is fine.
func rareString(strs []string, rd *proto.DOMSnapshotRareStringData, i int) string {
	if rd == nil {
		return ""
	}
	for k, node := range rd.Index {
		if node == i {
			return str(strs, rd.Value[k])
		}
	}
	return ""
}

// PageEval runs a JavaScript function in the page — the primary escape hatch
// for any page manipulation — and returns the JSON result. Backed by
// p.Rod.Context(ctx).Eval(js); js must be a JS function definition, e.g.
// "() => document.title". Mirrors the Eval pattern proven in extractText.
func PageEval(ctx context.Context, p *browsers.Page, js string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(js) // Eval only accepts a JS function definition
	if err != nil {
		return "", fmt.Errorf("evaluate js: %w", err)
	}
	return res.Value.JSON("", ""), nil // gson marshals to compact JSON text
}

// PageClick clicks the element matched by selector (CSS or XPath). It resolves
// the element, waits until it is interactable, then clicks it with the left
// button. Returns the same confirmation string as the MCP page_act click.
func PageClick(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx) // bound to ctx so a missing element aborts

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("click: element %q: %w", selector, err)
	}
	if _, err := el.WaitInteractable(); err != nil {
		return "", fmt.Errorf("click: element %q: %w", selector, err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return "", fmt.Errorf("click: element %q: %w", selector, err)
	}
	return fmt.Sprintf("clicked selector=%q", selector), nil
}

// PageInput sets the value of the element matched by selector: it selects all
// existing text (el.SelectAllText) then types value via rod's Input, so the
// field ends up exactly value. Returns the same confirmation string as the MCP
// page_act input.
func PageInput(ctx context.Context, p *browsers.Page, selector, value string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("input: element %q: %w", selector, err)
	}
	if err := el.SelectAllText(); err != nil {
		return "", fmt.Errorf("input: element %q: %w", selector, err)
	}
	if err := el.Input(value); err != nil {
		return "", fmt.Errorf("input: element %q: %w", selector, err)
	}
	return fmt.Sprintf("set input selector=%q value=%q", selector, value), nil
}

// PageType types keystrokes into the element matched by selector without
// clearing it — unlike PageInput (the input action selects all text first),
// type is Focus + InsertText: it focuses the element (scrolling it into view
// by user gesture) and pastes value at the current caret position. Returns the
// same confirmation string as the MCP page_act type.
func PageType(ctx context.Context, p *browsers.Page, selector, value string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("type: element %q: %w", selector, err)
	}
	if err := el.Focus(); err != nil {
		return "", fmt.Errorf("type: element %q: %w", selector, err)
	}
	if err := page.InsertText(value); err != nil {
		return "", fmt.Errorf("type: %w", err)
	}
	return fmt.Sprintf("typed selector=%q value=%q", selector, value), nil
}

// PageHover moves the mouse over the element matched by selector (CSS or
// XPath), triggering hover-dependent UI. Returns the same confirmation string
// as the MCP page_act hover.
func PageHover(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("hover: element %q: %w", selector, err)
	}
	if err := el.Hover(); err != nil {
		return "", fmt.Errorf("hover: element %q: %w", selector, err)
	}
	return fmt.Sprintf("hovered selector=%q", selector), nil
}

// PageScroll scrolls the window by x/y pixels (window.scrollBy via Eval — the
// primary path) and returns the resulting scroll position [scrollX, scrollY].
// Missing x/y are treated as 0. Mirrors the MCP page_act scroll confirmation.
func PageScroll(ctx context.Context, p *browsers.Page, x, y float64) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(
		"(x, y) => { window.scrollBy(x, y); return [window.scrollX, window.scrollY]; }",
		x, y,
	)
	if err != nil {
		return "", fmt.Errorf("scroll: %w", err)
	}
	return fmt.Sprintf("scrolled to %s", res.Value.JSON("", "  ")), nil
}

// PageSelect selects an <option> with the given value in the element matched by
// selector. rod v0.116.2 has no SelectorTypeValue, so the option is matched
// with a CSS attribute selector [value="..."]. Returns the same confirmation
// string as the MCP page_act select.
func PageSelect(ctx context.Context, p *browsers.Page, selector, value string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("select: element %q: %w", selector, err)
	}
	opt := `[value="` + value + `"]`
	if err := el.Select([]string{opt}, true, rod.SelectorTypeCSSSector); err != nil {
		return "", fmt.Errorf("select: element %q: %w", selector, err)
	}
	return fmt.Sprintf("selected option value=%q in selector=%q", value, selector), nil
}

// PageNavigate navigates the page to url and waits for the new page to load
// (rod's Navigate returns after the response header, so WaitLoad is
// mandatory). Returns the final URL and title — the same confirmation shape as
// the MCP page_act navigate.
func PageNavigate(ctx context.Context, p *browsers.Page, url string) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if err := page.Navigate(url); err != nil {
		return "", fmt.Errorf("navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("wait for load: %w", err)
	}
	// Info() reports the final URL (post-redirect) and title; fall back to the
	// requested URL if the metadata read fails — the navigation already landed.
	msg := fmt.Sprintf("navigated to %q", url)
	if info, err := page.Info(); err == nil {
		msg = fmt.Sprintf("navigated to %q (title %q)", info.URL, info.Title)
	}
	return msg, nil
}

// PageBack navigates one step back in the page's history and waits for the
// previous page to load (history.back() triggers a fresh navigation). Returns
// the resulting URL — the same confirmation shape as the MCP page_act back.
func PageBack(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if err := page.NavigateBack(); err != nil {
		return "", fmt.Errorf("navigate back: %w", err)
	}
	// WaitLoad returns immediately if no navigation happened (no history); when
	// it does, the URL below reflects the previous page.
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("wait for load: %w", err)
	}
	info, err := page.Info()
	if err != nil {
		return "", fmt.Errorf("read page info: %w", err)
	}
	return fmt.Sprintf("navigated back to %q", info.URL), nil
}

// PageForward navigates one step forward in the page's history and waits for
// the page to load. Returns the resulting URL — the same confirmation shape as
// the MCP page_act forward.
func PageForward(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if err := page.NavigateForward(); err != nil {
		return "", fmt.Errorf("navigate forward: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("wait for load: %w", err)
	}
	info, err := page.Info()
	if err != nil {
		return "", fmt.Errorf("read page info: %w", err)
	}
	return fmt.Sprintf("navigated forward to %q", info.URL), nil
}

// PageReload reloads the page and waits for it to load again. Returns the same
// confirmation string as the MCP page_act reload.
func PageReload(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if err := page.Reload(); err != nil {
		return "", fmt.Errorf("reload: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("wait for load: %w", err)
	}
	return "reloaded page", nil
}

// PageSetViewport changes the live page's viewport size to w × h CSS pixels
// via the engine seam Page.SetViewport (Emulation.setDeviceMetricsOverride
// with a device scale factor of 1, desktop, no mobile emulation, and matching
// screen metrics — screen.width tracks window.innerWidth). The engine method
// owns the page lock and the nil-Rod guard. Callers validate that w and h are
// positive (the CLI --width/--height flags and the IPC page.act handler do);
// a non-positive value would disable the override per the proto semantics.
// Returns the same confirmation string as the MCP page_act set_viewport.
func PageSetViewport(ctx context.Context, p *browsers.Page, w, h int) (string, error) {
	if err := p.SetViewport(ctx, &browsers.Viewport{Width: w, Height: h}); err != nil {
		return "", err
	}
	return fmt.Sprintf("set viewport to %dx%d", w, h), nil
}

// PageEmulate applies a go-rod device preset (lib/devices, v0.116.2) to the
// live page via the engine seam Page.Emulate — the primary, opt-in emulation
// mechanism (device UA + metrics + touch), so the CLI can switch a live page
// to a mobile/desktop preset without re-opening it. The engine method owns
// the page lock and the nil-Rod guard and resolves the name
// case-insensitively, failing with the pinned unknown-device error. Callers
// pre-validate the name via browsers.ValidateDeviceName (the CLI --device
// flag and the IPC page.act handler do, mirroring the MCP page_act). Returns
// the same confirmation string as the MCP page_act emulate.
func PageEmulate(ctx context.Context, p *browsers.Page, device string) (string, error) {
	if err := p.Emulate(ctx, device); err != nil {
		return "", err
	}
	return fmt.Sprintf("emulated device: %s", device), nil
}

// PageWait blocks until the page satisfies the requested readiness level, or
// sleeps for a fixed duration. It supports the readiness modes shared with
// fetch ("load", "idle", "stable"), an element-level "interactable" mode
// (requires selector), and the default "ms" mode — a ctx-aware sleep honoring
// timeout that lets in-flight requests and animations settle before the next
// action. The "ms" sleep touches no rod state, so it does not take the page
// lock; every rod-backed mode is fully locked (load/idle/stable delegate to
// WaitForPage, which owns the lock). Mirrors the MCP page_act wait helper.
func PageWait(ctx context.Context, p *browsers.Page, mode, selector string, timeout time.Duration) (string, error) {
	// Synchronized read (under p.mu, like every other nil guard): the
	// eviction path can nil p.Rod concurrently, so the check must not run
	// lock-free. The lock is released before the mode switch — rod-backed
	// modes re-lock (and re-check) themselves below.
	p.Lock()
	if p.Rod == nil {
		p.Unlock()
		return "", fmt.Errorf("page session is closed")
	}
	p.Unlock()

	switch mode {
	case "", "ms":
		// Backward-compatible default: sleep (ctx-aware) for timeout, so a
		// cancelled context aborts the sleep instead of blocking the caller.
		d := timeout
		if d <= 0 {
			d = defaultActTimeout
		}
		select {
		case <-time.After(d):
			return fmt.Sprintf("waited %s", d), nil
		case <-ctx.Done():
			return "", fmt.Errorf("wait: %w", ctx.Err())
		}

	case "load", "idle", "stable":
		// WaitForPage takes the page lock itself (shared with fetch).
		if err := WaitForPage(ctx, p, mode); err != nil {
			return "", fmt.Errorf("wait: %w", err)
		}
		return fmt.Sprintf("waited for %q", mode), nil

	case "interactable":
		if selector == "" {
			return "", fmt.Errorf("wait: interactable mode requires a selector")
		}
		p.Lock()
		defer p.Unlock()
		// Re-check under the lock: a concurrent eviction may have nilled p.Rod
		// since the entry check above released the lock.
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		page := p.Rod.Context(ctx)
		el, err := page.Element(selector)
		if err != nil {
			return "", fmt.Errorf("wait: element %q: %w", selector, err)
		}
		if _, err := el.WaitInteractable(); err != nil {
			return "", fmt.Errorf("wait: element %q: %w", selector, err)
		}
		return fmt.Sprintf("element %q is interactable", selector), nil

	default:
		return "", fmt.Errorf("wait: unknown wait_mode %q (must be one of load, idle, stable, interactable, ms)", mode)
	}
}

// PageLink is one anchor link collected by PageLinks: its text (the anchor's
// own text or its accessible name) and its absolute href.
type PageLink struct {
	// Text is the anchor's whitespace-normalized text, or its accessible name
	// when it has no text (fallback chain: aria-label → title → img[alt]).
	// Never empty — text-less anchors are filtered out.
	Text string `json:"text"`
	// Href is the anchor's absolute, entity-decoded URL (resolved against the
	// document base).
	Href string `json:"href"`
}

// PageMetaResult is the JSON shape returned by PageMeta: the page title and URL
// plus the collected <meta> tags.
type PageMetaResult struct {
	// Title is the page's document title.
	Title string `json:"title"`
	// URL is the page's current URL.
	URL string `json:"url"`
	// Meta is the ordered list of <meta> tags.
	Meta []PageMetaTag `json:"meta"`
}

// PageMetaTag is one <meta> tag flattened to its meaningful attributes.
// Attributes absent on the tag are dropped via omitempty.
type PageMetaTag struct {
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

// PageTruncate caps s at maxBytes bytes, cutting on a UTF-8 rune boundary so
// the printed text never contains a half-decoded character. A non-positive
// maxBytes returns s unchanged. A truncation note is appended so the caller
// knows the output was dehydrated. Mirrors the MCP page_fetch truncate.
func PageTruncate(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	// Guard maxBytes > 0 so the walk-back is total even for malformed UTF-8
	// input (a run of orphan continuation bytes could otherwise decrement
	// past zero and index b[-1], panicking).
	for maxBytes > 0 && len(b) > maxBytes && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes]) + "\n… (output truncated — raise --max-bytes or narrow the selector)"
}
