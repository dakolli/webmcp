package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/browsers"
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
//	html | dom | text | css | js | links | screenshot | meta
//
// The "dom" output is the page's DOM re-nested from the flattened Documents
// array + Strings table produced by CaptureDOMSnapshot into a compact,
// LLM-friendly tree (iframes, shadow DOM and all). "css"/"js" enumerate the
// page's resource tree and fetch the matching resources. "screenshot" returns
// a base64 PNG data-URI.
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
		"links, screenshot (base64 PNG data-URI), or meta. Pass the session_id " +
		"returned by fetch_page plus what=<aspect>; use selector to scope " +
		"extraction, target to fetch exactly one css/js resource's full content " +
		"(instead of the resource list), full_page to capture the whole " +
		"scrollable page in a screenshot (default: visible viewport; full-page " +
		"captures of tall/heavy pages get an extended internal budget of ~90s " +
		"and may still time out on extreme pages), and max_bytes to cap the " +
		"response size."
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
				"enum":        []string{"html", "dom", "text", "css", "js", "links", "screenshot", "meta"},
				"description": "The aspect of the page to extract: html (outerHTML), dom (flattened DOM snapshot), text (visible text), css (style resources), js (script resources), links (anchor hrefs), screenshot (base64 PNG data-URI; full_page captures the whole scrollable page), meta (title/url/description).",
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
	default:
		return "", fmt.Errorf("page_fetch: unknown what=%q (must be one of html, dom, text, css, js, links, screenshot, meta)", a.What)
	}
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}
	// Screenshots bypass truncate: the data-URI is binary-as-text and cutting
	// it mid-stream would produce an undecodable image. Image size scales with
	// the viewport (or full-page height), not max_bytes — the 50KB truncation
	// convention applies to text extractions only.
	if a.What == "screenshot" {
		return out, nil
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

// extractLinks returns the page's anchor links as a JSON array of {text, href},
// deduplicated by href (first occurrence wins). Non-navigable hrefs
// (javascript:/mailto:/data:/tel:, empty, exactly "#") and text-less anchors
// are dropped; remaining hrefs are entity-decoded. Backed by Eval collecting
// document.querySelectorAll('a[href]').
func (t *Page) extractLinks(ctx context.Context, p *browsers.Page) (string, error) {
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

	var links []linkEntry
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
	dedup := make([]linkEntry, 0, len(links))
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

// extractDOMSnapshot returns the page's DOM as a compact, LLM-friendly JSON
// tree. CaptureDOMSnapshot yields a flattened Documents array (index 0 = root
// document, later indexes = iframe/imported documents) plus a shared Strings
// table; this helper re-nests each document's parallel-array node tables by
// ParentIndex and resolves every string index through the table, so the agent
// reads a real tree (iframes, template contents and flattened shadow DOM
// included) instead of raw CDP arrays. Backed by p.Rod.CaptureDOMSnapshot().
func (t *Page) extractDOMSnapshot(ctx context.Context, p *browsers.Page) (string, error) {
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
// available via what=js/css instead).
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
		// the bodies of script/style elements (sources via what=js/css).
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

// linkEntry is one anchor link collected by extractLinks: its text (the
// anchor's own text or its accessible name) and its absolute href.
type linkEntry struct {
	// Text is the anchor's whitespace-normalized text, or its accessible name
	// when it has no text (fallback chain: aria-label → title → img[alt]).
	// Never empty — text-less anchors are filtered out.
	Text string `json:"text"`
	// Href is the anchor's absolute, entity-decoded URL (resolved against the
	// document base).
	Href string `json:"href"`
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
// maxBytes returns s unchanged. A truncation note is appended so the caller
// knows the output was dehydrated.
func (t *Page) truncate(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	for len(b) > maxBytes && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes]) + "\n… (output truncated — raise max_bytes or narrow the selector)"
}

// Ensure Page satisfies the Tool interface at compile time.
var _ Tool = (*Page)(nil)
