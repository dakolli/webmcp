# session_list — worked example

A complete live sequence on an isolated browser, 2026-10-25. Read the tool description first. Use this file when a call fails.

Reference: `.agents/skills/web-mcp/references/tools/session_list.md`.

## Step 1 — create an isolated browser

```json
{ "name": "browser_create", "arguments": { "headless": true, "profile": "skill-session-list-98", "stealth": true } }
```

Expected `{"browser_id":"browser-19"}`. The number varies. Do not send `"persist": "false"` as a string. The handler wants a JSON boolean, and a string fails with:

```
MCP error -32603: browser_create: invalid arguments: json: cannot unmarshal string into Go struct field browserCreateArgs.persist of type bool
```

## Step 2 — list before you open anything

```json
{ "name": "session_list", "arguments": {} }
```

Expected live result: `[]`. The array is empty because `browser-1` had no pages.

## Step 3 — open two pages in your browser

```json
{ "name": "fetch_page", "arguments": { "browser_id": "browser-19", "url": "https://en.wikipedia.org/wiki/Web_browser", "wait_for": "idle" } }
{ "name": "fetch_page", "arguments": { "browser_id": "browser-19", "url": "https://httpbin.org/forms/post", "wait_for": "idle" } }
```

Expected ids: `page-1` for the article and `page-2` for the form.

## Step 4 — list again

```json
{ "name": "session_list", "arguments": {} }
```

Expected live result:

```json
[]
```

Two tabs are live and the result is empty. This is the scope limit. The tool reads `browser-1` only. Your tabs live in `browser-19`.

## Step 5 — read your own browser instead

```json
{ "name": "page_list", "arguments": { "browser_id": "browser-19" } }
```

Expected live result:

```json
[
  {
    "id": "page-1",
    "url": "https://en.wikipedia.org/wiki/Web_browser"
  },
  {
    "id": "page-2",
    "url": "https://httpbin.org/forms/post"
  }
]
```

This is the array shape that `session_list` returns, scoped to one browser.

## Step 6 — close your browser

```json
{ "name": "browser_close", "arguments": { "browser_id": "browser-19" } }
```

Expected: `browser browser-19 closed`. Then call `session_list` again:

```json
{ "name": "session_list", "arguments": {} }
```

Expected live result: `[]`. Nothing changes, because nothing was ever listed. `browser_list` no longer shows `browser-19`.

## Step 7 — the non-empty result

A page in `browser-1` produces this shape:

```json
[
  {
    "id": "page-1",
    "url": "https://example.com"
  }
]
```

This test did not write into `browser-1`. The shape comes from the source. It holds `id` and `url` only. Do not expect a title or a timestamp.

## Step 8 — find stale sessions

1. Call `session_list` for `browser-1`.
2. Call `browser_list` for every browser id.
3. Call `page_list` for each of those ids.
4. Confirm a suspect id with `page_fetch`.
5. Close only a tab that you created.

A closed tab fails at once. The live run used id `none`:

```
page_fetch: session "page-2" not found
```
