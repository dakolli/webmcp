> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_act example

Use this file only when tool calling for `page_act` fails. The tool description is
primary. Every result rides the envelope: `{"ok":true,"data":<payload>}` on success,
`{"ok":false,"error":{"type":…,"message":…,"hint":…}}` on failure. One success and one
failure appear below in full envelope form. Reference:
`.agents/skills/web-mcp/references/tools/page_act.md`.

## 1. Create and confirm your browser

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"profile":"skill-page-act-example"}}
```

Expect `{"browser_id":"browser-10"}` in `data`. A bare create call used the shared default
profile and failed with a SingletonLock error. Use only your id. Never close an empty id.

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
{"tool":"page_act","arguments":{"session_id":"page-1","action":"press_key","selector":"input[name=\"custname\"]","key":"Enter"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"wait","wait_mode":"load","timeout_ms":1000}}
```

Expect `set input … value="Ada Lovelace"`, `set input … value="555-0100"`,
`typed … value=" EXT"`, `{"name":"Ada Lovelace","tel":"555-0100 EXT"}`, then the delta
carries the redirect inside one `data` string, then `waited for "load"`. `input`
replaces, `type` appends at the caret.

## 3. select and upload_file

The httpbin form has no `<select>` and no file input. Open the Selenium web-form page and
use its `session_id` (`page-2` in this run):

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"select","selector":"select[name=\"my-select\"]","value":"2"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"upload_file","selector":"input[type=\"file\"]","paths":["/tmp/opencode/example.txt"]}}
```

Expect `selected option value="2" in selector="select[name=\"my-select\"]"` and
`uploaded files [/tmp/opencode/example.txt] to selector="input[type=\"file\"]"`. `select`
matches the option `value`, not its text.

## 4. Clicks, hover, coordinates, scroll, and keys

```json
{"tool":"fetch_page","arguments":{"url":"https://en.wikipedia.org/wiki/Web_browser","browser_id":"browser-10","wait_for":"load"}}
{"tool":"page_fetch","arguments":{"session_id":"page-3","what":"interactives"}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click","handle":"gcq7sm3ng-b48"}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click_at","x":34,"y":33}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"hover_at","x":512,"y":33}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"scroll","x":0,"y":400}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"press_key","key":"Control+A"}}
{"tool":"page_act","arguments":{"session_id":"page-3","action":"click","selector":"h1#firstHeading","click_count":2}}
```

Expect `clicked handle="gcq7sm3ng-b48"`, `clicked at (34.0, 33.0) button=left count=1`,
`hovered at (512.0, 33.0)`, `scrolled to [\n  0,\n  400\n]`, `pressed key "Control+A"`,
and `clicked selector="h1#firstHeading" button=left count=2`.

## 5. Navigation, eval, logs, viewport, and emulation

```json
{"tool":"fetch_page","arguments":{"url":"https://react.dev/learn","browser_id":"browser-10","wait_for":"stable"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"navigate","value":"https://react.dev/learn/describing-the-ui"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"back"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"forward"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"reload"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"eval","value":"() => ({title: document.title, ready: document.readyState})"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"clear_logs"}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"set_viewport","viewport":{"width":1024,"height":768}}}
{"tool":"page_act","arguments":{"session_id":"page-5","action":"emulate","device":"IPadPro"}}
```

Expect `navigated to "https://react.dev/learn/describing-the-ui" (title "Describing the UI – React")`
(`navigate` never appends a delta), `navigated back to "https://react.dev/learn"`,
`navigated forward to "https://react.dev/learn/describing-the-ui"`, `reloaded page`,
`{"ready":"complete","title":"Describing the UI – React"}` (a DOM node returns `{}`),
`{"network":132,"console":1}`, `set viewport to 1024x768`, and `emulated device: IPadPro`.
The page then reports `1024x1366` at DPR 2. `emulate` wins. `{"device":"clear"}` resets both.

## 6. Drag and drop

Open the W3Schools drag page as `page-4`, then drag the range slider:

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"drag_and_drop","selector":"#img1","target_x":170,"target_y":533}}
```

Expect `dragged from (97.0, 532.2) to (170.0, 533.0)`. This is a mouse drag and it moved a
live range slider from `5` to `9`. Native HTML5 drag-and-drop does not fire: on
`w3schools.com/html/html5_draganddrop.asp` the drop listener stayed `0`. Use `eval` to
dispatch `dragstart` and `drop` on HTML5 pages.

## 7. Replay a captured request

Open `https://news.ycombinator.com/news` as `page-6` and read its captured ring:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"network"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay","target":"2"}}
```

The second `target` is the seq of this run's `news.css` request. Success rides `data`:

```json
{"ok":true,"data":{"status":200,"headers":{"Content-Type":["text/css; charset=utf-8"],"Server":["nginx"],"Vary":["Accept-Encoding"]},"body":"body  { font-family:Verdana, Geneva, sans-serif; … }","bytes":7418,"ms":63,"request":{"method":"GET","url":"https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb","headers":{"Accept-Language":"en","Referer":"https://news.ycombinator.com/","User-Agent":"Mozilla/5.0 …"}}}}
```

Replay re-reads the request and carries the page's cookies for the target host. It never
writes to the page. Pass `method` to override the method, `headers` to replace captured
headers, or `value` to replace the request body.

## 8. Failure examples

An ambiguous replay URL substring anchors the failure path (tested live on 2026-10-26):

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay","target":"news.ycombinator.com/news"}}
```

```json
{"ok":false,"error":{"type":"unknown","message":"page_act: replay: target \"news.ycombinator.com/news\" matches 2 network entries — pass a seq or a longer URL substring: seq=1 GET https://news.ycombinator.com/news; seq=2 GET https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb"}}
```

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"click"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"press_key","key":"Banana"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"eval","value":"() => {"}}
```

Expect `page_act: click requires selector (or a handle from page_fetch
what=interactives)`, `page_act: press_key: unknown key name "Banana"`, and
`page_act: evaluate js: eval js error: SyntaxError: Unexpected token ')' <nil>`. Stale
handles return the `page_act: handle "…" is stale — …` message. Re-read `interactives`.

Do not send an unknown `select` value. A live `"value":"nope"` returned
`MCP error -32001: Request timed out` and wedged the whole server.

## 9. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-10"}}
```

Expect the browser to disappear from `browser_list`. Close only your own id. Never close
an empty id.
