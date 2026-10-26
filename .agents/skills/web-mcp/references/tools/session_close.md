> Tool: `session_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

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

The schema declares one property and one required field. The source is `code://pkg/mcp/tools/session_tools.go#L45-L56`.

Pass the id as a string, for example `"page-2"`. Do not pass a bare number.

## Returns

The success payload rides inside the result envelope, so the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the `{"ok":false,"error":…}` shape.

The payload is one prose string inside `data`. Source (`code://pkg/mcp/tools/session_tools.go#L88`), tested live on 2026-10-26 on `https://www.iana.org/`:

```json
{"ok":true,"data":"session page-1 closed (browser browser-1) — tab released"}
```

The string names the exact id you sent and the browser that owned it. There is no state delta and no count.

## Behavior and limits

- **Tab effect.** The tab closes. The page leaves the browser page map before the network close starts. A wedged close still removes the id.
- **Resource effect.** Network observation and console capture stop. The page handle is released. Memory for that tab is freed.
- **Browser.** The browser process stays alive. The profile directory stays on disk.
- **Cookies.** They stay. They live in the profile, not in the tab. A new `fetch_page` in the same browser can reuse the login.
- **Other sessions.** They stay live. Closing one tab does not touch another tab.
- **Not idempotent.** A second close fails. See the next section.
- **Close budget.** The engine gives the network close about 10 seconds. A failed close returns an error and still drops the id.
- **Idle sweep.** The idle TTL sweep can close the tab before you do. A later close then fails as unknown.
- **The call can launch Chromium.** With no browser running the server launches one with default options before it resolves the id. Call `browser_create` first to control launch options.
- **Session ids are not unique across browsers.** Every browser numbers its tabs from `page-1`. Two live browsers can both hold a `page-1`. The tool takes no `browser_id`. The server resolves the id across live browsers, and the winner comes from a Go map walk, so it is not stable. Keep one browser per task when ids can collide.
- **Tested effect of that limit.** A session in a secondary browser stayed open. A close call with its id closed a same-numbered tab in `browser-1` instead. To close a secondary-browser tab, use `browser_close` with that `browser_id`.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The prefix `session_close: ` opens the message. This tool produces the types `invalid_args`, `page_closed`, and `unknown`. Source: `code://pkg/mcp/tools/envelope.go#L16-L56`.

**Missing or empty `session_id`.** Type `invalid_args`.

```json
{"ok":false,"error":{"type":"invalid_args","message":"session_close: missing required argument \"session_id\"","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

**Unknown session id.** Type `page_closed`, classified by the `session "` marker.

```json
{"ok":false,"error":{"type":"page_closed","message":"session_close: session \"page-999\" not found","hint":"the page was closed, swept by the idle TTL, or the browser restarted — call fetch_page to re-open the URL"}}
```

**Second close of the same id.** Type `page_closed`. When another live browser holds that number:

```json
{"ok":false,"error":{"type":"page_closed","message":"session_close: browsers: page \"page-2\" not found","hint":"the page was closed, swept by the idle TTL, or the browser restarted — call fetch_page to re-open the URL"}}
```

Without such a browser the message is `session_close: session "page-2" not found` in the same envelope. Both mean the tab is already gone.

**Session from before a browser restart.** Type `page_closed`. Source-only, message as a template:

```
session_close: session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs
```

**Bad argument JSON.** Type `invalid_args`. Message `session_close: invalid arguments: json: cannot unmarshal …`. Send one JSON object with a string `session_id`. Do not send an array, a bare string, or a number.

Recovery in all cases: a close is final. Do not retry. Read live ids with `session_list` or `page_list`, or open the URL again with `fetch_page`.

## Tested calls

- 2026-10-25 — close, missing id, empty id, unknown id, double close. Exact message strings recorded live.
- 2026-10-26 — success envelope tested live on `https://www.iana.org/`: `{"ok":true,"data":"session page-1 closed (browser browser-1) — tab released"}`. Failure envelopes for the same message strings verified live across the shared dispatch (`page_fetch`, `page_act`) in the same session.

## When not to use it

Do not use `session_close` to close a browser. Use `browser_close`. A browser close also removes the Chromium process.

Do not use it to read page content. Use `page_fetch`.

Do not use it to list sessions. Use `session_list` or `page_list`.

Do not use it for a tab in a secondary browser that you created with `browser_create`. Use `browser_close` with your own `browser_id`.

Do not close a session you did not create. Other agents share the server, and ids can collide.

## Related tools

- `fetch_page` — opens a page and returns the `session_id` that this tool needs.
- `session_list` — lists live sessions. Use it to choose an id and to confirm a close.
- `page_list` — lists the pages of one browser. Use it to confirm a close inside a known browser.
- `page_fetch` — reads a live session. It fails after the session closes.
- `browser_close` — closes a whole browser, its pages, and the Chromium process. Prefer it for secondary browsers and for full cleanup.

## Maintainer anchors

- Dispatch and prose payload: `code://pkg/mcp/tools/session_tools.go#L67-L89`.
- Id resolution across browsers: `code://pkg/mcp/tools/session_tools.go#L75-L84`.
- Error markers that classify `invalid_args` and `page_closed`: `code://pkg/mcp/tools/envelope.go#L67-L130`.

Worked example: `.agents/skills/web-mcp/references/examples/session_close_example.md`.
