> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-28

# page_fetch example

Use this file only when tool calling for `page_fetch` fails. The tool description is primary. Your ids and caps can differ. Every result rides the envelope: `{"ok":true,"data":<payload>}` on success, `{"ok":false,"error":{"type":…,"message":…,"hint":…}}` on failure. One success and one failure appear below in full envelope form. Reference: `.agents/skills/web-mcp/references/tools/page_fetch.md`.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expect `{"browser_id":"browser-20"}` in `data`. `persist:false` gave an ephemeral profile. Use only your id and never close an empty id.

## 2. Open the page and read meta

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-20","wait_for":"idle"}}
```

Expect `{"session_id":"page-1","title":"Web browser - Wikipedia","html_bytes":614792}`.

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"meta","max_bytes":4000}}
```

Expect `{"title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","meta":[{"charset":"UTF-8"}]}`.
A bare `what=dom` here knows only whole-document cuts and returns `{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`, so use `article` instead.

## 3. Scope text with a selector

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"text","selector":"h1#firstHeading"}}
```

Expect the heading text only. An unknown value fails the same way. The list below was read from the binary on 2026-10-27:

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_fetch: unknown what=\"bogus\" (must be one of html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, body, console, websocket, article, tables, forms, marks, security, sinks, postmessage, storage, har)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

## 4. Fetch one CSS file with target

List `css` first when you need a URL, then copy a `url` exactly.

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"css","target":"https://en.wikipedia.org/w/load.php?lang=en&modules=site.styles&only=styles&skin=vector-2022","max_bytes":1200}}
```

Expect a `// url=` line, a `// bytes=6881` line, then raw CSS. `js` works the same way.

An inline `<style>` block is listed as a label-only row, `inline-style[1]`, with no real URL. `target` fetches a file resource only, so it cannot reach that row:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"css","target":"inline-style[1]"}}
```

Expect `{"ok":false,"error":{"type":"unknown","message":"page_fetch: resource \"inline-style[1]\" not found in the page's resource tree: {-32000 No resource with given URL found }",…}}`. Read the block with `page_act` `eval` instead. Swap `'style'` for `'script'` to read an inline script:

```json
{"tool":"page_act","arguments":{"session_id":"page-1","action":"eval","value":"() => document.querySelectorAll('style')[0].textContent"}}
```

Expect the CSS text in `data`. `js` never lists inline `<script>` blocks, so `eval` is the only read for one.

## 5. Read the action map

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"interactives"}}
```

Expect `{"interactives":[{"handle":"gy1g0vstq-b49","role":"button","text":"Main menu","type":"checkbox","x":34,"y":33,"width":68,"height":26,"box":{"x":0,"y":20,"w":68,"h":26},"visible":true}],"truncated":335,"frames":[],"frames_truncated":0}`. Every coordinate is page-absolute CSS px. `box.x + width/2 == x` exactly. A child-frame entry also carries `frame_url`. The root returns up to 200 entries, each child frame up to 50, and all child frames together up to 150. `frames` reports each child frame's `{url,count,truncated}`. `truncated` is the root remainder only. Feed a mark's `x` and `y` from `marks` to `page_act` `click_at`.

## 6. Audit the page with what=security

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"security"}}
```

Expect `data` to hold `{"url":…,"checks":[…],"count":N,"truncated":0}`, each check shaped `{"id":"csp","status":"ok","evidence":"…","detail":"…"}`. On the 2026-10-26 live run against `https://go.dev/` the report returned `count: 20` with check ids `csp`, `csp_unsafe_inline`, `csp_unsafe_eval`, `hsts`, `x_frame_options`, `x_content_type_options`, `referrer_policy`, `permissions_policy`, `coop`, five `cookie:<name>` checks, `mixed_content`, `sri`, `form_target`, `password_autocomplete`, and `cors`. The current build adds eight policy probes: `csp_bypass_unsafe_inline`, `csp_bypass_unsafe_eval`, `csp_bypass_script_wildcard`, `csp_bypass_object_src`, `csp_bypass_base_uri`, `csp_bypass_frame_ancestors`, `csp_bypass_report_only`, and `csp_bypass_trusted_host`. They are passive (no injection, execution, or load), evidence quotes the real directive, and they report policy weakness rather than exploitability. See the reference for each status.

## 7. Attribute traffic to one action

Open `https://react.dev/learn` as `page-3`, call `page_act` `clear_logs` (expect
`{"network":39,"console":0,"websocket":0}`), act once, then read:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network"}}
```

Expect one entry with `"seq":40` and `"url":"https://react.dev/_next/static/chunks/616.bd871377a76d946f.js"`. Each entry also carries `frame_id` and `loader_id`, and only the page's document request sets `"top_document":true`. `seq` keeps its count after `clear_logs`, so it proves the action caused the request.

Filter the same ring twice, first to the page's own assets, then to its API traffic:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network","filter":"document,stylesheet,script"}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click","selector":"h1"}}
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network","filter":"xhr,fetch"}}
```

Expect only `Document`, `Stylesheet` and `Script` entries from the first read, and only `XHR` and `Fetch` entries from the last. Each filtered read adds `filtered_out` with the number it dropped. Every entry still carries its `status`. Chrome omits `bytes` and `ms` when it reports no size, for example a cache-served stylesheet. `count:0` with `filtered_out` above 0 means nothing matched, not that nothing was captured. The drop form is one line, `filter:"-image,-font"`. An unknown name fails as `invalid_args` with the accepted list.

## 8. Read headers, one body, and redaction

Open `https://httpbin.org/forms/post` as `page-4`. Clear the rings, then submit:

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"clear_logs"}}
{"tool":"page_act","arguments":{"session_id":"page-4","action":"click","selector":"form button"}}
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"network"}}
```

Expect `clicked selector="form button"` and a delta line `→ url=https://httpbin.org/post title="httpbin.org/post"` in `data`, then a `POST` entry for `https://httpbin.org/post` with `"status":200` and `"req_headers":{"Content-Type":"application/x-www-form-urlencoded",…}`. `network` carries no bodies, so read the body on demand with `target`:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"body","target":"httpbin.org/post"}}
```

Expect `{"seq":N,"method":"POST","url":"https://httpbin.org/post","status":200,"content_type":"application/json","bytes":1143,"body":"{\"args\":{},…}"}`. Binary content returns `base64`, and bodies are evicted quickly, so read at once.

`page-4` now holds httpbin cookies. Clear the rings and read the next request to check redaction.

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"clear_logs"}}
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"network"}}
```

Expect one httpbin entry with `"Cookie":"<redacted>"` in `req_headers`. `Authorization` and `X-CSRF-Token` redact the same way. `Set-Cookie` is absent, because Chrome omits it from CDP events.

## 9. Read WebSocket frames

Open a real page, clear the rings, then let `eval` open a socket and send one message. The promise resolves with the echoed frame, so the read runs after both frames land.

```json
{"tool":"fetch_page","arguments":{"url":"https://www.gnu.org/","browser_id":"browser-20","wait_for":"idle"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"clear_logs"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"eval","value":"() => new Promise((resolve) => { const ws = new WebSocket('wss://ws.postman-echo.com/raw'); ws.onopen = () => ws.send('hello from web-mcp'); ws.onmessage = (event) => { resolve(event.data) }; setTimeout(() => resolve('timeout'), 5000) })"}}
{"tool":"page_fetch","arguments":{"session_id":"page-5","what":"websocket"}}
```

Expect `{"network":N,"console":M,"websocket":0}`, then `"hello from web-mcp"`, then:

```json
{"ok":true,"data":{"entries":[{"seq":1,"socket_id":"…","url":"wss://ws.postman-echo.com/raw","direction":"sent","opcode":1,"payload":"hello from web-mcp","payload_bytes":18,"payload_cut":false,"status":101,"ms":0},{"seq":2,"socket_id":"…","url":"wss://ws.postman-echo.com/raw","direction":"received","opcode":1,"payload":"hello from web-mcp","payload_bytes":18,"payload_cut":false,"status":101,"ms":0}],"count":2,"dropped":0,"truncated":0}}
```

`status` is `101` from the observed handshake, and `ms` can be `0` even then. A binary frame carries an empty `payload` and its size in `payload_bytes`. The socket stays open, so the read holds only the sent and echoed frames. Read this aspect with no `filter` and no `target`. The CLI:

```sh
browser fetch https://www.gnu.org/ --wait idle
browser page act last clear_logs
browser page act last eval "() => new Promise((resolve) => { const ws = new WebSocket('wss://ws.postman-echo.com/raw'); ws.onopen = () => ws.send('hello from web-mcp'); ws.onmessage = (event) => { resolve(event.data) }; setTimeout(() => resolve('timeout'), 5000) })"
browser page fetch last websocket
```

## 10. Capture a screenshot as two blocks

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"screenshot","full_page":false}}
```

Expect two blocks in `result.content`. The text block is `{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}`. The next block is `{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}`. `marks` returns the mark table as text plus the annotated PNG. A client that sends `capabilities.imageContent:false` at `initialize` gets one text block instead. Then `data` holds the data URI.

## 11. Trace a source to a sink

Open a new page with a hash, arm, load the hash into `innerHTML`, read, clear:

```json
{"tool":"fetch_page","arguments":{"url":"https://www.iana.org/help/example-domains#wm-canary-160","browser_id":"browser-20","wait_for":"idle"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"sinks_install"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"eval","value":"() => { const d=document.createElement('div'); d.innerHTML=location.hash; document.body.appendChild(d); return d.textContent }"}}
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"sinks"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"sinks_clear"}}
```

Expect `{"installed":true}`, then `data` as `{"installed":true,"hits":[{"source":"location.hash","sink":"innerHTML","context":"html","value":"#wm-canary-160","stack":[…],"selector":"…","ts":…}],"dropped":0,"truncated":0}`, then `{"cleared":1}`. A read after the clear returns `hits:[]`.

Filter the ring to the canary:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"sinks","filter":"canary"}}
```

Expect the one hit whose `value` holds `wm-canary-`, plus `filtered_out` for the hits dropped. `filter: "known"` instead drops every hit whose `source` is `"unknown"`. `inject_canary` can return `hits: []` with no error when no instrumented sink received the marker. That is a normal result, not a failure.

## 12. Export HAR

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"har"}}
```

Expect `data` as `{"log":{"version":"1.2","creator":{"name":"web-mcp","version":"1.0"},"pages":[{"startedDateTime":"…","id":"page_1","title":"…","pageTimings":{"onContentLoad":-1,"onLoad":-1}}],"entries":[{"request":{"method":"GET","url":"https://…","httpVersion":"http/1.1","headers":[…],"cookies":[…]},"response":{"status":200,"httpVersion":"http/1.1","content":{"size":1256,"mimeType":"text/html","text":"<!doctype html>…"},"cookies":[]},"serverIPAddress":"203.0.113.7",…}],"comment":"web-mcp HAR 1.2 export. … Cap 524288 bytes. …","truncated":0,"bodies_skipped":0}}`. `response.cookies` is always `[]`, a binary body sets `content.encoding:"base64"`, and header and cookie values read `<redacted>` while bodies stay verbatim.

The CLI writes the same document uncapped and prints one line, never the body: `browser page fetch page-1 har --out traffic.har` prints `wrote traffic.har (42 entries)`. The file is mode 0600 and its `log.comment` says `Cap: none`.

## 13. Errors and cleanup

A bad selector on `text` returns `{"ok":false,"error":{"type":"element_not_found","message":"page_fetch: no element matched selector \"#does-not-exist-xyz\": cannot find element"}}`, tested live on 2026-10-26.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-20"}}
```

Close your own browser only. The ephemeral profile is removed on close. Confirm with `browser_list` that only other browsers remain.

Tool reference: `.agents/skills/web-mcp/references/tools/page_fetch.md`.
