> Tool: `session_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# session_list

List the live pages of the default browser. Return a JSON array of page ids and URLs.

## What it does

`session_list` reads the live page map of the default browser (`browser-1`). It returns one JSON array. Each entry holds a page `id` and a `url`.

A session is one browser tab. The id is the address that `page_fetch`, `page_act`, and `session_close` use. A session stays live across navigations, redirects, and tool calls.

The tool takes no arguments. Use it to audit tabs when you do not know a browser id. Use it before you close tabs.

The tool has one large limit. It reads the default browser only. Pages in a browser that you created with `browser_create` do not appear. Test run: `browser-19` held `page-1` and `page-2`, and `session_list` returned `[]`.

The tool also has a side effect. It can launch Chromium. See the next sections.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| none | — | — | — | The tool declares no properties and no required fields. |

Send an empty object as the argument. The schema is `{"type":"object","properties":{}}` with no `required` list. The source is `code://pkg/mcp/tools/session_tools.go#L120-L125`.

The handler does not decode its arguments. The source is `code://pkg/mcp/tools/session_tools.go#L130-L140`. A host that forwards an unknown property gets the same result. Do not send options. The tool accepts none.

## Returns

The tool returns JSON text. It does not wrap the array in an object. The text is indented with two spaces.

Empty result, exact live payload:

```json
[]
```

Result with pages, exact shape:

```json
[
  {
    "id": "page-1",
    "url": "https://example.com"
  }
]
```

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id. Use it with `page_fetch`, `page_act`, and `session_close`. |
| `url` | string | The URL that `fetch_page` received when it created the page. It is not the current URL. |

Two fields exist. The tool description also mentions a title and timestamps. The code emits neither. The returned struct holds `id` and `url` only (`code://pkg/browsers/session.go#L92-L97`).

The server sorts entries by `id` as text. A browser with more than nine pages lists `page-10` before `page-2`.

Test run: the non-empty array was not observed through `session_list`. It needs a page in `browser-1`, and this test did not write into the shared browser. The shape comes from `code://pkg/browsers/browser.go#L690-L701`. `page_list` uses the same struct and returned this exact shape live.

## Behavior and limits

- **Scope: default browser only.** The handler calls `Manager.Open`, which resolves `browser-1`. The source is `code://pkg/mcp/tools/session_tools.go#L131`. Test run: `browser_list` showed `browser-1` empty and `browser-19` holding two pages. `session_list` returned `[]`.
- **A missing default browser hides live work.** If `browser-1` is not running, the call starts it and lists its empty page map. Pages in other live browsers stay invisible. This path was not run live.
- **It launches Chromium.** `Manager.Open` starts `browser-1` when no default browser runs. The launch uses the server options, not your `browser_create` options. That includes a persistent profile. This path was not run live, because `browser-1` was already live.
- **Read only on pages.** The tool does not navigate, reload, focus, or close. It copies the page map under the browser lock.
- **The URL is stale after navigation.** The `url` field is the creation URL, not the live address. The `page_list` reference records a live run: a page opened `https://example.com`, navigated to `https://example.org`, and still listed `https://example.com`. Use `page_fetch` with `what: "meta"` for the current URL and title.
- **Ids are unique inside one browser only.** Every browser numbers its tabs from `page-1`. Two live browsers can both hold a `page-1`. Keep the browser id with the session id.
- **No truncation.** There is no `max_bytes` argument. The tool returns every page in the map.
- **The 15 minute idle sweep removes pages.** A swept page disappears from the next result. Each result is a live snapshot.
- **No browser id in the result.** The array holds pages, not browsers. Use `browser_list` to map a page to its browser.

## Failure modes and recovery

The MCP server prefixes tool errors with `MCP error -32603: `. The strings below show the message after that prefix. Every error is wrapped with the `session_list: ` prefix (`code://pkg/mcp/tools/session_tools.go#L133`).

**The server is shutting down.**

```
session_list: browsers: manager has been shut down
```

This path was not run live. The message comes from `code://pkg/browsers/manager.go#L384`.

Recovery: restart the MCP server. Do not retry the call.

**The default browser fails to launch.**

```
session_list: browsers: launch browser browser-1: <cause>
```

This path was not run live. The message comes from `code://pkg/browsers/manager.go#L433`.

Recovery: read the cause. A bad binary path or a full disk stops the launch. Fix the server configuration. A locked profile needs a different profile or a stopped duplicate browser.

**The default profile cannot resolve.**

```
session_list: browsers: resolve profile for browser-1: <cause>
```

This path was not run live. The message comes from `code://pkg/browsers/manager.go#L417`.

Recovery: fix the profile directory in the `browser-mcp` config directory. Then retry.

**An empty array is not an error.**

`[]` means the default browser holds no pages. It does not mean the server has no pages. Test run: `[]` came back while another live browser held two tabs.

Recovery: call `browser_list`. Then call `page_list` with the id of your browser.

**A session is missing from the array.**

A page id from an earlier call does not appear. The tab may be closed, swept, or owned by a different browser.

Recovery: call `page_fetch` with the id. See the next section for the exact errors.

## How to find stale sessions

1. Call `session_list` and collect the live ids of `browser-1`.
2. Call `browser_list` and collect every live browser id.
3. Call `page_list` for each browser id that is not `browser-1`.
4. Mark every id in your transcript that appears in none of the results as stale.
5. Confirm a stale id with `page_fetch`. Do not call `session_close` on a guess.

A closed or swept session leaves the map at once. A later call for it fails.

**Closed session, per `page_fetch`:**

```
page_fetch: session "page-2" not found
```

Test run: a call with id `none` returned the same message with that id.

**Session from before a browser restart:**

```
page_fetch: session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs
```

This path was not run live. The message comes from `code://pkg/browsers/manager.go#L623`.

Recovery: the tab is gone. Call `fetch_page` and use the new id.

## When not to use it

Do not use `session_list` to list another browser. It never reads the browser id. Use `page_list` with the id from `browser_create`.

Do not use it to list browsers. Use `browser_list`.

Do not use it to prove that your own tabs are alive. A page you opened in a secondary browser is absent from every result. Use `page_list` with your `browser_id`.

Do not use it to get the current URL or title. Use `page_fetch` with `what: "meta"`.

Do not use it to read page content. Use `page_fetch`.

Do not call it in a loop to watch for changes. Each call is a snapshot and each call can launch Chromium.

## Related tools

- `browser_list` — lists every browser with its profile path and pages. Use it to find browsers that `session_list` cannot see.
- `page_list` — lists the pages of one named browser. It never launches Chromium.
- `fetch_page` — opens a page and returns the `session_id`. A new page then appears in the list.
- `page_fetch` — reads one session. It reports a stale id with the exact strings above.
- `page_act` — drives one session by id.
- `session_close` — closes one session. It takes no `browser_id` and closes in the default browser.
- `browser_close` — closes a browser and all of its pages. Use it with your own `browser_id`.

Worked example: `.agents/skills/web-mcp/references/examples/session_list_example.md`.
