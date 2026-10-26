> Tool: `fetch_page` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# fetch_page example

Use this file only when tool calling for `fetch_page` fails. The tool description is
primary. Your ids and byte counts can differ.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

```json
{"ok":true,"data":{"browser_id":"browser-4","owner":"unowned","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"reused":false}}
```

Record the `browser_id` inside `data`. Use it in every later call. `serve mcp` starts no
browser, so this call can take launch time.

## 2. Fetch one page

```json
{"tool":"fetch_page","arguments":{"url":"https://react.dev/learn","wait_for":"stable","browser_id":"browser-4","timeout_ms":30000}}
```

```json
{"ok":true,"data":{"session_id":"page-1","title":"Quick Start – React","url":"https://react.dev/learn","text_snippet":"React\nv19.3\nSearch\nCtrl\nK\n…","html_bytes":285239,"load_ms":2760}}
```

Read `session_id` from `data`. The full `text_snippet` runs 500 characters.

## 3. Failure: bad wait mode

```json
{"tool":"fetch_page","arguments":{"url":"https://react.dev/learn","wait_for":"bogus","browser_id":"browser-4"}}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"fetch_page: invalid wait_for \"bogus\" (must be one of load, idle, stable)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

The failure happened at validation, before any browser work. No session was created.

## 4. Failure: unreachable host

```json
{"tool":"fetch_page","arguments":{"url":"https://this-host-does-not-exist-96.invalid/","browser_id":"browser-4"}}
```

```json
{"ok":false,"error":{"type":"nav_failed","message":"fetch_page: browsers: navigate: navigation failed: net::ERR_NAME_NOT_RESOLVED","hint":"the page navigation failed — verify the URL is reachable, retry, or re-fetch the page"}}
```

## 5. Failure: timeout closes the tab

The message text is from a live run with `timeout_ms: 1200`. The envelope wrapping is the
rule in [fetch_page.md](../tools/fetch_page.md).

```json
{"ok":false,"error":{"type":"timeout","message":"fetch_page: context deadline exceeded (session page-10 closed)","hint":"the call exceeded its timeout (default 30s) — retry with a higher timeout_ms, narrow the selector, or the page is wedged"}}
```

The tab number depends on your session count. The timeout closes the tab. Do not reuse
that id.

## 6. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":"browser browser-4 closed"}
```

Then run `browser_list` and confirm the id is absent from `data`. Do not close
`browser-1`. Never send `{}` or an empty `browser_id` to `browser_close`.

See [fetch_page.md](../tools/fetch_page.md) for the full input table, return shape, and
error types.
