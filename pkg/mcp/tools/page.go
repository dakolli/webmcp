package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// defaultMaxBytes caps how much of any extraction is returned to the LLM in one
// call. Huge pages are dehydrated to a first slice; the agent can re-request
// with a narrower selector or a higher max_bytes.
const defaultMaxBytes = 50_000

// defaultPageTimeout bounds the whole page_fetch call when the caller does not
// provide one: a hung page (or a wedged CDP connection) must not block the
// server. Every extraction inherits this bound through pageops.
const defaultPageTimeout = 30 * time.Second

// Page implements the "page_fetch" tool: it reads any aspect of a LIVE page
// that was previously opened with fetch_page. The session_id from fetch_page
// identifies which browser tab to read; what selects the aspect:
//
//	html | dom | text | css | js | links | screenshot | meta | interactives | a11y |
//	network | console | article | marks | security
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
		"viewport screenshot whose x/y coordinates feed page_act click_at), " +
		"security (a passive audit of response headers, CSP, cookie flags, mixed " +
		"content, subresource integrity and form targets, returned as " +
		"{\"checks\":[{\"id\",\"status\",\"evidence\",\"detail\"}],\"count\":N}), " +
		"or meta. Pass the session_id " +
		"returned by fetch_page plus what=<aspect>; use selector to scope " +
		"extraction, target to fetch exactly one css/js resource's full content " +
		"(instead of the resource list), full_page to capture the whole " +
		"scrollable page in a screenshot (default: visible viewport; full-page " +
		"captures of tall/heavy pages get an extended internal budget of ~90s " +
		"and may still time out on extreme pages), and max_bytes to cap the " +
		"response size. Each aspect is capped at a fixed 30s per-call timeout " +
		"(full-page screenshots ~90s); timeout_ms is not supported on this tool. " +
		"Example: { \"session_id\": \"page-1\", \"what\": \"dom\", \"max_bytes\": 8000 }." +
		resultEnvelopeDoc
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
				"enum":        append([]string(nil), aspectNames...),
				"description": "The aspect of the page to extract: html (outerHTML), dom (flattened DOM snapshot; every element node carries a g<document>-b<backendNodeId> handle usable as page_act's handle arg; the document token makes handles from a previous document stale after navigation), text (visible text), css (stylesheet resources, including inline <style> blocks reported with type inline-style), js (script resources), links (anchor hrefs, each with a handle where one matched), screenshot (base64 PNG data-URI; full_page captures the whole scrollable page), interactives (dense action map of every visible clickable/typeable element — with stable handles usable as page_act's handle arg; whole-page by design, the selector arg is ignored), a11y (flattened accessibility tree in DFS order: computed role/name/value plus the state flags checked, expanded, selected, disabled, required, pressed, invalid, focused, and depth — e.g. what: \"a11y\" → {\"nodes\":[{\"role\":\"button\",\"name\":\"Sign in\",\"handle\":\"g3k9x2ab-b6\",…}],\"count\":N,\"truncated\":M}; DOM-backed nodes carry a handle usable as page_act's handle arg; root frame only — iframes are separate trees; selector/target/full_page are ignored), network/console (bounded log envelope: what: \"network\" → {\"entries\":[{\"method\":\"GET\",\"url\":\"…\",\"status\":200,…}],\"count\":N,\"dropped\":D,\"truncated\":M}; entries are the newest 100 buffered, oldest first, and dropped counts true ring overflow; capture is cumulative from page creation — filter client-side or call page_act action clear_logs right before the step under observation; selector/target/full_page are ignored), article (the readable article body as markdown: {title, byline?, excerpt?, text, words, truncated, url} — the densest article/main root with page chrome removed and headings, paragraphs, lists, code, quotes, links, images and tables rendered; text is capped at 40000 runes and truncated reports the cut; prefer it over text for prose — selector/target/full_page are ignored; e.g. what: \"article\" → {\"title\":\"Example\",\"byline\":\"Ada\",\"text\":\"# Heading …\",\"words\":812,\"truncated\":false,\"url\":\"https://example.com/a\"}; example: {\"session_id\":\"page-1\",\"what\":\"article\"}), marks (numbered set-of-marks: {marks:[{index, role, text?, x, y, width, height}], screenshot, viewport_width, viewport_height, truncated} — one numbered box per visible interactive element plus the annotated viewport screenshot as a base64 PNG data-URI; x/y/width/height are viewport CSS px and x/y can be passed straight to page_act click_at; capped at the first 60 marks with truncated counting the rest — selector/target/full_page are ignored; e.g. what: \"marks\" → {\"marks\":[{\"index\":1,\"role\":\"a\",\"text\":\"Sign in\",\"x\":120.5,\"y\":48,\"width\":88,\"height\":32}],\"screenshot\":\"data:image/png;base64,…\",\"viewport_width\":1280,\"viewport_height\":800,\"truncated\":0}; example: {\"session_id\":\"page-1\",\"what\":\"marks\"}), security (passive audit: {checks:[{id,status,evidence,detail}],count} covering response headers, CSP (header or meta), cookie flags, mixed content, subresource integrity, form targets and CORS — selector/target/full_page are ignored; e.g. what: \"security\" → {\"checks\":[{\"id\":\"csp\",\"status\":\"warn\",\"evidence\":\"\",\"detail\":\"…\"}],\"count\":12}), meta (title/url/description). article, marks and security bypass max_bytes truncation.",
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

// Execute parses the page_fetch arguments, resolves the session, dispatches to
// the registered aspect and applies that aspect's truncation policy. Aspects
// that emit a data-URI or a self-truncating JSON envelope bypass max_bytes;
// dom, links and meta keep their JSON parseable when cut, and plain text is cut
// on a rune boundary.
func (t *Page) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a pageFetchArgs
	if err := decodeArgs("page_fetch", args, &a); err != nil {
		return failResult(err)
	}
	if a.SessionID == "" {
		return failResult(fmt.Errorf("page_fetch: missing required argument %q", "session_id"))
	}
	if a.What == "" {
		return failResult(fmt.Errorf("page_fetch: missing required argument %q", "what"))
	}
	if a.MaxBytes <= 0 {
		a.MaxBytes = defaultMaxBytes
	}

	ctx, cancel := context.WithTimeout(ctx, defaultPageTimeout)
	defer cancel()

	p, err := t.mgr.Page(ctx, a.SessionID)
	if err != nil {
		return failResult(fmt.Errorf("page_fetch: %w", err))
	}

	asp, ok := aspects[a.What]
	if !ok {
		return failResult(fmt.Errorf(
			"page_fetch: unknown what=%q (must be one of %s)",
			a.What,
			validAspects(),
		))
	}

	out, err := asp.read(ctx, p, a)
	if err != nil {
		return failResultForPage(ctx, p, fmt.Errorf("page_fetch: %w", err))
	}

	switch asp.trunc {
	case truncationNone:
		return okResult(out)
	case truncationJSON:
		return okResult(truncateJSON(out, a.MaxBytes))
	default:
		return okResult(t.truncate(out, a.MaxBytes))
	}
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
