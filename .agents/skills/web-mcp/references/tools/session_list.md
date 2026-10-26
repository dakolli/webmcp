> Tool: `session_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# session_list

List every live session across every live browser and identity. Return a JSON array of page
ids, URLs, and contexts.

## What it does

`session_list` reads the live page maps of every live browser and identity. It returns them
under `data`. Each entry holds a page `id`, `url`, and `context`.

A session is one browser tab. The id is the address that `page_fetch`, `page_act`, and
`session_close` use. A session stays live across navigations, redirects, and tool calls.

The tool takes one optional `identity` filter. Send `{}` to list every identity.

**Scope.** The tool reads every live browser and every identity in one snapshot. It never
resolves a single browser and never launches Chromium. With no browser running it returns
`{"ok":true,"data":[]}`. Use `page_list` with an explicit `browser_id` when you need one
browser only.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `identity` | string | no | none, lists every identity | Optional incognito identity filter. Lookup only. An unknown name fails and is never created. |

Send `{}` as the argument to list every identity. Send `{"identity":"victim"}` for one. The
schema is `code://pkg/mcp/tools/session_tools.go#L125-L135`. The handler is
`code://pkg/mcp/tools/session_tools.go#L146-L160`.

The handler decodes one optional `identity` string and passes it to `Manager.Sessions`.
That trims the string, and an empty string lists all.

## Returns

Every result is one envelope. The payload lives in `data`. The envelope rule with the
failure branch and the full error-type list is in [SKILL.md](../../SKILL.md) under
Cross-cutting rules.

On success `data` is an array of page objects. Live result:

```json
{"ok":true,"data":[{"id":"page-1","url":"https://example.org","context":"attacker"},{"id":"page-1","url":"https://news.ycombinator.com/news","context":"attacker"},{"id":"page-2","url":"https://www.iana.org/help/example-domains","context":"victim"},{"id":"page-2","url":"https://news.ycombinator.com/news?p=2","context":"victim"},{"id":"page-3","url":"https://developer.mozilla.org/en-US/docs/Web/API/Window/localStorage","context":"default"}]}
```

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id. Use it with `page_fetch`, `page_act`, and `session_close`. |
| `url` | string | The URL that `fetch_page` received when it created the page. It is not the current URL. |
| `context` | string | The incognito identity that owns the page. `default` is the browser's own cookie jar. |

The code emits three fields: `id`, `url`, and `context`. It emits no title and no timestamp.
Source: `code://pkg/browsers/session.go#L153-L161`. The tool description still names title
and timestamps, and the code omits them.

The server sorts entries by `id` as text. A browser with more than nine pages lists
`page-10` before `page-2`. Two browsers can both hold a `page-1`, so ids can repeat.

An empty array means no live browser holds a page.

## Behavior and limits

- **Aggregation, not one browser.** The handler calls `Manager.Sessions`
  (`code://pkg/mcp/tools/session_tools.go#L151`). That reads every live browser and every
  identity, then sorts the pages by `id` (`code://pkg/browsers/manager.go#L1016-L1041`).
- **It never launches Chromium.** `Manager.Sessions` reads `Manager.Browsers` and never
  calls `Open` or `OpenBrowser` (`code://pkg/browsers/manager.go#L1016-L1021`).
- **The identity filter is lookup only.** An unknown name fails with the live names and is
  never created (`code://pkg/browsers/manager.go#L1029-L1032`).
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

The message starts with `session_list: `. The `invalid_args` paths are a malformed argument
object and an unknown `identity`. Listing never launches or probes a browser.

**Unknown identity.** `session_list` looks the name up and does not create it. Live result:

```json
{"ok":false,"error":{"type":"invalid_args","message":"session_list: browsers: unknown identity \"ghost\" (known: attacker, default, victim)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

The `known:` list holds the live names plus `default`. Recovery: call `session_list` with
`{}` first, then reuse a name from a `context` value. Never guess a name.

**A session is missing from the array.** A page id from an earlier call does not appear.
The tab may be closed, swept, or present in another browser. Confirm a stale id with
`page_fetch`. Both stale messages are typed `page_closed`:

```
page_fetch: session "page-2" not found
```

```
page_fetch: session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs
```

The second message was not run live. It comes from `code://pkg/browsers/manager.go#L1245`.
Recovery: the tab is gone. Call `fetch_page` and use the new id.

## How to find stale sessions

1. Call `session_list` and `page_list` for every browser that matters.
2. Mark each id in your transcript that appears in neither result as stale.
3. Confirm a stale id with `page_fetch` before you close anything.

## When not to use it

Do not use `session_list` when you need the page set of one browser. It aggregates every
live browser. Use `page_list` with the id from `browser_create`.

Do not use it to list browsers. Use `browser_list`.

Do not use it to prove that your own tabs are alive. It includes every live browser, so
another agent's pages appear too. Create a browser first, then use `page_list`.

Do not use it to get the current URL or title. Use `page_fetch` with `what: "meta"`.
Do not use it to read page content. Use `page_fetch`.
Do not call it in a loop to watch for changes. Each call is a snapshot and nothing launches.

## Related tools

- `browser_list` — lists every browser with its profile path and pages.
- `page_list` — lists the pages of one named browser. It never launches Chromium.
- `fetch_page` — opens a page and returns the `session_id`. A new page then appears in the list.
- `page_fetch` — reads one session. It reports a stale id with a `page_closed` envelope.
- `page_act` — drives one session by id.
- `session_close` — closes one session.
- `browser_close` — closes a browser and all of its pages. Use it with your own `browser_id`.

Worked example: `.agents/skills/web-mcp/references/examples/session_list_example.md`.
