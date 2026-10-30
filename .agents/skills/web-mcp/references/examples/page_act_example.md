> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# page_act example

Use this file only when tool calling for `page_act` fails. The tool description is primary, and every result rides the envelope with the payload at `data`. Reference: `.agents/skills/web-mcp/references/tools/page_act.md`.

## 1. Create and confirm your browser

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

Expect `{"browser_id":"browser-10"}` in `data`. Use only your id and never close an empty id.

## 2. Form input, type, verify, and submit

```json
{"tool":"fetch_page","arguments":{"url":"https://httpbin.org/forms/post","browser_id":"browser-10","wait_for":"load"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"input","selector":"input[name=\"custname\"]","value":"Ada Lovelace"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"input","selector":"input[name=\"custtel\"]","value":"555-0100"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"type","selector":"input[name=\"custtel\"]","value":" EXT"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"eval","value":"() => ({name: document.querySelector('input[name=\"custname\"]').value, tel: document.querySelector('input[name=\"custtel\"]').value})"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"press_key","selector":"input[name=\"custname\"]","key":"Enter"}}
```

Expect `set input selector="input[name=\"custname\"]" length=12`, `length=8`, `typed … length=4`, then `{"name":"Ada Lovelace","tel":"555-0100 EXT"}`. Neither confirmation echoes the value. `input` replaces, `type` appends at the caret, and `Enter` submits.

## 3. select and upload_file

The httpbin form has no `<select>` or file input, so use the Selenium web-form page as `page-2`:

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"select","selector":"select[name=\"my-select\"]","value":"2"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"upload_file","selector":"input[type=\"file\"]","paths":["/tmp/opencode/example.txt"]}}
```

Expect `selected option value="2" …` and `uploaded files [/tmp/opencode/example.txt] …`. `select` matches the option `value`, not its text.

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

Expect `navigated to …` with no delta, then `navigated back to …`, `navigated forward to …`, `reloaded page`, `{"ready":"complete","title":"…"}`, `{"network":132,"console":1,"websocket":0}`, `set viewport to 1024x768`, and `emulated device: IPadPro`. A DOM node from `eval` returns `{}`. The page then reports `1024x1366` at DPR 2, and `{"device":"clear"}` resets both.

## 5b. Set inline CSS with set_style

Open `https://www.debian.org` as `page-8`, then restyle the footer:

```json
{"tool":"fetch_page","arguments":{"url":"https://www.debian.org","browser_id":"browser-10","wait_for":"idle"}}
{"tool":"page_act","arguments":{"session_id":"page-8","action":"set_style","selector":"#footer","value":"background-color:#003366; color:#ffffff; --debian-accent:#d70a53; colr:red"}}
{"tool":"page_fetch","arguments":{"session_id":"page-8","what":"interactives","selector":"#footer"}}
{"tool":"page_act","arguments":{"session_id":"page-8","action":"eval","value":"() => ({background:getComputedStyle(document.querySelector('#footer')).backgroundColor,accent:getComputedStyle(document.querySelector('#footer')).getPropertyValue('--debian-accent')})"}}
{"tool":"page_act","arguments":{"session_id":"page-8","action":"set_style","selector":"#footer","value":""}}
```

Expect `{"target":"selector=\"#footer\"","cleared":false,"applied":["background-color","color","--debian-accent"],"rejected":["colr:red"],"style":"background-color: rgb(0, 51, 102); color: rgb(255, 255, 255); --debian-accent: #d70a53;"}`. The scoped `interactives` read still resolves the footer links, and the computed style reads `rgb(0, 51, 102)` and `#d70a53`. Declarations split only at bracket depth zero, so `url('a;b')` and `rgb(1,2,3)` stay intact. An empty `value` clears the inline style and returns `"cleared":true`, `"applied":[]`, `"style":""`. The CLI form is `browser page act set_style '#footer' 'color:red'`, or `--handle g3k9x2ab-b6 --value 'color:red !important'`.

Verified: 2026-10-30 https://www.debian.org

## 5c. Encode a payload for a context (no page needed)

`encode` is page-free: it runs with no browser and no `session_id`.

```json
{"tool":"page_act","arguments":{"action":"encode","context":"attr-double","value":"<script>alert(1)</script>"}}
{"tool":"page_act","arguments":{"action":"encode","context":"js-string","value":"<script>alert(1)</script>"}}
{"tool":"page_act","arguments":{"action":"encode","context":"url-query","value":"wm 307 & <x>"}}
{"tool":"page_act","arguments":{"action":"encode","context":"html","value":"x"}}
```

Expect `{"context":"attr-double","variants":[{"label":"raw",…},{"label":"entity","value":"&lt;script&gt;alert(1)&lt;/script&gt;"},{"label":"url",…},{"label":"html5-attr",…}]}`, then the `js` variant `\x3cscript\x3ealert(1)\x3c\/script\x3e`, then `{"context":"url-query","variants":[{"label":"raw","value":"wm 307 & <x>"},{"label":"url","value":"wm+307+%26+%3Cx%3E"},{"label":"url-double","value":"wm%2B307%2B%2526%2B%253Cx%253E"}]}`. One `URLSearchParams` decode turns `url` back into the payload and two turn `url-double` back into it. `decodeURIComponent` alone does not round-trip a query value because `+` means space. The unknown `html` context fails as `invalid_args` and lists every accepted context.

Verified: 2026-10-30 https://www.mozilla.org

## 5d. Write an attribute and markup with set_attr and set_html

Open `https://www.kernel.org` as `page-9` and `https://www.gimp.org` as `page-10`:

```json
{"tool":"fetch_page","arguments":{"url":"https://www.kernel.org","browser_id":"browser-10","wait_for":"idle"}}
{"tool":"fetch_page","arguments":{"url":"https://www.gimp.org","browser_id":"browser-10","wait_for":"idle"}}
{"tool":"page_act","arguments":{"session_id":"page-9","action":"set_attr","selector":"#logo-akamai","name":"data-wm-307","value":"set-state"}}
{"tool":"page_act","arguments":{"session_id":"page-9","action":"set_attr","selector":"#logo-akamai","name":"data-wm-307","value":""}}
{"tool":"page_act","arguments":{"session_id":"page-10","action":"set_html","selector":"#cap-photos","mode":"inner","value":"<div id=\"wm-307\"><h3>Replaced by set_html</h3><p>web-mcp probe 307</p></div>"}}
{"tool":"page_fetch","arguments":{"session_id":"page-10","what":"html","selector":"#wm-307"}}
```

Expect `{"target":"selector=\"#logo-akamai\"","name":"data-wm-307","value":"set-state","removed":false,"nodes_added":0}`, then the same shape with `"value":"","removed":true`. The `set_html` call returns `{"target":"selector=\"#cap-photos\"","mode":"inner","nodes_added":5,"elements":[{"tag":"div","attributes":{"id":"wm-307"}}],"scripts":0,"executed":false}`, and the scoped `html` read returns `<div id="wm-307">…</div>`. `nodes_added` counts text nodes and `elements` caps at 50. `executed` is true only when the tracer saw a hit beyond the write, and markup inserted this way does not run its `<script>` children. A `srcdoc` write on an iframe reports `nodes_added:1`. `dom` ignores `selector`, so a scoped subtree read is `html` or `interactives`, never `dom`. The CLI forms are `browser page act set_attr 'a' href 'javascript:void(0)'` and `browser page act set_html 'body' append '<b id=wm1>hi</b>'`.

Verified: 2026-10-30 https://www.kernel.org
Verified: 2026-10-30 https://www.gimp.org

## 5e. Fill a form and submit it with fill_form

Read the form with `what=forms`, then fill seven control types in one call without submitting:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-1","what":"forms"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"fill_form","selector":"form","fields":{"form > p:nth-of-type(1) input":"Ada Lovelace","form > p:nth-of-type(2) input":"+15550100","form > p:nth-of-type(3) input":"ada@example.com","fieldset:nth-of-type(1) > p:nth-of-type(2) input":"on","fieldset:nth-of-type(2) > p:nth-of-type(4) input":"on","form > p:nth-of-type(4) input":"18:30","form > p:nth-of-type(5) textarea":"Ring twice"},"submit":"none"}}
```

Expect `{"form":"html > body > form","filled":[{"name":"size","selector":"…"},{"name":"topping","selector":"…"},{"name":"custname","selector":"…"},…],"skipped":[],"submit":"none","url_after":"https://httpbin.org/forms/post"}`. The `forms` read proves each write with `has_value:true`, including the Medium radio with `Small` still false. A name-to-value `values` entry cannot select a radio or checkbox member: the resolver takes the first control with that name and discards the value, so `{"size":"medium"}` checks `Small`. Use `fields` with an explicit selector to pick a member, and note a checkbox is only checked, never cleared. `skipped` carries a reason and is never an error. The CLI `--field` flag splits at the first `=`, so `--field 'input[name=size][value=large]=on'` sends the key `input[name` and Chrome resolves that fragment as an attribute-presence selector, filling the wrong element with no error. Prefer `--form-value`, or a selector without `=` such as one read from `what=forms`.

Verified: 2026-10-30 https://httpbin.org/forms/post

## 6. Drag and drop

```json
{"tool":"page_act","arguments":{"session_id":"page-2","action":"drag_and_drop","selector":"#img1","target_x":170,"target_y":533}}
```

Expect `dragged from (97.0, 532.2) to (170.0, 533.0)`. This is a mouse drag and it moved a live range slider from `5` to `9`. Native HTML5 drag-and-drop does not fire, so use `eval` to dispatch `dragstart` and `drop` on those pages.

## 7. Replay a captured request

Open `https://news.ycombinator.com/news` as `page-6`, read the ring, and replay one entry:

```json
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"network"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay","target":"2"}}
```

Expect `{"status":200,"headers":{"Content-Type":["text/css; charset=utf-8"],…},"body":"…","bytes":7418,"ms":63,"request":{"method":"GET","url":"https://news.ycombinator.com/news.css?…"}}`. Then edit a URL parameter and fuzz one entry:

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay","target":"1","query":{"probe":"two"},"max_bytes":300}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay_fuzz","target":"1","points":[{"param":"probe","location":"query"}],"payloads":["a","b"],"strategy":"sniper","max_bytes":500}}
```

`request.url` proves the edit, because parameter order can change. The fuzz result carries `"summary":{"baseline":{"status":200,"bytes":22060,"cold_ms":30,"ms":44},"anomalies":[]}`. An over-bound matrix is refused before any request: `page_act: replay_fuzz: requests computed 243, over the 100 bound`. Replay carries the page's cookies for the target host and never writes to the page. Pass `method`, `headers`, or `value` to override them; `""` removes a query parameter.

## 8. Intercept and rewrite requests

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"intercept_add","rules":[{"id":"css","url_pattern":"*news.css*","action":"fulfill","status":200,"headers":{"Content-Type":"text/css"},"body":"body{background:#fff}"}]}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"reload"}}
{"tool":"page_fetch","arguments":{"session_id":"page-6","what":"network"}}
{"tool":"page_act","arguments":{"session_id":"page-6","action":"intercept_clear"}}
```

Expect `{"rules":[…],"count":1,"active":true}`, then a `news.css` entry carrying `"intercepted":"fulfill"`, `"intercept_rule_id":"css"`, and `"status":200`, then `{"rules_removed":1,"active":false}`. A cached response bypasses the rules, `rewrite` cannot change the method, and a rewritten header does not appear in `req_headers`.

## 9. Arm the sink tracer and inject a canary

```json
{"tool":"page_act","arguments":{"session_id":"page-7","action":"sinks_install"}}
{"tool":"page_act","arguments":{"session_id":"page-7","action":"inject_canary","targets":["hash"]}}
{"tool":"page_fetch","arguments":{"session_id":"page-7","what":"sinks"}}
{"tool":"page_act","arguments":{"session_id":"page-7","action":"sinks_clear"}}
```

Expect `{"installed":true}`, then `{"canary":"wm-canary-…","injected":[{"target":"hash","where":"…"}],"hits":[],"installed":true}`. A static page with no hash sink returns `hits:[]`, which is a clean result. Without `sinks_install` the call fails with `page_act: inject_canary needs the sink tracer — run action "sinks_install" first`.

## 10. Failure examples

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"replay","target":"news.ycombinator.com/news"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"click"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"press_key","key":"Banana"}}
{"tool":"page_act","arguments":{"session_id":"page-2","action":"eval","value":"() => {"}}
```

Expect the ambiguous replay message naming both candidate `seq` values, `page_act: click requires selector (or a handle from page_fetch what=interactives)`, `page_act: press_key: unknown key name "Banana"`, and `page_act: evaluate js: eval js error: SyntaxError: Unexpected token ')' <nil>`. Stale handles return `page_act: handle "…" is stale — …`, so re-read `interactives`. Do not send an unknown `select` value: it can wedge the server.

## 11. Save and restore a login

```json
{"tool":"page_act","arguments":{"session_id":"page-1","action":"input","selector":"#user-name","value":"standard_user"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"input","selector":"#password","value":"secret_sauce"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"click","selector":"#login-button"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"auth_save","site":"www.saucedemo.com","name":"main"}}
{"tool":"page_act","arguments":{"session_id":"page-1","action":"auth_restore","site":"www.saucedemo.com","name":"main"}}
{"tool":"page_act","arguments":{"action":"auth_list","site":"www.saucedemo.com"}}
```

Expect `auth_save` to return counts plus `warning`, `auth_restore` to return counts and `forced:false`, and `auth_list` to work with no live session. The store is plaintext at mode 0600, so the `warning` is the only confidentiality signal. Host-only cookies stay scoped to their host.

## 11b. Bundle a finding as a PoC

```json
{"tool":"page_act","arguments":{"session_id":"page-6","action":"poc_bundle","value":"Stored XSS in search","paths":["/tmp/poc-xss"]}}
```

Expect `{"directory":"/tmp/poc-xss","files":[{"name":"meta.json","bytes":231},…],"omitted":[{"name":"sinks.json","reason":"not installed"}]}`. The directory holds the request/response pair, screenshot, DOM, storage, and README at mode 0600. Run `sinks_install` first to add `sinks.json`, and pass `target` to pin the request. A relative path or a non-empty directory is refused, and secret header values stay `<redacted>`.

## 12. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-10"}}
```

Expect the browser to disappear from `browser_list`. Close only your own id and never close an empty id.
