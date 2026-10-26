> Tool: `session_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# session_list

List the live sessions the manager resolves for you. Return a JSON array of page ids and
URLs.

## What it does

`session_list` reads the live page map of one browser and returns it under `data`. Each
entry holds a page `id` and a `url`.

A session is one browser tab. The id is the address that `page_fetch`, `page_act`, and
`session_close` use. A session stays live across navigations, redirects, and tool calls.

The tool takes no arguments.

**Scope.** The tool does not read one fixed id. The manager resolves a browser for the
caller: your own browser first, then an unowned default, else it launches one for you.
Live test: no `browser-1` was running and two unowned browsers were live. `session_list`
returned the pages of `browser-3`, an unowned browser created by another client. Which
candidate wins is not deterministic. Use `page_list` with an explicit `browser_id` when
the answer must be exact.

The tool also has a side effect. It can launch Chromium. See Behavior and limits.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| none | — | — | — | The tool declares no properties and no required fields. |

Send an empty object as the argument. The schema is `{"type":"object","properties":{}}`
with no `required` list. The source is `code://pkg/mcp/tools/session_tools.go#L121-L126`.

The handler does not decode its arguments. The source is
`code://pkg/mcp/tools/session_tools.go#L131-L141`. A host that forwards an unknown
property gets the same result. Do not send options. The tool accepts none.

## Returns

Every result is one envelope. The payload lives in `data`. The envelope rule with the
failure branch and the full error-type list is in [SKILL.md](../../SKILL.md) under
Cross-cutting rules.

On success `data` is an array of page objects. Live result:

```json
{"ok":true,"data":[{"id":"page-1","url":"https://news.ycombinator.com/news"}]}
```

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id. Use it with `page_fetch`, `page_act`, and `session_close`. |
| `url` | string | The URL that `fetch_page` received when it created the page. It is not the current URL. |

Two fields exist. The tool description also mentions a title and timestamps. The code
emits neither. The returned struct holds `id` and `url` only
(`code://pkg/browsers/session.go#L153-L158`).

The server sorts entries by `id` as text. A browser with more than nine pages lists
`page-10` before `page-2`.

An empty resolution returns `{"ok":true,"data":[]}`. That means the resolved browser holds
no pages. It does not mean the server has no pages.

## Behavior and limits

- **Resolution, not `browser-1`.** The handler calls `Manager.Open`
  (`code://pkg/mcp/tools/session_tools.go#L132`). `Manager.Open` resolves your own browser
  first, then an unowned default, else it launches one for you
  (`code://pkg/browsers/manager.go#L723-L744`). It never returns a browser owned by
  another client. Live test: see Scope above.
- **It can launch Chromium.** With nothing to resolve, the call launches a browser keyed
  to your owner. On `serve mcp` that browser gets an ephemeral profile
  (`code://pkg/browsers/manager.go#L715-L721`). Call `browser_create` first when you want
  to control the launch options. This launch path was not observed live.
- **Read only on pages.** The tool does not navigate, reload, focus, or close. It copies
  the page map under the browser lock.
- **The URL is stale after navigation.** The `url` field is the creation URL, not the live
  address. Use `page_fetch` with `what: "meta"` for the current URL and title.
- **Ids are unique inside one browser only.** Every browser numbers its tabs from
  `page-1`. Two live browsers can both hold a `page-1`. Keep the browser id with the
  session id.
- **No truncation.** There is no `max_bytes` argument. The tool returns every page in the map.
- **The 15 minute idle sweep removes pages.** A swept page disappears from the next
  result. Each result is a live snapshot.
- **No browser id in the result.** The array holds pages, not browsers. Use
  `browser_list` to map a page to its browser.

## Failure modes and recovery

Every failure is one envelope:

```json
{"ok":false,"error":{"type":<type>,"message":"session_list: …","hint":"<one-line fix>"}}
```

The message starts with `session_list: `. The tool decodes no arguments, so there is no
`invalid_args` path. Failures come from the engine launch or shutdown and are typed
`unknown`.

**The server is shutting down.** Typed `unknown`, message
`session_list: browsers: manager has been shut down`. This path was not run live. The
message comes from `code://pkg/browsers/manager.go#L749`. Recovery: restart the MCP
server. Do not retry the call.

**The launch fails.** Typed `unknown`, message
`session_list: browsers: launch browser <id>: <cause>`
(`code://pkg/browsers/manager.go#L841`). This path was not run live. Recovery: read the
cause. A bad binary path or a full disk stops the launch. Fix the server configuration.

**The profile cannot resolve.** Typed `unknown`, message
`session_list: browsers: resolve profile for <id>: <cause>`
(`code://pkg/browsers/manager.go#L809`). This path was not run live. Recovery: fix the
profile directory in the `browser-mcp` config directory. Then retry.

**A session is missing from the array.** A page id from an earlier call does not appear.
The tab may be closed, swept, or resolved into a different browser this time. Confirm a
stale id with `page_fetch`. Both stale messages are typed `page_closed`:

```
page_fetch: session "page-2" not found
```

```
page_fetch: session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs
```

The second message was not run live. It comes from `code://pkg/browsers/manager.go#L1169`.
Recovery: the tab is gone. Call `fetch_page` and use the new id.

## How to find stale sessions

1. Call `session_list` and note the resolved browser's pages.
2. Call `browser_list` and collect every live browser id.
3. Call `page_list` for each browser id that matters to you.
4. Mark every id in your transcript that appears in none of the results as stale.
5. Confirm a stale id with `page_fetch`. Do not close a tab on a guess.

A closed or swept session leaves the map at once. A later call for it fails.

## When not to use it

Do not use `session_list` when you need the exact page set of one browser. Its resolution
is not deterministic with several candidates. Use `page_list` with the id from
`browser_create`.

Do not use it to list browsers. Use `browser_list`.

Do not use it to prove that your own tabs are alive if you did not create a browser. The
resolution can hand you an unowned browser that is not yours. Create a browser first,
then use `page_list`.

Do not use it to get the current URL or title. Use `page_fetch` with `what: "meta"`.
Do not use it to read page content. Use `page_fetch`.
Do not call it in a loop to watch for changes. Each call is a snapshot and each call can
launch Chromium.

## Related tools

- `browser_list` — lists every browser with its profile path and pages.
- `page_list` — lists the pages of one named browser. It never launches Chromium.
- `fetch_page` — opens a page and returns the `session_id`. A new page then appears in the list.
- `page_fetch` — reads one session. It reports a stale id with a `page_closed` envelope.
- `page_act` — drives one session by id.
- `session_close` — closes one session.
- `browser_close` — closes a browser and all of its pages. Use it with your own `browser_id`.

Worked example: `.agents/skills/web-mcp/references/examples/session_list_example.md`.
