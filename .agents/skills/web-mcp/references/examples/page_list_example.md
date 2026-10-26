> Tool: `page_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_list example

Use this file only when tool calling for `page_list` fails. The tool description is primary.

This sequence creates a browser you own, lists an empty page set, opens two pages, lists them, tests two failures, and cleans up. The live test used `browser-4`. Your id can differ.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expected output:

```json
{"browser_id":"browser-4"}
```

`persist:false` gives an ephemeral profile. The manager deletes it on close. Record the exact id. Use it below.

## 2. List an empty browser

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

Expected output:

```
[]
```

No page opened. No browser launched.

## 3. Open two pages

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-4","url":"https://example.com","wait_for":"load"}}
```

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-4","url":"https://httpbin.org/forms/post","wait_for":"load"}}
```

Expected output, one line per call, in order:

```json
{"session_id":"page-1","title":"Example Domain","url":"https://example.com/","html_bytes":2358,"load_ms":497}
{"session_id":"page-2","title":"httpbin.org/forms/post","url":"https://httpbin.org/forms/post","html_bytes":1404,"load_ms":383}
```

The full results also hold a `text_snippet` field. `fetch_page` reports the final URL with a trailing slash. `page_list` reports the raw creation URL.

## 4. List the two pages

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

Expected output:

```json
[
  {
    "id": "page-1",
    "url": "https://example.com"
  },
  {
    "id": "page-2",
    "url": "https://httpbin.org/forms/post"
  }
]
```

Read `id` from this result. Use it with `page_fetch` and `page_act`.

## 5. Failure: use an unknown id

```json
{"tool":"page_list","arguments":{"browser_id":"browser-999"}}
```

Expected error:

```
MCP error -32603: page_list: browser "browser-999" not found (running: browser-1, browser-4): unknown browser
```

Do not retry `browser-999`. Recovery: call `browser_list` and copy a live `browser_id` from its result.

## 6. Failure: omit `browser_id`

```json
{"tool":"page_list","arguments":{}}
```

Expected error:

```
MCP error -32603: page_list: missing required argument "browser_id"
```

Recovery: send the object with the `browser_id` key. Do not rely on a default browser.

## Cleanup

Close only your browser. Its pages close with it. Do not call `session_close`.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-4"}}
```

Expected output:

```
browser browser-4 closed
```
Never send `{}` or an empty `browser_id` to `browser_close`.
