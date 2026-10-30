package tools

import (
	"github.com/dakolli/webmcp/pkg/cli/pageops"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// PageActDescription is the LLM-facing description of the Act tool.
const PageActDescription = "Interacts with a live page opened by fetch_page: click (supports button selection & double-click), " +
	"input, type, press_key (send keys/combos like Enter, Escape, Control+A, Meta+C), " +
	"click_at / hover_at (coordinate-based mouse actions), drag_and_drop (mouse drag), " +
	"upload_file (set files on <input type='file'>), eval (run JS — the primary escape hatch), " +
	"set_style (write inline CSS declarations on an element from a value like " +
	"\"background-image:url('/a.png');--page-bg:red\"; split only at bracket depth zero, so " +
	"url('a;b') and rgb(1,2,3) survive; an empty value removes the inline style; returns " +
	"{target,cleared,applied,rejected,style} where applied lists the property names the CSSOM kept " +
	"and rejected the declarations it dropped), " +
	"set_attr (set one attribute from name and value onto the element; an empty value removes it; " +
	"there is no allowlist, so javascript: and onerror values reach the page unchanged; returns " +
	"{target,name,value,removed,nodes_added}), " +
	"set_html (write markup from value into the element; mode is inner (default, innerHTML), outer " +
	"(outerHTML) or append (insertAdjacentHTML beforeend); no sanitisation; returns " +
	"{target,mode,nodes_added,elements,scripts,executed} where elements caps at 50 and executed is " +
	"true only when the tracer recorded a hit beyond the write itself, so run sinks_install to " +
	"observe it), " +
	"fill_form (fill a form and optionally submit it in one call: pass fields as a selector→value " +
	"object, or selector naming the <form> plus values as a name→value object resolved inside it; " +
	"submit is requestSubmit (default), click or none, and wait_mode load|idle|stable waits after " +
	"the submit; each control gets its native setter and the input/change events React and Vue " +
	"listen for, a checkbox or radio is checked, a select takes the option value, a textarea takes " +
	"the value; returns {form,filled:[{name,selector}],skipped:[{selector,reason}],submit,url_after} " +
	"where an unresolved field is skipped, never an error, and a missing form is element_not_found), " +
	"navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, " +
	"clear_logs (empties the network, console, websocket and dialog buffers, the dialog ring covering " +
	"alert, confirm, prompt, beforeunload and auth challenges, returning the cleared counts, e.g. " +
	"page_act { action: \"clear_logs\" } → {\"network\":12,\"console\":5,\"websocket\":0,\"dialogs\":0}), " +
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
	"at a 256 KB ceiling, and truncated/chain are always present. query edits the captured URL " +
	"parameters — a non-empty value replaces one and an empty value or JSON null removes it — while method, " +
	"headers and value override the captured method, headers and body; request.url shows the " +
	"edited URL), or " +
	"replay_fuzz (run a bounded matrix of variants over one captured network entry: target uses " +
	"replay's seq-or-URL-substring rule, points is an array of {param, location} with location " +
	"query, body, header or cookie, payloads is the value list, and strategy is sniper (one " +
	"point at a time, points*payloads requests; default), pitchfork (nth point with nth payload) " +
	"or clusterbomb (every combination, payloads^points). The size is computed and refused BEFORE " +
	"any request: at most 100 matrix requests (the two baseline probes are extra), 20 points, 50 " +
	"payloads and 200 runes per payload, with 5 requests in flight and 10s each. It probes the " +
	"unmodified baseline twice so cold_ms is the warming sample and ms the compared warm one, " +
	"and returns {runs:[{seq, payload, point, status, ms, bytes, body_cut, body_redactions}], " +
	"summary:{baseline:{status,bytes,cold_ms,ms}, anomalies:[{seq, rule, why}]}}; the rules are " +
	"status_changed, size_delta (>10% either way) and time_delta (>3x either way and beyond the " +
	"baseline's cold-to-warm spread), " +
	"bytes is the true response size measured before max_bytes, and every run body is redacted " +
	"like replay. Example: target=/api/search, " +
	"points=[{param:\"q\",location:\"query\"}], payloads=[\"a\",\"b\"] with sniper sends 2 " +
	"requests; 3 points with 4 payloads sends 12. An over-bound matrix is refused before any " +
	"request, e.g. 5 points with 3 payloads on clusterbomb returns \"replay_fuzz: requests " +
	"computed 243, over the 100 bound\" — lower the payloads or use sniper), or " +
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
	"{id, url_pattern, method, action, headers, rewrite_method, status, body, delay_ms} where " +
	"action is block, allow, rewrite or fulfill, url_pattern is a wildcard glob like *api*, and " +
	"a miss continues immediately; rewrite continues the request with headers, a non-empty body " +
	"and rewrite_method (method only filters the match and never changes the request), and the " +
	"network ring records the method and headers as sent; interception " +
	"is opt-in, changes page behaviour and disables the page HTTP cache while any rule is armed, so " +
	"cached subresources still match; the page holds at most 50 rules across calls, so the cap is " +
	"page-wide rather than per call; the result reports rules and the remaining budget), " +
	"intercept_list (returns the page's rules with sensitive header values redacted, plus the " +
	"remaining rule budget), or " +
	"intercept_clear (remove every rule, continue any paused request, disable interception and " +
	"restore the page cache; rules never outlive the page), or " +
	"dialog_accept (set the page's dialog policy to accept; value is the prompt text typed before " +
	"accepting a prompt, and the result is {policy,prompt_text,count,dropped,last}) or " +
	"dialog_dismiss (set the policy to dismiss), with the caveat that the engine already answers " +
	"every alert, confirm, prompt and beforeunload the moment it opens — the default policy is " +
	"dismiss — so a dialog that arrived before you set the policy was already dismissed; set the " +
	"policy before the step that opens it. " +
	"http_auth (arm a per-page HTTP basic-auth credential: value is user:password split at the first " +
	"colon, and an empty value clears it. The credential is never echoed, and it applies to later 401 " +
	"challenges on that page. The result is {http_auth:\"armed\"} or {http_auth:\"cleared\"}, plus " +
	"note when Chromium already cached a credential for this origin — then open a new page to test " +
	"a different credential. http_auth and intercept_add both need the Fetch domain, so one must be " +
	"cleared first), " +
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
	"session_id, so a stale id is harmless), or " +
	"poc_bundle (package one finding as a self-contained evidence directory; pass value=<finding title>, " +
	"paths=[<absolute output directory>] and optionally target=<seq or URL substring> to select the captured " +
	"request, otherwise the main document request is used. request.http and response.http come from the " +
	"network ring, so a prior clear_logs omits both files with reason no network entry. It writes meta.json, request.http, response.http, " +
	"screenshot.png, dom.html, storage.json and README.md, plus sinks.json only when the tracer was installed, " +
	"returns {directory,files:[{name,bytes}],omitted:[{name,reason}]} where reason is one of not installed, " +
	"no body, no network entry, cap reached or secrets hidden; it refuses a relative path or a non-empty " +
	"directory, and keeps Cookie, Set-Cookie, Authorization and X-CSRF-Token values redacted unless the page " +
	"opted into secret capture). " +
	"encode (a page-free helper: encodes value for context and returns " +
	"{context,variants:[{label,value}]} with the mechanically equivalent payloads raw, entity, " +
	"url, url-double, js, js-hex, unicode and html5-attr; context is html-text, attr-double, " +
	"attr-single, attr-unquoted, js-string, js-template, css-value, url-query or url-path; " +
	"variants narrows the labels; it needs value and context but no session_id and never " +
	"opens, locks or touches a page). " +
	"Pass session_id plus action=... and any required parameters. When more than " +
	"one browser is live, pass browser_id or owner to declare which browser holds " +
	"the session; with one live browser both may be omitted, and a session held " +
	"outside the declared owner is refused as not_found. Element actions take a selector " +
	"or a stable handle from page_fetch (handle wins when both are given). A selector that " +
	"matches nothing returns error.type=element_not_found and does not wait for timeout_ms. " +
	"input and type never echo the value: the result reports the target and the value's rune " +
	"length, plus value_redacted=true for a password or hidden field. " +
	"Pointer and keyboard actions (click, click_at, input, hover, hover_at, press_key) are " +
	"humanized by default: the pointer steps to the target with small jitter and a randomized " +
	"100-500 ms pause precedes and follows the action. A sub-second timeout_ms can expire during " +
	"that pause and leave the action unperformed, so allow at least 1000 ms or use an instant hatch. " +
	"Pass raw=true, set WEB_MCP_RAW_ACTIONS=1 in the server environment, or start the server " +
	"with raw actions enabled to make them instant. " +
	"Logs are cumulative " +
	"from page creation, so call clear_logs right before the step whose traffic/logs you want to " +
	"observe, then read page_fetch what=network|console and filter client-side. " +
	"Numbers may be sent as JSON numbers or as numeric strings; a non-numeric string is rejected. " +
	ResultEnvelopeDoc

// FormSearchAndSubmitPromptDescription is the LLM-facing description of the FormSearchAndSubmitPrompt tool.
const FormSearchAndSubmitPromptDescription = "Provides step-by-step guidance for inputting text into search/form fields and submitting the form."

// newActInputSchema returns the input schema of the act tool.
func newActInputSchema() models.InputSchema {
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
				"description": "The interaction to perform: click (selector), input (set field text; the result reports the target and rune length, never the value, plus value_redacted=true for a password or hidden field), type (keystrokes, same result rule as input), press_key (key/combo e.g. Enter, Escape, Control+A), click_at/hover_at ({x, y} coordinates), drag_and_drop (from {x,y}/selector to {target_x,target_y}/target_selector), upload_file (set file input paths), eval (run JS), navigate (URL), back/forward/reload, hover (selector), set_style (write inline CSS declarations from a value such as \"background-image:url('/a.png');--page-bg:red\", split only at bracket depth zero; an empty value removes the inline style; returns {target,cleared,applied,rejected,style}), set_attr (set one attribute from name and value on the element; an empty value removes it; no allowlist, so javascript: and onerror reach the page unchanged; returns {target,name,value,removed,nodes_added}), set_html (write markup from value; mode is inner (default, innerHTML), outer (outerHTML) or append (insertAdjacentHTML beforeend); no sanitisation; returns {target,mode,nodes_added,elements,scripts,executed}, elements capped at 50, executed true only when the tracer saw a hit beyond the write itself), fill_form (fill a form and optionally submit it in one call: fields is a selector→value object, or selector names the <form> and values is a name→value object resolved inside it; submit is requestSubmit (default), click or none; wait_mode load|idle|stable waits after the submit; returns {form,filled:[{name,selector}],skipped:[{selector,reason}],submit,url_after}; an unresolved field is skipped, never an error, and a missing form is element_not_found), scroll (x/y), select (option value), wait (readiness mode), set_viewport (W x H), emulate (device preset), clear_logs (empty the network, console, websocket and dialog buffers; no parameters; returns {\"network\":N,\"console\":M,\"websocket\":K,\"dialogs\":D} cleared counts), dialog_accept (set the dialog policy to accept; value is the prompt text; the engine answers every dialog as it opens, so a dialog that arrived before the policy was set was already dismissed; returns {policy,prompt_text,count,dropped,last}), dialog_dismiss (set the dialog policy to dismiss; the default), http_auth (set a per-page HTTP basic-auth credential; value is user:password split at the first colon, an empty value clears it, the credential is never echoed and applies to later 401 challenges on that page; returns {http_auth:\"armed\"} or {http_auth:\"cleared\"}, plus note when Chromium already cached a credential for this origin, so open a new page to test a different credential; http_auth and intercept_add both need the Fetch domain, so one must be cleared first), sinks_install (arm the source-to-sink tracer; no parameters; returns {\"installed\":true}; capture is opt-in, changes page behavior, survives client-side route changes and full navigations while its per-document ring resets on a full navigation; read it with page_fetch what=sinks, which is read-only and never arms capture), sinks_clear (empty the tracer ring; no parameters; returns {\"cleared\":N}), replay (resend one captured network entry; needs target, optionally method, headers, query, value and max_bytes, where query edits URL parameters (an empty value or JSON null removes one) and request.url shows the edited URL; returns {status,headers,body,bytes,truncated,body_redactions,ms,request,chain} with sensitive headers and text-body secrets redacted and truncated/chain always present), replay_fuzz (bounded matrix over one captured entry; needs target, points:[{param,location}] and payloads (each also accepted as a stringified JSON array), optional strategy sniper|pitchfork|clusterbomb (default sniper) and max_bytes; computes and refuses the size before any request (max 100 matrix requests, 20 points, 50 payloads, 200 runes each; 5 in flight, 10s each); the baseline is probed twice, so summary.baseline:{status,bytes,cold_ms,ms} compares against the warm second sample and a time_delta inside the cold-to-warm spread is suppressed; returns {runs:[{seq,payload,point,status,ms,bytes,body_cut,body_redactions}],summary:{baseline,anomalies}}), inject_canary (inject a canary into query/hash/postmessage/name — default all four, narrow with targets — and return {canary, injected:[{target, where}], hits:[{sink, context, selector, value, stack}], installed}; requires sinks_install first; query reloads, hash does not; capped at 20 frames), postmessage_install (arm the postMessage recorder: one capture-phase listener per window and iframe without wrapping addEventListener, so page listeners keep their identity; no parameters; returns {\"installed\":true}; read it with page_fetch what=postmessage), postmessage_clear (empty every recorder ring; no parameters; returns {\"cleared\":N}), post_message (deliver value to window.top/self/parent when target_selector is \"top\", \"self\", \"parent\" or omitted, or to the contentWindow of the <iframe>/<frame> matched by any other target_selector; a selector that is not a frame errors instead of falling back to top; origin is the target origin, default \"*\"; arms the recorder first and returns {origin,target}), wait_open (block until this page opens a popup or new tab — target=\"_blank\" or window.open — and return {session_id,url,opener}; timeout_ms bounds the wait; a popup opened before the call is returned immediately), intercept_add (arm a page-scoped in-flight request interceptor with rules=[{id,url_pattern,method,action,headers,rewrite_method,status,body,delay_ms}]; action is block|allow|rewrite|fulfill, url_pattern is a wildcard glob (default *), rewrite continues with headers/body/rewrite_method, a miss continues immediately, and the page holds at most 50 rules across calls (page-wide, not per call); opt-in, changes page behaviour and disables the page HTTP cache while armed so cached subresources still match; returns {rules,count,remaining,active}), intercept_list (return the page's rules with sensitive header values redacted and the remaining rule budget; returns {rules,count,remaining,active}), intercept_clear (remove every rule, continue any paused request, disable interception and restore the cache; returns {rules_removed,active:false}). Rules are per session and never outlive the page. poc_bundle (package one finding as a self-contained evidence directory: value is the finding title, paths[0] is the absolute output directory, and optional target selects the captured request by seq or URL substring; writes meta.json, request.http, response.http, screenshot.png, dom.html, storage.json and README.md, plus sinks.json only when the tracer was installed, and returns {directory,files,omitted}). encode (encode a payload for a context and return the mechanically equivalent variants — raw, entity, url, url-double, js, js-hex, unicode, html5-attr; needs value and context but no session_id, never touches the page, and returns {context,variants:[{label,value}]}). Logs are cumulative from page creation — filter client-side or clear_logs right before the step under observation.",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector or XPath identifying the target element. Required by click, input, type, hover, select, set_style, set_attr, set_html and upload_file; optional for press_key and the drag_and_drop source; for fill_form it names the <form> and may be omitted when fields carries full selectors; unused by click_at/hover_at (pass x/y), eval/navigate (pass value), scroll and the navigation actions. Use handle= for an element handle from page_fetch. Example: { \"session_id\": \"page-1\", \"action\": \"click\", \"selector\": \"a.primary\" }.",
			},
			"handle": map[string]any{
				"type":        "string",
				"description": "Element handle from a previous page_fetch (what=interactives|dom|links), e.g. g3k9x2ab-b6. It carries the document that minted it: a top-document handle from a previous document (after navigation, reload or redirect) is rejected as stale, while a child-frame handle is rejected when its backend node id dies. Alternative to selector; when both given, handle wins.",
			},
			"value": map[string]any{
				"type":        "string",
				"description": "Payload value for input/type (text; the result never echoes it), press_key (key combo fallback), eval (JS source), navigate (URL), select (option value), upload_file (single path fallback), set_style (a CSS declaration list such as \"background-image:url('/a.png');--page-bg:red\"; an empty value removes the inline style), set_attr (the attribute value; an empty value removes the attribute), set_html (the markup to write; not sanitised), poc_bundle (the finding title), or encode (the payload to encode). For replay it replaces the request body; omit it to send no body. For inject_canary it is the custom canary value; omit it to mint wm-canary-<8 hex>.",
			},
			"context": map[string]any{
				"type": "string",
				"enum": pageops.EncodeContexts(),
				"description": "encode only: where the payload will land, which selects the variant set. " +
					"html-text (HTML text node), attr-double / attr-single / attr-unquoted (inside an " +
					"attribute, plus url for an href/src target), js-string / js-template (inside a " +
					"JavaScript string or template literal), css-value (inside a CSS declaration), " +
					"url-query / url-path (inside a URL). An unknown context fails as invalid_args and " +
					"lists every accepted value.",
			},
			"variants": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "encode only: narrows the returned variants to these labels. Omit for the " +
					"full set of the context. raw is always present. Labels are raw, entity, url, " +
					"url-double, js, js-hex, unicode and html5-attr; a label the context does not " +
					"offer fails as invalid_args.",
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
				"description": "Network entry for replay, replay_fuzz or poc_bundle: a seq number or a substring of " +
					"the request URL, resolved against the captured ring read by page_fetch what=network. A substring " +
					"matching more than one entry is refused with the candidate seq numbers, so pass a longer substring " +
					"or the seq. Omit it to select the main document request.",
			},
			"method": map[string]any{
				"type":        "string",
				"description": "HTTP method override for replay (e.g. PUT, PATCH); omit to keep the captured method.",
			},
			"headers": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "replay only: each named header replaces the captured value on the resent request; " +
					"omitted headers keep theirs. It does not set headers on page requests — use " +
					"browser_create.headers for browser-wide request headers, or intercept_add with " +
					"action:\"rewrite\" for a per-request rewrite.",
			},
			"query": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "replay only: URL query parameters to edit on the captured request. A string " +
					"value replaces the parameter; an empty string (\"\") or JSON null removes it. Every " +
					"other parameter keeps its captured value, and the result's request.url shows the " +
					"edited URL. " +
					"Replace: {\"action\":\"replay\",\"target\":\"1\",\"query\":{\"q\":\"other\"}}. " +
					"Remove: {\"action\":\"replay\",\"target\":\"1\",\"query\":{\"q\":\"\"}} " +
					"or {\"action\":\"replay\",\"target\":\"1\",\"query\":{\"q\":null}}.",
			},
			"max_bytes": map[string]any{
				"type": "integer",
				"description": "Optional cap on the returned body in bytes; the ceiling is 262144 (256 KB) " +
					"and larger requests are clamped. A body over the cap returns truncated:true with bytes " +
					"reporting the returned size. With replay_fuzz the cap bounds each returned variant body " +
					"only: the run still measures the true response size before the cap, so bytes and the " +
					"size_delta rule stay accurate. Ignored by other actions.",
			},
			"points": map[string]any{
				"type": "array",
				"description": "replay_fuzz only: injection points over the captured request. Each entry is " +
					"{param, location}; param is the query parameter, body field, header name or cookie name, " +
					"and location is query, body, header or cookie. Max 20 points. A body point needs a " +
					"captured request body. A stringified JSON array is accepted.",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"param", "location"},
					"properties": map[string]any{
						"param": map[string]any{
							"type":        "string",
							"description": "Query parameter, body field, header name or cookie name to inject into.",
						},
						"location": map[string]any{
							"type":        "string",
							"enum":        []string{"query", "body", "header", "cookie"},
							"description": "Where param lives in the captured request.",
						},
					},
				},
			},
			"payloads": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "replay_fuzz only: values injected at each point, in plan order. Max 50 payloads " +
					"of 200 runes each. The matrix size is computed and refused before any request. " +
					"A stringified JSON array is accepted.",
			},
			"strategy": map[string]any{
				"type":        "string",
				"enum":        []string{"sniper", "pitchfork", "clusterbomb"},
				"default":     "sniper",
				"description": "replay_fuzz only: sniper injects one point at a time (points*payloads requests), pitchfork pairs the nth point with the nth payload, clusterbomb covers every combination (payloads^points). Default sniper. The 100-request bound is checked before the run.",
			},
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Array of local file paths for upload_file action, or for poc_bundle the single absolute output directory in paths[0] (created with mode 0700 when absent; a relative path or a non-empty directory is refused).",
			},
			"timeout_ms": map[string]any{
				"type": "integer",
				"description": "Optional per-call timeout in milliseconds (default 30000); the wait action " +
					"sleeps for this long in ms mode. A sub-second timeout can expire during the humanized " +
					"pre-action pause and leave the click unperformed — pass raw:true or at least 1000 ms " +
					"when the action must land.",
			},
			"raw": map[string]any{
				"type":    "boolean",
				"default": false,
				"description": "Disable the humanized timing for this call: one instant pointer move, no " +
					"pre/post action pause, and no post-action settle poll — the page state is read once " +
					"and the action returns, so the delta reflects that single read rather than a polled " +
					"settle. The default humanizes click, click_at, input, hover, hover_at and press_key " +
					"with stepped, jittered pointer movement and a randomized 100-500 ms pause, then polls " +
					"up to 900 ms for a click-driven navigation so the state delta is reliable; a short " +
					"timeout can be spent in that pause. Three hatches disable it and any one is enough: " +
					"this per-call raw flag, WEB_MCP_RAW_ACTIONS=1 in the server environment, or the " +
					"server started with browsers.WithRawActions(true). eval, navigate and other " +
					"non-pointer actions never pause.",
			},
			"wait_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "idle", "stable", "interactable", "ms"},
				"default":     "ms",
				"description": "What to wait for (wait action): load, idle, stable, interactable (needs selector), or ms (sleeps timeout_ms). fill_form reuses load, idle or stable as the post-submit wait; ms and interactable are ignored there.",
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
				"description": "Snapshot name, unique per site (required by auth_save and auth_restore; a second save with the same site and name updates it instead of duplicating). For set_attr it is the attribute name to write, e.g. href, src, onerror or srcdoc.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"inner", "outer", "append"},
				"default":     "inner",
				"description": "set_html only: inner (default) assigns innerHTML, outer assigns outerHTML, append calls insertAdjacentHTML('beforeend'). An unknown mode fails as invalid_args.",
			},
			"fields": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "fill_form only: a JSON object of field selector → value. Mutually exclusive " +
					"with values. Without a form selector the selectors resolve against the document; with " +
					"one they resolve inside that form. Example: " +
					"{\"fields\":{\"#q\":\"wm1\",\"#go\":\"1\"},\"submit\":\"requestSubmit\"}.",
			},
			"values": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "fill_form only: a JSON object of field name → value, resolved inside the " +
					"form named by selector. Requires selector. Mutually exclusive with fields. Example: " +
					"{\"selector\":\"form#login\",\"values\":{\"user\":\"a\",\"pass\":\"b\"}}.",
			},
			"submit": map[string]any{
				"type":    "string",
				"enum":    []string{"requestSubmit", "click", "none"},
				"default": "requestSubmit",
				"description": "fill_form only: requestSubmit (default) calls form.requestSubmit(), click " +
					"clicks the form's submit control, none only fills. A submit other than none requires a " +
					"form (a form selector or a field inside a <form>).",
			},
			"force": map[string]any{
				"type":    "boolean",
				"default": false,
				"description": "auth_restore only: restore even when the live page user agent differs from " +
					"the stored one. Without it the restore is refused and both agents are named.",
			},
			"rules": map[string]any{
				"type": "array",
				"description": "Rules for intercept_add. Each rule matches requests in order; the first match " +
					"wins and a request that matches none continues immediately. A matching block fails the " +
					"request (the page sees a network error), allow passes it through, rewrite continues it with " +
					"the listed headers, non-empty body and rewrite_method, and fulfill answers it with status " +
					"(default 200), headers and body without any network fetch. The page holds at most 50 rules " +
					"across calls: the cap is page-wide, not per call, and both intercept_add and intercept_list " +
					"report the remaining budget. Interception is opt-in, changes page behaviour and disables " +
					"the page HTTP cache while any rule is armed (restored by intercept_clear and by page " +
					"close), so a cached subresource still matches. The network ring records the headers and " +
					"method actually sent by a rewrite. Rules are per session and disappear when the page closes.",
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
							"description": "Optional HTTP method filter (e.g. POST); empty matches every method. This filters the incoming request and does not change it; use rewrite_method to send a different method.",
						},
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"block", "allow", "rewrite", "fulfill"},
							"description": "block fails the request, allow passes it through, rewrite continues it with headers/body/rewrite_method, fulfill answers it synthetically.",
						},
						"headers": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
							"description":          "Request header overrides for rewrite, or response headers for fulfill (max 32).",
						},
						"rewrite_method": map[string]any{
							"type":        "string",
							"description": "rewrite only: send the request with this HTTP method (e.g. POST) instead of the original; empty keeps the original. Refused with any other action.",
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
