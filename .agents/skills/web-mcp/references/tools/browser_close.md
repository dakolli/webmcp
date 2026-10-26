> Tool: `browser_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_close

Close one live browser, or every live browser. A close closes all pages in that browser and frees its Chromium process.

Use this reference only when the tool description and input schema are not enough. The tool description is primary.

## What it does

`browser_close` tears down a browser. It closes every page in that browser. It stops the Chromium process behind it.

Pass `browser_id` to close one browser. Omit `browser_id` to close every browser on the server.

This tool does not close one tab. Use `session_close` for one tab.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `browser_id` | string | no | empty | The id from `browser_create`. Omit or pass `""` to close all browsers. |

The schema has no required fields. Source: `code://pkg/mcp/tools/browser.go#L267`.

> **WARNING: An empty `browser_id` closes every browser on the server.**
>
> That includes browsers owned by other agents and other clients. It also closes every page in them.
>
> Never send `{}`, `{"browser_id":""}`, or a misspelled key in a shared session. A misspelled key becomes an empty id.

## Returns

The tool returns plain text. It does not return JSON.

| Condition | Return value |
| :--- | :--- |
| `browser_id` given and closed | `browser <browser_id> closed` |
| `browser_id` omitted and every close succeeds | `all browsers closed` |
| `browser_id` omitted and a close fails | The first close error, as an MCP error |

Test run: `{"browser_id":"browser-5"}` returned `browser browser-5 closed`.

The empty-id path was not run live. It would disrupt other agents. The value comes from the source.

## Behavior and limits

A close closes every page in that browser. Every `session_id` from that browser becomes invalid. A later `page_fetch` or `page_act` call with an old id returns an error such as `session "page-1" not found`.

The tool removes the browser from the manager map before it stops the process. A failed process stop still removes the id. The tool then returns the stop error.

The process teardown has one shared budget of about 10 seconds. The budget does not use the caller context. A canceled request does not abort a teardown that already started.

When the last browser closes, the manager stops the idle TTL sweeper. A new `browser_create` starts the sweeper again.

An ephemeral profile is deleted on close. A persistent profile stays. `browser-1` with no profile is persistent. A secondary browser is ephemeral unless you set `persist:true`.

A typo is dangerous. The parser ignores unknown keys. `{"browser": "browser-5"}` becomes an empty id and closes every browser. Send the exact key `browser_id`.

Do not race a close against page work. The tool closes the pages that exist at call time. It does not wait for an in-flight action to finish.

The call is not idempotent. A second close of the same id fails with a not-found error.

## Failure modes and recovery

The MCP transport adds the prefix `MCP error -32603: `. The strings below include that prefix.

**Unknown id.** Tested with `browser-999`:

```
MCP error -32603: browser_close: browser "browser-999" not found (running: browser-1, browser-4): unknown browser
```

**Already closed id.** Tested with a second close of `browser-5`:

```
MCP error -32603: browser_close: browser "browser-5" not found (running: browser-1, browser-4): unknown browser
```

The first close succeeded. The second close failed. The list after `running:` shows the live ids at that moment.

Recovery: call `browser_list`. Use a current id. Or call `browser_create` for a new browser. Do not retry the closed id.

**Unknown id when nothing runs.**

```
MCP error -32603: browser_close: browser "browser-999" not found (running: none): unknown browser
```

Recovery: call `browser_create` first.

**Bad argument type.**

```
MCP error -32603: browser_close: invalid arguments: json: cannot unmarshal number into Go struct field browserCloseArgs.browser_id of type string
```

Recovery: send a JSON object with a string value. Do not send an array, a bare string, or a number.

**Failed process stop.** The tool removes the id first. The id does not appear in `browser_list` after the error. Do not retry the same id. Call `browser_create` for a new browser.

**Empty-id close with one failed process.** The tool returns the first error. The other browsers stay closed. The return text is not `all browsers closed`.

## Tested calls

Create an isolated browser. Do not pass a profile name.

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Result: `{"browser_id":"browser-5"}`. The id can differ. Record your id.

Open one page in that browser.

```json
{"tool":"fetch_page","arguments":{"url":"https://example.com","wait_for":"load","browser_id":"browser-5"}}
```

Result: `{"session_id":"page-1","title":"Example Domain","url":"https://example.com/"}`.

Read the live ids before the close.

```json
{"tool":"browser_list","arguments":{}}
```

Result: an array. It contained `browser-5` with an ephemeral `profile_dir` and one page.

Close your browser with its exact id.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-5"}}
```

Result: `browser browser-5 closed`.

Confirm the close.

```json
{"tool":"browser_list","arguments":{}}
```

Result: an array without `browser-5`. It still contained `browser-1` and `browser-4`, which belong to other agents.

Do not expect `[]` on a shared server. Expect only your id to disappear.

## Cleanup order

Close sessions first when you can. Use `session_close` for each `session_id` you own. Then close the browser. This order gives each page a clean close.

`browser_close` also works alone. It closes all pages in the browser. If one tab is wedged, it consumes the shared teardown budget.

## When not to use it

Do not use `browser_close` to close one tab. Use `session_close` with a `session_id`.

Do not close a browser you did not create. Other agents may share the server.

Do not omit `browser_id` in a shared session. That closes every browser.

Do not use a close as a health check. Use `browser_list`.

## Related tools

- `browser_create` — creates a browser and returns its `browser_id`.
- `browser_list` — lists live browsers and their pages. Use it to confirm a close and to read current ids.
- `session_close` — closes one session in one browser.
- `page_list` — lists the pages of one browser before you close it.

## Maintainer anchors

- Tool description: `code://pkg/mcp/tools/browser.go#L258`
- Input schema: `code://pkg/mcp/tools/browser.go#L267`
- Execute path: `code://pkg/mcp/tools/browser.go#L285`
- Manager close: `code://pkg/browsers/manager.go#L635`
- Browser teardown: `code://pkg/browsers/browser.go#L758`
