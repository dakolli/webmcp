package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

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
	// sessions is the shared SessionManager owning the live browser sessions.
	sessions *SessionManager
}

// NewPage constructs the page_fetch tool wired to the given SessionManager.
func NewPage(sessions *SessionManager) *Page {
	return &Page{sessions: sessions}
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
	// SessionID identifies the live session to read (required).
	SessionID SessionID `json:"session_id"`
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

	s, err := t.sessions.Get(ctx, a.SessionID)
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}

	var out string
	switch a.What {
	case "html":
		out, err = t.extractHTML(ctx, s, a.Selector)
	case "dom":
		out, err = t.extractDOMSnapshot(ctx, s)
	case "text":
		out, err = t.extractText(ctx, s, a.Selector)
	case "css":
		out, err = t.extractCSS(ctx, s)
	case "js":
		out, err = t.extractJS(ctx, s)
	case "links":
		out, err = t.extractLinks(ctx, s)
	case "screenshot":
		out, err = t.extractScreenshot(ctx, s)
	case "meta":
		out, err = t.extractMeta(ctx, s)
	default:
		return "", fmt.Errorf("page_fetch: unknown what=%q (must be one of html, dom, text, css, js, links, screenshot, meta)", a.What)
	}
	if err != nil {
		return "", fmt.Errorf("page_fetch: %w", err)
	}
	return t.truncate(out, a.MaxBytes), nil
}

// extractHTML returns the page's outerHTML, or of the element matched by
// selector when one is given. Backed by s.Page.HTML() / element HTML().
func (t *Page) extractHTML(ctx context.Context, s *Session, selector string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   if selector == "" { return s.Page.HTML() }
	//   el, err := s.Page.Element(selector)
	//   return el.HTML()
	return "", fmt.Errorf("page_fetch.extractHTML: not implemented")
}

// extractText returns the visible text of the page (or of the element matched
// by selector). Backed by Eval over document.body.innerText / element.innerText.
func (t *Page) extractText(ctx context.Context, s *Session, selector string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   js := `() => document.body.innerText`
	//   if selector != "" { js = `() => document.querySelector(` + JSON-quoted selector + `).innerText` }
	//   res, err := s.Page.Eval(js)
	//   return res.Str(), nil   // gson .Str()
	return "", fmt.Errorf("page_fetch.extractText: not implemented")
}

// extractLinks returns the page's anchor links as a JSON array of {text, href}.
// Backed by Eval collecting document.querySelectorAll('a[href]').
func (t *Page) extractLinks(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   res, err := s.Page.Eval(
	//     `() => [...document.querySelectorAll('a[href]')].map(a => ({text: a.innerText.trim(), href: a.href}))`)
	//   return res.JSON string
	return "", fmt.Errorf("page_fetch.extractLinks: not implemented")
}

// extractDOMSnapshot returns the page's full DOM as a flattened Documents array
// plus the shared Strings table (via s.Page.CaptureDOMSnapshot()). This is the
// LLM-friendly total view of the page — it pierces iframes and shadow DOM.
func (t *Page) extractDOMSnapshot(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   s.Lock(); defer s.Unlock()
	//   snap, err := s.Page.CaptureDOMSnapshot()
	//   // snap.Documents[0] is the root; strings live in the shared Strings table.
	//   // Flatten both into a single JSON document for the LLM.
	return "", fmt.Errorf("page_fetch.extractDOMSnapshot: not implemented")
}

// extractCSS returns the URLs and contents of the page's stylesheet resources.
// Backed by proto.PageGetResourceTree enumeration + s.Page.GetResource(url).
func (t *Page) extractCSS(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   s.Lock(); defer s.Unlock()
	//   tree, err := s.Page.Call(proto.PageGetResourceTree{})
	//   walk resources filtering type == Stylesheet, then GetResource each URL.
	return "", fmt.Errorf("page_fetch.extractCSS: not implemented")
}

// extractJS returns the URLs and contents of the page's script resources.
// Backed by proto.PageGetResourceTree enumeration + s.Page.GetResource(url).
func (t *Page) extractJS(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   s.Lock(); defer s.Unlock()
	//   tree, err := s.Page.Call(proto.PageGetResourceTree{})
	//   walk resources filtering type == Script, then GetResource each URL.
	return "", fmt.Errorf("page_fetch.extractJS: not implemented")
}

// extractScreenshot returns a base64-encoded PNG screenshot of the visible
// viewport. Backed by s.Page.Screenshot(false, nil).
func (t *Page) extractScreenshot(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 2):
	//   s.Lock(); defer s.Unlock()
	//   b, err := s.Page.Screenshot(false, nil)
	//   return base64.StdEncoding.EncodeToString(b)  // data URL for the LLM
	return "", fmt.Errorf("page_fetch.extractScreenshot: not implemented")
}

// extractMeta returns the page's meta info (title, url, description) as JSON.
// Backed by s.Page.Info() plus Eval for meta description/tags.
func (t *Page) extractMeta(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   info, err := s.Page.Info()
	//   res, err := s.Page.Eval(`() => document.querySelector('meta[name="description"]')?.content || ''`)
	//   return JSON {title: info.Title, url: info.URL, description: res.Str()}
	return "", fmt.Errorf("page_fetch.extractMeta: not implemented")
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
