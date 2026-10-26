---
name: web-mcp
description: Drive a live, stateful Chromium browser over the web-mcp MCP server. Use for real browsing, page inspection (DOM, accessibility, network, console, article, marks), form filling, and UI testing. The MCP tool description is primary. This skill is the fallback for exact shapes, limits, and errors.
---

# web-mcp — MCP browser skill

This skill supports the 9 `web-mcp` MCP tools. It does not replace the tool descriptions.

## Read order

1. Read the tool description and input schema first. They are authoritative.
2. Read `references/tools/<tool>.md` when the description is unclear, incomplete, or a call failed.
3. Read `references/examples/<tool>_example.md` when tool calling itself fails.

Do not invent parameters. The Go source in `pkg/mcp/tools/` is the schema source of truth.

## Mental model

- **Browser** (`browser_id`, for example `browser-1`). One Chromium process and one profile.
- **Session** (`session_id`, for example `page-1`). One live tab inside a browser.
- `browser-1` uses the persistent profile at `~/.config/browser-mcp/profiles/default`.
- A secondary browser is ephemeral when you pass `persist:false`. Its profile is removed on close.
- A bare `profile` name resolves under the config dir. A relative path with a separator is rejected.
- Idle sessions expire after 15 minutes. A dead Chromium is relaunched under the same `browser_id`.
- Page ids are unique inside one browser only. Two live browsers can both have `page-1`.

## Coverage

| Tool | Reference | Example | State |
| :--- | :--- | :--- | :--- |
| `browser_create` | [browser_create.md](references/tools/browser_create.md) | [browser_create_example.md](references/examples/browser_create_example.md) | tested |
| `browser_list` | [browser_list.md](references/tools/browser_list.md) | [browser_list_example.md](references/examples/browser_list_example.md) | tested |
| `browser_close` | [browser_close.md](references/tools/browser_close.md) | [browser_close_example.md](references/examples/browser_close_example.md) | draft, re-test pending |
| `fetch_page` | [fetch_page.md](references/tools/fetch_page.md) | [fetch_page_example.md](references/examples/fetch_page_example.md) | tested |
| `page_list` | [page_list.md](references/tools/page_list.md) | [page_list_example.md](references/examples/page_list_example.md) | draft, re-test pending |
| `session_list` | [session_list.md](references/tools/session_list.md) | [session_list_example.md](references/examples/session_list_example.md) | tested |
| `session_close` | [session_close.md](references/tools/session_close.md) | [session_close_example.md](references/examples/session_close_example.md) | tested |
| `page_fetch` | [page_fetch.md](references/tools/page_fetch.md) | [page_fetch_example.md](references/examples/page_fetch_example.md) | tested |
| `page_act` | [page_act.md](references/tools/page_act.md) | [page_act_example.md](references/examples/page_act_example.md) | tested |

## Cross-cutting rules

1. Call `browser_list` before you use a `browser_id`. Confirm the id and its `profile_dir`.
2. Pass `persist:false` for an isolated secondary browser. If your client does not show `persist`, reconnect it before you create a browser.
3. Never close `browser-1` unless the user asks for a full reset. Never call `browser_close` with an empty `browser_id` in a shared session.
4. Use one browser per task. Page ids collide across browsers, and `page_fetch` and `page_act` resolve an id across live browsers.
5. `session_close` is not browser-scoped. It closes the tab in the default browser. For a tab in a secondary browser, use `browser_close` with your own id.
6. `session_list` reports the default browser only. Use `page_list` with a `browser_id` for any other browser.
7. An unknown `browser_id` can create a new browser. A typo spends a full Chromium process. Verify the id first.
8. `fetch_page` creates a new session on every call. Close sessions you no longer need.
9. `page_fetch` network and console capture is cumulative from page creation. Call `page_act` `clear_logs` before the step you want to observe.
10. `page_act` `eval` needs a JavaScript function expression, for example `() => document.title`. A bare expression can fail.
11. `page_act` `select` matches the option `value`, not its text. An invalid value can block the server. Use a value you read with `eval`, or use `eval` to set the field.
12. `page_act` `drag_and_drop` uses mouse events. It does not fire native HTML5 drag events. Use `eval` for HTML5 drag handlers.
13. `page_act` `wait` defaults to `wait_mode:"ms"`. If you omit `timeout_ms`, it sleeps for 30 seconds.
14. Prefer `article` over `text` for prose pages. Prefer `interactives` or `a11y` before you act.

## Known live-server limits

- `page_fetch` large aspects bypass `max_bytes`: `interactives`, `a11y`, `network`, `console`, `article`, `marks`, and `screenshot`. The client can cut the response.
- `page_fetch` `dom` truncation can return an empty `items` array for one huge document. A larger `max_bytes` does not always help.
- `page_fetch` `css` and `js` lists are cut as plain text. A cut list is not valid JSON.
- `fetch_page` `browser_id` is not checked. It can launch a new browser with a literal unknown id.
- `page_act` `input` on `<input type="time">` can leave the value empty. Use `eval`.
- `page_act` invalid `select` value can time out the request and wedge the server. Close that browser and retry.

## Isolated test recipe

```json
{ "tool": "browser_create", "arguments": { "headless": true, "stealth": true, "persist": false } }
{ "tool": "browser_list", "arguments": {} }
{ "tool": "browser_close", "arguments": { "browser_id": "browser-2" } }
```

Use only the id you created. Do not close `browser-1`.

## Test pages

- `https://en.wikipedia.org/wiki/Web_browser` — large DOM, the 200-interactive cap, many links, one search input.
- `https://httpbin.org/forms/post` — full form, POST round trip, file input.
- `https://react.dev/learn` — SPA, client-side routes, React handlers, theme toggle.

## Maintainer status

- Wave A complete: `browser_create`, `browser_list`, `fetch_page`, `session_close`.
- Wave B complete: `session_list`, `page_fetch`, `page_act`.
- Wave C pending: re-test the `browser_close` and `page_list` drafts.
- Extension tickets: blocked until their tool tickets land.
