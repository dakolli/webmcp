> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_act

Interact with a live page that `fetch_page` opened.

## What it does

`page_act` performs one action in an open session. The `session_id` picks the tab.
The `action` field picks the operation. Actions cover clicking, typing, keys, mouse
coordinates, drag, upload, JavaScript, navigation, waiting, and emulation.

Every call returns one short text line, plus a state delta when the URL or title
changed. `eval` returns the JavaScript result instead. The tool description and
input schema are authoritative. Use this file when the description is unclear or a
call failed. See
[page_act_example.md](../examples/page_act_example.md) for copyable sequences.

## Inputs

The schema is `code://pkg/mcp/tools/act.go#L58`. Only `session_id` and `action` are
required. Actions ignore fields they do not use.

| Name | Type | Required by | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | always | — | Tab from `fetch_page`, for example `page-3`. |
| `action` | string | always | — | One of the 20 actions below. |
| `selector` | string | `click`, `input`, `type`, `hover`, `select`, `upload_file` | — | CSS selector or XPath. Optional for `press_key` and the drag source. |
| `handle` | string | alternative to `selector` | — | Handle from `page_fetch` `what=interactives\|dom\|links`. Wins over `selector`. |
| `value` | string | `eval`, `navigate`, `input`, `type`, `select` | — | Payload text, JavaScript source, or URL. Single-path fallback for `upload_file`. |
| `key` | string | `press_key` | — | Key or combo, for example `Enter` or `Control+A`. `value` is a fallback. |
| `button` | string | no | `left` | `left`, `right`, or `middle` for `click` and `click_at`. |
| `click_count` | integer | no | `1` | Click count for `click` and `click_at`. `2` is a double click. |
| `x` | number | `click_at`, `hover_at` | `0` | Horizontal pixel coordinate. Also the drag source and `scroll` amount. |
| `y` | number | `click_at`, `hover_at` | `0` | Vertical pixel coordinate. Also the drag source and `scroll` amount. |
| `target_x` | number | no | `0` | Drag target pixel coordinate. |
| `target_y` | number | no | `0` | Drag target pixel coordinate. |
| `target_selector` | string | no | — | Drag target element. Its center overrides `target_x`/`target_y`. |
| `paths` | array | `upload_file` | — | Absolute local file paths. `value` is the single-path fallback. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. For `wait` `ms` mode it is the sleep time. |
| `wait_mode` | string | no | `ms` | `load`, `idle`, `stable`, `interactable`, or `ms`. |
| `viewport` | object | `set_viewport` | — | `{"width":<int>,"height":<int>}` in CSS pixels. Both must be positive. |
| `device` | string | `emulate` | — | go-rod preset name, for example `IPhoneX`. `clear` resets. |

`value` is required for `eval` and `navigate`. The server rejects an empty value
before it touches the page. For `upload_file`, pass `paths`. A single `value` path
also works.

## Actions

| `action` | Required arguments | Effect | Return text | State delta |
| :--- | :--- | :--- | :--- | :--- |
| `click` | `selector` or `handle` | Waits for the element to be interactable, then clicks. | `clicked selector="…"` | Yes, when navigation commits. |
| `input` | `selector` or `handle`, `value` | Selects all text, then types. The field ends exactly as `value`. | `set input selector="…" value="…"` | No. |
| `type` | `selector` or `handle`, `value` | Focuses, then inserts at the caret. It does not clear. | `typed selector="…" value="…"` | No. |
| `press_key` | `key` (or `value`) | Presses a key or combo. Focuses a target when given. | `pressed key "Enter" in selector="…"` | Yes, if the key navigates. |
| `click_at` | `x`, `y` | Moves the mouse to the point and clicks. | `clicked at (34.0, 33.0) button=left count=1` | Yes, if a click navigates. |
| `hover_at` | `x`, `y` | Moves the mouse to the point. | `hovered at (512.0, 33.0)` | No. |
| `drag_and_drop` | source and target | Mouse drag. See the combinations below. | `dragged from (97.0, 532.2) to (170.0, 533.0)` | No. |
| `upload_file` | `selector` or `handle`, plus `paths` | Sets files on an `<input type="file">`. | `uploaded files [/abs/path] to selector="…"` | Yes, when state changes. |
| `eval` | `value` | Runs a JavaScript function in the page. | Compact JSON of the return value. | Yes, when state changes. |
| `navigate` | `value` | Navigates and waits for load. | `navigated to "…" (title "…")` | Suppressed. |
| `back` | — | Goes back and waits for load. | `navigated back to "…"` | Yes. |
| `forward` | — | Goes forward and waits for load. | `navigated forward to "…"` | Yes. |
| `reload` | — | Reloads and waits for load. | `reloaded page` | Yes, when state changes. |
| `hover` | `selector` or `handle` | Moves the mouse over the element. | `hovered selector="…"` | No. |
| `scroll` | `x`, `y` (default `0`) | Calls `window.scrollBy(x, y)`. | `scrolled to [\n  0,\n  400\n]` | No. |
| `select` | `selector` or `handle`, `value` | Selects an `<option>` by exact `value` attribute. | `selected option value="2" in selector="…"` | Yes, when state changes. |
| `wait` | `wait_mode` | Waits for a readiness level or sleeps. | `waited 250ms`, `waited for "load"`, or `element "…" is interactable` | No. |
| `set_viewport` | `viewport` | Applies a manual width and height override. | `set viewport to 800x600` | No. |
| `emulate` | `device` | Applies a device preset, or clears it. | `emulated device: IPhoneX` | No. |
| `clear_logs` | — | Empties the network and console rings. | `{"network":132,"console":1}` | No. |

A non-positive `click_count` becomes `1`. An unknown `button` is rejected before
the click. The server rejects a missing `x` or `y` key for `click_at` and
`hover_at`. An explicit `0` is valid.

## Returns

The tool returns plain text. Tool errors carry `MCP error -32603: `. `eval` returns
compact JSON. A DOM node is not serializable and returns `{}`.

The state delta starts on a new line. It lists only changed fields:

```
→ url=https://httpbin.org/post title="httpbin.org/post"
```

`navigate` reports its destination and never appends a delta. `back` and `forward`
report the URL in the text and also append a delta. `reload` appends one only when
the state changed. `click`, `click_at`, `press_key`, `upload_file`, `eval`, and
`select` poll up to 900 ms for a navigation.

## Behavior and limits

### Handles

A handle has the form `g<document>-b<backendNodeId>`, for example
`gcq7sm3ng-b165`. Get handles from `page_fetch` with `what=interactives`, `dom`, or
`links`.

A handle wins over `selector`. A live `input` with a text-field handle and a
password-field selector wrote to the text field. The password field stayed empty.

The `g` token names the document that minted the handle. Any navigation, reload,
redirect, or document replacement changes that token. The old handle then fails as
stale. Re-read `interactives` after every navigation. A stale handle never acts on a
reused backend node id.

### Element actions

`input` replaces the field contents. It selects all text first, then types `value`.
`type` does not clear. It focuses the element, then inserts at the caret. A live
`input` of `555-0100` then `type` of `" EXT"` produced `555-0100 EXT`.

Some input types reject synthetic text. A live `input` on `<input type="time">`
left the value empty. Use `eval` to set `value` for those controls.

`click` and `wait` `interactable` check interactability. `input` and `type` do not.
A hidden element can still receive text.

### Keyboard combos

Join modifiers and the main key with `+`. The last token is the main key. Names are
case-insensitive.

| Modifiers | Accepted names |
| :--- | :--- |
| Control | `Control`, `Ctrl` |
| Alt | `Alt`, `Option` |
| Shift | `Shift` |
| Meta | `Meta`, `Cmd`, `Command`, `Super`, `Windows`, `Win` |

Main keys include `Enter`/`Return`, `Escape`/`Esc`, `Tab`, `Backspace`,
`Delete`/`Del`, `Space`, `Home`, `End`, `PageUp`, `PageDown`, `Insert`, the arrows
(`ArrowLeft`/`Left`, `ArrowRight`/`Right`, `ArrowUp`/`Up`, `ArrowDown`/`Down`),
`CapsLock`, `PrintScreen`, `ScrollLock`, `Pause`, `NumLock`, `F1` to `F12`, single
letters, digits, and common punctuation.

`press_key` focuses the element when you pass `selector` or `handle`. Without a
target it acts on the page. A live `Enter` in the httpbin name field submitted the
form and the delta showed `url=https://httpbin.org/post`.

### Drag and drop

The source and target each accept two forms. A source `selector` uses the element's
center. Otherwise the source uses `x` and `y`. A `target_selector` uses the target's
center. Otherwise the target uses `target_x` and `target_y`. A selector overrides
its coordinate pair.

The action sends mouse events: `Down`, `MoveLinear` over 10 steps, then `Up`. It
does not send CDP drag events.

Live result: a mouse-event range slider moved from value `5` to `9`. A native HTML5
drag on `w3schools.com/html/html5_draganddrop.asp` returned success but fired no
`dragstart` or `drop`. A listener on the drop zone counted `0` drops. Do not use
this action for native HTML5 drag-and-drop. Use `eval` to dispatch the drag events.

### Wait modes and timeouts

| `wait_mode` | Returns when | Notes |
| :--- | :--- | :--- |
| `ms` | The sleep ends. | Default. Sleeps `timeout_ms`. |
| `load` | `window.onload` fires. | Can stall on a slow subresource. |
| `idle` | No network request for 500 ms. | Can stall on polling or sockets. |
| `stable` | The DOM stops mutating for 500 ms. | Can stall on animations. |
| `interactable` | The target is visible and enabled. | Needs `selector` or `handle`. |

`timeout_ms` defaults to `30000` and bounds the whole call. The `ms` sleep is the
exception. If you omit `timeout_ms`, an `ms` wait sleeps 30 seconds. Always set
`timeout_ms` for `ms` mode. For `ms` mode the server adds a 5 second margin to the
call deadline.

### Emulation and viewport

`set_viewport` applies a manual override with DPR 1 and no mobile emulation. Both
values must be positive.

`emulate` applies a go-rod preset with a user agent, metrics, and touch. A later
`emulate` wins over an earlier `set_viewport`. Live result: `1024x768` then
`IPadPro` gave `1024x1366` at DPR 2.

`emulate` with `clear` resets the preset and drops a manual viewport override.
Live result: after `clear`, the page reported its native `780x499` viewport.

Apply emulation before you rely on layout. A metrics change can reflow the page.

### Logs

Network and console capture is cumulative from page creation. `clear_logs` empties
both rings, resets `dropped`, and returns the counts. The `seq` counter does not
reset. The recipe is `clear_logs`, then one action, then `page_fetch` `network` or
`console`.

Pages expire after 15 idle minutes. A dead Chromium relaunches under the same
browser id, and older session ids then fail. Keep one browser per task and confirm
the page with `page_list`.

## Failure modes and recovery

| Situation | Exact message | Recovery |
| :--- | :--- | :--- |
| `click` without a selector | `page_act: click requires selector (or a handle from page_fetch what=interactives)` | Add `selector` or a fresh `handle`. |
| `click_at` without `x`/`y` | `page_act: click_at requires x and y coordinates (e.g. {"action":"click_at","x":100,"y":200})` | Send both fields. |
| Bad key name | `page_act: press_key: unknown key name "Banana"` | Use a listed key. |
| `eval` without `value` | `page_act: eval requires value (JavaScript function source, e.g. "() => document.title")` | Pass an arrow function. |
| Bad JavaScript | `page_act: evaluate js: eval js error: SyntaxError: Unexpected token ')' <nil>` | Fix the syntax. |
| Stale handle after reload | `page_act: handle "gzlaqy9xe-b10" is stale — element no longer in the document (re-run page_fetch what=interactives to re-mint handles): {-32000 Node with given id does not belong to the document }` | Re-read `interactives`. |
| Bad handle format | `page_act: invalid handle "x" (expected g<document>-b<backendNodeId> minted by page_fetch what=interactives\|dom\|links)` | Copy a handle exactly. |
| Unknown device | `page_act: unknown device "Nokia3310" (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)` | Use a real preset or `clear`. |
| `emulate` without `device` | `page_act: emulate requires a device` | Pass a preset name. |
| `set_viewport` without `viewport` | `page_act: set_viewport requires a viewport` | Pass `width` and `height`. |
| Non-positive viewport | `page_act: viewport width and height must be positive` | Send positive integers. |
| Unknown session | `page_act: session "page-999" not found` | Use `page_list` or `fetch_page`. |
| Unknown action | `page_act: unknown action="konami" (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs)` | Use a listed action. |
| Unknown button | `page_act: click: unknown mouse button "back" (must be left, right, or middle)` | Use `left`, `right`, or `middle`. |
| `upload_file` without paths | `page_act: upload_file: no file paths provided` | Pass `paths` or `value`. |
| `interactable` without a target | `page_act: wait: interactable mode requires a selector or handle` | Add a target. |
| Unknown wait mode | `page_act: wait: unknown wait_mode "forever" (must be one of load, idle, stable, interactable, ms)` | Use a listed mode. |
| Missing action | `page_act: missing required argument "action"` | Send `action`. |
| `select` with an unknown value | `MCP error -32001: Request timed out` | Use an exact option `value`. This call wedged the whole server in the live test. Terminating the affected browser released it. This is a defect. |
| Closed page | `page_act: page session is closed` | Open a new session. |

## When not to use

Do not use `page_act` to open a URL in a new tab. Use `fetch_page`.

Do not use it to read content. Use `page_fetch`.

Do not use `select` with an unverified value. Read the option list with `eval` first.

Do not use `drag_and_drop` for native HTML5 drag-and-drop. It sends mouse events
only, and the browser fires no `dragstart` or `drop`.

Do not use `eval` to return a DOM node. It returns `{}`. Return a property, an
array, or a plain object.

Do not omit `timeout_ms` on an `ms` wait. The default sleep is 30 seconds.

## Related tools

- `fetch_page` — opens the session and returns `session_id`.
- `page_fetch` — reads `interactives`, `dom`, `links`, `network`, `console`, and more.
- `page_list` — lists pages in one browser. Confirm your `session_id` here.
- `session_list` — lists every live session across browsers.
- `session_close` — closes one page.
- `browser_create`, `browser_list`, `browser_close` — manage the owning browser.

Copyable calls with expected output are in
[page_act_example.md](../examples/page_act_example.md).
