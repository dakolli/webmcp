package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// defaultFetchTimeout bounds the whole fetch_page tool call when the caller
// does not provide timeout_ms: a hung page must not block the server forever.
const defaultFetchTimeout = 30 * time.Second

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
				"description": "Optional MANUAL viewport size for the session's page (an explicit option, never a default — pages launch plain with rod's own defaults; applied per-page via emulation incl. matching screen metrics). When both device and viewport are given, device wins.",
			},
			"device": map[string]any{
				"type":        "string",
				"description": "Optional go-rod device preset to emulate on the new page (the primary, opt-in emulation mechanism): a preset name or Title, case-insensitive, e.g. IPhoneX, iphone x, IPadPro, LaptopWithHiDPIScreen; 'clear' resets emulation. Applied BEFORE navigation, so the device UA + metrics govern the load itself (no re-navigate needed). When both device and viewport are given, device wins.",
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
	// Viewport optionally overrides the page viewport size via a MANUAL
	// SetViewport (nil = plain launch with rod's own defaults — no default
	// size is forced). Ignored when Device is set (device wins).
	Viewport *browsers.Viewport `json:"viewport"`
	// Device optionally applies a go-rod device preset (lib/devices) to the
	// new page BEFORE navigation via Page.Emulate — the primary, opt-in
	// emulation mechanism, so the device UA + metrics govern the load itself.
	// The name is case-insensitive on the preset Go identifier or its Title
	// (e.g. "IPhoneX" / "iphone x"); "clear" resets emulation. When both
	// Device and Viewport are given, Device wins.
	Device string `json:"device"`
	// UserAgent optionally overrides the page's User-Agent header, applied
	// BEFORE navigation (governs the load itself; wins over a device preset's
	// UA when both are given).
	UserAgent string `json:"user_agent"`
	// TimeoutMS bounds the whole call in milliseconds (default 30000).
	TimeoutMS int `json:"timeout_ms"`
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
	if a.Device != "" {
		// Engine-owned lookup so the tool layer never touches rod: the exact
		// pinned error (unknown device %q ...) surfaces verbatim, prefixed by
		// the caller ("fetch_page: ...").
		if err := browsers.ValidateDeviceName(a.Device); err != nil {
			return err
		}
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

	// Enforce an explicit per-call timeout so a hung page (or a first-run
	// Chromium download) cannot block the server: default 30s, honoring
	// timeout_ms from the caller. The derived ctx flows through Open, NewPage
	// and every wait below.
	timeout := defaultFetchTimeout
	if a.TimeoutMS > 0 {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now() // load timing for the summary

	b, err := t.mgr.Open(ctx) // lazily launches the single Browser
	if err != nil {
		return "", fmt.Errorf("fetch_page: %w", err)
	}
	// Device wins over a manual viewport when both are given (user decision
	// 2026-08-14): a device preset is the primary emulation mechanism, so the
	// page opens with the preset applied pre-navigation by the engine (NewPage
	// applies viewport → device → UA before Navigate) — the device UA + metrics
	// govern the load itself, no re-navigate needed.
	vp := a.Viewport
	if a.Device != "" {
		vp = nil
	}
	// NewPage applies the device preset and UA override BEFORE navigating; the
	// manual viewport only when no device was requested.
	p, err := b.NewPage(ctx, a.URL, vp, a.Device, a.UserAgent) // new tab; stealth applied by default
	if err != nil {
		return "", fmt.Errorf("fetch_page: %w", err)
	}
	if err := waitForPage(ctx, p, a.WaitFor); err != nil {
		return "", fmt.Errorf("fetch_page: %w", err)
	}
	return t.pageSummary(ctx, p, start)
}

// pageSummaryResult is the JSON shape returned by fetch_page. It carries the
// session_id the caller must pass to page_fetch/page_act, page metadata, and
// compact size/timing hints — never the full HTML (first-50KB rule).
type pageSummaryResult struct {
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	TextSnippet string `json:"text_snippet"`
	HTMLBytes   int    `json:"html_bytes"`
	LoadMs      int64  `json:"load_ms"`
}

// pageSummary renders a compact, LLM-friendly JSON summary of a live page: the
// page ID, its title and URL (via p.Rod.Info()), plus a short text snippet
// with byte-size hints so the caller can decide what to fetch next. loadStarted
// anchors the load_ms timing (captured around launch → navigate → wait).
func (t *Fetch) pageSummary(ctx context.Context, p *browsers.Page, loadStarted time.Time) (string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	// Bound each rod call by ctx so a stuck page cannot outlive the tool call.
	info, err := p.Rod.Context(ctx).Info() // title + url
	if err != nil {
		return "", fmt.Errorf("fetch_page: page info: %w", err)
	}

	html, err := p.Rod.Context(ctx).HTML() // byte-size hint only; not shipped
	if err != nil {
		return "", fmt.Errorf("fetch_page: read html: %w", err)
	}

	text, err := p.Rod.Context(ctx).Eval(`() => document.body.innerText.slice(0, 500)`)
	if err != nil {
		return "", fmt.Errorf("fetch_page: read text: %w", err)
	}

	summary := pageSummaryResult{
		SessionID:   string(p.ID),
		Title:       info.Title,
		URL:         info.URL,
		TextSnippet: text.Value.Str(),
		HTMLBytes:   len(html),
		LoadMs:      time.Since(loadStarted).Milliseconds(),
	}
	out, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("fetch_page: marshal summary: %w", err)
	}
	return string(out), nil
}

// Ensure Fetch satisfies the Tool interface at compile time.
var _ Tool = (*Fetch)(nil)
