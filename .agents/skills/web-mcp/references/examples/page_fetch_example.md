> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# page_fetch example

Use this file only when tool calling for `page_fetch` fails. The tool description is primary. Your ids and caps can differ. Every result rides the envelope: `{"ok":true,"data":<payload>}` on success, `{"ok":false,"error":{"type":…,"message":…,"hint":…}}` on failure. Reference: `.agents/skills/web-mcp/references/tools/page_fetch.md`.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expect `{"browser_id":"browser-20"}` in `data`. `persist:false` gave an ephemeral profile. Use only your id and never close an empty id.

## 2. Open the page and read meta

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-20","wait_for":"idle"}}
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"meta","max_bytes":4000}}
```

Expect `{"session_id":"page-1","title":"Web browser - Wikipedia","html_bytes":614792}`, then `{"title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","meta":[{"charset":"UTF-8"}]}`. A bare `what=dom` here returns `{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`, so use `article`.

## 3. Scope text with a selector

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"text","selector":"h1#firstHeading"}}
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"bogus"}}
```

Expect the heading text, then `{"ok":false,"error":{"type":"invalid_args","message":"page_fetch: unknown what=\"bogus\" (must be one of <the live enum>)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}`.

## 4. Fetch one CSS file with target

List `css` first when you need a URL, then copy a `url` exactly:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"css","target":"https://en.wikipedia.org/w/load.php?lang=en&modules=site.styles&only=styles&skin=vector-2022","max_bytes":1200}}
```

Expect a `// url=` line, a `// bytes=6881` line, then raw CSS. `js` works the same way. An inline `<style>` block is listed as `inline-style[1]` with no real URL, and `target` cannot reach it:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"css","target":"inline-style[1]"}}
```

Expect `page_fetch: resource "inline-style[1]" not found in the page's resource tree: {-32000 No resource with given URL found }`. Read the block with `page_act` `eval` instead.

## 5. Read the action map

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"interactives"}}
```

Expect `{"interactives":[{"handle":"gy1g0vstq-b49","role":"button","text":"Main menu","type":"checkbox","x":34,"y":33,"width":68,"height":26,"box":{"x":0,"y":20,"w":68,"h":26},"visible":true}],"truncated":335,"frames":[],"frames_truncated":0}`. Every coordinate is page-absolute CSS px, `box.x + width/2 == x`, and a child-frame entry carries `frame_url`. The root returns 200 entries, each child frame 50, and all child frames together 150.

## 6. Audit the page with what=security

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"security"}}
```

Expect `data` to hold `{"url":…,"checks":[…],"count":N,"truncated":0}`, each check shaped `{"id":"csp","status":"ok","evidence":"…","detail":"…"}`. The eight passive `csp_bypass_*` probes report policy weakness, not exploitability.

## 7. Attribute traffic to one action

```json
{"tool":"page_act","arguments":{"session_id":"page-3","action":"clear_logs"}}
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network"}}
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network","filter":"document,stylesheet,script"}}
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network","filter":"xhr,fetch"}}
```

Expect `{"network":39,"console":0,"websocket":0}`, then entries with `frame_id`, `loader_id`, and `top_document:true` on the document request only. `seq` keeps its count after `clear_logs`, so it proves the action caused the request. The filtered reads keep only those types and add `filtered_out`. `count:0` with `filtered_out>0` means nothing matched, not that nothing was captured. The drop form is `filter:"-image,-font"`, and an unknown name fails as `invalid_args` with the accepted list.

## 8. Read headers, one body, and redaction

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"click","selector":"form button"}}
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"network"}}
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"body","target":"httpbin.org/post"}}
```

Expect a `POST` entry for `https://httpbin.org/post` with `"status":200` and `"req_headers":{…}`, then `{"seq":N,"method":"POST","url":"…","status":200,"content_type":"application/json","bytes":1143,"body":"{\"args\":{},…}"}`. Binary content returns `base64`, and bodies are evicted quickly, so read at once. `network` shows `"Cookie":"<redacted>"`; `Authorization` and `X-CSRF-Token` redact the same way, and `Set-Cookie` is absent because Chrome omits it from CDP events.

## 9. Read WebSocket frames

```json
{"tool":"page_act","arguments":{"session_id":"page-5","action":"clear_logs"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"eval","value":"() => new Promise((resolve) => { const ws = new WebSocket('wss://ws.postman-echo.com/raw'); ws.onopen = () => ws.send('hello from web-mcp'); ws.onmessage = (event) => { resolve(event.data) }; setTimeout(() => resolve('timeout'), 5000) })"}}
{"tool":"page_fetch","arguments":{"session_id":"page-5","what":"websocket"}}
```

Expect `{"network":N,"console":M,"websocket":0}`, then `"hello from web-mcp"`, then `{"entries":[{"seq":1,"socket_id":"…","url":"wss://ws.postman-echo.com/raw","direction":"sent","opcode":1,"payload":"hello from web-mcp","payload_bytes":18,"payload_cut":false,"status":101,"ms":0},…],"count":2,"dropped":0,"truncated":0}`. A binary frame carries an empty `payload` and its size in `payload_bytes`.

## 10. Capture a screenshot as two blocks

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"screenshot","full_page":false}}
```

Expect two blocks in `result.content`. The text block is `{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}` and the next is `{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}`. `marks` returns the mark table as text plus the annotated PNG. A client that sends `capabilities.imageContent:false` gets the data URI under `data`.

## 11. Trace a source to a sink

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"sinks_install"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"eval","value":"() => { const d=document.createElement('div'); d.innerHTML=location.hash; document.body.appendChild(d); return d.textContent }"}}
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"sinks"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"sinks_clear"}}
```

Expect `{"installed":true}`, then `{"installed":true,"hits":[{"source":"location.hash","sink":"innerHTML","context":"html","value":"#wm-canary-160","stack":[…],"selector":"…","ts":…}],"dropped":0,"truncated":0}`, then `{"cleared":1}`. `filter:"canary"` keeps marker hits and adds `filtered_out`; `filter:"known"` drops `source:"unknown"`.

## 12. Export HAR

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"har"}}
```

Expect `{"log":{"version":"1.2","creator":{"name":"web-mcp","version":"1.0"},"pages":[…],"entries":[{"request":{"method":"GET","url":"https://…","httpVersion":"http/1.1","headers":[…],"cookies":[…]},"response":{"status":200,"content":{"size":1256,"mimeType":"text/html","text":"<!doctype html>…"},"cookies":[]},…}],"comment":"… Cap 524288 bytes. …","truncated":0,"bodies_skipped":0}}`. `response.cookies` is always `[]`, a binary body sets `content.encoding:"base64"`, and header and cookie values read `<redacted>` while bodies stay verbatim.

## 13. Hunt a reflected marker

The marker is your input, so use one unlikely token. Open `https://www.w3.org/?wm307=wm307probe`, then read `reflections`:

```json
{"tool":"fetch_page","arguments":{"url":"https://www.w3.org/?wm307=wm307probe","browser_id":"browser-20","wait_for":"idle"}}
{"tool":"page_fetch","arguments":{"session_id":"page-7","what":"reflections","target":"wm307probe"}}
```

Expect `{"marker":"wm307probe","count":4,"truncated":0,"hits":[{"where":"url","context":"url","snippet":"https://www.w3.org/?wm307=wm307probe","survived":[],"risk":"inert"},{"where":"attr","selector":"html > head > meta:nth-of-type(5)","attribute":"content","context":"attr-double","snippet":"…","survived":[],"risk":"inert"}]}`. Three of the four hits are `attr` hits, from `meta:nth-of-type(5)`, `meta:nth-of-type(15)`, and `a#account-login-link`. A marker with no hits returns `{"marker":"wm307-absent","count":0,"truncated":0,"hits":[]}`, not an error.

`selector` scopes the scan, `survived` lists the marker's own special characters that stayed raw, and `risk` is `dangerous`, `escaped`, or `inert`. `sinks_correlated` appears only after `sinks_install`. A marker with `<` or `"` is percent-encoded in the URL, so run `encode` first or hunt a marker with no special characters.

Verified: 2026-10-30 https://www.w3.org

## 14. Errors and cleanup

A bad selector on `text` returns `{"ok":false,"error":{"type":"element_not_found","message":"page_fetch: no element matched selector \"#does-not-exist-xyz\": cannot find element"}}`, tested live on 2026-10-26.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-20"}}
```

Close your own browser only. The ephemeral profile is removed on close. Confirm with `browser_list` that only other browsers remain.
