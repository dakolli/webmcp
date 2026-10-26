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
| `browser_id` | string | no | none | Optional. Resolve the session inside this browser only, for example `browser-2`. Omit it when exactly one live browser holds the id. |
| `owner` | string | no | none | Optional. Restrict the lookup to browsers with this owner label, 1 to 64 characters of `A-Z`, `a-z`, `0-9`, `.`, `_`, `-`. |

The schema declares three properties and one required field. The source is `code://pkg/mcp/tools/session_tools.go#L51-L69`.

Pass the id as a string, for example `"page-2"`. Do not pass a bare number. `browser_id` wins over `owner` when both are given.

## Returns

The success payload rides inside the result envelope, so the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the `{"ok":false,"error":…}` shape.

The payload is one prose string inside `data`. Source (`code://pkg/mcp/tools/session_tools.go#L105-L109`), tested live on 2026-10-26 on `https://www.iana.org/`:

```json
{"ok":true,"data":"session page-1 closed (browser browser-1) — tab released"}
```

The string names the id you sent and the browser that owned it. There is no state delta and no count.

## Behavior and limits

- **Tab effect.** The tab closes. The page leaves the browser page map before the network close starts. A wedged close still removes the id.
- **Resource effect.** Network observation and console capture stop. The page handle is released. Memory for that tab is freed.
- **Browser.** The browser process stays alive. The profile directory stays on disk.
- **Cookies.** They stay. They live in the profile, not in the tab. A new `fetch_page` in the same browser can reuse the login.
- **Other sessions.** They stay live. Closing one tab does not touch another tab.
- **Not idempotent.** A second close fails. See the next section.
- **Close budget.** The engine gives the network close about 10 seconds. A failed close returns an error and still drops the id.
- **Idle sweep.** The idle TTL sweep can close the tab before you do. A later close then fails as unknown.
- **Lookup scope.** `browser_id` resolves the id inside that one browser. `owner` restricts the lookup to browsers with that owner. With one live browser a bare id resolves.
- **Ambiguous bare id.** With two or more live browsers a bare id fails as `invalid_args`, and the message names every browser and its owner. Pass `browser_id` or `owner`.
- **Foreign owner.** A session held outside the declared owner is refused as `not_found`. The message names the holding browser and owner, and the holding tab is left untouched.
- **No launch.** The id resolves from the live browser registry. The call never launches Chromium and never touches a browser that does not own the session. Call `browser_create` first to control launch options.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The prefix `session_close: ` opens the message. This tool produces the types `invalid_args`, `not_found`, `page_closed`, and `unknown`. Source: `code://pkg/mcp/tools/envelope.go#L16-L56`.

**Missing or empty `session_id`.** Type `invalid_args`.

```json
{"ok":false,"error":{"type":"invalid_args","message":"session_close: missing required argument \"session_id\"","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

**Unknown session id.** Type `page_closed`, classified by the `session "` marker.

```json
{"ok":false,"error":{"type":"page_closed","message":"session_close: session \"page-999\" not found","hint":"the page was closed, swept by the idle TTL, or the browser restarted — call fetch_page to re-open the URL"}}
```

**Second close of the same id.** Type `page_closed`. With one live browser:

```json
{"ok":false,"error":{"type":"page_closed","message":"session_close: session \"page-2\" not found","hint":"the page was closed, swept by the idle TTL, or the browser restarted — call fetch_page to re-open the URL"}}
```

With two or more live browsers the bare id is refused before the lookup runs, so the same call reports `invalid_args` instead. See the ambiguous case below.

**Session held outside the declared owner.** Type `not_found`. The message names the holding browser and owner, and the holding tab is not touched:

```json
{"ok":false,"error":{"type":"not_found","message":"session_close: session \"page-2\" is not in owner task-a — held by browser-2 (owner task-b); pass browser_id to reach it on purpose","hint":"the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner"}}
```

**Bare id with two or more live browsers.** Type `invalid_args`. Source strings, pinned by `code://pkg/mcp/tools/envelope_test.go#L282-L325`:

```
session_close: session "page-3" needs a browser — 2 browsers are live: browser-1 (owner task-a), browser-2 (owner task-b); pass browser_id or owner
session_close: session "page-3" needs a browser — 2 browsers hold it: browser-1 (owner task-a), browser-2 (owner task-b); pass browser_id
```

The hint is `this session id needs a browser — pass browser_id or owner to choose the browser that holds it`.

**Unknown `browser_id`.** Type `invalid_args` with the hint `that browser_id is not live — call browser_list for the running ids and pass one of them`. Read `browser_list` first.

**Session from before a browser restart.** Type `page_closed`. Source-only, message as a template:

```
session_close: session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs (live: page-2)
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

Do not use it for another agent's tab. Pass `browser_id` or `owner` to close your own tab in a secondary browser, and use `browser_close` when you want the whole browser gone.

Do not close a session you did not create. Other agents share the server, and ids can collide.

## Related tools

- `fetch_page` — opens a page and returns the `session_id` that this tool needs.
- `session_list` — lists live sessions. Use it to choose an id and to confirm a close.
- `page_list` — lists the pages of one browser. Use it to confirm a close inside a known browser.
- `page_fetch` — reads a live session. It fails after the session closes.
- `browser_close` — closes a whole browser, its pages, and the Chromium process. Prefer it for secondary browsers and for full cleanup.

## Maintainer anchors

- Dispatch and prose payload: `code://pkg/mcp/tools/session_tools.go#L86-L110`.
- Declarative resolution order: `code://pkg/browsers/manager.go#L1293-L1364`.
- Foreign and ambiguous messages: `code://pkg/browsers/manager.go#L1418-L1445`.
- Error markers that classify `invalid_args`, `not_found`, and `page_closed`: `code://pkg/mcp/tools/envelope.go#L67-L130`.

Worked example: `.agents/skills/web-mcp/references/examples/session_close_example.md`.
