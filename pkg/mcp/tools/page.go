package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"sort"
	"strings"
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
//	network | console | article | marks | security | sinks | storage | har
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
// screenshot as an image block. Both bypass max_bytes truncation.
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
		"links, screenshot (a metadata text block {mime_type, width, height, " +
		"bytes, full_page} plus the PNG as a separate image content block with " +
		"mimeType, or the base64 data URI in data when the client advertises " +
		"imageContent=false), interactives (dense action map of every visible " +
		"clickable/typeable element — role, text, href, type, plus x/y as the " +
		"element centre and width/height in page-absolute CSS px; the top-left " +
		"is left = x - width/2, top = y - height/2, and marks reports top-left " +
		"instead, so the two differ. Each entry carries a stable handle usable " +
		"as page_act's handle arg; whole-page by design, selector is ignored; " +
		"capped at 200 with a truncated remainder count), a11y (flattened accessibility tree: computed " +
		"role/name/value/state flags plus depth, with handles on DOM-backed " +
		"nodes usable as page_act's handle; root frame only), network and " +
		"console (best-effort request and console logs as " +
		"{\"entries\":[...],\"count\",\"dropped\",\"truncated\"} holding the " +
		"newest 100 buffered entries; capture is cumulative from page creation, " +
		"so filter client-side or call page_act action clear_logs right before " +
		"the step whose traffic/logs you want to observe), article (the readable " +
		"article body as markdown with title, byline, excerpt, word count and " +
		"truncated flag), marks (the mark table as a text block plus the " +
		"annotated viewport PNG as an image block, with the data URI returned " +
		"in screenshot for a text-only client. Its x/y coordinates feed " +
		"page_act click_at), " +
		"security (a passive audit of response headers, CSP, cookie flags, mixed " +
		"content, subresource integrity and form targets, returned as " +
		"{\"checks\":[{\"id\",\"status\",\"evidence\",\"detail\"}],\"count\":N}), " +
		"sinks (source-to-sink tracer ring: {\"installed\",\"hits\":[{\"source\",\"sink\"," +
		"\"context\",\"value\",\"value_cut\",\"stack\",\"selector\",\"ts\"}],\"dropped\"," +
		"\"truncated\"}. It is read-only and never arms capture: capture starts only with " +
		"page_act action=sinks_install, survives client-side route changes and full navigations, " +
		"and its ring is per document, so a full navigation resets it. The newest 100 hits of a " +
		"200-hit ring are emitted, values are clipped at 200 runes and stacks keep 5 frames; " +
		"reset with page_act action=sinks_clear), " +
		"storage (read-only browser-context snapshot: cookies with flags plus " +
		"local_storage, session_storage, indexed_db and caches. Every section is " +
		"present even when empty and capped with its own *_truncated counter. " +
		"Every value is <redacted> unless include_values is true, which exposes " +
		"live session tokens. It never writes cookies), " +
		"har (HAR 1.2 export of the observed network ring: " +
		"{log:{version,creator,pages,entries[]}} where each entry carries request " +
		"and response headers, status, mimeType, size and timings. Cookie and " +
		"Set-Cookie are not captured, because Chrome omits them from CDP events; " +
		"Authorization and X-CSRF-Token values are <redacted>, and no argument " +
		"enables secret capture. The export is capped at 512 KiB, and " +
		"log.truncated counts only the entries dropped to fit that cap — entries " +
		"lost to the network ring's own bound are not counted yet and will be " +
		"reported separately after WEB-168. Read-only, never arms capture), " +
		"or meta. Pass the session_id " +
		"returned by fetch_page plus what=<aspect>; use selector to scope " +
		"extraction, target to fetch exactly one css/js resource's full content " +
		"(instead of the resource list), full_page to capture the whole " +
		"scrollable page in a screenshot (default: visible viewport; full-page " +
		"captures of tall/heavy pages get an extended internal budget of ~90s " +
		"and may still time out on extreme pages), and max_bytes to cap the " +
		"response size. A selector that matches nothing returns element_not_found naming the " +
		"selector. Each aspect is capped at a fixed 30s per-call timeout " +
		"(full-page screenshots ~90s); timeout_ms is not supported on this tool. " +
		"Example: { \"session_id\": \"page-1\", \"what\": \"dom\", \"max_bytes\": 8000 }." +
		resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for page_fetch's arguments: a required
// session_id and what, plus optional selector, target, max_bytes, full_page and
// include_values.
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
				"description": "The aspect of the page to extract: html (outerHTML), dom (flattened DOM snapshot; every element node carries a g<document>-b<backendNodeId> handle usable as page_act's handle arg; the document token makes handles from a previous document stale after navigation), text (visible text), css (stylesheet resources, including inline <style> blocks reported with type inline-style), js (script resources), links (anchor hrefs, each with a handle where one matched), screenshot (a metadata text block {mime_type, width, height, bytes, full_page} plus the PNG as a separate image block with mimeType. full_page captures the whole scrollable page. A client advertising imageContent=false gets the base64 data URI in data instead — e.g. what: \"screenshot\" → {\"ok\":true,\"data\":{\"mime_type\":\"image/png\",\"width\":1280,\"height\":800,\"bytes\":42133,\"full_page\":false}} plus an image block), interactives (dense action map of every visible clickable/typeable element — role, text, href, type, plus x/y as the element centre and width/height in page-absolute CSS px; the top-left is left = x - width/2, top = y - height/2, while marks reports top-left, so the two differ. Each entry carries a stable handle usable as page_act's handle arg; whole-page by design, selector is ignored; capped at 200 with a truncated remainder count), a11y (flattened accessibility tree in DFS order: computed role/name/value plus the state flags checked, expanded, selected, disabled, required, pressed, invalid, focused, and depth — e.g. what: \"a11y\" → {\"nodes\":[{\"role\":\"button\",\"name\":\"Sign in\",\"handle\":\"g3k9x2ab-b6\",…}],\"count\":N,\"truncated\":M}; DOM-backed nodes carry a handle usable as page_act's handle arg; root frame only — iframes are separate trees; selector/target/full_page are ignored), network/console (bounded log envelope: what: \"network\" → {\"entries\":[{\"method\":\"GET\",\"url\":\"…\",\"status\":200,…}],\"count\":N,\"dropped\":D,\"truncated\":M}; entries are the newest 100 buffered, oldest first, and dropped counts true ring overflow; capture is cumulative from page creation — filter client-side or call page_act action clear_logs right before the step under observation; selector/target/full_page are ignored), article (the readable article body as markdown: {title, byline?, excerpt?, text, words, truncated, url} — the densest article/main root with page chrome removed and headings, paragraphs, lists, code, quotes, links, images and tables rendered; text is capped at 40000 runes and truncated reports the cut; prefer it over text for prose — selector/target/full_page are ignored; e.g. what: \"article\" → {\"title\":\"Example\",\"byline\":\"Ada\",\"text\":\"# Heading …\",\"words\":812,\"truncated\":false,\"url\":\"https://example.com/a\"}; example: {\"session_id\":\"page-1\",\"what\":\"article\"}), marks (numbered set-of-marks: the text block is {marks:[{index, role, text?, x, y, width, height}], viewport_width, viewport_height, truncated} and the PNG is a separate image block with mimeType — one numbered box per visible interactive element; x/y/width/height are viewport CSS px and x/y can be passed straight to page_act click_at; capped at the first 60 marks with truncated counting the rest — selector/target/full_page are ignored; e.g. what: \"marks\" → text {\"marks\":[{\"index\":1,\"role\":\"a\",\"text\":\"Sign in\",\"x\":120.5,\"y\":48,\"width\":88,\"height\":32}],\"viewport_width\":1280,\"viewport_height\":800,\"truncated\":0} plus the annotated PNG image block. For a text-only client the data URI comes back in screenshot); example: {\"session_id\":\"page-1\",\"what\":\"marks\"}), security (passive audit: {checks:[{id,status,evidence,detail}],count} covering response headers, CSP (header or meta), cookie flags, mixed content, subresource integrity, form targets and CORS — selector/target/full_page are ignored; e.g. what: \"security\" → {\"checks\":[{\"id\":\"csp\",\"status\":\"warn\",\"evidence\":\"\",\"detail\":\"…\"}],\"count\":12}), sinks (source-to-sink tracer ring: {installed, hits:[{source, sink, context, value, value_cut, stack, selector, ts}], dropped, truncated}. It is read-only and never arms capture — capture starts only with page_act action=sinks_install and survives client-side route changes and full navigations; the ring is per document, so a full navigation resets it. The newest 100 hits of a 200-hit ring are emitted, each value is clipped at 200 runes (value_cut marks it) and each stack keeps 5 frames — selector/target/full_page are ignored; e.g. what: \"sinks\" → {\"installed\":true,\"hits\":[{\"source\":\"location.hash\",\"sink\":\"innerHTML\",\"context\":\"html\",\"value\":\"#canary\",\"stack\":[\"anonymous https://example.com/:12\"],\"selector\":\"#out\",\"ts\":1700000000000}],\"dropped\":0,\"truncated\":0}), meta (title/url/description), storage (read-only snapshot of the page's browser context: cookies with name, value, domain, path, secure, http_only, same_site, expires and size, plus local_storage and session_storage [{key, value_length, value}], indexed_db [{name, stores}] and caches [name]. Every section is present even when empty and capped at 200 cookies, 200 keys per store, 20 databases and 40 caches, each with its own *_truncated counter. Values are the literal <redacted> unless include_values=true, which exposes live tokens. It never writes cookies. selector/target/full_page are ignored. e.g. what: \"storage\" → {\"cookies\":[{\"name\":\"sid\",\"value\":\"<redacted>\",\"domain\":\"example.com\",\"path\":\"/\",\"secure\":true,\"http_only\":true,\"same_site\":\"Lax\",\"expires\":-1,\"size\":42}],\"local_storage\":[{\"key\":\"token\",\"value_length\":32,\"value\":\"<redacted>\"}],...}. A page_closed error means the session expired, re-fetch the page), har (HAR 1.2 export of the observed network ring: {log:{version,creator,pages,entries[]}} where each entry carries request and response headers, status, mimeType, size and timings. Cookie and Set-Cookie are not captured, because Chrome omits them from CDP events; Authorization and X-CSRF-Token values are <redacted>, and no argument enables secret capture. The export is capped at 512 KiB, and log.truncated counts only the entries dropped to fit that cap — entries lost to the network ring's own bound are not counted yet and will be reported separately after WEB-168. Read-only, never arms capture). article, marks, security, storage, sinks and har bypass max_bytes truncation.",
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
			"include_values": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "With what=storage, return real cookie and local_storage/session_storage values instead of the literal <redacted>. Default false. true exposes live session tokens, so set it only when the values themselves are the question. Example: { \"session_id\": \"page-1\", \"what\": \"storage\", \"include_values\": true }. If the report still shows <redacted>, include_values was not set on this call.",
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
	// IncludeValues exposes cookie and storage values instead of redacting
	// them (what=storage only).
	IncludeValues bool `json:"include_values"`
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
