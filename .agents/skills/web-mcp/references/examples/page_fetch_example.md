> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_fetch example

Use this file only when tool calling for `page_fetch` fails. The tool description is
primary. Your ids and caps can differ. Reference:
`.agents/skills/web-mcp/references/tools/page_fetch.md`.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expect `{"browser_id":"browser-20"}`. `persist:false` gave an ephemeral profile. Use only
your id. Never close an empty id. Never call `session_close` on a shared server.

## 2. Open the page and read meta

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-20","wait_for":"idle"}}
```

Expect `{"session_id":"page-1","title":"Web browser - Wikipedia","html_bytes":614792}`.
Keep `session_id`. Every call below uses it.

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"meta","max_bytes":4000}}
```

Expect `{"title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","meta":[{"charset":"UTF-8"}]}`.

## 3. Scope text with a selector

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"text","selector":"h1#firstHeading"}}
```

Expect `Web browser`. A miss returns
`page_fetch: no element matched selector "#does-not-exist-xyz"`.

## 4. Watch links truncate safely

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"links","max_bytes":1200}}
```

Expect `{"items":[{"handle":"gy1g0vstq-b162","href":"…#bodyContent","text":"Jump to content"}],"max_bytes":1200,"omitted":878,"truncated":true}`.
The envelope stays parseable. `omitted` is the dropped count.

## 5. Do not use dom on a huge page

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"dom"}}
```

Expect `{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`. The one document
exceeds 400000 bytes. Use `article`, `text`, or `html` with a selector instead.

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"article"}}
```

Expect `{"title":"Web browser - Wikipedia","text":"…","words":1878,"truncated":false,"url":"https://en.wikipedia.org/wiki/Web_browser"}`.

## 6. Fetch one CSS file with target

List `css` first when you need a URL. Expect a JSON array of `{url,type,size}` plus a hint
line. Copy a `url` exactly.

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"css","target":"https://en.wikipedia.org/w/load.php?lang=en&modules=site.styles&only=styles&skin=vector-2022","max_bytes":1200}}
```

Expect a `// url=` line, a `// bytes=6881` line, then raw CSS. A bad target returns
`page_fetch: resource "…" not found in the page's resource tree: {-32000 No resource with
given URL found }`. `js` works the same way.

## 7. Read the action map

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"interactives"}}
```

Expect `{"interactives":[{"handle":"gy1g0vstq-b49","role":"button","text":"Main menu","type":"checkbox","x":34,"y":33,"visible":true}],"truncated":335}`.
The cap is 200 entries. The tool ignores `max_bytes` here. `a11y` uses the same handles and
stops at 400 nodes.

## 8. Use marks for viewport boxes

```json
{"tool":"page_act","arguments":{"session_id":"page-1","action":"set_viewport","viewport":{"width":400,"height":300}}}
```

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"marks"}}
```

Expect `{"marks":[{"index":1,"role":"a","text":"Jump to content","x":-1,"y":-1,"width":1,"height":1}],"screenshot":"data:image/png;base64,…","viewport_width":400,"viewport_height":300,"truncated":0}`.
Feed a mark's `x` and `y` to `page_act` with `click_at`.

## 9. Attribute traffic to one action

Open `https://react.dev/learn`, then clear the rings and act once.

```json
{"tool":"page_act","arguments":{"session_id":"page-3","action":"clear_logs"}}
```

Expect `{"network":39,"console":0}`.

```json
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click","handle":"g6ki3lskc-b110"}}
```

```json
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"network"}}
```

Expect one entry with `"seq":40` and
`"url":"https://react.dev/_next/static/chunks/616.bd871377a76d946f.js"`. `seq` keeps
counting after `clear_logs`, so it proves the action caused the request. `console` returned
`{"entries":[],"count":0,"dropped":0,"truncated":0}`.

## 10. Errors and cleanup

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"tablez"}}
```

Expect `page_fetch: unknown what="tablez" (must be one of html, dom, text, css, js, links,
screenshot, meta, interactives, a11y, network, console, article, marks)`.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-20"}}
```

Close your own browser only. The ephemeral profile is removed on close. Confirm with
`browser_list` that only other browsers remain.
