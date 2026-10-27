> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-27

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

Expect one entry with `"seq":40` and `"url":"https://react.dev/_next/static/chunks/616.bd871377a76d946f.js"`. `seq` keeps counting after `clear_logs`, so it proves the action caused the request.

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

## 10. Capture a screenshot as two blocks

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"screenshot","full_page":false}}
```

Expect two blocks in `result.content`. The text block is `{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}`. The next block is `{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}`. `marks` returns the mark table as text plus the annotated PNG. A client that sends `capabilities.imageContent:false` at `initialize` gets one text block instead. Then `data` holds the data URI.

## 11. Trace a source to a sink

Open a new page with a hash, arm, load the hash into `innerHTML`, read, clear:

```json
{"tool":"fetch_page","arguments":{"url":"https://www.iana.org/help/example-domains#wm-canary-160","browser_id":"browser-20","wait_for":"idle"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"sinks_install"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"eval","value":"() => { const d=document.createElement('div'); d.innerHTML=location.hash; document.body.appendChild(d); return d.textContent }"}}
{"tool":"page_fetch","arguments":{"session_id":"page-5","what":"sinks"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"sinks_clear"}}
```

Expect `{"installed":true}`, then `data` as `{"installed":true,"hits":[{"source":"location.hash","sink":"innerHTML","context":"html","value":"#wm-canary-160","stack":[…],"selector":"…","ts":…}],"dropped":0,"truncated":0}`, then `{"cleared":1}`. A read after the clear returns `hits:[]`.

## 12. Errors and cleanup

A bad selector on `text` returns `{"ok":false,"error":{"type":"element_not_found","message":"page_fetch: no element matched selector \"#does-not-exist-xyz\": cannot find element"}}`, tested live on 2026-10-26.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-20"}}
```

Close your own browser only. The ephemeral profile is removed on close. Confirm with `browser_list` that only other browsers remain.

Tool reference: `.agents/skills/web-mcp/references/tools/page_fetch.md`.
