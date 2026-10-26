> Tool: `session_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# session_close

Close one live browser session. Release its tab and its page resources.

## What it does

`session_close` closes one session that `fetch_page` opened. The `session_id` becomes invalid.

Closing a session does not close the browser. The Chromium process stays alive. The profile stays alive, so cookies and logins persist. Other sessions stay live.

Use it when you finish one page. Use `browser_close` when you finish the whole browser.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | yes | none | The id that `fetch_page` returned for the tab to close. |

The schema declares one property and one required field. The source is `code://pkg/mcp/tools/session_tools.go#L44-L55`.

Pass the id as a string, for example `"page-2"`. Do not pass a bare number.

## Returns

The tool returns plain text. It does not return JSON.

| Condition | Return value |
| :--- | :--- |
| Session closed | `session <session_id> closed` |

Test run: `{"session_id":"page-2"}` returned `session page-2 closed`.

The text has no trailing period. The id is the exact string you sent. There is no JSON wrapper and no count.

## Behavior and limits

- **Tab effect.** The tab closes. The page leaves the browser page map before the network close starts. A wedged close still removes the id.
- **Resource effect.** Network observation and console capture stop. The page handle is released. Memory for that tab is freed.
- **Browser.** The browser process stays alive. The profile directory stays on disk.
- **Cookies.** They stay. They live in the profile, not in the tab. A new `fetch_page` in the same browser can reuse the login.
- **Other sessions.** They stay live. Closing one tab does not touch another tab.
- **Not idempotent.** A second close fails. See the next section.
- **Close budget.** The engine gives the network close about 10 seconds. A failed close returns an error and still drops the id.
- **Idle sweep.** The 15 minute idle sweep can close the tab before you do. A later close then fails as unknown.
- **The call can launch Chromium.** The tool opens the default browser before it resolves the id. If no browser is running, the server launches one with default options.
- **Session ids are not unique across browsers.** Every browser numbers its tabs from `page-1`. Two live browsers can both hold a `page-1`. `session_close` takes no `browser_id`. The server resolves the id across live browsers and closes the tab in the default browser (`browser-1`).
- **Tested effect of that limit.** A session in a secondary browser stayed open. A close call with its id closed a same-numbered tab in `browser-1` instead. A second call returned `session_close: browsers: page "page-2" not found`. To close a secondary-browser tab, use `browser_close` with that `browser_id`.
- **Lookup order is not stable.** Session lookup walks a Go map. Two browsers that hold the same id are picked in no fixed order. Keep one browser per task when ids can collide.

## Failure modes and recovery

The MCP server prefixes tool errors with `MCP error -32603: `. The strings below show the message after that prefix.

**Missing or empty `session_id`.**

```
session_close: missing required argument "session_id"
```

Test run: `{}` and `{"session_id":""}` returned this error.

Recovery: send the id from `fetch_page`. Read live ids with `session_list` or `page_list`.

**Unknown session id.**

```
session_close: session "page-999" not found
```

Test run: `{"session_id":"page-999"}` returned this error.

Recovery: call `session_list` or `page_list` and use a live id. Do not retry the same id.

**Second close of the same id.**

```
session_close: browsers: page "page-2" not found
```

Test run: this was the second close in the same browser. The first close returned success. The second close failed with this error because a same-numbered tab existed in another browser.

```
session_close: session "page-2" not found
```

This is the same double close when no live browser holds that id. The message comes from the source. This exact case was not run live.

Recovery: a close is final. Do not retry. Use `fetch_page` to open the URL again.

**Session from before a browser restart.**

```
session_close: session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs
```

This path was not run live. The message comes from the source.

Recovery: the old tab is gone. Call `fetch_page` and use the new id.

**Bad argument JSON.**

```
session_close: invalid arguments: json: cannot unmarshal ...
```

Recovery: send one JSON object with a string `session_id`. Do not send an array, a bare string, or a number.

## When not to use it

Do not use `session_close` to close a browser. Use `browser_close`. A browser close also removes the Chromium process.

Do not use it to read page content. Use `page_fetch`.

Do not use it to list sessions. Use `session_list` or `page_list`.

Do not use it for a tab in a secondary browser that you created with `browser_create`. The tool closes in the default browser. Use `browser_close` with your own `browser_id`.

Do not close a session you did not create. Other agents share the server, and ids can collide.

## Related tools

- `fetch_page` — opens a page and returns the `session_id` that this tool needs.
- `session_list` — lists live sessions. Use it to choose an id and to confirm a close.
- `page_list` — lists the pages of one browser. Use it to confirm a close inside a known browser.
- `page_fetch` — reads a live session. It fails after the session closes.
- `browser_close` — closes a whole browser, its pages, and the Chromium process. Prefer it for secondary browsers and for full cleanup.

Worked example: `.agents/skills/web-mcp/references/examples/session_close_example.md`.
