package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// Fetch implements the "fetch_page" tool: it opens a URL in a new browser
// session and returns a compact page summary (title, url, session_id, size
// hints) so the agent can decide what to read next via page_fetch.
type Fetch struct {
	// mgr is the shared browsers.Manager that owns the single Browser and its
	// live pages (sessions). Every page opened here becomes usable by the
	// other tools.
	mgr *browsers.Manager
}

// NewFetch constructs the fetch_page tool wired to the given browsers.Manager.
func NewFetch(mgr *browsers.Manager) *Fetch {
	return &Fetch{mgr: mgr}
}

// Name returns the MCP tool name "fetch_page".
func (t *Fetch) Name() string {
	return "fetch_page"
}

// Description returns a customer/LLM-facing description of fetch_page.
func (t *Fetch) Description() string {
	return "Opens a URL in a new live browser session and returns a page " +
		"summary (title, url, session_id, byte-size hints). Call this FIRST to " +
		"load a page; then use page_fetch with the returned session_id to read " +
		"its html/dom/text/css/js/links/screenshot/meta, and page_act to " +
		"interact with it. Each fetch_page call creates a new session."
}

// InputSchema returns the JSON Schema for fetch_page's arguments: a required
// url, plus optional wait_for / viewport / user_agent / timeout_ms controls.
func (t *Fetch) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The absolute URL (http/https) to open in a new browser session.",
			},
			"wait_for": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "idle", "stable"},
				"default":     "idle",
				"description": "How long to wait before returning: 'load' = window.onload, 'idle' = no network requests for ~500ms, 'stable' = DOM unchanged for ~500ms. Defaults to 'idle'.",
			},
			"viewport": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels."},
					"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels."},
				},
				"description": "Optional viewport size for the session's page.",
			},
			"user_agent": map[string]any{
				"type":        "string",
				"description": "Optional User-Agent override for the session's page.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional per-call timeout in milliseconds (default 30000).",
			},
		},
		Required: []string{"url"},
	}
}

// fetchArgs is the strict decoding target for fetch_page arguments.
type fetchArgs struct {
	// URL is the absolute URL to open (required).
	URL string `json:"url"`
	// WaitFor is one of "load" | "idle" | "stable" (default "idle").
	WaitFor string `json:"wait_for"`
	// Viewport optionally overrides the page viewport size.
	Viewport *viewportConfig `json:"viewport"`
	// UserAgent optionally overrides the page's User-Agent header.
	UserAgent string `json:"user_agent"`
	// TimeoutMS bounds the whole call in milliseconds (default 30000).
	TimeoutMS int `json:"timeout_ms"`
}

// viewportConfig is the width/height pair carried by fetchArgs.Viewport.
type viewportConfig struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// validate checks fetchArgs for required fields and legal enum values. It is
// invoked by Execute before any browser work happens.
func (a *fetchArgs) validate() error {
	if a.URL == "" {
		return fmt.Errorf("missing required argument %q", "url")
	}
	switch a.WaitFor {
	case "", "load", "idle", "stable":
	default:
		return fmt.Errorf("invalid wait_for %q (must be one of load, idle, stable)", a.WaitFor)
	}
	if a.Viewport != nil && (a.Viewport.Width <= 0 || a.Viewport.Height <= 0) {
		return fmt.Errorf("viewport width and height must be positive")
	}
	if a.TimeoutMS < 0 {
		return fmt.Errorf("timeout_ms must be >= 0")
	}
	return nil
}

// Execute parses the fetch_page arguments, opens the URL in a new session and
// returns the page summary as a JSON string for the LLM.
func (t *Fetch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a fetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("fetch_page: invalid arguments: %w", err)
	}
	if err := a.validate(); err != nil {
		return "", fmt.Errorf("fetch_page: %w", err)
	}

	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   b, err := t.mgr.Open(ctx)                 // lazily launches the single Browser
	//   if err != nil { return "", fmt.Errorf("fetch_page: %w", err) }
	//   p, err := b.NewPage(ctx, a.URL)          // new tab; stealth applied by default
	//   if err != nil { return "", fmt.Errorf("fetch_page: %w", err) }
	//   if err := t.waitForPage(ctx, p, a.WaitFor); err != nil {
	//       return "", fmt.Errorf("fetch_page: %w", err)
	//   }
	//   return t.pageSummary(ctx, p)
	return "", fmt.Errorf("fetch_page: not implemented yet")
}

// pageSummary renders a compact, LLM-friendly JSON summary of a live page: the
// page ID, its title and URL (via p.Rod.Info()), plus a short text snippet
// with byte-size hints so the caller can decide what to fetch next.
func (t *Fetch) pageSummary(ctx context.Context, p *browsers.Page) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   p.Lock(); defer p.Unlock()
	//   info, err := p.Rod.Info()              // title + url
	//   html, err := p.Rod.HTML()              // for byte-size hint only
	//   text, err := p.Rod.Eval(`() => document.body.innerText.slice(0, 500)`)
	//   return json.Marshal({session_id, title, url, text_snippet, html_bytes, ...})
	return "", fmt.Errorf("fetch_page.pageSummary: not implemented")
}

// waitForPage blocks until the page satisfies the requested readiness level.
// It maps "load" to WaitLoad, "idle" to WaitRequestIdle, and "stable" to
// WaitDOMStable; an empty waitFor is treated as "idle".
func (t *Fetch) waitForPage(ctx context.Context, p *browsers.Page, waitFor string) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   p.Lock(); defer p.Unlock()
	//   switch waitFor {
	//   case "", "idle":
	//       wait := p.Rod.WaitRequestIdle(500*time.Millisecond, nil, nil, nil); wait()
	//   case "load":
	//       return p.Rod.WaitLoad()
	//   case "stable":
	//       return p.Rod.WaitDOMStable(500*time.Millisecond, 0.05)
	//   }
	return fmt.Errorf("fetch_page.waitForPage: not implemented")
}

// Ensure Fetch satisfies the Tool interface at compile time.
var _ Tool = (*Fetch)(nil)
