> Tool: `page_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_list

List the open pages of one browser. Return the id and creation URL of each page.

## What it does

`page_list` reads the live page map of one browser. It returns a JSON array. Each entry holds an `id` and a `url`.

Use it to audit tabs before you close them. Use it to recover a page id after a lost tool result. Use it to confirm that `fetch_page` created a page.

The call is read only. It does not open, close, navigate, or focus a page.

`page_list` requires a `browser_id`. This is the main difference from `session_list`.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `browser_id` | string | yes | none | The id from `browser_create`. The tool lists the pages of this browser only. |

The schema declares one property and one required field. Source: `code://pkg/mcp/tools/browser.go#L332-L345`.

Pass the id as a string. Do not pass a number. The host may coerce `123` to `"123"`. The call then fails as an unknown id.

## Returns

The tool returns indented JSON text. The live shape is an array of page objects:

```json
[
  {
    "id": "page-1",
    "url": "https://example.com"
  },
  {
    "id": "page-2",
    "url": "https://httpbin.org/forms/post"
  }
]
```

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id. Use it with `page_fetch`, `page_act`, and `session_close`. |
| `url` | string | The URL that `fetch_page` received at creation. This is not the current URL after navigation. |

The source type is `PageInfo` at `code://pkg/browsers/session.go#L90-L97`. The browser sorts the array by `id` as text at `code://pkg/browsers/browser.go#L686-L697`.

- An empty browser returns `[]` on one line.
- There is no wrapper object and no `pages` field.
- Sorting is lexical. A browser with ten pages lists `page-10` before `page-2`.
- The tool does not truncate. It has no `max_bytes` argument. It returns every page in the map.

## Behavior and limits

- The result is a live snapshot. The tool holds the browser lock while it copies the page map.
- The `url` field goes stale after navigation. Live test: `page-1` opened `https://example.com` and then navigated to `https://example.org/`. `page_list` still returned `https://example.com`. `page_fetch` with `what: "meta"` returned `https://example.org/`. Use `meta` for the current URL and title.
- Page ids are per-browser counters. Two browsers can each have a `page-1`. Always keep the `browser_id` with the page id.
- The 15 minute idle sweep removes pages. A closed page disappears from the next result.
- The tool does not launch Chromium. It calls `Manager.Browser`, not `Manager.Open`. Source: `code://pkg/browsers/manager.go#L521-L562`.
- Test run: `page_list` on `browser-4` before any page returned `[]`. The browser stayed in place and no page appeared.
- Test run: `page_list` with `browser-999` returned a not-found error. The error listed the live ids at that time, `browser-1` and `browser-4`. It created no browser.
- A dead Chromium is removed on the probe. The call then returns a died error instead of a page list.
- The tool has no side effects on pages. It does not navigate, reload, or focus.
- There is no title, status, timestamp, or browser field in an entry.

## Failure modes and recovery

The MCP server prefixes tool errors with `MCP error -32603: `. The strings below show the full message.

**Missing `browser_id`.**

```
MCP error -32603: page_list: missing required argument "browser_id"
```

Test run: a call with no arguments returned this error. An empty string `""` returns the same message. Recovery: send `{"browser_id": "<id>"}`. Read the id from `browser_create` or `browser_list`.

**Unknown or closed id.**

```
MCP error -32603: page_list: browser "browser-999" not found (running: browser-1, browser-4): unknown browser
```

Test run: `browser-999` returned this error. The running list shows the live ids. Recovery: call `browser_list` and use a live id. If no browser is live, call `browser_create`.

**No browser at all.**

```
page_list: no browser running (create one with browser_create): unknown browser
```

This path was not run live, because the shared server had live browsers. The message comes from the source at `code://pkg/browsers/manager.go#L541-L546`.

**Dead browser.**

```
MCP error -32603: page_list: browsers: browser browser-4 died
```

The probe found a dead Chromium. The manager removed it. Recovery: call `browser_create` and open the pages again.

**Bad argument JSON.**

```
MCP error -32603: page_list: invalid arguments: json: cannot unmarshal ...
```

Recovery: send one JSON object with a string `browser_id`. Do not send an array, a bare string, or a number.

## Difference from session_list

`session_list` takes no arguments. It calls `Manager.Open`, so it can launch the default browser `browser-1` with default options. Source: `code://pkg/mcp/tools/session_tools.go#L127-L140`.

In this build, `session_list` returns the pages of the default browser only. It does not merge pages from other browsers. Live test: `browser-4` had `page-1` and `page-2`. `session_list` returned `[]` because `browser-1` had no pages.

`page_list` never launches a browser. It reports the browser you name. It is the safe choice when several browsers are live.

Both tools return the same `{id,url}` page objects. Neither returns a title or a timestamp, although the `session_list` description mentions them.

## When to use it

Use `page_list` when you know the `browser_id` and need its open pages. Use it before `session_close` so you close the right page.

Use `browser_list` to discover browsers. It returns `browser_id`, `bin`, `profile_dir`, and a nested `pages` array for every live browser.

Do not use `page_list` to read page content. Use `page_fetch`.

Do not use `page_list` to get the current URL or title. Use `page_fetch` with `what: "meta"`.

Do not use `page_list` when you do not know the browser id. Use `browser_list` first.

## Call examples

List a browser with two pages:

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

Expected result:

```json
[{"id":"page-1","url":"https://example.com"},{"id":"page-2","url":"https://httpbin.org/forms/post"}]
```

Use an unknown id:

```json
{"tool":"page_list","arguments":{"browser_id":"browser-999"}}
```

Expected error:

```
MCP error -32603: page_list: browser "browser-999" not found (running: browser-1, browser-4): unknown browser
```

Omit the id:

```json
{"tool":"page_list","arguments":{}}
```

Expected error:

```
MCP error -32603: page_list: missing required argument "browser_id"
```

## Related tools

- `browser_create` — creates a browser and returns the `browser_id` that `page_list` needs.
- `browser_list` — lists all live browsers with their pages. Use it to find the right `browser_id`.
- `fetch_page` — opens a new page in a browser. The page then appears in `page_list`.
- `page_fetch` — reads one page. Use `what: "meta"` for the current URL and title.
- `session_close` — closes one page. Confirm the browser first.
- `session_list` — lists the default browser's pages without a `browser_id`. It may launch `browser-1`.
