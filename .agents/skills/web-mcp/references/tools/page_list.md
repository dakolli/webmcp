> Tool: `page_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_list

List the open pages of one browser. Return the id, live URL, title, context, and opener of each page.

## What it does

`page_list` reads the live page map of one browser. Its `data` payload is a JSON array.
Each entry holds an `id`, `url`, `context`, and `opener`, plus `title` and `stale` when present.

Use it to audit tabs before you close them. Use it to recover a page id after a lost
tool result. Use it to confirm that `fetch_page` created a page.

The call is read only. It does not open, close, navigate, or focus a page. It never
launches a browser.

`page_list` requires a `browser_id`. This is the main difference from `session_list`.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `browser_id` | string | yes | none | The id from `browser_create`. The tool lists the pages of this browser only. |

The schema declares one property and one required field. Source:
`code://pkg/mcp/tools/browser.go#L501-L512`.

Pass the id as a string. Do not pass a number. The host may coerce `123` to `"123"`. The
call then fails as an unknown id.

## Returns

Every result is one envelope. The payload lives in `data`. The envelope rule with the
failure branch and the full error-type list is in [SKILL.md](../../SKILL.md) under
Cross-cutting rules.

On success `data` is an array of page objects. Live result for a two-page browser:

```json
{"ok":true,"data":[{"id":"page-1","url":"https://example.org","context":"attacker"},{"id":"page-2","url":"https://www.iana.org/help/example-domains","context":"victim"}]}
```

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id. Use it with `page_fetch`, `page_act`, and `session_close`. |
| `url` | string | The live URL of the tab, updated after navigation and redirects. |
| `title` | string | The live document title. Omitted when empty. |
| `stale` | boolean | Present when the live URL and title could not be read. `url` then holds the last known value. |
| `context` | string | The incognito identity that owns the page. `default` is the browser's own cookie jar. |
| `opener` | string | The id of the page that opened this popup. Absent for a top-level page. |

The source type is `PageInfo` at `code://pkg/browsers/session.go#L156-L171`. The browser
sorts the array by `id` as text at `code://pkg/browsers/browser.go#L1058-L1073`.

- An empty browser returns `{"ok":true,"data":[]}`. Live test: `page_list` on a browser
  before any page returned exactly that.
- There is no second wrapper. `data` is the array itself.
- Sorting is lexical. A browser with ten pages lists `page-10` before `page-2`.
- The tool does not truncate. It has no `max_bytes` argument. It returns every page in the map.

## Behavior and limits

- The result is a live snapshot. The tool holds the browser lock while it copies the page map.
- The `url` and `title` fields come from the live tab at call time. They refresh after navigation and redirects. Live test: `page-1` opened `https://example.com` and then navigated to `https://example.org/`. `page_list` returned `https://example.org/` with its new title.
- A busy or dead tab reports `stale: true` and the last known URL. The tool never waits on the page operation lock.
- Page ids are per-browser counters. Two browsers can each have a `page-1`. Always keep the `browser_id` with the page id.
- The 15 minute idle sweep removes pages. A closed page disappears from the next result.
- The tool never launches Chromium. It calls `Manager.Browser`, not `Manager.Open`. Source: `code://pkg/browsers/manager.go#L946-L985`.
- Test run: `page_list` with `browser-999` returned the unknown envelope below. It created no browser.
- A dead Chromium is removed on the probe. The call then returns an `unknown` envelope whose message is `page_list: browsers: browser <id> died`.
- **Popups.** A page opened by a `target="_blank"` link or `window.open` carries `opener`. Closing the opener closes its popups. A browser holds at most 20 pages, popups included.
- The tool has no side effects on pages. It does not navigate, reload, or focus.
- There is no status, timestamp, or `browser_id` field in an entry. `context` names
  the identity that owns the page.

## Failure modes and recovery

Every failure is one envelope:

```json
{"ok":false,"error":{"type":<type>,"message":"page_list: …","hint":"<one-line fix>"}}
```

The message starts with `page_list: `. Types seen by this tool: `invalid_args` and
`unknown`.

**Missing `browser_id`.** Live result:

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_list: missing required argument \"browser_id\"","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Test run: a call with no arguments returned this error. An empty string `""` returns the
same message. Recovery: send `{"browser_id": "<id>"}`. Read the id from `browser_create`
or `browser_list`.

**Unknown or closed id.** Live result:

```json
{"ok":false,"error":{"type":"unknown","message":"page_list: browser \"browser-999\" not found (running: browser-3 (owner: unowned), browser-4 (owner: unowned)): unknown browser"}}
```

Test run: `browser-999` returned this error. The running list annotates each live id with
its owner. It created no browser. Recovery: call `browser_list` and use a live id. If no
browser is live, call `browser_create`.

**No browser at all.**

```json
{"ok":false,"error":{"type":"unknown","message":"page_list: no browser running (create one with browser_create): unknown browser"}}
```

This path was not run live, because live browsers existed. The message comes from the
source at `code://pkg/browsers/manager.go#L966`. Recovery: call `browser_create`.

**Dead browser.** The probe found a dead Chromium and the manager removed it. The
envelope is typed `unknown` with message `page_list: browsers: browser browser-4 died`.
Recovery: call `browser_create` and open the pages again.

**Bad argument JSON.**

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_list: invalid arguments: json: cannot unmarshal …","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: send one JSON object with a string `browser_id`. Do not send an array, a bare
string, or a number.

## Difference from session_list

`session_list` takes an optional `identity` filter. It reads every live browser through
`Manager.Sessions`, so it never launches one. Source:
`code://pkg/mcp/tools/session_tools.go#L146-L160`.

In this build `session_list` reads every live browser and identity. See its reference for
the filter rule.

`page_list` never launches a browser. It reports the browser you name. It is the safe
choice when several browsers are live.

Both tools return the same page objects under `data`. Both emit `id`, `url`, `context`, and
`opener`, plus `title` and `stale` when present. Neither emits a timestamp.

## When to use it

Use `page_list` when you know the `browser_id` and need its open pages. Use it before
`session_close` so you close the right page.

Use `browser_list` to discover browsers. It returns `browser_id`, `bin`, `profile_dir`,
and a nested `pages` array for every live browser.

Do not use `page_list` to read page content. Use `page_fetch`.
Do not use `page_list` for meta tags. Use `page_fetch` with `what: "meta"`.
Do not use `page_list` when you do not know the browser id. Use `browser_list` first.

## Related tools

- `browser_create` — creates a browser and returns the `browser_id` that `page_list` needs.
- `browser_list` — lists all live browsers with their pages. Use it to find the right `browser_id`.
- `fetch_page` — opens a new page in a browser. The page then appears in `page_list`.
- `page_fetch` — reads one page. Use `what: "meta"` for the meta tags.
- `session_close` — closes one page. Confirm the browser first.
- `session_list` — lists pages across every live browser and identity. It never launches one.

Worked example: `.agents/skills/web-mcp/references/examples/page_list_example.md`.
