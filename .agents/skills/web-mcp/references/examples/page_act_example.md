> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-27

# page_act example

Use this file only when tool calling for `page_act` fails. The tool description is primary, and every result rides the envelope with the payload at `data`. One success and one failure appear in full envelope form. Reference: `.agents/skills/web-mcp/references/tools/page_act.md`.

## 1. Create and confirm your browser

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"profile":"skill-page-act-example"}}
```

Expect `{"browser_id":"browser-10"}` in `data`. A bare create call used the shared default profile and failed with a SingletonLock error. Use only your id and never close an empty id.

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

Expect `set input selector="input[name=\"custname\"]" length=12`, `set input selector="input[name=\"custtel\"]" length=8`, `typed selector="input[name=\"custtel\"]" length=4`, then the eval result `{"name":"Ada Lovelace","tel":"555-0100 EXT"}` proving the field state, then the redirect delta inside one `data` string, then `waited for "load"`. Neither confirmation echoes the value. `input` replaces, `type` appends at the caret.

## 3. select and upload_file

The httpbin form has no `<select>` and no file input, so open the Selenium web-form page as `page-2`:

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"select","selector":"select[name=\"my-select\"]","value":"2"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"upload_file","selector":"input[type=\"file\"]","paths":["/tmp/opencode/example.txt"]}}
```

Expect `selected option value="2" in selector="select[name=\"my-select\"]"` and `uploaded files [/tmp/opencode/example.txt] to selector="input[type=\"file\"]"`. `select` matches the option `value`, not its text.

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

Expect `clicked handle="gcq7sm3ng-b48"`, `clicked at (34.0, 33.0) button=left count=1`, `hovered at (512.0, 33.0)`, `scrolled to [\n  0,\n  400\n]`, `pressed key "Control+A"`, and `clicked selector="h1#firstHeading" button=left count=2`.

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

Expect `navigated to "https://react.dev/learn/describing-the-ui" (title "Describing the UI – React")` with no delta, then `navigated back to "https://react.dev/learn"`, `navigated forward to "https://react.dev/learn/describing-the-ui"`, `reloaded page`, `{"ready":"complete","title":"Describing the UI – React"}` (a DOM node returns `{}`), `{"network":132,"console":1,"websocket":0}`, `set viewport to 1024x768`, and `emulated device: IPadPro`. The page then reports `1024x1366` at DPR 2. `emulate` wins, and `{"device":"clear"}` resets both.

## 6. Drag and drop

Open the W3Schools drag page as `page-4`, then drag the range slider:

```json
{"tool":"page_act","arguments":{"session_id":"page-4","action":"drag_and_drop","selector":"#img1","target_x":170,"target_y":533}}
```

Expect `dragged from (97.0, 532.2) to (170.0, 533.0)`. This is a mouse drag and it moved a live range slider from `5` to `9`. Native HTML5 drag-and-drop does not fire: on `w3schools.com/html/html5_draganddrop.asp` the drop listener stayed `0`, so use `eval` to dispatch `dragstart` and `drop` on HTML5 pages.

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

Then fuzz one query parameter over the same entry:

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay_fuzz","target":"2","points":[{"param":"q","location":"query"}],"payloads":["a","b"],"strategy":"sniper","max_bytes":2000}}
```

Live on 2026-10-27 against `https://www.debian.org/`, sniper with one point and two payloads returned two runs plus the baseline:

```json
{"ok":true,"data":{"runs":[{"seq":1,"payload":"a","point":"q","status":200,"ms":105,"bytes":2000,"body_cut":true,"body_redactions":0},{"seq":2,"payload":"b","point":"q","status":200,"ms":93,"bytes":2000,"body_cut":true,"body_redactions":0}],"summary":{"baseline":{"status":200,"bytes":2000,"ms":271},"anomalies":[]}}}
```

`anomalies` is `[]` when no run differs from the baseline by status, by more than 10% in size, or by more than 3× in time. An over-bound matrix is refused before any request:

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_act: replay_fuzz: requests computed 243, over the 100 bound","hint":"lower the payload count or the point count, or use strategy sniper (points*payloads) to stay under the 100-request bound"}}
```

Replay re-reads the request and carries the page's cookies for the target host, and it never writes to the page. Pass `method` to override the method, `headers` to replace captured headers, or `value` to replace the request body. From a shell with the daemon up, the same replay is `browser page act last replay 2`, or `browser page act last replay /news.css --method GET --max-bytes 4000 --value ''`. The first positional is the target, and `--value` (not a positional) carries the replacement body.

## 8. Intercept and rewrite requests

Reuse `page-6` from §7: fulfill its `news.css`, reload, then read the tag and clear:

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"intercept_add","rules":[{"id":"css","url_pattern":"*news.css*","action":"fulfill","status":200,"headers":{"Content-Type":"text/css"},"body":"body{background:#fff}"}]}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"reload"}}
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"network"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"intercept_list"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"intercept_clear"}}
```

Expect `{"rules":[{"id":"css","url_pattern":"*news.css*","action":"fulfill","headers":{"Content-Type":"text/css"},"status":200,"body":"body{background:#fff}"}],"count":1,"active":true}`, then `reloaded page`, then a `news.css` entry carrying `"intercepted":"fulfill"`, `"intercept_rule_id":"css"`, `"status":200`, and `"mime":"text/css"`. `intercept_list` repeats the rule, and `intercept_clear` returns `{"rules_removed":1,"active":false}`. `block`, `allow`, and `rewrite` rules work the same way, and every match is tagged on the ring. The three actions work on MCP, CLI, and IPC: `browser page act last intercept_add --rules '<json>'` reaches the same engine. Three limits apply. A response served from cache bypasses the rules, so a rule for a cached CSS file does nothing until you use a fresh context or clear the cache. `rewrite` cannot change the HTTP method, because `method` is a match filter only. A rewritten header does not appear in the network ring's `req_headers`.

## 9. Arm the sink tracer and inject a canary

Open `https://www.sqlite.org/index.html` as `page-7`, install, inject the hash, read the hits, then clear:

```json
{"tool":"fetch_page","arguments":{"url":"https://www.sqlite.org/index.html","browser_id":"browser-10","wait_for":"idle"}}
{"tool":"page_act","arguments":{"session_id":"page-7","action":"sinks_install"}}
{"tool":"page_act","arguments":{"session_id":"page-7","action":"inject_canary","targets":["hash"]}}
{"tool":"page_act","arguments":{"session_id":"page-7","action":"eval","value":"() => { const d=document.createElement('div'); d.id='probe'; d.innerHTML=location.hash; document.body.appendChild(d); return d.textContent }"}}
{"tool":"page_fetch","arguments":{"session_id":"page-7","what":"sinks"}}
{"tool":"page_act","arguments":{"session_id":"page-7","action":"sinks_clear"}}
```

Expect `{"installed":true}`, then `{"canary":"wm-canary-…","injected":[{"target":"hash","where":"https://www.sqlite.org/index.html"}],"hits":[],"installed":true}`. The static page has no hash sink, so `hits:[]` is a clean result. The control eval makes a real finding: `{"source":"location.hash","sink":"innerHTML","context":"html","selector":"div#probe","value":"#wm-canary-…","stack":[…]}`. `selector` and `stack` are omitted when empty. Without `sinks_install` the call fails with `page_act: inject_canary needs the sink tracer — run action "sinks_install" first`. Then `{"cleared":1}` and a second `sinks_install` is a no-op.

## 10. Failure examples

An ambiguous replay URL substring anchors the failure path (tested live on 2026-10-26):

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay","target":"news.ycombinator.com/news"}}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_act: replay: target \"news.ycombinator.com/news\" matches 2 network entries — pass a seq or a longer URL substring: seq=1 GET https://news.ycombinator.com/news; seq=2 GET https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb"}}
```

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"click"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"press_key","key":"Banana"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"eval","value":"() => {"}}
```

Expect `page_act: click requires selector (or a handle from page_fetch what=interactives)`, `page_act: press_key: unknown key name "Banana"`, and `page_act: evaluate js: eval js error: SyntaxError: Unexpected token ')' <nil>`. Stale handles return the `page_act: handle "…" is stale — …` message, so re-read `interactives`. Do not send an unknown `select` value: a live `"value":"nope"` returned `MCP error -32001: Request timed out` and wedged the whole server.

## 11. Save and restore a login

Log in on `www.saucedemo.com`, save the session, close the browser, then restore it into a new browser and a new identity:

```json
{"tool":"browser_create","arguments":{"owner":"auth-demo","headless":true,"stealth":true}}
{"tool":"fetch_page","arguments":{"url":"https://www.saucedemo.com/","browser_id":"browser-11","wait_for":"load"}}
{"tool":"page_act","arguments":{"browser_id":"browser-11","session_id":"page-1","action":"input","selector":"#user-name","value":"standard_user"}}
{"tool":"page_act","arguments":{"browser_id":"browser-11","session_id":"page-1","action":"input","selector":"#password","value":"secret_sauce"}}
{"tool":"page_act","arguments":{"browser_id":"browser-11","session_id":"page-1","action":"click","selector":"#login-button"}}
{"tool":"page_fetch","arguments":{"browser_id":"browser-11","session_id":"page-1","what":"meta"}}
{"tool":"page_act","arguments":{"browser_id":"browser-11","session_id":"page-1","action":"auth_save","site":"www.saucedemo.com","name":"main"}}
{"tool":"browser_close","arguments":{"browser_id":"browser-11"}}
{"tool":"browser_create","arguments":{"owner":"auth-demo-2","headless":true,"stealth":true}}
{"tool":"fetch_page","arguments":{"url":"https://www.saucedemo.com/","browser_id":"browser-12","identity":"restored","wait_for":"load"}}
{"tool":"page_act","arguments":{"browser_id":"browser-12","session_id":"page-1","action":"auth_restore","site":"www.saucedemo.com","name":"main"}}
{"tool":"page_act","arguments":{"browser_id":"browser-12","session_id":"page-1","action":"navigate","value":"https://www.saucedemo.com/inventory.html"}}
{"tool":"page_fetch","arguments":{"browser_id":"browser-12","session_id":"page-1","what":"meta"}}
```

Expect the login click to land on `https://www.saucedemo.com/inventory.html` with title `Swag Labs`. `auth_save` returns `{"site":"www.saucedemo.com","name":"main","cookies":N,"origins":1,"user_agent":"…","updated_at":"…","warning":"auth snapshots are stored unencrypted in the local state database (mode 0600) …"}`. `auth_list` needs no session: it answers with a stale `session_id` and with no live browser, returning `{"snapshots":[{"site":"www.saucedemo.com","name":"main","cookie_count":N,"origin_count":1,"user_agent":"…","updated_at":"…"}],"count":1,"warning":"…"}` and never a value. `auth_restore` returns counts plus `forced:false` and the same `warning`, and the new identity renders the inventory page. The host-only `session-username` cookie stays scoped to `www.saucedemo.com` after restore. The store is plaintext with mode 0600, so the `warning` is the only confidentiality signal. Verified live on 2026-10-27.

## 12. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-12"}}
{"tool":"browser_close","arguments":{"browser_id":"browser-10"}}
```

Expect each browser to disappear from `browser_list`. Close only your own ids and never close an empty id.
