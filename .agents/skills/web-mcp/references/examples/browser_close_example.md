> Tool: `browser_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_close example

Use this file only when tool calling for `browser_close` fails. The tool description is primary.

This sequence creates an isolated browser, opens one page, closes the browser, and checks the result. Each step shows the call and the expected output.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"profile":"skill-browser-close","headless":true,"stealth":true}}
```

Expected output:

```json
{"browser_id":"browser-1"}
```

The id can differ. Record it. Use that exact id in the next steps.

## 2. Open one page

```json
{"tool":"fetch_page","arguments":{"url":"https://example.com","wait_for":"load","browser_id":"browser-1"}}
```

Expected output:

```json
{"session_id":"page-1","title":"Example Domain","url":"https://example.com/"}
```

## 3. Read the live ids

```json
{"tool":"browser_list","arguments":{}}
```

Expected output:

```json
[{"browser_id":"browser-1","profile_dir":"skill-browser-close","pages":[{"id":"page-1","url":"https://example.com/"}]}]
```

## 4. Close your browser

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-1"}}
```

Expected output:

```
browser browser-1 closed
```

## 5. Confirm the close

```json
{"tool":"browser_list","arguments":{}}
```

Expected output:

```json
[]
```

The browser no longer appears. Its `session_id` values are now invalid.

## Failure and recovery

A second close of the same id fails.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-1"}}
```

Expected error:

```
MCP error -32603: browser_close: browser "browser-1" not found (running: browser-2): unknown browser
```

The message lists live ids after `running:`. The browser is already closed. Do not retry the same id.

If you still need a browser, call `browser_create` again.

## Cleanup

Close the browser you created. Use its exact id.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-1"}}
```

Never send `{}` or an empty `browser_id` in a shared session. That closes every browser.

## Schema error fallback

If your client rejects `browser_id`, send a plain object. Send `{}` only when you own every browser on the server. Otherwise send `{"browser_id":"<id>"}`.
