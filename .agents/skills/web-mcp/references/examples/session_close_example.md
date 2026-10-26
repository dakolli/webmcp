> Example: `session_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# session_close — worked example

Close one tab. Prove it is gone. Keep the browser and the other tab alive.

## 1. Open two pages in one browser

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-2","wait_for":"idle"}}
```

```json
{"tool":"fetch_page","arguments":{"url":"https://httpbin.org/forms/post","browser_id":"browser-2","wait_for":"idle"}}
```

The calls returned `page-1` for Wikipedia and `page-2` for the form. Real results also hold `text_snippet`, `html_bytes`, and `load_ms`.

## 2. Close the second tab

```json
{"tool":"session_close","arguments":{"session_id":"page-2"}}
```

Expected text result:

```
session page-2 closed
```

## 3. Confirm the tab is gone

```json
{"tool":"page_list","arguments":{"browser_id":"browser-2"}}
```

Expected result:

```json
[{"id":"page-1","url":"https://en.wikipedia.org/wiki/Web_browser"}]
```

`session_list` takes no browser id. It reports the default browser (`browser-1`). Use `page_list` to check your own browser.

## 4. Close the same tab again

```json
{"tool":"session_close","arguments":{"session_id":"page-2"}}
```

Expected error when no other live browser holds a `page-2`:

```
MCP error -32603: session_close: session "page-2" not found
```

Expected error when another live browser holds a `page-2`:

```
MCP error -32603: session_close: browsers: page "page-2" not found
```

Do not retry a close. The tab is already gone.

## 5. Close an unknown id or send no id

```json
{"tool":"session_close","arguments":{"session_id":"page-999"}}
```

```
MCP error -32603: session_close: session "page-999" not found
```

```json
{"tool":"session_close","arguments":{}}
```

```
MCP error -32603: session_close: missing required argument "session_id"
```

The empty string `""` returns the same missing-argument error.

## 6. The other tab still works

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"meta"}}
```

The result starts with this JSON:

```json
{"title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","meta":[]}
```

The browser stayed alive. The other tab stayed live. A later `page_fetch` on `page-2` fails with `page_fetch: session "page-2" not found`.

## 7. Cleanup order

1. Call `session_close` for each tab you opened in the default browser.
2. Call `page_list` with your `browser_id` and expect `[]`.
3. Call `browser_close` with your `browser_id`, for example `{"browser_id":"browser-2"}`. Expect `browser browser-2 closed`.
4. Call `browser_list` and confirm the id is gone.

For a secondary browser that you created with options, skip step 1. `session_close` closes in the default browser. Call `browser_close` with your own id instead. It closes every tab and the Chromium process.

## Pitfalls

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| `session "<id>" not found` | Wrong id, closed tab, or idle sweep | Read live ids with `page_list`. |
| `browsers: page "<id>" not found` | The id resolved to another browser, or the tab is already closed | Use `browser_close` with your own `browser_id`. |
| Another agent lost a tab | Two browsers held the same `page-N` id | Keep one browser per task. Close your own browser, not a shared tab. |
| Session vanished after 15 minutes | Idle sweep closed it | Open the URL again with `fetch_page`. |

Tool reference: `.agents/skills/web-mcp/references/tools/session_close.md`.
