> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_act example

Use this file only when tool calling for `page_act` fails. The tool description is
primary. Reference: `.agents/skills/web-mcp/references/tools/page_act.md`.

## 1. Create and confirm your browser

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"profile":"skill-page-act-example"}}
```
Expect `{"browser_id":"browser-10"}`. This client has no `persist` input. A bare call
used the shared default profile and failed with a SingletonLock error. Use only your
id. Never close an empty id.

## 2. Form input, type, verify, and submit

```json
{"tool":"fetch_page","arguments":{"url":"https://httpbin.org/forms/post","browser_id":"browser-10","wait_for":"load"}}
```
Expect `{"session_id":"page-1","title":"httpbin.org/forms/post","html_bytes":1404}`.

```json
{"tool":"page_act","arguments":{"session_id":"page-1","action":"input","selector":"input[name=\"custname\"]","value":"Ada Lovelace"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"input","selector":"input[name=\"custtel\"]","value":"555-0100"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"type","selector":"input[name=\"custtel\"]","value":" EXT"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"eval","value":"() => ({name: document.querySelector('input[name=\"custname\"]').value, tel: document.querySelector('input[name=\"custtel\"]').value})"}}
```
Expect `set input … value="Ada Lovelace"`, `set input … value="555-0100"`,
`typed … value=" EXT"`, and `{"name":"Ada Lovelace","tel":"555-0100 EXT"}`.
`input` replaces. `type` appends at the caret.

```json
{"tool":"page_act","arguments":{"session_id":"page-1","action":"press_key","selector":"input[name=\"custname\"]","key":"Enter"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"wait","wait_mode":"load","timeout_ms":1000}}
```
Expect `pressed key "Enter" in selector="input[name=\"custname\"]"`, then
`→ url=https://httpbin.org/post title="httpbin.org/post"`, then `waited for "load"`.

## 3. select and upload_file

The httpbin form has no `<select>` and no file input. Open the Selenium web-form
page and use its `session_id` (`page-2` in this run).

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"select","selector":"select[name=\"my-select\"]","value":"2"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"upload_file","selector":"input[type=\"file\"]","paths":["/tmp/opencode/example.txt"]}}
```
Expect `selected option value="2" in selector="select[name=\"my-select\"]"` and
`uploaded files [/tmp/opencode/example.txt] to selector="input[type=\"file\"]"`.
`select` matches the option `value`, not its text.

## 4. Clicks, hover, coordinates, scroll, and keys

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-10","wait_for":"load"}}
```
Expect `{"session_id":"page-3","title":"Web browser - Wikipedia"}`. Read
`interactives` and take `gcq7sm3ng-b48` for Main menu and `gcq7sm3ng-b64` for Search.

```json
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click","handle":"gcq7sm3ng-b48"}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click_at","x":34,"y":33}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"hover_at","x":512,"y":33}}
```
Expect `clicked handle="gcq7sm3ng-b48"`, `clicked at (34.0, 33.0)
button=left count=1`, and `hovered at (512.0, 33.0)`.

```json
{"tool":"page_act","arguments":{"session_id":"page-3","action":"scroll","x":0,"y":400}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"press_key","key":"Control+A"}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click","selector":"h1#firstHeading","click_count":2}}
```
Expect `scrolled to [\n  0,\n  400\n]`, `pressed key "Control+A"`, and
`clicked selector="h1#firstHeading" button=left count=2`.

## 5. Navigation, eval, logs, viewport, and emulation

```json
{"tool":"fetch_page","arguments":{"url":"https://react.dev/learn","browser_id":"browser-10","wait_for":"stable"}}
```
Expect `{"session_id":"page-5","title":"Quick Start – React"}`.

```json
{"tool":"page_act","arguments":{"session_id":"page-5","action":"navigate","value":"https://react.dev/learn/describing-the-ui"}}
```
Expect `navigated to "https://react.dev/learn/describing-the-ui" (title "Describing the UI – React")`.
`navigate` never appends a delta.

```json
{"tool":"page_act","arguments":{"session_id":"page-5","action":"back"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"forward"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"reload"}}
```
Expect `navigated back to "https://react.dev/learn"`, `navigated forward to
"https://react.dev/learn/describing-the-ui"`, and `reloaded page`.

```json
{"tool":"page_act","arguments":{"session_id":"page-5","action":"eval","value":"() => ({title: document.title, ready: document.readyState})"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"clear_logs"}}
```
Expect `{"ready":"complete","title":"Describing the UI – React"}` and
`{"network":132,"console":1}`. A DOM node returns `{}` from `eval`.

```json
{"tool":"page_act","arguments":{"session_id":"page-5","action":"set_viewport","viewport":{"width":1024,"height":768}}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"emulate","device":"IPadPro"}}
```
Expect `set viewport to 1024x768` and `emulated device: IPadPro`. The page then
reports `1024x1366` at DPR 2. `emulate` wins. `{"device":"clear"}` resets both.

## 6. Drag and drop

Open the W3Schools drag page as `page-4`, then drag:
```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"drag_and_drop","selector":"#img1","target_x":170,"target_y":533}}
```
Expect `dragged from (97.0, 532.2) to (170.0, 533.0)`. This is a mouse drag. It
moved a live range slider from 5 to 9.
Warning: native HTML5 drag-and-drop does not fire. On
`w3schools.com/html/html5_draganddrop.asp` the call succeeded but the drop listener
stayed `0`. Use `eval` to dispatch `dragstart` and `drop` on HTML5 pages.

## 7. Failure examples

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"click"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"press_key","key":"Banana"}}
```
Expect `page_act: click requires selector (or a handle from page_fetch
what=interactives)` and `page_act: press_key: unknown key name "Banana"`.

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"eval","value":"() => {"}}
```
Expect `page_act: evaluate js: eval js error: SyntaxError: Unexpected token ')' <nil>`.

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"input","handle":"gzlaqy9xe-b10","value":"stale"}}
```
Expect `page_act: handle "gzlaqy9xe-b10" is stale — element no longer in the
document (re-run page_fetch what=interactives to re-mint handles): {-32000 Node with
given id does not belong to the document }`.

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"emulate","device":"Nokia3310"}}
```
Expect `page_act: unknown device "Nokia3310" (see go-rod lib/devices presets, e.g.
IPhoneX, IPadPro, LaptopWithHiDPIScreen)`.
Do not send an unknown `select` value. A live `"value":"nope"` returned
`MCP error -32001: Request timed out` and wedged the whole server.

## 8. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-10"}}
```
Expect the browser to disappear from `browser_list`. Close only your own id. Never
close an empty id.
