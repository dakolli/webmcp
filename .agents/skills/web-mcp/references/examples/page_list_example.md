> Tool: `page_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_list example

Use this file only when tool calling for `page_list` fails. The tool description is
primary. Your ids can differ.

This sequence creates a browser you own, lists an empty page set, opens two pages, lists
them, tests two failures, and cleans up. The live test used `browser-4`.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

```json
{"ok":true,"data":{"browser_id":"browser-4","owner":"unowned","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}}
```

`persist:false` gives an ephemeral profile. The manager deletes it on close. Record the
`browser_id` inside `data`. Use it below.

## 2. List an empty browser

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":[]}
```

No page opened. No browser launched.

## 3. Open two pages

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-4","url":"https://react.dev/learn","wait_for":"stable","timeout_ms":30000}}
```

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-4","url":"https://news.ycombinator.com/news","wait_for":"load"}}
```

Expected `data` payloads, one per call, in order:

```json
{"session_id":"page-1","title":"Quick Start – React","url":"https://react.dev/learn","html_bytes":285239,"load_ms":2760}
{"session_id":"page-2","title":"Hacker News","url":"https://news.ycombinator.com/news","html_bytes":34861,"load_ms":571}
```

The full payloads also hold a `text_snippet` field. `fetch_page` reports the final URL.
`page_list` reports the live URL and title.

## 4. List the two pages

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":[{"id":"page-1","url":"https://react.dev/learn","title":"Quick Start – React","context":"default"},{"id":"page-2","url":"https://news.ycombinator.com/news","title":"Hacker News","context":"default"}]}
```

Read `id` from `data`. Use it with `page_fetch` and `page_act`.

## 5. Failure: use an unknown id

```json
{"tool":"page_list","arguments":{"browser_id":"browser-999"}}
```

```json
{"ok":false,"error":{"type":"unknown","message":"page_list: browser \"browser-999\" not found (running: browser-3 (owner: unowned), browser-4 (owner: unowned)): unknown browser"}}
```

The running list shows the live ids of that moment. Yours will differ. Do not retry
`browser-999`. Recovery: call `browser_list` and copy a live `browser_id` from `data`.

## 6. Failure: omit `browser_id`

```json
{"tool":"page_list","arguments":{}}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_list: missing required argument \"browser_id\"","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: send the object with the `browser_id` key. Do not rely on a default browser.

## Cleanup

Close only your browser. Its pages close with it. Do not call `session_close`.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":"browser browser-4 closed"}
```

Never send `{}` or an empty `browser_id` to `browser_close`.

See [page_list.md](../tools/page_list.md) for the full input table, return shape, and
error strings.
