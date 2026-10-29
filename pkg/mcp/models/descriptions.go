package models

// ResultEnvelopeDoc is appended to every tool description so an agent knows
// where the success payload and the failure branch live.
const ResultEnvelopeDoc = " Every result is one JSON envelope: " +
	`{"ok":true,"data":<payload>}` +
	" on success (data is the JSON object/array to address directly, or a JSON " +
	"string for prose), or " +
	`{"ok":false,"error":{"type":<type>,"message":<human sentence>,"hint":<one-line fix>}}` +
	" on failure. Branch on error.type (invalid_args, not_found, element_not_found, " +
	"not_interactable, anti_bot, page_closed, timeout, nav_failed, unknown), " +
	"apply error.hint when present, and read success payloads under data."

// FetchPageDescription is the LLM-facing description of the Fetch tool.
const FetchPageDescription = "Opens a URL in a new live browser session and returns a page " +
	"summary (title, url, session_id, byte-size hints). Call this FIRST to " +
	"load a page; then use page_fetch with the returned session_id to read " +
	"its html/dom/text/css/js/links/screenshot/meta, and page_act to " +
	"interact with it. Each fetch_page call creates a new session. Set owner " +
	"(1 to 64 characters from A-Z, a-z, 0-9, '.', '_', '-') to use your own " +
	"browser when several agents share one session; with an owner set it never " +
	"falls back to the shared unowned browser. Example: " +
	"{ \"owner\": \"scraper-a\", \"url\": \"https://example.com\" } and " +
	"{ \"owner\": \"scraper-b\", \"url\": \"https://example.com\" } open two " +
	"separate browsers. identity opens the page in an incognito context " +
	"inside the target browser and defaults to 'default': a new name is " +
	"created on first use, each name is one isolated context, and contexts " +
	"keep separate cookies and storage while sharing one Chromium process. " +
	"Example: { \"url\": \"https://x.test\", \"identity\": \"victim\" }." +
	ResultEnvelopeDoc

// FetchAndSummarizePromptDescription is the LLM-facing description of the FetchAndSummarizePrompt tool.
const FetchAndSummarizePromptDescription = "Loads a web page and provides step-by-step instructions to read metadata and extract main content."

// PageFetchDescription is the LLM-facing description of the Page tool.
const PageFetchDescription = "Reads an aspect of a live page that was opened with fetch_page: " +
	"html, dom (full flattened DOM snapshot, LLM-friendly), text, css, js, " +
	"links, screenshot (a metadata text block {mime_type, width, height, " +
	"bytes, full_page} plus the PNG as a separate image content block with " +
	"mimeType, or the base64 data URI in data when the client advertises " +
	"imageContent=false), interactives (dense action map of every visible " +
	"clickable/typeable element with role, text, href, type, x/y as the " +
	"element centre and width/height in page-absolute CSS px. The top-left " +
	"is left = x - width/2, top = y - height/2, while marks reports top-left, " +
	"so the two differ. Each entry also carries box {x,y,w,h} and a stable " +
	"handle for page_act's handle arg. frame_url marks an entry that comes " +
	"from a child frame. All coordinates are page-absolute, so a child " +
	"iframe entry feeds click_at directly. Budgets: the root document returns " +
	"up to 200 entries, each child frame up to 50, and all child frames " +
	"together up to 150, so one response never exceeds 350; truncated is the " +
	"root remainder, frames_truncated is the total across child frames, and " +
	"frames is one row per child frame {url,count,truncated}. selector scopes " +
	"the map: an <iframe> selector returns that frame's entries, any other " +
	"selector returns only root entries in its subtree, and no match returns " +
	"element_not_found. A frame-scoped read returns only the selected frame's " +
	"row, and the cut appears in that row's truncated and in frames_truncated, " +
	"not in truncated), a11y (flattened accessibility tree: computed " +
	"role/name/value/state flags plus depth, with handles on DOM-backed " +
	"nodes usable as page_act's handle; root frame only), network and console " +
	"(best-effort request and console logs as " +
	"{\"entries\":[...],\"count\",\"dropped\",\"truncated\"} with the " +
	"newest 100 buffered entries. Every network entry carries frame_id and " +
	"loader_id when the browser reported them, and top_document marks the " +
	"document request that produced the page's current top-level document, " +
	"so the current document's traffic is distinguishable from an earlier " +
	"navigation's. Capture is cumulative from page creation. " +
	"Pass filter to narrow the network read by request type: a comma list matched " +
	"case-insensitively against each entry's type, where a bare name keeps " +
	"that type and a '-' prefix drops it (filter=\"document,script,xhr\" " +
	"keeps those three, filter=\"-image,-font\" drops images and fonts); an " +
	"unknown name fails as invalid_args and lists the accepted names, and a " +
	"filtered read adds filtered_out. Otherwise filter the network read client-side " +
	"or call page_act action clear_logs right before the " +
	"step you want to observe. clear_logs clears network and console), " +
	"websocket (the WebSocket frame ring as " +
	"{\"entries\":[...],\"count\",\"dropped\",\"truncated\"}: the newest 100 " +
	"frames of a 200-frame ring, oldest first. count is the emitted count, at " +
	"most 100, so count + truncated + dropped equals the frames observed; " +
	"dropped counts evictions when the 200-frame ring turns over and can " +
	"exceed 200. Each entry carries seq, socket_id, url, direction, opcode, " +
	"payload, payload_bytes, payload_cut, status and ms; opcode is 1 text, " +
	"2 binary, 8 close, 9 ping or 10 pong, and socket_id is the CDP request " +
	"id of the connection, so frames of one socket share it. payload holds " +
	"UTF-8 text and is clipped at 2000 runes; a binary frame reports " +
	"payload_bytes only. status is 0, and ms is 0, when the open event was " +
	"missed. Secrets are redacted at capture. Capture starts before the first " +
	"document load: call page_act action clear_logs to reset, then act, then " +
	"read; filter does not apply), dialogs (the JavaScript-dialog and HTTP-auth " +
	"ring as {\"entries\":[...],\"count\",\"dropped\",\"truncated\",\"policy\"}: " +
	"the newest 100 records of a 50-dialog ring, oldest first, and truncated is " +
	"always 0 because the 50-entry ring never reaches the 100-entry emit cap. Each entry " +
	"carries seq, type, message, message_cut, default_prompt, url, policy, " +
	"prompt_text and ms; type is alert, confirm, prompt, beforeunload or auth, " +
	"and an auth entry is a rejected HTTP basic-auth challenge. The engine " +
	"answers every dialog as it opens, and the default policy is dismiss. The " +
	"envelope's policy answers the next dialog, while each entry's policy is " +
	"the one that answered it. This read never changes the policy; use " +
	"page_act dialog_accept or dialog_dismiss to change it; clear_logs empties " +
	"the ring), mutations (the DOM mutation timeline as " +
	"{\"entries\":[{\"seq\",\"ts\",\"kind\",\"target\",\"attribute\",\"added\",\"removed\",\"text\"}],\"count\",\"dropped\",\"truncated\",\"last_seq\"} over the newest 100 batches of a 500-batch per-page ring. It is read-only and always capturing: capture starts with the page and survives navigation. To see what one action changed, bracket it with two reads: read what=mutations and keep last_seq (e.g. {\"session_id\":\"page-1\",\"what\":\"mutations\"}), run the action (e.g. {\"session_id\":\"page-1\",\"action\":\"click\",\"selector\":\"#toggle\"}), then read again with since=<last_seq> ({\"session_id\":\"page-1\",\"what\":\"mutations\",\"since\":42}); every returned entry has seq > 42 and is what the action changed. kind is attributes, characterData or childList, target is a short CSS path clipped at 120 runes, attribute names the changed attribute, added and removed are node counts, and text is the new characterData text clipped at 120 runes. selector keeps only entries whose target contains it), article (the readable " +
	"article body as markdown with title, byline, excerpt, word count and " +
	"truncated flag), marks (the mark table as a text block plus the " +
	"annotated viewport PNG as an image block, with the data URI returned " +
	"in screenshot for a text-only client. Its x/y coordinates feed " +
	"page_act click_at), " +
	"security (a passive, read-only audit of response headers, CSP, cookie " +
	"flags, mixed content, subresource integrity and form targets, returned as " +
	"{\"checks\":[{\"id\",\"status\",\"evidence\",\"detail\"}],\"count\":N}, which " +
	"never changes the page and never runs a bypass and reports policy weakness, " +
	"not exploitability, with each finding quoting the real header or meta tag, " +
	"for example check csp_bypass_trusted_host), " +
	"sinks (source-to-sink tracer ring: {\"installed\",\"hits\":[{\"source\",\"sink\"," +
	"\"context\",\"value\",\"value_cut\",\"stack\",\"selector\",\"ts\"}],\"dropped\"," +
	"\"truncated\"}. It is read-only and never arms capture: capture starts only with " +
	"page_act action=sinks_install, survives client-side route changes and full navigations, " +
	"and its ring is per document, so a full navigation resets it. The newest 100 hits of a " +
	"200-hit ring are emitted, values are clipped at 200 runes and stacks keep 5 frames; " +
	"reset with page_act action=sinks_clear. Pass filter=canary to keep only hits " +
	"whose value holds the injected wm-canary-… marker (inject_canary can return " +
	"an empty hits list, which is not an error) or filter=known to drop hits whose " +
	"source is \"unknown\"; both add filtered_out), " +
	"postmessage (the postMessage recorder ring: {\"installed\",\"messages\":[{\"origin\"," +
	"\"source_kind\",\"data\",\"data_type\",\"ts\",\"stack\"}],\"dropped\",\"truncated\"," +
	"\"frames\":[{\"url\",\"probed\"}]}. It is read-only and never arms capture: arm it with " +
	"page_act action=postmessage_install, which adds one capture-phase listener per window and " +
	"iframe without wrapping addEventListener, so page listeners keep their identity. Messages are " +
	"aggregated across up to 20 frames, data is clipped at 500 runes and data_type is string, json " +
	"(a serialized object/array), object (an unserializable object) or other. Reset with " +
	"page_act action=postmessage_clear, or pass target=clear for the same envelope {\"cleared\":N}), " +
	"storage (read-only browser-context snapshot: cookies with flags plus " +
	"local_storage, session_storage, indexed_db and caches. Every section is " +
	"present even when empty and capped with its own *_truncated counter. " +
	"Every value is <redacted> unless include_values is true, which exposes " +
	"live session tokens. It never writes cookies), " +
	"har (HAR 1.2 export of the observed network ring: " +
	"{log:{version,creator,pages,entries[]}} where each entry carries request " +
	"and response headers, status, mimeType, size, timings and httpVersion " +
	"(the CDP protocol, HTTP/1.1 when unknown); content.text carries the " +
	"response body, content.encoding is base64 for a binary body and " +
	"content.size is the decoded byte count; request.postData carries " +
	"{mimeType,text} on a request with a body; serverIPAddress names the peer " +
	"IP Chrome reported, omitted when Chrome reported none; request.cookies " +
	"is snapshotted from the browser jar and matched to each request URL by " +
	"domain, path and Secure scheme, while response.cookies is always empty " +
	"because Chrome omits Set-Cookie from CDP events. Redaction covers header " +
	"values and cookie values only, so Authorization and X-CSRF-Token read " +
	"<redacted> and no argument enables secret capture; response bodies are " +
	"verbatim and can echo a request cookie. The MCP export defaults to a " +
	"512 KiB cap, while the CLI har --out writes the uncapped export and says " +
	"Cap: none in log.comment; log.truncated counts the entries dropped to fit " +
	"the cap plus the entries the network ring evicted. Read-only, " +
	"never arms capture), " +
	"or meta. Pass the session_id " +
	"returned by fetch_page plus what=<aspect>. When more than one browser is " +
	"live, pass browser_id or owner to declare which browser holds the session; " +
	"with one live browser both may be omitted, and a session held outside the " +
	"declared owner is refused as not_found. Use selector to scope " +
	"extraction, target to fetch exactly one css/js resource's full content " +
	"(instead of the resource list), full_page to capture the whole " +
	"scrollable page in a screenshot (default: visible viewport; full-page " +
	"captures of tall/heavy pages get an extended internal budget of ~90s " +
	"and may still time out on extreme pages), max_bytes to cap the " +
	"response size, and timeout_ms to bound the call (default 30000, " +
	"clamped to 1..120000; a full-page screenshot keeps its 90s floor " +
	"unless you ask for more). A selector that matches nothing returns " +
	"element_not_found naming the selector. Example: " +
	"{ \"session_id\": \"page-1\", \"what\": \"dom\", \"max_bytes\": 8000 } or " +
	"{ \"session_id\": \"page-1\", \"what\": \"screenshot\", \"full_page\": true, " +
	"\"timeout_ms\": 60000 }, " +
	"{ \"session_id\": \"page-1\", \"what\": \"network\", \"filter\": \"document,script\" } " +
	"for signal-only traffic, or { \"session_id\": \"page-1\", \"what\": \"sinks\", " +
	"\"filter\": \"canary\" } for hits carrying the injected canary. " +
	"Numbers may be sent as JSON numbers or as numeric strings; a non-numeric string is rejected. " +
	ResultEnvelopeDoc

// InspectPageDecompositionPromptDescription is the LLM-facing description of the InspectPageDecompositionPrompt tool.
const InspectPageDecompositionPromptDescription = "Provides structured guidance to decompose a live session into metadata, navigation links, and DOM tree snapshot."

// PageActDescription is the LLM-facing description of the Act tool.
const PageActDescription = "Interacts with a live page opened by fetch_page: click (supports button selection & double-click), " +
	"input, type, press_key (send keys/combos like Enter, Escape, Control+A, Meta+C), " +
	"click_at / hover_at (coordinate-based mouse actions), drag_and_drop (mouse drag), " +
	"upload_file (set files on <input type='file'>), eval (run JS — the primary escape hatch), " +
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

// BrowserCreateDescription is the LLM-facing description of the BrowserCreate tool.
const BrowserCreateDescription = "Creates or reuses a live Chromium browser and returns its browser_id. " +
	"Idempotent: calling again in the same session with identical options gets the same " +
	"browser_id back, without launching a second process. Set owner to claim your own " +
	"browser when several agents share one session: it joins the create key, so the same " +
	"owner with the same options reuses one browser and a different owner gets a separate " +
	"one. Browsers are EPHEMERAL by default: the profile lives under " +
	"~/.config/browser-mcp/profiles/.ephemeral and is deleted when the browser closes, so " +
	"close browsers you are done with (browser_close) and idle ones expire automatically. " +
	"Pass persist=true (with an optional profile name) to keep a persistent profile and its " +
	"logins across restarts. A bare profile name resolves under " +
	"~/.config/browser-mcp/profiles; relative paths are rejected. Proxy: pass a local proxy " +
	"such as 127.0.0.1:8080 for intercept tools; credentials never go in an argument — put " +
	"them in ~/.config/browser-mcp/proxy.json (mode 0600). " +
	"Headers: pass headers to send an identity header on EVERY request from every page of " +
	"this browser, e.g. {\"X-Bug-Bounty\":\"your-handle\"}. Scope: pass scope as the request " +
	"allowlist (bare hosts also cover their subdomains, '*.example.com' works, 'host:port' is " +
	"accepted). No scope means no limit; scope_strict=true with an empty scope denies every " +
	"request. Scope now governs navigation, replay, redirect hops and intercepted subresources: " +
	"an out-of-scope request is refused before it is sent. Rate limit: pass rps and burst to cap " +
	"requests per host (default 5/s with a burst of 10); a throttled request waits, and one whose " +
	"deadline cannot fit the wait fails as timeout. The effective policy is reported as limits. " +
	"Example: { \"owner\": \"scraper-a\" } and { \"owner\": \"scraper-b\" } get two browsers; " +
	"{ \"persist\": true, \"profile\": \"audit\" } keeps a profile; " +
	"{ \"headers\": {\"X-Bug-Bounty\":\"me\"}, \"scope\": [\"example.com\", \"*.iana.org\"], " +
	"\"rps\": 2, \"burst\": 4 } confines navigation and spaces requests. " +
	"Chromium switches: pass chromium_args to append switches for this browser only, e.g. " +
	"[\"--disable-gpu\"], on top of the built-in quiet set and [browser] chromium_flags." +
	ResultEnvelopeDoc

// BrowserListDescription is the LLM-facing description of the BrowserList tool.
const BrowserListDescription = "Lists live browsers with their lifecycle state as compact JSON: browser_id, " +
	"owner, bin, profile_dir, persist, ephemeral, created_at, idle_for (how long the " +
	"browser has been unused), ttl (when idle cleanup closes it), scope (the request " +
	"allowlist: \"open\" when unrestricted), scope_strict (whether an empty allowlist " +
	"denies all requests), limits (the effective per-host budget: {rps, burst, hosts:[{host, " +
	"throttled}]}) and pages, each {id, url, title?, context?, stale?}. " +
	"Returns [] when none are running. Use it to see what you own and what to close; " +
	"idle browsers expire automatically. Example: {}." + ResultEnvelopeDoc

// BrowserCloseDescription is the LLM-facing description of the BrowserClose tool.
const BrowserCloseDescription = "Closes a live browser and all of its pages, releasing the Chromium process. " +
	"Pass browser_id to close one browser, or omit to close all. purge=true also " +
	"deletes that browser's profile directory (guarded: only paths under " +
	"~/.config/browser-mcp/profiles; this is the only way to delete a named persistent " +
	"profile). prune=true (with no browser_id) closes all browsers and deletes leftover " +
	"profiles under profiles/.ephemeral that no live browser owns — named persistent " +
	"profiles are never deleted by prune. Idle browsers expire on their own, but close " +
	"explicitly when done; an unknown browser_id returns a not-found error listing live " +
	"ids and owners. Example: { \"browser_id\": \"browser-2\", \"purge\": true }." +
	ResultEnvelopeDoc

// PageListDescription is the LLM-facing description of the PageList tool.
const PageListDescription = "Lists every live page of the given browser as a JSON array. Each " +
	"entry is {id, url, context, title?, stale?}: url and title are read " +
	"from the live tab, so they follow navigations and redirects; context " +
	"is the incognito identity ('default' for the browser's own cookie " +
	"jar); stale marks an entry whose tab was busy or unreadable, in which " +
	"case url is the last known value. Use it to see which sessions/tabs " +
	"are still open inside the browser before closing them with " +
	"session_close. An unknown browser_id returns a not-found error listing " +
	"the running ids. " +
	"Example: { \"browser_id\": \"browser-1\" }." + ResultEnvelopeDoc

// BrowserSetupAndEmulatePromptDescription is the LLM-facing description of the BrowserSetupAndEmulatePrompt tool.
const BrowserSetupAndEmulatePromptDescription = "Provides guidance for initializing a browser session and setting up optional device emulation."

// SessionCloseDescription is the LLM-facing description of the SessionClose tool.
const SessionCloseDescription = "Closes a live browser session opened by fetch_page, releasing its " +
	"browser tab. When more than one browser is live, pass browser_id or owner " +
	"to declare which browser holds the session; with one live browser both may " +
	"be omitted, and a session held outside the declared owner is refused as " +
	"not_found with the holding browser named and its content untouched. The id " +
	"is resolved from the live browser registry without probing the tab, then closed through the " +
	"browser that owns it, without launching a browser. A page an operation " +
	"still holds is evicted from the session registry after a short bound, " +
	"and its tab is left to the browser teardown. Closing a page that opened " +
	"popups also closes those popups. The session_id becomes " +
	"invalid afterwards. Use session_list to see which sessions are still " +
	"open; an unknown id fails and names the live ids. This never launches a " +
	"browser and never touches a browser that does not own the session. " +
	"Example: { \"session_id\": \"page-1\" }." + ResultEnvelopeDoc

// SessionListDescription is the LLM-facing description of the SessionList tool.
const SessionListDescription = "Lists every live browser session across every live browser and " +
	"identity as a JSON array. Each entry is {id, url, opener?, context, " +
	"title?, stale?}: url and title are read from the live tab, so they " +
	"follow navigations and redirects; opener is the id of the page that " +
	"opened this popup (a target=\"_blank\" link or window.open), absent for " +
	"a top-level page; context is the incognito identity " +
	"('default' for the browser's own cookie jar); stale marks an entry " +
	"whose tab was busy or unreadable, in which case url is the last known " +
	"value. Use it to see which fetch_page sessions are still open before " +
	"closing them with session_close. Pass identity to list one identity " +
	"only; an unknown identity reports the live names and creates nothing. " +
	"It never launches a browser: with none running it returns an empty " +
	"array. Example: {}." + ResultEnvelopeDoc

// ManageSessionLifecyclePromptDescription is the LLM-facing description of the ManageSessionLifecyclePrompt tool.
const ManageSessionLifecyclePromptDescription = "Provides guidance for inspecting active sessions and releasing unused browser tabs."

// CaidoSearchDescription is the LLM-facing description of the CaidoSearch tool.
const CaidoSearchDescription = "Reads the Caido proxy history over GraphQL and returns HTTP transactions " +
	"as JSON. Caido must run on the same host: the proxy listens on " +
	"127.0.0.1:8081, while the UI and GraphQL API listen on 127.0.0.1:8080. " +
	"The tool reads recorded history and never changes, replays, or blocks " +
	"traffic. " +
	"Auth: access_token (a direct instance token copied from the Caido UI " +
	"console) is tried as the bearer first, then a PAT, then a guest session " +
	"when allow_guest is on (the default). A rejected credential falls " +
	"through to the next step, so a bad PAT still yields a guest session. " +
	"Guest history exists only while the Caido instance runs. " +
	"Only method and status filter on the server; url and since filter here. " +
	"Every call scans the newest 500 rows (5 pages of 100) at any limit, " +
	"newest first, so older history is not reachable. count is the rows " +
	"returned. total is the server count for the method and status filter " +
	"only, so it ignores url and since. scanned is the rows examined. " +
	"dropped is the rows that failed the url or since filter. " +
	"limit clamps at 100 and body_bytes clamps at 65536, reported as " +
	"limit_applied and body_bytes_applied only when clamped. " +
	"body_bytes 0 returns metadata and headers with empty bodies and no " +
	"request_cut or response_cut. request_cut and response_cut appear only " +
	"when a returned body was shortened, or dropped with its headers because " +
	"the captured message exceeds 8 MiB. " +
	"Redaction is on unless raw is true. It covers credential-shaped header " +
	"names, credential-shaped keys in JSON, form, XML and plain bodies, and " +
	"credential-shaped query parameters in the url, which sets url_redacted. " +
	"Transport and CORS headers stay readable. " +
	"bytes is the request size. response_bytes is the raw captured response " +
	"size, which includes its headers and can exceed content-length. part " +
	"selects which side carries a body: request, response, or both. " +
	"Results are newest first: {entries:[...],count,total,scanned,dropped}. " +
	"Empty history returns entries:[] and is not an error. " +
	"Example: {} lists the newest 20 transactions. Example: " +
	`{"url":"/api","method":"POST","status":200,"since":3600,` +
	`"limit":50,"body_bytes":4000,"part":"both"}.` + ResultEnvelopeDoc
