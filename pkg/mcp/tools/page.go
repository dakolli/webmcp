package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/pageops"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

const (
	// defaultPageTimeout bounds the whole page_fetch call when the caller does
	// not provide one: a hung page (or a wedged CDP connection) must not block
	// the server. Every extraction inherits this bound through pageops.
	defaultPageTimeout = 30 * time.Second
	// maxPageTimeoutMS caps timeout_ms so a caller cannot pin a page call open
	// indefinitely. The IPC page.fetch budget is 180s, leaving headroom.
	maxPageTimeoutMS = 120_000
)

// Page implements the "page_fetch" tool: it reads any aspect of a LIVE page
// that was previously opened with fetch_page. The session_id from fetch_page
// identifies which browser tab to read; what selects the aspect:
//
//	html | dom | text | css | js | links | screenshot | meta | interactives | a11y |
//	network | console | article | marks | security | sinks | postmessage | storage |
//	dialogs | har | mutations
//
// The "dom" output is the page's DOM re-nested from the flattened Documents
// array + Strings table produced by CaptureDOMSnapshot into a compact,
// LLM-friendly tree (iframes, shadow DOM and all). "css"/"js" enumerate the
// page's resource tree and fetch the matching resources. "screenshot" returns
// the PNG as an image content block plus a metadata text block. "interactives"
// returns a dense action map of every visible, non-inert clickable/typeable
// element (with stable handles for page_act). "a11y" returns the flattened
// accessibility tree — computed roles, accessible names, values and state flags
// — with handles on DOM-backed nodes. "article" returns the readable body as
// markdown. "marks" returns the numbered mark table as text plus the annotated
// screenshot as an image block. Both bypass max_bytes truncation. "mutations"
// returns the page's DOM mutation timeline, which is always captured; bracket an
// action with a read before it and a read with since=<last_seq> after it.
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
	return models.PageFetchDescription
}

// InputSchema returns the JSON Schema for page_fetch's arguments: a required
// session_id and what, plus optional selector, target, max_bytes, timeout_ms,
// full_page and include_values.
func (t *Page) InputSchema() models.InputSchema {
	return newPageInputSchema()
}

// pageFetchArgs is the strict decoding target for page_fetch arguments.
type pageFetchArgs struct {
	// SessionID identifies the live page/session to read (required).
	SessionID browsers.PageID `json:"session_id"`
	// BrowserID optionally pins the lookup to one browser.
	BrowserID string `json:"browser_id"`
	// Owner optionally restricts the lookup to browsers with that owner.
	Owner string `json:"owner"`
	// What is the aspect to extract (required).
	What string `json:"what"`
	// Selector optionally scopes the extraction to a matching element.
	Selector string `json:"selector"`
	// MaxBytes caps the response size (default pageops.DefaultPageMaxBytes).
	MaxBytes int `json:"max_bytes"`
	// TimeoutMS bounds the whole extraction in milliseconds (absent or
	// non-positive → 30000, clamped to 1..120000).
	TimeoutMS int `json:"timeout_ms"`
	// FullPage captures the entire scrollable page instead of the visible
	// viewport (screenshot only; zero value = viewport-only capture).
	FullPage bool `json:"full_page"`
	// Target is a resource URL that, when set, makes what=css|js fetch exactly
	// that one resource's content instead of listing the page's resources.
	Target string `json:"target"`
	// IncludeValues exposes cookie and storage values instead of redacting
	// them (what=storage only).
	IncludeValues bool `json:"include_values"`
	// Filter narrows what=network by request type and what=sinks by canary or
	// known source; other aspects ignore it.
	Filter string `json:"filter"`
	// Since keeps only mutations entries newer than this sequence (what=mutations
	// only).
	Since int64 `json:"since"`
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
		a.MaxBytes = pageops.DefaultPageMaxBytes
	}
	a.TimeoutMS = clampTimeoutMS(a.TimeoutMS)
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p, err := t.mgr.Page(ctx, a.SessionID, browsers.BrowserID(a.BrowserID), a.Owner)
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

	out, err := readAspect(ctx, p, a, asp)
	if err != nil {
		budget := aspectBudget(a, timeout)
		timedOut := errors.Is(err, context.DeadlineExceeded)
		if !timedOut && budget <= timeout {
			timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		}
		if timedOut {
			return failResultForPage(ctx, p, timeoutError(a, budget, err))
		}
		return failResultForPage(ctx, p, fmt.Errorf("page_fetch: %w", err))
	}

	switch a.What {
	case "screenshot":
		return screenshotCarrier(out, a.FullPage)
	case "marks":
		return marksCarrier(out)
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

// clampTimeoutMS normalizes the caller's timeout_ms: an absent value (0 or
// negative) becomes the 30s default and a supplied value is clamped to
// 1..120000.
func clampTimeoutMS(ms int) int {
	if ms <= 0 {
		return int(defaultPageTimeout / time.Millisecond)
	}
	if ms > maxPageTimeoutMS {
		return maxPageTimeoutMS
	}
	return ms
}

// readAspect dispatches one aspect read. A full-page screenshot keeps the
// registry's 90s capture floor, unless the caller asked for longer, in which
// case the caller's budget governs the capture.
func readAspect(
	ctx context.Context,
	p *browsers.Page,
	a pageFetchArgs,
	asp aspectEntry,
) (string, error) {
	if a.What == "screenshot" && a.FullPage &&
		time.Duration(a.TimeoutMS)*time.Millisecond > fullPageScreenshotTimeout {
		return pageops.PageScreenshot(ctx, p, true)
	}
	return asp.read(ctx, p, a)
}

// aspectBudget is the effective deadline an aspect ran under, for the timeout
// message: a full-page screenshot never gets less than its 90s capture floor.
func aspectBudget(a pageFetchArgs, timeout time.Duration) time.Duration {
	if a.What == "screenshot" && a.FullPage && timeout < fullPageScreenshotTimeout {
		return fullPageScreenshotTimeout
	}
	return timeout
}

// timeoutError names the aspect, the effective budget and the caller's
// timeout_ms so a timeout failure is actionable without guessing.
func timeoutError(a pageFetchArgs, budget time.Duration, err error) error {
	return fmt.Errorf(
		"page_fetch: what=%q timed out after %s (timeout_ms=%d, max %d) — raise timeout_ms, narrow the selector, or the page is wedged: %w",
		a.What, budget, a.TimeoutMS, maxPageTimeoutMS, err,
	)
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

// imageCarrier is page_fetch's internal success result for a screenshot-bearing
// aspect: the standard result envelope, the PNG as an MCP image block, and the
// rule that exposes its data URI to a text-only client. It never reaches the
// wire; the MCP server lifts Image into a content block and sends one
// representation of the bytes.
type imageCarrier struct {
	resultEnvelope
	Image    *models.ContentBlock `json:"image,omitempty"`
	Fallback imageFallback        `json:"fallback,omitempty"`
}

// imageFallback tells the server how to expose the base64 data URI when the
// client cannot render image blocks.
type imageFallback struct {
	AsDataURI bool   `json:"as_data_uri,omitempty"`
	Field     string `json:"field,omitempty"`
}

// screenshotCarrier splits a screenshot data URI into an image block and a
// compact metadata text envelope (MIME type, bytes, pixel size, full_page). The
// data URI is removed from the text so an image-capable client receives the
// bytes exactly once. A non-data-URI capture is returned as plain text.
func screenshotCarrier(dataURI string, fullPage bool) (string, error) {
	mime, raw, ok := decodeDataURI(dataURI)
	if !ok {
		return okResult(dataURI)
	}
	meta := map[string]any{
		"mime_type": mime,
		"bytes":     len(raw),
		"full_page": fullPage,
	}
	if w, h, err := imageSize(raw); err == nil {
		meta["width"] = w
		meta["height"] = h
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encode screenshot metadata: %w", err)
	}
	return marshalImageCarrier(imageCarrier{
		resultEnvelope: resultEnvelope{OK: true, Data: data},
		Image: &models.ContentBlock{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(raw),
			MimeType: mime,
		},
		Fallback: imageFallback{AsDataURI: true},
	})
}

// marksCarrier splits the annotated screenshot out of the marks JSON into an
// image block and keeps the mark table in the text envelope. The screenshot
// field is removed from the text and restored only for a text-only client.
func marksCarrier(out string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		return okResult(out)
	}
	rawURI, ok := fields["screenshot"]
	if !ok {
		return okResult(out)
	}
	var uri string
	if err := json.Unmarshal(rawURI, &uri); err != nil {
		return okResult(out)
	}
	mime, raw, ok := decodeDataURI(uri)
	if !ok {
		return okResult(out)
	}
	delete(fields, "screenshot")
	data, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encode marks table: %w", err)
	}
	return marshalImageCarrier(imageCarrier{
		resultEnvelope: resultEnvelope{OK: true, Data: data},
		Image: &models.ContentBlock{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(raw),
			MimeType: mime,
		},
		Fallback: imageFallback{Field: "screenshot"},
	})
}

// marshalImageCarrier encodes one image carrier as its internal JSON transport.
func marshalImageCarrier(c imageCarrier) (string, error) {
	out, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode image result: %w", err)
	}
	return string(out), nil
}

// decodeDataURI splits a "data:<mime>;base64,<payload>" URI into its MIME type
// and decoded bytes.
func decodeDataURI(uri string) (string, []byte, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", nil, false
	}
	rest := uri[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 || !strings.HasSuffix(rest[:comma], ";base64") {
		return "", nil, false
	}
	mime := strings.TrimSuffix(rest[:comma], ";base64")
	raw, err := base64.StdEncoding.DecodeString(rest[comma+1:])
	if err != nil {
		return "", nil, false
	}
	return mime, raw, true
}

// imageSize reads the pixel dimensions from an encoded image header.
func imageSize(raw []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image size: %w", err)
	}
	return cfg.Width, cfg.Height, nil
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
	return models.InspectPageDecompositionPromptDescription
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
