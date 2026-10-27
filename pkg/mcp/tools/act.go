package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/pageops"
	"webmcp/pkg/mcp/models"
)

// defaultActTimeout bounds every page_act helper so a missing element (sleeper
// retries) or a wedged page cannot block the server.
const defaultActTimeout = 30 * time.Second

// waitSleepMargin gives the wait action's "ms" sleep room to complete: the
// sleep is bounded by timeout_ms, but the tool-call deadline would otherwise
// be set to the same duration and fire first.
const waitSleepMargin = 5 * time.Second

// actActionNames lists page_act's actions in schema order. The schema enum and
// the unknown-action error both read it so the two cannot drift.
var actActionNames = []string{
	"click", "input", "type", "press_key", "click_at", "hover_at",
	"drag_and_drop", "upload_file", "eval", "navigate",
	"back", "forward", "reload", "hover", "scroll", "select",
	"wait", "set_viewport", "emulate", "clear_logs",
	"sinks_install", "sinks_clear", "replay", "inject_canary",
	"postmessage_install", "postmessage_clear", "post_message",
	"wait_open", "intercept_add", "intercept_list", "intercept_clear",
	"auth_save", "auth_restore", "auth_list",
}

// actActionList renders actActionNames for an error message.
func actActionList() string {
	return strings.Join(actActionNames, ", ")
}

// Act implements the "page_act" tool: it interacts with a LIVE page that was
// previously opened with fetch_page. Interaction logic is backed by package
// pageops, shared byte-for-byte with the CLI and IPC server.
type Act struct {
	// mgr is the shared browsers.Manager owning the single Browser and its
	// live pages (sessions).
	mgr *browsers.Manager
}

// NewAct constructs the page_act tool wired to the given browsers.Manager.
func NewAct(mgr *browsers.Manager) *Act {
	return &Act{mgr: mgr}
}

// Name returns the MCP tool name "page_act".
func (t *Act) Name() string {
	return "page_act"
}

// Description returns a customer/LLM-facing description of page_act.
func (t *Act) Description() string {
	return "Interacts with a live page opened by fetch_page: click (supports button selection & double-click), " +
		"input, type, press_key (send keys/combos like Enter, Escape, Control+A, Meta+C), " +
		"click_at / hover_at (coordinate-based mouse actions), drag_and_drop (mouse drag), " +
		"upload_file (set files on <input type='file'>), eval (run JS — the primary escape hatch), " +
		"navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, " +
		"clear_logs (empties the network, console and websocket buffers, returning the cleared counts, e.g. " +
		"page_act { action: \"clear_logs\" } → {\"network\":12,\"console\":5,\"websocket\":0}), " +
		"sinks_install (arms the source-to-sink tracer and returns {\"installed\":true}; capture is " +
		"opt-in and never starts by itself because instrumentation changes page behavior. The " +
		"wrappers survive client-side route changes and full navigations, but the hit ring is per " +
		"document and resets after a full navigation. Read it with page_fetch what=sinks, which is " +
		"read-only and never arms capture; it emits the newest 100 hits of a 200-hit ring), " +
		"sinks_clear (empties the tracer ring and returns {\"cleared\":N}), or " +
		"replay (resends one captured network entry through a Go HTTP client — bypasses CORS, " +
		"carries the page's cookies for the target host without writing any back, follows up to 5 " +
		"redirects, and returns {status,headers,body,bytes,truncated,body_redactions,ms,request,chain} " +
		"with sensitive response headers and text-body secrets (cookies, authorization, session ids, " +
		"bearer tokens) replaced by <redacted>; body_redactions counts them, max_bytes caps the body " +
		"at a 256 KB ceiling, and truncated/chain are always present), or " +
		"inject_canary (proves sink reachability: injects a unique wm-canary-<8 hex> value into " +
		"query, hash, postmessage and name — the default set, or pass targets to narrow it — and " +
		"returns {canary, injected:[{target, where}], hits:[{sink, context, selector, value, stack}], " +
		"installed}. query reloads the page, hash does not, postmessage posts a wm-probe message to " +
		"every frame, name writes window.name. Work is capped at 20 frames and each frame is reported " +
		"with its URL in injected.where. Requires sinks_install first), " +
		"postmessage_install (arm the postMessage recorder: one capture-phase listener per window and " +
		"iframe, without wrapping addEventListener, so a page listener keeps its identity; no " +
		"parameters; returns {\"installed\":true}; read the ring with page_fetch what=postmessage), " +
		"postmessage_clear (empty every recorder ring; no parameters; returns {\"cleared\":N}), or " +
		"post_message (deliver value to window.top, window.self or window.parent when " +
		"target_selector is \"top\", \"self\", \"parent\" or omitted, or to the contentWindow of " +
		"the <iframe>/<frame> matched by any other target_selector; a selector that is not a frame " +
		"returns an error instead of falling back to top; origin is the target origin, default " +
		"\"*\"; it arms the recorder first so the delivery can be read back, and returns " +
		"{origin,target}. A value that is a complete JSON object or array is delivered as that " +
		"structure, anything else as a string), or " +
		"wait_open (block until this page opens a popup or new tab — a target=\"_blank\" link " +
		"or window.open — and return {session_id,url,opener} for the new page; a popup opened " +
		"before the call is returned immediately, each popup is reported once, and timeout_ms " +
		"bounds the wait. Use the returned session_id with page_fetch/page_act), " +
		"intercept_add (arm a page-scoped in-flight request interceptor; rules is an array of " +
		"{id, url_pattern, method, action, headers, status, body, delay_ms} where action is " +
		"block, allow, rewrite or fulfill, url_pattern is a wildcard glob like *api*, and a " +
		"miss continues immediately; interception is opt-in, changes page behaviour and " +
		"disables caching, so clear it when done; max 50 rules per page), " +
		"intercept_list (returns the page's rules with sensitive header values redacted), or " +
		"intercept_clear (remove every rule, continue any paused request and disable " +
		"interception; rules never outlive the page). " +
		"auth_save (capture the live page's cookies for site, plus local storage, session " +
		"storage and IndexedDB database/store names, and persist them under name; needs " +
		"session_id, site and name; returns counts only — no cookie or storage value is ever " +
		"printed; snapshots are stored unencrypted at rest in the local 0600 state database, " +
		"so treat that file as a credential), " +
		"auth_restore (write the named snapshot into the page's browser context — cookies " +
		"first, then storage for the live origin; needs site and name; a stored user agent " +
		"that differs from the live page is refused with both agents named unless force=true; " +
		"restoring into a fresh identity writes only that identity's browser context), or " +
		"auth_list (list saved snapshots as {site,name,cookie_count,origin_count,user_agent," +
		"updated_at} with no values; site is an optional filter and the call does not use " +
		"session_id, so a stale id is harmless). " +
		"Pass session_id plus action=... and any required parameters. When more than " +
		"one browser is live, pass browser_id or owner to declare which browser holds " +
		"the session; with one live browser both may be omitted, and a session held " +
		"outside the declared owner is refused as not_found. Element actions take a selector " +
		"or a stable handle from page_fetch (handle wins when both are given). A selector that " +
		"matches nothing returns error.type=element_not_found and does not wait for timeout_ms. " +
		"input and type never echo the value: the result reports the target and the value's rune " +
		"length, plus value_redacted=true for a password or hidden field. " +
		"Logs are cumulative " +
		"from page creation, so call clear_logs right before the step whose traffic/logs you want to " +
		"observe, then read page_fetch what=network|console and filter client-side." +
		resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for page_act's arguments.
func (t *Act) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by fetch_page for the page to act on.",
			},
			"browser_id": map[string]any{
				"type":        "string",
				"description": "Optional browser_id to resolve the session inside that browser only (e.g. 'browser-2'). Use it when more than one browser is live; with exactly one live browser it may be omitted.",
			},
			"owner": map[string]any{
				"type":        "string",
				"description": "Optional owner label (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-') restricting the lookup to browsers with that owner. A session held by another owner is not_found, never acted on.",
			},
			"action": map[string]any{
				"type":        "string",
				"enum":        append([]string(nil), actActionNames...),
				"description": "The interaction to perform: click (selector), input (set field text; the result reports the target and rune length, never the value, plus value_redacted=true for a password or hidden field), type (keystrokes, same result rule as input), press_key (key/combo e.g. Enter, Escape, Control+A), click_at/hover_at ({x, y} coordinates), drag_and_drop (from {x,y}/selector to {target_x,target_y}/target_selector), upload_file (set file input paths), eval (run JS), navigate (URL), back/forward/reload, hover (selector), scroll (x/y), select (option value), wait (readiness mode), set_viewport (W x H), emulate (device preset), clear_logs (empty the network, console and websocket buffers; no parameters; returns {\"network\":N,\"console\":M,\"websocket\":K} cleared counts), sinks_install (arm the source-to-sink tracer; no parameters; returns {\"installed\":true}; capture is opt-in, changes page behavior, survives client-side route changes and full navigations while its per-document ring resets on a full navigation; read it with page_fetch what=sinks, which is read-only and never arms capture), sinks_clear (empty the tracer ring; no parameters; returns {\"cleared\":N}), replay (resend one captured network entry; needs target, optionally method, headers, value and max_bytes; returns {status,headers,body,bytes,truncated,body_redactions,ms,request,chain} with sensitive headers and text-body secrets redacted and truncated/chain always present), inject_canary (inject a canary into query/hash/postmessage/name — default all four, narrow with targets — and return {canary, injected:[{target, where}], hits:[{sink, context, selector, value, stack}], installed}; requires sinks_install first; query reloads, hash does not; capped at 20 frames), postmessage_install (arm the postMessage recorder: one capture-phase listener per window and iframe without wrapping addEventListener, so page listeners keep their identity; no parameters; returns {\"installed\":true}; read it with page_fetch what=postmessage), postmessage_clear (empty every recorder ring; no parameters; returns {\"cleared\":N}), post_message (deliver value to window.top/self/parent when target_selector is \"top\", \"self\", \"parent\" or omitted, or to the contentWindow of the <iframe>/<frame> matched by any other target_selector; a selector that is not a frame errors instead of falling back to top; origin is the target origin, default \"*\"; arms the recorder first and returns {origin,target}), wait_open (block until this page opens a popup or new tab — target=\"_blank\" or window.open — and return {session_id,url,opener}; timeout_ms bounds the wait; a popup opened before the call is returned immediately), intercept_add (arm a page-scoped in-flight request interceptor with rules=[{id,url_pattern,method,action,headers,status,body,delay_ms}]; action is block|allow|rewrite|fulfill, url_pattern is a wildcard glob (default *), a miss continues immediately, at most 50 rules per page; opt-in, changes page behaviour and disables caching; returns {rules,count,active}), intercept_list (return the page's rules with sensitive header values redacted; returns {rules,count,active}), intercept_clear (remove every rule, continue any paused request and disable interception; returns {rules_removed,active:false}). Rules are per session and never outlive the page. Logs are cumulative from page creation — filter client-side or clear_logs right before the step under observation.",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector or XPath identifying the target element. Required by click, input, type, hover, select and upload_file; optional for press_key and the drag_and_drop source; unused by click_at/hover_at (pass x/y), eval/navigate (pass value), scroll and the navigation actions. Use handle= for an element handle from page_fetch. Example: { \"session_id\": \"page-1\", \"action\": \"click\", \"selector\": \"a.primary\" }.",
			},
			"handle": map[string]any{
				"type":        "string",
				"description": "Element handle from a previous page_fetch (what=interactives|dom|links), e.g. g3k9x2ab-b6. It carries the document that minted it: a top-document handle from a previous document (after navigation, reload or redirect) is rejected as stale, while a child-frame handle is rejected when its backend node id dies. Alternative to selector; when both given, handle wins.",
			},
			"value": map[string]any{
				"type":        "string",
				"description": "Payload value for input/type (text; the result never echoes it), press_key (key combo fallback), eval (JS source), navigate (URL), select (option value), or upload_file (single path fallback). For replay it replaces the request body; omit it to send no body. For inject_canary it is the custom canary value; omit it to mint wm-canary-<8 hex>.",
			},
			"targets": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": []string{"query", "hash", "postmessage", "name"}},
				"description": "Injection points for inject_canary (default all four): query (append wmcanary and reload), " +
					"hash (set location.hash and dispatch hashchange, no reload), postmessage (post a wm-probe message " +
					"carrying the canary to every frame), or name (write window.name). Each entry may also be a comma-separated list.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Key or key combination for press_key action (e.g., Enter, Escape, Tab, Backspace, ArrowDown, Control+A, Meta+C, Shift+Tab).",
			},
			"button": map[string]any{
				"type":        "string",
				"enum":        []string{"left", "right", "middle"},
				"default":     "left",
				"description": "Mouse button for click and click_at actions: left, right, or middle.",
			},
			"click_count": map[string]any{
				"type":        "integer",
				"default":     1,
				"description": "Number of mouse clicks for click and click_at actions (1 for single click, 2 for double click).",
			},
			"x": map[string]any{
				"type":        "number",
				"description": "Horizontal coordinate in pixels (scroll, click_at, hover_at, drag_and_drop source).",
			},
			"y": map[string]any{
				"type":        "number",
				"description": "Vertical coordinate in pixels (scroll, click_at, hover_at, drag_and_drop source).",
			},
			"target_x": map[string]any{
				"type":        "number",
				"description": "Target horizontal coordinate in pixels for drag_and_drop action.",
			},
			"target_y": map[string]any{
				"type":        "number",
				"description": "Target vertical coordinate in pixels for drag_and_drop action.",
			},
			"target_selector": map[string]any{
				"type": "string",
				"description": "Target element selector for drag_and_drop, or the destination frame for post_message: " +
					"\"top\", \"self\" and \"parent\" are keywords for window.top/self/parent, and any other " +
					"value is a CSS selector for an <iframe> or <frame> whose contentWindow alone receives " +
					"the message. A selector that matches an element that is not a frame, or a frame " +
					"without a contentWindow, returns an error instead of falling back to top. Example: " +
					"{ \"action\": \"post_message\", \"value\": \"{\\\"t\\\":\\\"wm\\\"}\", \"target_selector\": \"iframe#widget\" }.",
			},
			"origin": map[string]any{
				"type":        "string",
				"default":     "*",
				"description": "Target origin for post_message (default \"*\"). Pass the receiver's origin, e.g. https://widget.example, to keep the browser from delivering the message to an unexpected document.",
			},
			"target": map[string]any{
				"type": "string",
				"description": "Network entry to replay: a seq number or a substring of the request URL, resolved against the " +
					"captured ring read by page_fetch what=network. A substring matching more than one entry is refused " +
					"with the candidate seq numbers, so pass a longer substring or the seq.",
			},
			"method": map[string]any{
				"type":        "string",
				"description": "HTTP method override for replay (e.g. PUT, PATCH); omit to keep the captured method.",
			},
			"headers": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description":          "Header overrides for replay: each named header replaces the captured value; omitted headers keep theirs.",
			},
			"max_bytes": map[string]any{
				"type": "integer",
				"description": "Optional cap on the returned replay body in bytes; the ceiling is 262144 (256 KB) " +
					"and larger requests are clamped. A body over the cap returns truncated:true with bytes " +
					"still reporting the returned size. Ignored by actions other than replay.",
			},
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Array of local file paths for upload_file action.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional per-call timeout in milliseconds (default 30000); wait action sleeps for this long in ms mode.",
			},
			"wait_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "idle", "stable", "interactable", "ms"},
				"default":     "ms",
				"description": "What to wait for (wait action): load, idle, stable, interactable (needs selector), or ms (sleeps timeout_ms).",
			},
			"viewport": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels."},
					"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels."},
				},
				"description": "Target viewport size for set_viewport action.",
			},
			"device": map[string]any{
				"type":        "string",
				"description": "Go-rod device preset name for emulate action (e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen; 'clear' resets).",
			},
			"site": map[string]any{
				"type": "string",
				"description": "Host the auth snapshot is scoped to, e.g. example.org (required by auth_save " +
					"and auth_restore, optional filter for auth_list). Accepts a bare host or a URL; only " +
					"cookies whose domain matches the host are captured, and a cookie is never broadened to " +
					"a parent domain on restore.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Snapshot name, unique per site (required by auth_save and auth_restore; a second save with the same site and name updates it instead of duplicating).",
			},
			"force": map[string]any{
				"type":    "boolean",
				"default": false,
				"description": "auth_restore only: restore even when the live page user agent differs from " +
					"the stored one. Without it the restore is refused and both agents are named.",
			},
			"rules": map[string]any{
				"type":     "array",
				"maxItems": 50,
				"description": "Rules for intercept_add. Each rule matches requests in order; the first match wins " +
					"and a request that matches none continues immediately. A matching block fails the request " +
					"(the page sees a network error), allow passes it through, rewrite continues it with the " +
					"listed headers and non-empty body, and fulfill answers it with status (default 200), " +
					"headers and body without any network fetch. Interception is opt-in, changes page " +
					"behaviour and disables caching; rules are per session and disappear when the page closes.",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"action"},
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Rule id reported with every network hit; reusing an id replaces that rule; generated when omitted.",
						},
						"url_pattern": map[string]any{
							"type":        "string",
							"default":     "*",
							"description": "Wildcard glob matched against the full request URL: * matches any run, ? matches one character, everything else is literal (dots included). Default * matches every request.",
						},
						"method": map[string]any{
							"type":        "string",
							"description": "Optional HTTP method filter (e.g. POST); empty matches every method.",
						},
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"block", "allow", "rewrite", "fulfill"},
							"description": "block fails the request, allow passes it through, rewrite continues it with headers/body, fulfill answers it synthetically.",
						},
						"headers": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
							"description":          "Request header overrides for rewrite, or response headers for fulfill (max 32).",
						},
						"status": map[string]any{
							"type":        "integer",
							"description": "Synthetic response status for fulfill (default 200, range 100-599).",
						},
						"body": map[string]any{
							"type":        "string",
							"description": "Request payload for rewrite (non-empty replaces the sent body), or the response payload for fulfill.",
						},
						"delay_ms": map[string]any{
							"type":        "integer",
							"description": "Delay before resolving the request, clamped to 10000 ms.",
						},
					},
				},
			},
		},
		Required: []string{"session_id", "action"},
	}
}

// actArgs is the strict decoding target for page_act arguments.
type actArgs struct {
	SessionID      browsers.PageID          `json:"session_id"`
	BrowserID      string                   `json:"browser_id"`
	Owner          string                   `json:"owner"`
	Action         string                   `json:"action"`
	Selector       string                   `json:"selector"`
	Handle         string                   `json:"handle"`
	Value          string                   `json:"value"`
	Key            string                   `json:"key"`
	Button         string                   `json:"button"`
	ClickCount     int                      `json:"click_count"`
	X              float64                  `json:"x"`
	Y              float64                  `json:"y"`
	TargetX        float64                  `json:"target_x"`
	TargetY        float64                  `json:"target_y"`
	TargetSelector string                   `json:"target_selector"`
	Origin         string                   `json:"origin"`
	Target         string                   `json:"target"`
	Targets        []string                 `json:"targets"`
	Method         string                   `json:"method"`
	Headers        map[string]string        `json:"headers"`
	MaxBytes       int64                    `json:"max_bytes"`
	Paths          []string                 `json:"paths"`
	TimeoutMS      int                      `json:"timeout_ms"`
	WaitMode       string                   `json:"wait_mode"`
	Viewport       *browsers.Viewport       `json:"viewport"`
	Device         string                   `json:"device"`
	Site           string                   `json:"site"`
	Name           string                   `json:"name"`
	Force          bool                     `json:"force"`
	Rules          []browsers.InterceptRule `json:"rules"`
}

// validate checks the arguments an action requires and returns a one-line
// recovery hint when one is missing. provided is the raw argument object, so
// optional numeric fields can be distinguished from an explicit zero.
func (a *actArgs) validate(provided map[string]json.RawMessage) error {
	switch a.Action {
	case "click", "input", "type", "hover", "select", "upload_file":
		if a.Handle == "" && a.Selector == "" {
			return fmt.Errorf("%s requires selector (or a handle from page_fetch what=interactives)", a.Action)
		}
	case "click_at", "hover_at":
		if _, ok := provided["x"]; !ok {
			return fmt.Errorf("%s requires x and y coordinates (e.g. {\"action\":\"%s\",\"x\":100,\"y\":200})", a.Action, a.Action)
		}
		if _, ok := provided["y"]; !ok {
			return fmt.Errorf("%s requires x and y coordinates (e.g. {\"action\":\"%s\",\"x\":100,\"y\":200})", a.Action, a.Action)
		}
	case "eval":
		if a.Value == "" {
			return fmt.Errorf("eval requires value (JavaScript function source, e.g. \"() => document.title\")")
		}
	case "navigate":
		if a.Value == "" {
			return fmt.Errorf("navigate requires value (the target URL)")
		}
	case "post_message":
		if _, ok := provided["value"]; !ok {
			return fmt.Errorf("post_message requires value (the message payload, e.g. {\"t\":\"wm\"})")
		}
	case "replay":
		if a.Target == "" {
			return fmt.Errorf("replay requires target=<seq> or target=<url substring> from page_fetch what=network")
		}
		if a.MaxBytes < 0 {
			return fmt.Errorf("replay max_bytes must be >= 0")
		}
	case "intercept_add":
		if len(a.Rules) == 0 {
			return fmt.Errorf("intercept_add requires rules (e.g. [{\"url_pattern\":\"*api*\",\"action\":\"block\"}])")
		}
	case "auth_save", "auth_restore":
		if a.Site == "" {
			return fmt.Errorf("%s requires site (the host, e.g. example.org)", a.Action)
		}
		if a.Name == "" {
			return fmt.Errorf("%s requires name (the snapshot name, e.g. main)", a.Action)
		}
	}
	return nil
}

// Execute parses the page_act arguments, resolves the session and dispatches to
// the shared pageops implementation. It returns a short confirmation string.
func (t *Act) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a actArgs
	if err := decodeArgs("page_act", args, &a); err != nil {
		return failResult(err)
	}
	if a.Action == "" {
		return failResult(fmt.Errorf("page_act: missing required argument %q", "action"))
	}

	var provided map[string]json.RawMessage
	if err := json.Unmarshal(args, &provided); err != nil {
		return failResult(fmt.Errorf("page_act: invalid arguments: %w", err))
	}
	if err := a.validate(provided); err != nil {
		return failResult(fmt.Errorf("page_act: %w", err))
	}
	if a.Action == "auth_list" {
		out, err := pageops.PageAuthList(ctx, a.Site)
		if err != nil {
			return failResult(fmt.Errorf("page_act: %w", err))
		}
		return okResult(out)
	}
	if a.SessionID == "" {
		return failResult(fmt.Errorf("page_act: missing required argument %q", "session_id"))
	}

	if a.Action == "set_viewport" {
		if a.Viewport == nil {
			return failResult(fmt.Errorf("page_act: set_viewport requires a viewport"))
		}
		if a.Viewport.Width <= 0 || a.Viewport.Height <= 0 {
			return failResult(fmt.Errorf("page_act: viewport width and height must be positive"))
		}
	}

	if a.Action == "emulate" && a.Device == "" {
		return failResult(fmt.Errorf("page_act: emulate requires a device"))
	}

	timeout := defaultActTimeout
	if a.Action == "wait" && (a.WaitMode == "" || a.WaitMode == "ms") {
		if a.TimeoutMS > 0 {
			timeout = time.Duration(a.TimeoutMS)*time.Millisecond + waitSleepMargin
		}
	} else if a.TimeoutMS > 0 {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p, err := t.mgr.Page(ctx, a.SessionID, browsers.BrowserID(a.BrowserID), a.Owner)
	if err != nil {
		return failResult(fmt.Errorf("page_act: %w", err))
	}
	if a.Action == "navigate" {
		if err := t.mgr.CheckPageScope(a.SessionID, a.Value); err != nil {
			return failResultForPage(ctx, p, fmt.Errorf("page_act: %w", err))
		}
	}

	before, stateErr := pageops.PageStateIfFree(ctx, p)

	var out string
	switch a.Action {
	case "click":
		out, err = pageops.PageClickFrom(ctx, p, a.Handle, a.Selector, a.Button, a.ClickCount, before)
	case "input":
		out, err = pageops.PageInput(ctx, p, a.Handle, a.Selector, a.Value)
	case "type":
		out, err = pageops.PageType(ctx, p, a.Handle, a.Selector, a.Value)
	case "press_key":
		keyStr := a.Key
		if keyStr == "" {
			keyStr = a.Value
		}
		out, err = pageops.PagePressKey(ctx, p, a.Handle, a.Selector, keyStr)
	case "click_at":
		out, err = pageops.PageClickAt(ctx, p, a.X, a.Y, a.Button, a.ClickCount)
	case "hover_at":
		out, err = pageops.PageHoverAt(ctx, p, a.X, a.Y)
	case "drag_and_drop":
		out, err = pageops.PageDragAndDrop(ctx, p, a.X, a.Y, a.TargetX, a.TargetY, a.Selector, a.TargetSelector)
	case "upload_file":
		paths := a.Paths
		if len(paths) == 0 && a.Value != "" {
			paths = []string{a.Value}
		}
		out, err = pageops.PageUploadFile(ctx, p, a.Handle, a.Selector, paths)
	case "eval":
		out, err = pageops.PageEval(ctx, p, a.Value)
	case "navigate":
		out, err = pageops.PageNavigate(ctx, p, a.Value)
	case "back":
		out, err = pageops.PageBack(ctx, p)
	case "forward":
		out, err = pageops.PageForward(ctx, p)
	case "reload":
		out, err = pageops.PageReload(ctx, p)
	case "hover":
		out, err = pageops.PageHover(ctx, p, a.Handle, a.Selector)
	case "scroll":
		out, err = pageops.PageScroll(ctx, p, a.X, a.Y)
	case "select":
		out, err = pageops.PageSelect(ctx, p, a.Handle, a.Selector, a.Value)
	case "wait":
		d := timeout
		if a.TimeoutMS > 0 {
			d = time.Duration(a.TimeoutMS) * time.Millisecond
		}
		out, err = pageops.PageWait(ctx, p, a.WaitMode, a.Handle, a.Selector, d)
	case "set_viewport":
		out, err = pageops.PageSetViewport(ctx, p, a.Viewport.Width, a.Viewport.Height)
	case "emulate":
		out, err = pageops.PageEmulate(ctx, p, a.Device)
	case "clear_logs":
		out, err = pageops.PageClearCapture(p)
	case "sinks_install":
		out, err = pageops.PageSinksInstall(ctx, p)
	case "sinks_clear":
		out, err = pageops.PageSinksClear(ctx, p)
	case "replay":
		_, bodySet := provided["value"]
		out, err = pageops.PageReplay(ctx, p, pageops.ReplayRequest{
			Target:   a.Target,
			Method:   a.Method,
			Headers:  a.Headers,
			Body:     a.Value,
			BodySet:  bodySet,
			MaxBytes: a.MaxBytes,
			CheckScope: func(rawURL string) error {
				return t.mgr.CheckPageScope(a.SessionID, rawURL)
			},
		})
	case "inject_canary":
		out, err = pageops.PageCanaryInject(ctx, p, a.Targets, a.Value)
	case "postmessage_install":
		out, err = pageops.PagePostMessageInstall(ctx, p)
	case "postmessage_clear":
		out, err = pageops.PagePostMessageClear(ctx, p)
	case "post_message":
		out, err = pageops.PagePostMessageSend(ctx, p, a.Value, a.TargetSelector, a.Origin)
	case "wait_open":
		var popup browsers.Popup
		popup, err = t.mgr.WaitOpen(ctx, a.SessionID, browsers.BrowserID(a.BrowserID), a.Owner)
		if err == nil {
			var data []byte
			data, err = json.Marshal(popup)
			out = string(data)
		}
	case "intercept_add":
		out, err = pageops.PageInterceptAdd(ctx, p, a.Rules)
	case "intercept_list":
		out, err = pageops.PageInterceptList(ctx, p)
	case "intercept_clear":
		out, err = pageops.PageInterceptClear(ctx, p)
	case "auth_save":
		out, err = pageops.PageAuthSave(ctx, p, a.Site, a.Name)
	case "auth_restore":
		out, err = pageops.PageAuthRestore(ctx, p, a.Site, a.Name, a.Force)
	default:
		return failResult(fmt.Errorf("page_act: unknown action=%q (must be one of %s)", a.Action, actActionList()))
	}
	if err != nil {
		return failResultForPage(ctx, p, fmt.Errorf("page_act: %w", err))
	}
	if stateErr == nil {
		after := pageops.PageStateAfterAct(ctx, p, a.Action, before)
		out += pageops.ActStateDeltaFor(a.Action, before, after)
	}
	return okResult(out)
}

// Ensure Act satisfies the Tool interface at compile time.
var _ Tool = (*Act)(nil)

// FormSearchAndSubmitPrompt is a co-located prompt definition for page_act.
type FormSearchAndSubmitPrompt struct{}

// NewFormSearchAndSubmitPrompt constructs the form_search_and_submit prompt template.
func NewFormSearchAndSubmitPrompt() *FormSearchAndSubmitPrompt {
	return &FormSearchAndSubmitPrompt{}
}

// Name returns the prompt template name "form_search_and_submit".
func (p *FormSearchAndSubmitPrompt) Name() string { return "form_search_and_submit" }

// Description returns a description of the prompt template.
func (p *FormSearchAndSubmitPrompt) Description() string {
	return "Provides step-by-step guidance for inputting text into search/form fields and submitting the form."
}

// Arguments returns the parameter list accepted by the prompt template.
func (p *FormSearchAndSubmitPrompt) Arguments() []models.PromptArgument {
	return []models.PromptArgument{
		{Name: "session_id", Description: "ID of the active page session", Required: true},
		{Name: "search_query", Description: "Text query to input into search field", Required: true},
		{Name: "selector", Description: "Optional CSS selector for search field (default: input[type='text'])", Required: false},
	}
}

// GetMessages builds the prompt messages payload for the given argument map.
func (p *FormSearchAndSubmitPrompt) GetMessages(args map[string]string) ([]models.PromptMessage, error) {
	sessionID := args["session_id"]
	if sessionID == "" {
		sessionID = "page-1"
	}
	searchQuery := args["search_query"]
	if searchQuery == "" {
		searchQuery = "search query"
	}
	selector := args["selector"]
	if selector == "" {
		selector = "input[type='text'], input[name='q'], input.search"
	}

	text := fmt.Sprintf("Please enter search query %q into session %q:\n"+
		"1. Call page_act with session_id: %q, action: \"input\", selector: %q, value: %q.\n"+
		"2. Verify input value using page_act action: \"eval\" with value: \"() => document.querySelector(%q).value\".\n"+
		"3. Call page_act with action: \"click\" on search button or submit form.\n"+
		"4. Call page_act with action: \"wait\", wait_mode: \"load\" to ensure results land.",
		searchQuery, sessionID, sessionID, selector, searchQuery, selector)

	return []models.PromptMessage{
		{
			Role:    "user",
			Content: models.TextContent{Type: "text", Text: text},
		},
	}, nil
}

// Ensure FormSearchAndSubmitPrompt satisfies the Prompt interface at compile time.
var _ Prompt = (*FormSearchAndSubmitPrompt)(nil)
