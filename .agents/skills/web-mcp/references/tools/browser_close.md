> Tool: `browser_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_close

Close a live browser. Also close every page and session in that browser. Free the Chromium process.

## What it does

`browser_close` tears down one browser and all of its open pages. It releases the Chromium process behind that browser.

The tool takes one optional argument. Pass `browser_id` to close one browser. Omit `browser_id` to close every browser.

This is not a session close. Use `session_close` to close one tab. Use `browser_close` to close the whole browser.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `browser_id` | string | no | empty | The id from `browser_create`. Omit or pass `""` to close all browsers. |

The schema declares no required fields. The source is `code://pkg/mcp/tools/browser.go#L225`.

## Returns

The tool returns a plain text string. It does not return JSON.

| Condition | Return value |
| :--- | :--- |
| `browser_id` given and closed | `browser <browser_id> closed` |
| `browser_id` omitted | `all browsers closed` |

Test run: `{"browser_id":"browser-1"}` returned `browser browser-1 closed`.

The empty-id call returns `all browsers closed`. This path was not run live. The value comes from the source.

## Behavior and limits

> **WARNING: An empty `browser_id` closes every browser on the server.** It also closes the browsers of other agents and clients. Never omit `browser_id` in a shared session unless you want a full reset.

A close also closes every page in that browser. All sessions in that browser become invalid. A later call with an old `session_id` fails with a not-found error.

The tool removes the browser from the manager map before it closes the process. A failed process close still removes the id.

The empty-id path stops the idle TTL sweeper. A new `browser_create` starts the sweeper again.

The tool does not wait for page work to finish. It closes the pages that exist at the time of the call.

The call is not idempotent. A second close of the same id returns a not-found error. See the next section.

## Failure modes and recovery

The MCP server prefixes tool errors with `MCP error -32603: `. The strings below show the message after that prefix.

**Unknown or already-closed id.**

```
browser_close: browser "browser-999" not found (running: browser-1): unknown browser
```

Test run: closing `browser-999` returned this error and listed `browser-1`.

```
browser_close: browser "browser-1" not found (running: browser-2, browser-3, browser-4, browser-5): unknown browser
```

Test run: this was a second close of the same id. The first close succeeded. The second close failed with this error.

Recovery: call `browser_list` to read the live ids. Use a current id. Or create a new browser with `browser_create`.

**Bad argument shape.**

```
browser_close: invalid arguments: json: cannot unmarshal ...
```

Recovery: send `{"browser_id": "<id>"}` or `{}`. Do not send a JSON array or a bare string.

**All-browsers close with a failed process.** The tool returns the first close error. The other browsers stay closed.

## When not to use it

Do not use `browser_close` to close one tab. Use `session_close` with a `session_id`.

Do not close a browser you did not create. Other agents may share the server.

Do not omit `browser_id` in a shared session. That closes every browser.

## Related tools

- `browser_create` — creates a browser and returns its `browser_id`.
- `browser_list` — lists live browsers and their pages. Call it to confirm a close and to read current ids.
- `session_close` — closes one session in one browser.
- `page_list` — lists the pages of one browser before you close it.
