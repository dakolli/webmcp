> Tool: `page_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_list

List the open pages of one browser. Return the page id and the creation URL for each page.

## What it does

`page_list` reads the live page map of one browser. It returns a JSON array. Each entry holds a page `id` and a `url`.

Use it to audit tabs. Use it to map a page id to the browser that owns it. Use it before you close sessions.

The tool is read only. It does not open a page. It does not launch Chromium.

`page_list` requires a `browser_id`. This is the main difference from `session_list`, which takes no arguments and can launch a browser.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `browser_id` | string | yes | none | The id from `browser_create`. The tool lists the pages of this browser only. |

The schema declares one property and one required field. The source is `code://pkg/mcp/tools/browser.go#L292`.

Pass the id as a string. Do not pass a number. The host may coerce `123` to `"123"`. The call then fails as an unknown id.

## Returns

The tool returns JSON text. The text is an array of objects. This is the live shape:

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
| `url` | string | The URL that `fetch_page` received when it created the page. It is not the current URL. |

The array is indented with two spaces. An empty browser returns `[]` on one line. There is no wrapper object and no `pages` field.

The server sorts entries by `id` as text. A browser with more than nine pages lists `page-10` before `page-2`.

## Behavior and limits

- The call is a live snapshot. It holds the browser lock while it copies the page map.
- The `url` field is stale after navigation. Test run: `page-1` opened `https://example.com` and then navigated to `https://example.org`. `page_list` still returned `https://example.com`. Use `page_fetch` with `what: "meta"` for the current URL and title.
- A page id is unique inside one browser only. Test run: `browser-5` returned `page-1` while other browsers were running. Another browser can also have a `page-1`. Always keep the `browser_id` with the page id.
- The tool does not launch Chromium. Test run: `page_list` on an empty browser returned `[]` and created no browser. An unknown id returned a not-found error and launched nothing. `session_list` does the opposite and may launch a browser.
- The tool does not truncate. There is no `max_bytes` argument. It returns every page in the map.
- The 15 minute idle sweep removes pages. A closed page disappears from the next result.
- The tool has no side effects on pages. It does not navigate, reload, or focus.
- A closed browser cannot be listed. The call returns the not-found error.

## Failure modes and recovery

The MCP server prefixes tool errors with `MCP error -32603: `. The strings below show the message after that prefix.

**Missing `browser_id`.**

```
page_list: missing required argument "browser_id"
```

Test run: a call with no arguments returned this error. An empty string `""` returns the same message.

Recovery: send `{"browser_id": "<id>"}`. Read the id from `browser_create` or `browser_list`.

**Unknown or closed id.**

```
page_list: browser "browser-999" not found (running: browser-2, browser-3, browser-4, browser-5): unknown browser
```

Test run: `browser-999` returned this error. The running list shows the live ids.

```
page_list: browser "browser-5" not found (running: browser-2, browser-3, browser-4, browser-6): unknown browser
```

Test run: this was the same browser after `browser_close`. The closed id no longer appears in the running list.

Recovery: call `browser_list` and use a live id. If no browser is live, call `browser_create`.

**No browser at all.**

```
page_list: no browser running (create one with browser_create): unknown browser
```

This path was not run live, because the shared server had live browsers. The message comes from the source.

**Bad argument JSON.**

```
page_list: invalid arguments: json: cannot unmarshal ...
```

Recovery: send one JSON object with a string `browser_id`. Do not send an array, a bare string, or a number.

## When not to use it

Do not use `page_list` to list browsers. Use `browser_list`.

Do not use `page_list` to read page content. Use `page_fetch`.

Do not use `page_list` to get the current URL or title after navigation. Use `page_fetch` with `what: "meta"`.

Do not use `page_list` when you do not know the browser id. Use `browser_list` first.

Do not expect a title or a timestamp. The tool returns only `id` and `url`.

## Related tools

- `browser_create` — creates a browser and returns the `browser_id` that `page_list` needs.
- `browser_list` — lists all live browsers with their pages. Use it to find the right `browser_id`.
- `fetch_page` — opens a new page in a browser. The page then appears in `page_list`.
- `page_fetch` — reads one page. Use `what: "meta"` for the current URL and title.
- `session_close` — closes one page. Confirm the browser first.
- `session_list` — lists pages without a `browser_id`. It uses the default browser and may launch it.
