> Tool: `browser_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_list example

Complete MCP call sequences. Each request is a JSON object. Each response shows the shape the tool returns.

Read the reference at `.agents/skills/web-mcp/references/tools/browser_list.md` first. The tool takes no arguments, so every call sends `{}`. In the examples, `<profiles-dir>` is the absolute profiles directory.

## 1. Read the state before you create anything

Request:

```json
{ "tool": "browser_list", "arguments": {} }
```

Expected response when one browser is live:

```json
[ { "browser_id": "browser-1", "profile_dir": "<profiles-dir>/default", "pages": [ { "id": "page-1", "url": "https://react.dev/learn" } ] } ]
```

Use `browser-1` only when you intend to act on it. Do not close it if you did not create it.

## 2. Create your own browser and list again

Request:

```json
{ "tool": "browser_create", "arguments": { "headless": true, "stealth": true, "persist": false } }
```

Expected response:

```json
{ "browser_id": "browser-2" }
```

Request:

```json
{ "tool": "browser_list", "arguments": {} }
```

Expected response. The order of the two entries can change between calls:

```json
[
  { "browser_id": "browser-1", "profile_dir": "<profiles-dir>/default", "pages": [ { "id": "page-1", "url": "https://react.dev/learn" } ] },
  { "browser_id": "browser-2", "profile_dir": "<profiles-dir>/.ephemeral/browser-2-1a2b3c", "pages": [] }
]
```

Pass `persist:false` to get an ephemeral profile. The server deletes it on close. If your host does not expose `persist`, pass a distinct `profile` so the new browser does not reuse the default.

## 3. Open a page and confirm the page entry

Request:

```json
{ "tool": "fetch_page", "arguments": { "browser_id": "browser-2", "url": "https://en.wikipedia.org/wiki/Web_browser", "wait_for": "stable" } }
```

Expected response starts with:

```json
{ "session_id": "page-1", "title": "Web browser - Wikipedia", "url": "https://en.wikipedia.org/wiki/Web_browser" }
```

Request:

```json
{ "tool": "browser_list", "arguments": {} }
```

Expected entry for your browser:

```json
{
  "browser_id": "browser-2",
  "profile_dir": "<profiles-dir>/.ephemeral/browser-2-1a2b3c",
  "pages": [ { "id": "page-1", "url": "https://en.wikipedia.org/wiki/Web_browser" } ]
}
```

Both browsers can report `page-1`. Keep the `browser_id` with the page id.

## 4. Close your browser and confirm the removal

Request:

```json
{ "tool": "browser_close", "arguments": { "browser_id": "browser-2" } }
```

Expected response:

```
browser browser-2 closed
```

Request:

```json
{ "tool": "browser_list", "arguments": {} }
```

Expected response once your browser is gone. `browser-1` remains because you did not close it:

```json
[ { "browser_id": "browser-1", "profile_dir": "<profiles-dir>/default", "pages": [ { "id": "page-1", "url": "https://react.dev/learn" } ] } ]
```

## 5. Choose the next `browser_id`

- Read `browser_id` from the entry you own.
- Read `pages` to find a `session_id` before `page_fetch` or `page_act`.
- Read `profile_dir` to spot two browsers that share one profile. Two browsers must not share a profile.
- Call `browser_list` again after any close.
