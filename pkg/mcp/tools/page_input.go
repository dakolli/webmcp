package tools

import (
	"time"

	"github.com/dakolli/webmcp/pkg/cli/pageops"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

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
	"reflections (hunts a marker across the live DOM, the serialized outerHTML and " +
	"document.URL: {marker,count,truncated,hits:[{where,selector,context,snippet," +
	"survived,risk}],sinks_correlated?}. where is dom-text, attr, comment, script-text, " +
	"style-text or url; context is html-text, attr-double, attr-single, attr-unquoted, " +
	"js-string, js-template, css-value, url or comment; survived lists the marker's " +
	"special characters that reached the DOM unescaped; risk is dangerous, escaped or " +
	"inert. Pass the marker as target, scope with selector, and arm sinks_install to " +
	"correlate tracer hits. Read-only, never mutates), " +
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

// newPageInputSchema returns the input schema of the page_fetch tool.
func newPageInputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by fetch_page for the page to read.",
			},
			"browser_id": map[string]any{
				"type":        "string",
				"description": "Optional browser_id to resolve the session inside that browser only (e.g. 'browser-2'). Use it when more than one browser is live; with exactly one live browser it may be omitted.",
			},
			"owner": map[string]any{
				"type":        "string",
				"description": "Optional owner label (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-') restricting the lookup to browsers with that owner. A session held by another owner is not_found, never returned.",
			},
			"what": map[string]any{
				"type":        "string",
				"enum":        append([]string(nil), aspectNames...),
				"description": "The aspect of the page to extract: html (outerHTML), dom (flattened DOM snapshot; every element node carries a g<document>-b<backendNodeId> handle usable as page_act's handle arg; the document token makes handles from a previous document stale after navigation), text (visible text), css (stylesheet resources, including inline <style> blocks reported with type inline-style), js (script resources), links (anchor hrefs, each with a handle where one matched), screenshot (a metadata text block {mime_type, width, height, bytes, full_page} plus the PNG as a separate image block with mimeType. full_page captures the whole scrollable page. A client advertising imageContent=false gets the base64 data URI in data instead — e.g. what: \"screenshot\" → {\"ok\":true,\"data\":{\"mime_type\":\"image/png\",\"width\":1280,\"height\":800,\"bytes\":42133,\"full_page\":false}} plus an image block), interactives (dense action map of every visible clickable/typeable element — role, text, href, type, plus x/y as the element centre and width/height in page-absolute CSS px; the top-left is left = x - width/2, top = y - height/2, while marks reports top-left, so the two differ; each entry also carries box {x,y,w,h}, and all coordinates are page-absolute, so a child iframe's entries feed click_at directly, while frame_url marks an entry that came from a child frame. Each entry carries a stable handle usable as page_act's handle arg. Budgets: the root document returns up to 200 entries, each child frame up to 50, and all child frames together up to 150, so one response never exceeds 350; truncated is the root remainder only, frames_truncated is the total remainder across child frames, and frames has one row per child frame {url,count,truncated} (always present, [] when the page has no child frames). selector scopes the aspect: a selector on an <iframe> returns that frame's entries with page-absolute coordinates, any other selector returns only root entries inside its subtree with no frame rows, and a selector that matches nothing returns element_not_found. A frame-scoped read returns only the selected frame's row, and the cut appears in that row's truncated and in frames_truncated, not in truncated), a11y (flattened accessibility tree in DFS order: computed role/name/value plus the state flags checked, expanded, selected, disabled, required, pressed, invalid, focused, and depth — e.g. what: \"a11y\" → {\"nodes\":[{\"role\":\"button\",\"name\":\"Sign in\",\"handle\":\"g3k9x2ab-b6\",…}],\"count\":N,\"truncated\":M}; DOM-backed nodes carry a handle usable as page_act's handle arg; root frame only — iframes are separate trees; selector/target/full_page are ignored), network/console (bounded log envelope: what: \"network\" → {\"entries\":[{\"method\":\"GET\",\"url\":\"…\",\"status\":200,…}],\"count\":N,\"dropped\":D,\"truncated\":M}; entries are the newest 100 buffered, oldest first, and dropped counts true ring overflow; each network entry carries frame_id and loader_id when CDP reported them, and top_document marks the request for the page's current top-level document; capture is cumulative from page creation — filter client-side or call page_act action clear_logs right before the step under observation; selector/target/full_page are ignored), article (the readable article body as markdown: {title, byline?, excerpt?, text, words, truncated, url} — the densest article/main root with page chrome removed and headings, paragraphs, lists, code, quotes, links, images and tables rendered; text is capped at 40000 runes and truncated reports the cut; prefer it over text for prose — selector/target/full_page are ignored; e.g. what: \"article\" → {\"title\":\"Example\",\"byline\":\"Ada\",\"text\":\"# Heading …\",\"words\":812,\"truncated\":false,\"url\":\"https://example.com/a\"}; example: {\"session_id\":\"page-1\",\"what\":\"article\"}), marks (numbered set-of-marks: the text block is {marks:[{index, role, text?, x, y, width, height}], viewport_width, viewport_height, truncated} and the PNG is a separate image block with mimeType — one numbered box per visible interactive element; x/y/width/height are viewport CSS px and x/y can be passed straight to page_act click_at; capped at the first 60 marks with truncated counting the rest — selector/target/full_page are ignored; e.g. what: \"marks\" → text {\"marks\":[{\"index\":1,\"role\":\"a\",\"text\":\"Sign in\",\"x\":120.5,\"y\":48,\"width\":88,\"height\":32}],\"viewport_width\":1280,\"viewport_height\":800,\"truncated\":0} plus the annotated PNG image block. For a text-only client the data URI comes back in screenshot); example: {\"session_id\":\"page-1\",\"what\":\"marks\"}), security (passive audit: {checks:[{id,status,evidence,detail}],count} covering response headers, CSP (header or meta), cookie flags, mixed content, subresource integrity, form targets and CORS — selector/target/full_page are ignored; e.g. what: \"security\" → {\"checks\":[{\"id\":\"csp\",\"status\":\"warn\",\"evidence\":\"\",\"detail\":\"…\"}],\"count\":12}), sinks (source-to-sink tracer ring: {installed, hits:[{source, sink, context, value, value_cut, stack, selector, ts}], dropped, truncated}. It is read-only and never arms capture — capture starts only with page_act action=sinks_install and survives client-side route changes and full navigations; the ring is per document, so a full navigation resets it. The newest 100 hits of a 200-hit ring are emitted, each value is clipped at 200 runes (value_cut marks it) and each stack keeps 5 frames — selector/target/full_page are ignored; e.g. what: \"sinks\" → {\"installed\":true,\"hits\":[{\"source\":\"location.hash\",\"sink\":\"innerHTML\",\"context\":\"html\",\"value\":\"#canary\",\"stack\":[\"anonymous https://example.com/:12\"],\"selector\":\"#out\",\"ts\":1700000000000}],\"dropped\":0,\"truncated\":0}), postmessage (postMessage recorder ring: {installed, messages:[{origin, source_kind, data, data_type, ts, stack}], dropped, truncated, frames:[{url, probed}]}. Read-only; arm it with page_act action=postmessage_install and reset it with page_act action=postmessage_clear or target=clear (which returns {\"cleared\":N}). Messages are aggregated across up to 20 frames, data is clipped at 500 runes, data_type is string, json (a serialized object/array), object (an unserializable object) or other — selector/full_page are ignored; target=clear is the only target value), websocket (the WebSocket frame ring: {entries, count, dropped, truncated}; entries are the newest 100 frames of a 200-frame ring, oldest first, each carrying seq, socket_id, url, direction, opcode, payload, payload_bytes, payload_cut, status and ms. count is the emitted count, at most 100, so count + truncated + dropped equals the frames observed; dropped counts ring evictions when the 200-frame ring turns over and can exceed 200. direction is sent or received; opcode is 1 text, 2 binary, 8 close, 9 ping or 10 pong; socket_id is the CDP request id of the connection, so frames of one socket share it. payload holds UTF-8 text and is clipped at 2000 runes with payload_cut set on a cut, while a binary frame reports only payload_bytes and leaves payload empty; secrets are redacted at capture; status is 0, and ms is 0, when the open event was missed. Capture starts before the first document load and is cumulative: call page_act action clear_logs to reset, then act, then read. selector/target/full_page are ignored and filter does not apply; e.g. what: \"websocket\" → {\"entries\":[{\"seq\":1,\"socket_id\":\"123.4\",\"url\":\"wss://example.com/ws\",\"direction\":\"received\",\"opcode\":1,\"payload\":\"{\\\"type\\\":\\\"tick\\\"}\",\"payload_bytes\":14,\"payload_cut\":false,\"status\":101,\"ms\":42}],\"count\":1,\"dropped\":0,\"truncated\":0}), meta (title/url/description), storage (read-only snapshot of the page's browser context: cookies with name, value, domain, path, secure, http_only, same_site, expires and size, plus local_storage and session_storage [{key, value_length, value}], indexed_db [{name, stores}] and caches [name]. Every section is present even when empty and capped at 200 cookies, 200 keys per store, 20 databases and 40 caches, each with its own *_truncated counter. Values are the literal <redacted> unless include_values=true, which exposes live tokens. It never writes cookies. selector/target/full_page are ignored. e.g. what: \"storage\" → {\"cookies\":[{\"name\":\"sid\",\"value\":\"<redacted>\",\"domain\":\"example.com\",\"path\":\"/\",\"secure\":true,\"http_only\":true,\"same_site\":\"Lax\",\"expires\":-1,\"size\":42}],\"local_storage\":[{\"key\":\"token\",\"value_length\":32,\"value\":\"<redacted>\"}],...}. A page_closed error means the session expired, re-fetch the page), har (HAR 1.2 export of the observed network ring: {log:{version,creator,pages,entries[]}} where each entry carries request and response headers, status, mimeType, size, timings and httpVersion (the CDP protocol, HTTP/1.1 when unknown); content.text carries the response body, content.encoding is base64 for a binary body and content.size is the decoded byte count; request.postData carries {mimeType,text} on a request with a body; serverIPAddress names the peer IP Chrome reported, omitted when Chrome reported none; request.cookies is snapshotted from the browser jar and matched to each request URL by domain, path and Secure scheme, while response.cookies is always empty because Chrome omits Set-Cookie from CDP events. Redaction covers header values and cookie values only, so Authorization and X-CSRF-Token read <redacted> and no argument enables secret capture; response bodies are verbatim and can echo a request cookie. The MCP export defaults to a 512 KiB cap, while the CLI har --out writes the uncapped export and says Cap: none in log.comment; log.truncated counts only the entries dropped to fit the cap — entries lost to the network ring's own bound are not counted yet and will be reported separately after WEB-168. Read-only, never arms capture), dialogs (the JavaScript-dialog and HTTP-auth ring as {\"entries\":[...],\"count\",\"dropped\",\"truncated\",\"policy\"}: the newest 100 records of a 50-dialog ring, oldest first, and truncated is always 0 because the 50-entry ring never reaches the 100-entry emit cap. Each entry carries seq, type, message, message_cut, default_prompt, url, policy, prompt_text and ms; type is alert, confirm, prompt, beforeunload or auth, and an auth entry is a rejected HTTP basic-auth challenge. The engine answers every dialog as it opens, and the default policy is dismiss. The envelope's policy answers the next dialog, while each entry's policy is the one that answered it. This read never changes the policy; use page_act dialog_accept or dialog_dismiss to change it. clear_logs empties the ring), mutations (the DOM mutation timeline: {\"entries\":[{\"seq\",\"ts\",\"kind\",\"target\",\"attribute\",\"added\",\"removed\",\"text\"}],\"count\",\"dropped\",\"truncated\",\"last_seq\"} over the newest 100 batches of a 500-batch per-page ring. Capture starts with the page and survives navigation, so it never needs arming. The two-read recipe brackets an action: read what=mutations and note last_seq, run the page_act call, then read what=mutations again with since=<last_seq>; every entry the action caused has seq > last_seq. kind is attributes, characterData or childList; target is a short CSS path clipped at 120 runes; attribute names the changed attribute; added and removed are node counts; text is the new characterData text clipped at 120 runes. selector keeps only entries whose target contains it, and dropped counts batches the ring evicted). article, marks, security, storage, sinks, websocket, dialogs, har, mutations and reflections bypass max_bytes truncation. reflections hunts the target marker across the live DOM, the serialized document.documentElement.outerHTML and document.URL and returns {marker,count,truncated,hits:[{where,selector,context,snippet,survived,risk}],sinks_correlated?}; where is dom-text, attr, comment, script-text, style-text or url, context is html-text, attr-double, attr-single, attr-unquoted, js-string, js-template, css-value, url or comment, survived lists which of < > \" ' & / the marker kept unescaped, and risk is dangerous, escaped or inert; selector scopes it and target carries the marker. dom rejects selector (use what=html with selector for a scoped read); links, meta, a11y, marks, security and article ignore it; html, text, interactives and reflections honour it.",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "Optional CSS selector scoping the extraction. html/text scope to the matching element; interactives scopes to the matching element's subtree, where a selector on an iframe returns that frame's entries with page-absolute coordinates and any other selector returns only root entries inside the subtree with no frame rows; reflections scopes the marker scan to the subtree. dom rejects selector (it fails as invalid_args; use html for a scoped read). A selector that matches nothing returns element_not_found.",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"default":     pageops.DefaultPageMaxBytes,
				"description": "Maximum response size in bytes (default 50000).",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"default":     int(defaultPageTimeout / time.Millisecond),
				"description": "Per-call timeout in milliseconds (default 30000, clamped to 1..120000). A full-page screenshot keeps its 90s capture floor unless timeout_ms is larger.",
			},
			"full_page": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "Capture the entire scrollable page instead of the visible viewport. Tall/heavy pages get an extended internal capture budget (~90s) and may still time out on extreme pages.",
			},
			"target": map[string]any{
				"type":        "string",
				"description": "Resource URL to fetch exactly one css/js resource (instead of listing). Use with what=css|js. With what=reflections, the marker string to hunt (for example wm-probe-1a2b).",
			},
			"include_values": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "With what=storage, return real cookie and local_storage/session_storage values instead of the literal <redacted>. Default false. true exposes live session tokens, so set it only when the values themselves are the question. Example: { \"session_id\": \"page-1\", \"what\": \"storage\", \"include_values\": true }. If the report still shows <redacted>, include_values was not set on this call.",
			},
			"filter": map[string]any{
				"type":        "string",
				"description": "Narrows what=network or what=sinks; every other aspect ignores it. what=network: a comma list of request types matched case-insensitively against each entry's type — a bare name keeps that type, a '-' prefix drops it. filter=\"document,script,xhr\" keeps only those, filter=\"-image,-font\" drops images and fonts. Accepted names: document, stylesheet, image, media, font, script, texttrack, xhr, fetch, prefetch, eventsource, websocket, manifest, signedexchange, ping, cspviolationreport, preflight, other; an unknown name fails as invalid_args and lists them. what=sinks: canary (only hits whose value holds the injected wm-canary-… marker) or known (drop hits whose source is \"unknown\"). A filtered read adds filtered_out, the number of entries the filter dropped, so count=0 with filtered_out>0 means nothing matched rather than nothing captured; an unfiltered read never carries filtered_out. Examples: { \"session_id\": \"page-1\", \"what\": \"network\", \"filter\": \"document,script\" } and { \"session_id\": \"page-1\", \"what\": \"sinks\", \"filter\": \"canary\" }.",
			},
			"since": map[string]any{
				"type":        "integer",
				"default":     0,
				"description": "With what=mutations, return only entries whose seq is greater than this value. Read what=mutations before an action, note the last_seq in the envelope, run your page_act call, then read what=mutations again with since=<that last_seq>: the two reads bracket the action and the second one lists exactly what it changed. Default 0 returns every buffered entry. Other aspects ignore it.",
			},
		},
		Required: []string{"session_id", "what"},
	}
}
