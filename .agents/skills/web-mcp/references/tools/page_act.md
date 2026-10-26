> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_act

Interact with a live page that `fetch_page` opened.

## What it does

`page_act` performs one action in an open session. The `session_id` picks the tab, the
`action` field picks the operation. Actions cover clicking, typing, keys, mouse
coordinates, drag, upload, JavaScript, navigation, waiting, emulation, log clearing, and
replaying a captured network request.

The confirmation rides the result envelope: the full reply is
`{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the
`{"ok":false,"error":…}` shape. The payload is one short prose string, plus a state delta
line when the URL or title changed. `eval` and `replay` return compact JSON instead.

## Inputs

The schema is `code://pkg/mcp/tools/act.go#L63`. Only `session_id` and `action` are
required. Actions ignore fields they do not use.

| Name | Type | Required by | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | always | — | Tab from `fetch_page`, for example `page-3`. |
| `action` | string | always | — | One of the 21 actions below. |
| `selector` | string | `click`, `input`, `type`, `hover`, `select`, `upload_file` | — | CSS selector or XPath. Optional for `press_key` and the drag source. |
| `handle` | string | alternative to `selector` | — | Handle from `page_fetch` `what=interactives\|dom\|links`. Wins over `selector`. |
| `value` | string | `eval`, `navigate`, `input`, `type`, `select`, `replay` | — | Payload text, JavaScript source, URL, or option value. For `replay` it replaces the request body and is sent only when the field is present. |
| `target` | string | `replay` | — | Captured network entry: a `seq` number or a substring of the request URL. |
| `method` | string | `replay` (optional) | — | HTTP method override, for example `PUT`. Omit to keep the captured method. |
| `headers` | object | `replay` (optional) | — | Per-header overrides. Each named header replaces the captured value, others keep theirs. |
| `key` | string | `press_key` | — | Key or combo, for example `Enter` or `Control+A`. `value` is a fallback. |
| `button` | string | no | `left` | `left`, `right`, or `middle` for `click` and `click_at`. |
| `click_count` | integer | no | `1` | Click count for `click` and `click_at`. `2` is a double click. |
| `x` | number | `click_at`, `hover_at` | `0` | Horizontal pixel coordinate. Also the drag source and `scroll` amount. |
| `y` | number | `click_at`, `hover_at` | `0` | Vertical pixel coordinate. Also the drag source and `scroll` amount. |
| `target_x`, `target_y` | number | no | `0` | Drag target pixel coordinates. |
| `target_selector` | string | no | — | Drag target element. Its center overrides `target_x`/`target_y`. |
| `paths` | array | `upload_file` | — | Absolute local file paths. `value` is the single-path fallback. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. For `wait` `ms` mode it is the sleep time. |
| `wait_mode` | string | no | `ms` | `load`, `idle`, `stable`, `interactable`, or `ms`. |
| `viewport` | object | `set_viewport` | — | `{"width":<int>,"height":<int>}` in CSS pixels. Both must be positive. |
| `device` | string | `emulate` | — | go-rod preset name, for example `IPhoneX`. `clear` resets. |

`value` is required for `eval` and `navigate`, and the server rejects an empty value before
it touches the page. For `upload_file`, pass `paths` (a single `value` path also works).

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
| `replay` | `target` | Resends one captured network entry. See below. | Compact JSON result. | No. |

A non-positive `click_count` becomes `1`. An unknown `button` is rejected before the
click. The server rejects a missing `x` or `y` key for `click_at` and `hover_at`, and an
explicit `0` is valid.

## Returns

The confirmation and the state delta arrive as one JSON string inside `data`. Tested live
on 2026-10-26: a form submit returned

```json
{"ok":true,"data":"clicked selector=\"form button\"\n→ url=https://httpbin.org/post title=\"httpbin.org/post\""}
```

The delta starts on a new line inside the same string and lists only changed fields.
`navigate` never appends one. `back` and `forward` do. `eval`, `clear_logs`, and `replay`
return compact JSON in `data` instead of prose. A DOM node from `eval` comes back as `{}`.
`click`, `click_at`, `press_key`, `upload_file`, `eval`, and `select` poll up to 900 ms for
a navigation.

## Behavior and limits

### Handles

A handle has the form `g<document>-b<backendNodeId>`, for example `gcq7sm3ng-b165`. Get
handles from `page_fetch` with `what=interactives`, `dom`, or `links`.

A handle wins over `selector`. A live `input` with a text-field handle and a password-field
selector wrote to the text field, and the password field stayed empty. The `g` token names
the document that minted the handle. Any navigation, reload, redirect, or document
replacement changes that token, and the old handle then fails as stale. A stale handle
never acts on a reused backend node id. Re-read `interactives` after every navigation.

### Element actions

`input` replaces the field contents: it selects all text first, then types `value`. `type`
does not clear. It focuses the element, then inserts at the caret. A live `input` of
`555-0100` then a `type` of `" EXT"` produced `555-0100 EXT`. Some controls reject
synthetic text — a live `input` on `<input type="time">` left the value empty, so use
`eval` to set those values. `click` and `wait` `interactable` check interactability, and
`input` and `type` do not. A hidden element can still receive text.

### Keyboard combos

Join modifiers and the main key with `+`, so `Control+A`. Modifiers are
`Control`/`Ctrl`, `Alt`/`Option`, `Shift`, and `Meta`/`Cmd`/`Command`/`Super`/`Windows`/`Win`.
Main keys include `Enter`/`Return`, `Escape`/`Esc`, `Tab`, `Backspace`, `Delete`/`Del`,
`Space`, `Home`, `End`, `PageUp`, `PageDown`, `Insert`, the arrows, `CapsLock`,
`PrintScreen`, `ScrollLock`, `Pause`, `NumLock`, `F1` to `F12`, single letters, digits,
and common punctuation. Names are case-insensitive, the last token is the main key, and
`press_key` focuses the element when you pass `selector` or `handle`. A live `Enter` in the
httpbin name field submitted the form.

### Drag and drop

The source takes a `selector` (element center) or `x`/`y`, the target takes `target_selector` or `target_x`/`target_y`, and a selector overrides its coordinate pair. The action sends mouse events — `Down`, `MoveLinear` over 10 steps, then `Up` — and no CDP drag events. Live result: a mouse-event range slider moved from value `5` to `9`, but a native HTML5 drag on `w3schools.com/html/html5_draganddrop.asp` succeeded with `0` drops. Do not use this action for native HTML5 drag-and-drop. Use `eval` to dispatch the events.

### Wait modes and timeouts

| `wait_mode` | Returns when | Notes |
| :--- | :--- | :--- |
| `ms` | The sleep ends. | Default. Sleeps `timeout_ms`. |
| `load` | `window.onload` fires. | Can stall on a slow subresource. |
| `idle` | No network request for 500 ms. | Can stall on polling or sockets. |
| `stable` | The DOM stops mutating for 500 ms. | Can stall on animations. |
| `interactable` | The target is visible and enabled. | Needs `selector` or `handle`. |

`timeout_ms` defaults to `30000` and bounds the whole call. For `ms` mode the server adds a
5 second margin to the call deadline. If you omit `timeout_ms`, an `ms` wait sleeps 30
seconds, so always set `timeout_ms` for `ms` mode.

### Emulation and viewport

`set_viewport` applies a manual override with DPR 1, and both values must be positive.
`emulate` applies a go-rod preset with user agent, metrics, and touch, and a later
`emulate` wins over an earlier `set_viewport`. Live result: `1024x768` then `IPadPro` gave
`1024x1366` at DPR 2. `emulate` with `clear` resets the preset and drops a manual override.
Apply emulation before you rely on layout, because a metrics change can reflow the page.

### Logs

Network and console capture is cumulative from page creation. `clear_logs` empties both
rings, resets `dropped`, and returns the cleared counts. The `seq` counter does not reset.
The recipe is `clear_logs`, then one action, then `page_fetch` `network` or `console`.

### Replay with what=network as the source

`replay` resends one captured network entry through a real Go HTTP client, outside the browser (`code://pkg/browsers/replay.go#L112-L235`). It bypasses CORS, carries the page's cookies for the target host in a private jar without writing anything back, and never touches the live page. Recipe: read `page_fetch` `what=network`, pick one entry, then replay it.

- `target` picks the entry: a `seq` number or a unique substring of the request URL. An ambiguous substring is refused with every candidate listed as `seq=N METHOD url`.
- `method` overrides the captured method when given. `headers` replaces captured headers per name, others keep theirs. Framing headers (`Host`, `Cookie`, `Connection`, `Accept-Encoding`, `Content-Length`, and kin) are never copied because the client owns them and the cookie jar owns `Cookie`.
- `value` replaces the request body, but it is sent only when the `value` field itself is present. Without it the captured body is fetched from the browser, and a body the browser no longer retains reports `body_uncaptured: true` instead of being sent empty. `GET` and `HEAD` never send a body.
- Each URL, redirects included, is checked against the browser's scope allowlist before any request runs (`code://pkg/mcp/tools/act.go#L352-L354`, `code://pkg/mcp/tools/act.go#L283`).
- Up to 5 redirects are followed and reported in `chain`. A 301, 302, or 303 switches the next hop to `GET` and drops the body.

The result is compact JSON inside `data` (`code://pkg/browsers/replay.go#L86-L110`), tested live on 2026-10-26 (body shortened):

```json
{"ok":true,"data":{"status":200,"headers":{"Content-Type":["text/css; charset=utf-8"],"Server":["nginx"]},"body":"body  { font-family:Verdana, Geneva, sans-serif; … }","bytes":7418,"ms":63,"request":{"method":"GET","url":"https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb","headers":{"Accept-Language":"en","Referer":"https://news.ycombinator.com/","User-Agent":"Mozilla/5.0 …"}}}}
```

Fields: `status`, `headers` (final response headers as string arrays), `body` for UTF-8 text or `base64` for binary, `bytes`, `truncated`, `body_uncaptured`, `ms`, `request` (first-hop method, url, and echoed headers), and `chain` (each hop's `status` and `url`, present when the replay followed a redirect). The response body is capped at 256 KB on a rune boundary — `truncated: true` marks the cut. Sensitive request and response header values (`Cookie`, `Set-Cookie`, `Authorization`, and kin) show `<redacted>` unless the page opted into secrets capture, and no surface exposes that switch. The whole replay is bounded by the call's `timeout_ms` deadline (engine cap 30 seconds).

### Session lifetime

Pages expire after the idle TTL. A dead Chromium relaunches under the same browser id,
and older session ids then fail. Keep one browser per task and confirm the page with
`page_list`.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The prefix `page_act: ` opens the message, and the table shows the message content. This tool can produce every type (`code://pkg/mcp/tools/envelope.go#L194-L227`). One full failure envelope, tested live on 2026-10-26 against an ambiguous replay target:

```json
{"ok":false,"error":{"type":"unknown","message":"page_act: replay: target \"news.ycombinator.com/news\" matches 2 network entries — pass a seq or a longer URL substring: seq=1 GET https://news.ycombinator.com/news; seq=2 GET https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb"}}
```

| Situation | Message at `error.message` | Recovery |
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
| Unknown action | `page_act: unknown action="konami" (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs, replay)` | Use a listed action. |
| Unknown button | `page_act: click: unknown mouse button "back" (must be left, right, or middle)` | Use `left`, `right`, or `middle`. |
| `upload_file` without paths | `page_act: upload_file: no file paths provided` | Pass `paths` or `value`. |
| `interactable` without a target | `page_act: wait: interactable mode requires a selector or handle` | Add a target. |
| Unknown wait mode | `page_act: wait: unknown wait_mode "forever" (must be one of load, idle, stable, interactable, ms)` | Use a listed mode. |
| Missing action | `page_act: missing required argument "action"` | Send `action`. |
| `replay` without `target` | `page_act: replay requires target=<seq> or target=<url substring> from page_fetch what=network` (`invalid_args`) | Read `what=network` and set `target`. |
| `replay` with an unknown seq | `page_act: replay: no network entry with seq %d (read page_fetch what=network for the current seq values)` | Re-read `network` for live `seq` values. |
| `replay` with an unmatched substring | `page_act: replay: no network entry matches target %q (target is a seq number or a substring of the request URL; read page_fetch what=network first)` | Pass a seq or a different substring. |
| `replay` of a non-http entry | `page_act: replay: captured URL %s uses scheme %q — only http and https requests can be replayed` | Replay only http and https entries. |
| `replay` network failure | `page_act: replay: %s %s: <error> (the host is unreachable or rejected the request — verify the URL, DNS and TLS trust)` | Verify the URL, DNS, and TLS trust. |
| Replay after the page closed | `page_act: replay: page session is closed — reopen the page and retry` | Open a new session. |
| Replay outside the scope | `page_act: replay: <url> refused: <scope reason>` | Pick an entry inside the browser's scope allowlist. |
| Closed page | `page_act: page session is closed` | Open a new session. |
| `select` with an unknown value | `MCP error -32001: Request timed out` at the transport, not the envelope | Use an exact option `value`. A live test wedged the whole server until its browser was terminated. Treat as a defect. |

## When not to use it

Do not use `page_act` to open a URL in a new tab. Use `fetch_page`. Do not use it to read
content, use `page_fetch`. Do not use `select` with an unverified value — read the option
list with `eval` first. Do not use `drag_and_drop` for native HTML5 drag-and-drop. Do not
use `eval` to return a DOM node, it returns `{}`. Do not omit `timeout_ms` on an `ms`
wait. Do not use `replay` instead of `navigate`: a replay sends a fresh HTTP request
outside the browser and cannot set cookies, storage, or page state.

## Tested calls

- 2026-10-25 — form input, type, press_key with delta, select, upload, clicks (element
  and coordinates), hover, scroll, keyboard combo, drag with a live slider, navigate,
  back/forward/reload, eval, clear_logs, viewport, emulate, and the miss-path errors.
- 2026-10-26 — replay failure envelope for an ambiguous target on
  `https://news.ycombinator.com/news`, and submit-click click + state delta in `data`.

## Related tools

- `fetch_page` — opens the session and returns `session_id`.
- `page_fetch` — reads `interactives`, `dom`, `links`, `network`, `console`, and more.
- `page_list` — lists pages in one browser. Confirm your `session_id` here.
- `session_list` — lists every live session across browsers.
- `session_close` — closes one page.
- `browser_create`, `browser_list`, `browser_close` — manage the owning browser.

Copyable calls with expected output are in
[page_act_example.md](../examples/page_act_example.md).
