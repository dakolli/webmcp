package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// defaultMaxBytes caps how much of any extraction is returned to the LLM in one
// call. Huge pages are dehydrated to a first slice; the agent can re-request
// with a narrower selector or a higher max_bytes.
const defaultMaxBytes = 50_000

// Page implements the "page_fetch" tool: it reads any aspect of a LIVE page
// that was previously opened with fetch_page. The session_id from fetch_page
// identifies which browser tab to read; what selects the aspect:
//
//	html | dom | text | css | js | links | screenshot | meta
//
// The "dom" output is the flattened Documents array + Strings table produced by
// CaptureDOMSnapshot and is meant to be an LLM-friendly total view of the page
// (iframes, shadow DOM and all). "css"/"js" enumerate the page's resource tree
// and fetch the matching resources. "screenshot" returns a base64 PNG.
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
		"links, screenshot (base64 PNG), or meta. Pass the session_id returned " +
		"by fetch_page plus what=<aspect>; use selector to scope extraction and " +
		"max_bytes to cap the response size."
}

// InputSchema returns the JSON Schema for page_fetch's arguments: a required
// session_id and what, plus optional selector and max_bytes.
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
				"description": "The aspect of the page to extract: html (outerHTML), dom (flattened DOM snapshot), text (visible text), css (style resources), js (script resources), links (anchor hrefs), screenshot (base64 PNG), meta (title/url/description).",
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

	b, err := t.mgr.Open(ctx) // lazily launches (or returns) the single Browser
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}
	p, err := b.Page(a.SessionID)
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
		out, err = t.extractCSS(ctx, p)
	case "js":
		out, err = t.extractJS(ctx, p)
	case "links":
		out, err = t.extractLinks(ctx, p)
	case "screenshot":
		out, err = t.extractScreenshot(ctx, p)
	case "meta":
		out, err = t.extractMeta(ctx, p)
	default:
		return "", fmt.Errorf("page_fetch: unknown what=%q (must be one of html, dom, text, css, js, links, screenshot, meta)", a.What)
	}
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}
	return t.truncate(out, a.MaxBytes), nil
}

// extractHTML returns the page's outerHTML, or of the element matched by
// selector when one is given. Backed by p.Rod.HTML() / element HTML().
func (t *Page) extractHTML(ctx context.Context, p *browsers.Page, selector string) (string, error) {
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

// extractText returns the visible text of the page (or of the element matched
// by selector). Backed by Eval over document.body.innerText / element.innerText.
func (t *Page) extractText(ctx context.Context, p *browsers.Page, selector string) (string, error) {
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

// extractLinks returns the page's anchor links as a JSON array of {text, href},
// deduplicated by href (first occurrence wins). Non-navigable hrefs
// (javascript:/mailto:/data:/tel:, empty, exactly "#") and text-less anchors
// are dropped; remaining hrefs are entity-decoded. Backed by Eval collecting
// document.querySelectorAll('a[href]').
func (t *Page) extractLinks(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()

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

// extractDOMSnapshot returns the page's full DOM as a flattened Documents array
// plus the shared Strings table (via p.Rod.CaptureDOMSnapshot()). This is the
// LLM-friendly total view of the page — it pierces iframes and shadow DOM.
func (t *Page) extractDOMSnapshot(ctx context.Context, p *browsers.Page) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   p.Lock(); defer p.Unlock()
	//   snap, err := p.Rod.CaptureDOMSnapshot()
	//   // snap.Documents[0] is the root; strings live in the shared Strings table.
	//   // Flatten both into a single JSON document for the LLM.
	return "", fmt.Errorf("page_fetch.extractDOMSnapshot: not implemented")
}

// extractCSS returns the URLs and contents of the page's stylesheet resources.
// Backed by proto.PageGetResourceTree enumeration + p.Rod.GetResource(url).
func (t *Page) extractCSS(ctx context.Context, p *browsers.Page) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   p.Lock(); defer p.Unlock()
	//   tree, err := p.Rod.Call(proto.PageGetResourceTree{})
	//   walk resources filtering type == Stylesheet, then GetResource each URL.
	return "", fmt.Errorf("page_fetch.extractCSS: not implemented")
}

// extractJS returns the URLs and contents of the page's script resources.
// Backed by proto.PageGetResourceTree enumeration + p.Rod.GetResource(url).
func (t *Page) extractJS(ctx context.Context, p *browsers.Page) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   p.Lock(); defer p.Unlock()
	//   tree, err := p.Rod.Call(proto.PageGetResourceTree{})
	//   walk resources filtering type == Script, then GetResource each URL.
	return "", fmt.Errorf("page_fetch.extractJS: not implemented")
}

// extractScreenshot returns a base64-encoded PNG screenshot of the visible
// viewport. Backed by p.Rod.Screenshot(false, nil).
func (t *Page) extractScreenshot(ctx context.Context, p *browsers.Page) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   p.Lock(); defer p.Unlock()
	//   b, err := p.Rod.Screenshot(false, nil)
	//   return base64.StdEncoding.EncodeToString(b)  // data URL for the LLM
	return "", fmt.Errorf("page_fetch.extractScreenshot: not implemented")
}

// extractMeta returns the page's meta info (title, url, meta tags) as a JSON
// object. Backed by p.Rod.Info() plus Eval collecting every <meta> tag.
func (t *Page) extractMeta(ctx context.Context, p *browsers.Page) (string, error) {
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
