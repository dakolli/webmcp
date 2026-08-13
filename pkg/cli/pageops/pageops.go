// Package pageops holds the cobra-free page helpers shared by the web-cli
// commands (pkg/cli/cmd) and the IPC server (pkg/cli/ipc): fetching page
// summaries, extracting page aspects (html, text, links, meta), running JS
// (eval) and truncating output. The helpers own the per-page Lock/Unlock
// discipline required because rod Pages are not thread-safe (pkg/browsers
// session.go) — callers must not lock themselves.
package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
	"unicode/utf8"

	"webmcp/pkg/browsers"
)

// DefaultPageMaxBytes caps how much of any page fetch extraction is returned
// in one call (the CLI --max-bytes default and the IPC page.fetch default).
// Huge pages are dehydrated to a first slice; the caller can re-run with a
// narrower selector or a higher max_bytes.
const DefaultPageMaxBytes = 50_000

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

// ValidateWait checks that the wait strategy is one of idle, load, stable.
func ValidateWait(waitFor string) error {
	switch waitFor {
	case "idle", "load", "stable":
		return nil
	default:
		return fmt.Errorf("invalid --wait %q (must be one of idle, load, stable)", waitFor)
	}
}

// WaitForPage blocks until the page satisfies the requested readiness level.
// It maps "load" to WaitLoad, "idle" to WaitRequestIdle, and "stable" to
// WaitDOMStable. All rod page calls are serialized with the per-page lock.
func WaitForPage(ctx context.Context, p *browsers.Page, waitFor string) error {
	p.Lock()
	defer p.Unlock()

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
// deduplicated by href (first occurrence wins) — mirrors the MCP extractLinks
// shape.
func PageLinks(ctx context.Context, p *browsers.Page) (string, error) {
	p.Lock()
	defer p.Unlock()

	res, err := p.Rod.Context(ctx).Eval(
		`() => [...document.querySelectorAll('a[href]')].map(a => ({text: a.innerText.trim(), href: a.href}))`)
	if err != nil {
		return "", fmt.Errorf("evaluate links: %w", err)
	}

	var links []PageLink
	if err := res.Value.Unmarshal(&links); err != nil {
		return "", fmt.Errorf("decode links: %w", err)
	}

	// Dedupe by href, first occurrence wins (the JS Map alternative would do
	// the same; doing it Go-side keeps the JS simple).
	seen := make(map[string]bool, len(links))
	dedup := make([]PageLink, 0, len(links))
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

// PageMeta returns the page's meta info (title, url, meta tags) as a JSON
// object — mirrors the MCP extractMeta shape. Backed by p.Rod.Info() plus Eval
// collecting every <meta> tag.
func PageMeta(ctx context.Context, p *browsers.Page) (string, error) {
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

// PageEval runs a JavaScript function in the page — the primary escape hatch
// for any page manipulation — and returns the JSON result. Backed by
// p.Rod.Context(ctx).Eval(js); js must be a JS function definition, e.g.
// "() => document.title". Mirrors the Eval pattern proven in extractText.
func PageEval(ctx context.Context, p *browsers.Page, js string) (string, error) {
	p.Lock()
	defer p.Unlock()

	res, err := p.Rod.Context(ctx).Eval(js) // Eval only accepts a JS function definition
	if err != nil {
		return "", fmt.Errorf("evaluate js: %w", err)
	}
	return res.Value.JSON("", ""), nil // gson marshals to compact JSON text
}

// PageLink is one anchor link collected by PageLinks: its visible text and its
// absolute href.
type PageLink struct {
	// Text is the anchor's visible text, trimmed of surrounding whitespace.
	Text string `json:"text"`
	// Href is the anchor's absolute URL (resolved against the document base).
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
	for len(b) > maxBytes && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes]) + "\n… (output truncated — raise --max-bytes or narrow the selector)"
}
