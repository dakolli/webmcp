> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_fetch example

Use this file only when tool calling for `page_fetch` fails. The tool description is primary. Your ids and caps can differ. Every result rides the envelope: `{"ok":true,"data":<payload>}` on success, `{"ok":false,"error":{"type":…,"message":…,"hint":…}}` on failure. One success and one failure appear below in full envelope form. Reference: `.agents/skills/web-mcp/references/tools/page_fetch.md`.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expect `{"browser_id":"browser-20"}` in `data`. `persist:false` gave an ephemeral profile.
Use only your id. Never close an empty id.

## 2. Open the page and read meta

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-20","wait_for":"idle"}}
```

Expect `{"session_id":"page-1","title":"Web browser - Wikipedia","html_bytes":614792}`.

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"meta","max_bytes":4000}}
```

Expect `{"title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","meta":[{"charset":"UTF-8"}]}`.
A bare `what=dom` on this huge page knows only whole-document cuts and returns
`{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`. Use `article` instead.

## 3. Scope text with a selector

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"text","selector":"h1#firstHeading"}}
```

Expect the heading text only. The failure path rides the envelope — tested live on
2026-10-26 (`what=sinks` was used in the live run, any unknown value behaves the same):

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_fetch: unknown what=\"sinks\" (must be one of html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, body, console, article, marks, security)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
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

Expect `{"interactives":[{"handle":"gy1g0vstq-b49","role":"button","text":"Main menu","type":"checkbox","x":34,"y":33,"visible":true}],"truncated":335}`.
Feed a mark's `x` and `y` from `marks` to `page_act` with `click_at`.

## 6. Audit the page with what=security

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"security"}}
```

Expect `data` to hold `{"url":…,"checks":[…],"count":N,"truncated":0}`, each check shaped `{"id":"csp","status":"ok","evidence":"…","detail":"…"}`. On the 2026-10-26 live run against `https://go.dev/` the report returned `count: 20` with check ids `csp`, `csp_unsafe_inline`, `csp_unsafe_eval`, `hsts`, `x_frame_options`, `x_content_type_options`, `referrer_policy`, `permissions_policy`, `coop`, five `cookie:<name>` checks, `mixed_content`, `sri`, `form_target`, `password_autocomplete`, and `cors`. See the reference for the full id table.

## 7. Attribute traffic to one action

Open `https://react.dev/learn` as `page-3`, call `page_act` `clear_logs` (expect
`{"network":39,"console":0}`), act once, then read:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network"}}
```

Expect one entry with `"seq":40` and
`"url":"https://react.dev/_next/static/chunks/616.bd871377a76d946f.js"`. `seq` keeps
counting after `clear_logs`, so it proves the action caused the request.

## 8. Read request headers and one response body

Open `https://httpbin.org/forms/post` as `page-4`. Clear the rings, then submit:

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"clear_logs"}}
{"tool":"page_act","arguments":{"session_id":"page-4","action":"click","selector":"form button"}}
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"network"}}
```

Expect `clicked selector="form button"` and a delta line `→ url=https://httpbin.org/post title="httpbin.org/post"` in `data`, then a `POST` entry for
`https://httpbin.org/post` with `"status":200` and
`"req_headers":{"Content-Type":"application/x-www-form-urlencoded",…}`. `network` carries
no bodies. Read the body on demand with `target`:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"body","target":"httpbin.org/post"}}
```

Expect `{"seq":N,"method":"POST","url":"https://httpbin.org/post","status":200,"content_type":"application/json","bytes":1143,"body":"{\"args\":{},…}"}`.
Binary content returns `base64`. Read at once, because bodies are evicted quickly.

## 9. Check header redaction

`page-4` now holds httpbin cookies. Clear the rings and read the next request.

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"clear_logs"}}
{"tool":"page_fetch","arguments":{"session_id":"page-4","what":"network"}}
```

Expect one httpbin entry with `"Cookie":"<redacted>"` in `req_headers`. `Set-Cookie`,
`Authorization`, and `X-CSRF-Token` redact the same way.

## 10. Capture a screenshot as two blocks

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"screenshot","full_page":false}}
```

Expect two blocks in `result.content`. The text block is `{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}`. The next block is `{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}`. `marks` returns the mark table as text plus the annotated PNG. A client that sends `capabilities.imageContent:false` at `initialize` gets one text block instead. Then `data` holds the data URI.

## 11. Errors and cleanup

A bad selector on `text` returns `{"ok":false,"error":{"type":"unknown","message":"page_fetch: no element matched selector \"#does-not-exist-xyz\""}}`, tested live on 2026-10-26. `element_not_found` is not assigned yet.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-20"}}
```

Close your own browser only. The ephemeral profile is removed on close. Confirm with
`browser_list` that only other browsers remain.

Tool reference: `.agents/skills/web-mcp/references/tools/page_fetch.md`.
