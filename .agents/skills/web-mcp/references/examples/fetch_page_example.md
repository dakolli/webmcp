> Tool: `fetch_page` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# fetch_page example

Use this file only when tool calling for `fetch_page` fails. The tool description is
primary. Your ids and byte counts can differ.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"profile":"skill-fetch-96","headless":true,"stealth":true}}
```

```json
{"browser_id":"browser-16"}
```

Record the id. Use it in every later call. Add `"persist":false` when your client accepts it.

## 2. Fetch with each wait mode

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","wait_for":"load","browser_id":"browser-16","timeout_ms":60000}}
```

```json
{"session_id":"page-1","title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","text_snippet":"Jump to content\nMain menu\nSearch\n…","html_bytes":597043,"load_ms":1185}
```

Repeat with `"wait_for":"idle"` and `"wait_for":"stable"`. Live test: `idle` returned in
1534 ms, `stable` returned in 1479 ms. Each call returned a new `session_id`.

## 3. Fetch a client-rendered page

```json
{"tool":"fetch_page","arguments":{"url":"https://react.dev/learn","wait_for":"stable","browser_id":"browser-16","timeout_ms":8000}}
```

Output (abridged): `{"session_id":"page-4","title":"Quick Start – React","url":"https://react.dev/learn","html_bytes":285239,"load_ms":2570}`.

## 4. Apply a viewport

```json
{"tool":"fetch_page","arguments":{"url":"https://example.com","wait_for":"load","browser_id":"browser-16","viewport":{"width":800,"height":600}}}
```

Confirm the viewport. The `eval` value must be a function expression.

```json
{"tool":"page_act","arguments":{"session_id":"page-5","action":"eval","value":"() => JSON.stringify({w:innerWidth,h:innerHeight,dpr:devicePixelRatio})"}}
```

Output: `"{\"w\":800,\"h\":600,\"dpr\":1}"`.

## 5. Device beats viewport, and UA beats both

```json
{"tool":"fetch_page","arguments":{"url":"https://example.com","wait_for":"load","browser_id":"browser-16","device":"IPadPro","viewport":{"width":400,"height":400}}}
```

The eval returns `1024x1366` at DPR 2. The device won.

```json
{"tool":"fetch_page","arguments":{"url":"https://example.com","wait_for":"load","browser_id":"browser-16","user_agent":"webmcp-skill-test/1.0"}}
```

`page_act` `eval` of `() => navigator.userAgent` returns `"webmcp-skill-test/1.0"`. A fetch
with `"device":"IPhoneX"` gives `375x812` at DPR 3. Call `page_list` to confirm the pages.

## 6. Errors you can expect

```json
{"tool":"fetch_page","arguments":{"url":"not-a-url","browser_id":"browser-16"}}
```

```text
MCP error -32603: fetch_page: browsers: navigate: {-32000 Cannot navigate to invalid URL }
```

```json
{"tool":"fetch_page","arguments":{"url":"ftp://example.com/file","browser_id":"browser-16"}}
```

```text
MCP error -32603: fetch_page: browsers: navigate: navigation failed: net::ERR_ABORTED
```

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","wait_for":"stable","browser_id":"browser-16","timeout_ms":1200}}
```

```text
MCP error -32603: fetch_page: context deadline exceeded (session page-10 closed)
```

The tab number depends on your session count. The timeout closes the tab. Do not reuse that id.

## 8. Clean up

Close the browser you created. This also closes every page in it.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-16"}}
```

Output: `browser browser-16 closed`. Then run `browser_list` and confirm the id is absent.
Do not close `browser-1`. Never send `{}` or an empty `browser_id` to `browser_close`.

This build's `session_close` resolves pages on the default browser. It cannot close a page
in your own browser. It returned `session_close: browsers: page "page-9" not found` for a
live page. Close your browser instead.

## Schema fallback

Send one JSON object with only the documented keys. Confirm `browser_id` with
`browser_list` first, because an unknown id creates a browser.

See [fetch_page.md](../tools/fetch_page.md) for the full input table, return shape, and error strings.
