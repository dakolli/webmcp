> Tool: `browser_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_close example

Use this file only when tool calling for `browser_close` fails. The tool description is primary.

This sequence creates an isolated browser, opens one page, closes the browser, and checks the result. Each step shows the call and the expected output.

> **WARNING: Never send `{}` or an empty `browser_id` on a shared server.** That closes every browser, including browsers owned by other agents.

## 1. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expected output:

```json
{"browser_id":"browser-5"}
```

The id can differ. Record it. Use that exact id in the next steps. Do not pass a profile name.

## 2. Open one page

```json
{"tool":"fetch_page","arguments":{"url":"https://example.com","wait_for":"load","browser_id":"browser-5"}}
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
[
  {
    "browser_id": "browser-5",
    "profile_dir": "<config-dir>/profiles/.ephemeral/browser-5-150041042",
    "pages": [{"id": "page-1", "url": "https://example.com"}]
  }
]
```

The real output also lists browsers owned by other agents. `profile_dir` is an absolute path under your browser-mcp config directory. A secondary browser uses an ephemeral profile.

## 4. Close your browser

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-5"}}
```

Expected output:

```
browser browser-5 closed
```

The close also closes every page in that browser. The `session_id` values from that browser are now invalid.

## 5. Confirm the close

```json
{"tool":"browser_list","arguments":{}}
```

Expected output: an array without `browser-5`.

```json
[{"browser_id":"browser-1","profile_dir":"<config-dir>/profiles/default","pages":[]},{"browser_id":"browser-4","profile_dir":"<config-dir>/profiles/.ephemeral/browser-4-2612212276","pages":[{"id":"page-1","url":"https://example.com"}]}]
```

## 6. A second close fails

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-5"}}
```

Expected error:

```
MCP error -32603: browser_close: browser "browser-5" not found (running: browser-1, browser-4): unknown browser
```

The browser is already closed. Do not retry the same id. The running list can differ from this example.

## 7. An unknown id fails

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-999"}}
```

Expected error:

```
MCP error -32603: browser_close: browser "browser-999" not found (running: browser-1, browser-4): unknown browser
```

Recovery: call `browser_list`. Use a live id. Or call `browser_create` for a new browser. The old `session_id` values stay invalid.

## Cleanup

Close the browser you created with the same call from step 4. Use its exact id. Do not close `browser-1` or a browser owned by another agent. Do not omit `browser_id`.

## Schema error fallback

If your client rejects `browser_id`, send a plain object. Send `{}` only when you own every browser on the server. Otherwise send `{"browser_id":"<id>"}`.

A misspelled key is ignored. It becomes an empty id and closes every browser. Send the exact key `browser_id`.
