> Tool: `browser_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_list

List every live browser. Return each browser id, its profile directory, and its open pages.

## What it does

`browser_list` reads the server's live browser map. It returns one JSON array. Each entry is one browser. Each entry holds the browser id, the profile directory, and the pages of that browser.

Use it before you open, close, or act on pages. It is the only tool that reports every browser in one call.

The tool is read only. It does not open a browser. It does not close a browser. It does not launch Chromium.

`browser_list` takes no arguments. Unlike `session_list`, it never opens a browser to answer.

## Inputs

The tool takes no arguments. The input schema is an empty object with no properties.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| none | none | no | none | The tool reads server state and ignores any arguments. |

The schema is `code://pkg/mcp/tools/browser.go#L167`. The handler is `code://pkg/mcp/tools/browser.go#L175`. The handler never decodes the arguments, so an extra argument does not cause a validation error.

## Returns

The tool returns JSON text. The text is an array of browser objects. The server indents it with two spaces.

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `browser_id` | string | The browser id. Use it with `fetch_page` and `page_list`, and to pick a target before `browser_close`. |
| `bin` | string | The configured executable path. The field is omitted when the path is empty. |
| `profile_dir` | string | The absolute profile directory of the browser. The field is omitted when the path is empty. |
| `pages` | array | The open pages of this browser. The field is always present. |

Each `pages` entry has two fields:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id. Use it with `page_fetch`, `page_act`, and `session_close`. |
| `url` | string | The URL that created the page. It is not the current URL after navigation. |

In the examples, `<profiles-dir>` stands for the server's profiles directory. It is the `profiles` folder inside the browser-mcp config directory. The real value is an absolute path.

This is a live multi-browser response. `browser-7` used an explicit absolute profile path in the test.

```json
[
  {
    "browser_id": "browser-1",
    "profile_dir": "<profiles-dir>/default",
    "pages": [
      {
        "id": "page-1",
        "url": "https://react.dev/learn"
      }
    ]
  },
  {
    "browser_id": "browser-7",
    "profile_dir": "/tmp/webmcp-browser-list-test",
    "pages": []
  }
]
```

An empty `pages` array means the browser has no open tabs. The browser is still live.

A secondary browser created with `persist:false` reports a path under `<profiles-dir>/.ephemeral/`. That profile is deleted when the browser closes.

## Behavior and limits

- The tool is a live snapshot. It takes the manager lock to copy the browser map, then each browser lock to copy its pages.
- The tool does not sort browser entries. The source iterates a Go map, and the order can change between calls. Live calls returned `browser-7` first in one call and `browser-1` first in another.
- The tool sorts `pages` by `id` as text. A browser with more than nine pages lists `page-10` before `page-2`.
- `profile_dir` is absolute. `browser-1` uses the persistent default profile under `<profiles-dir>/default`. A browser with an explicit absolute profile path reports that path as given.
- `bin` was absent in every live response. The server resolves the binary at launch and stores no path in this field when the launch uses the default binary.
- `url` is the creation URL. A page that navigates keeps the old value. Use `page_fetch` with `what: "meta"` for the current URL and title.
- A page id is unique inside one browser only. Two browsers can both report `page-1`. Keep the `browser_id` with every page id.
- The tool does not truncate. There is no `max_bytes` argument and no pagination. It returns every browser and every page.
- The 15 minute idle sweep removes pages. A swept page disappears from the next result.
- The tool does not change state. It does not navigate, reload, focus, or close.
- The tool does not launch Chromium. `Browsers()` only locks and copies the map at `code://pkg/browsers/manager.go#L527`. A live call after a close created no new browser entry.

## Failure modes and recovery

`browser_list` has no argument validation. It ignores a malformed or extra argument, because the handler does not decode arguments.

**No browser is live.**

The tool returns `[]`. This is a normal result, not an error.

```
[]
```

This path was not run live. The shared server had `browser-1` open during the test. The value comes from `code://pkg/mcp/tools/browser.go#L177-L179`.

**The transport fails.**

The MCP server prefixes tool errors with `MCP error -32603: `. The only error path inside the tool is a JSON marshal failure.

```
browser_list: <marshal error>
```

This path was not observed. The result holds strings and slices only, so a marshal failure is not expected.

Recovery: retry the call. If the server is not running, the tool is not available. Start the server first. That is a connection error, not a tool error.

## When not to use it

Do not use `browser_list` to read the current URL or title. Use `page_fetch` with `what: "meta"`.

Do not use `browser_list` to read page content. Use `page_fetch`.

Do not use it for the pages of one known browser. Use `page_list` with the `browser_id`.

Do not use it to close anything. Use `browser_close` for a browser and `session_close` for a session.

Do not depend on the array order. Read the `browser_id` you need by value.

Do not expect a title, a timestamp, a viewport, or a byte count. The tool returns browser ids, profile paths, and page ids with creation URLs only.

## Related tools

- `browser_create` — creates a browser and returns the new `browser_id`.
- `browser_close` — closes one browser and its pages. Confirm the result with `browser_list`.
- `fetch_page` — opens a page in a chosen browser. The page then appears under `pages`.
- `page_list` — lists the pages of one browser. Use it when you already know the browser.
- `session_list` — lists live sessions. It can open the default browser, so `browser_list` is safer for a pure read.
- `session_close` — closes one session. Resolve the browser first with `browser_list`.

For a full call sequence, see the example at `.agents/skills/web-mcp/references/examples/browser_list_example.md`.
