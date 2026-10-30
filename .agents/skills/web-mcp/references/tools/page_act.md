> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# page_act

Interact with a live page that `fetch_page` opened.

## What it does

`page_act` performs one action in an open session. The `session_id` picks the tab and the `action` field picks the operation. Actions cover clicking, typing, keys, mouse coordinates, drag, upload, JavaScript, navigation, waiting, waiting for a popup, emulation, log clearing, replaying and fuzzing a captured network request, in-flight request interception, sink control, canary injection, postMessage capture and sending, and auth snapshots. The page-free `encode` helper encodes one payload into the mechanically equivalent variants for a target context without opening or touching a page.

The confirmation rides the result envelope: the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the `{"ok":false,"error":…}` shape. The payload is one short prose string plus a state delta line. `eval`, `encode`, `fill_form`, `clear_logs`, the sink and canary actions, `replay`, `replay_fuzz`, the postMessage actions, `wait_open`, the `intercept_*` actions, and the dialog and HTTP-auth actions return compact JSON instead.

## Inputs

The schema is `code://pkg/mcp/tools/act.go#L125`. `session_id` and `action` are required by every page action, and a session held outside the declared `owner` is refused as `not_found`. `encode` needs neither `session_id` nor a live browser. Actions ignore fields they do not use.

| Name | Type | Required by | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | every action except `encode` and `auth_list` | — | Tab from `fetch_page`, for example `page-3`. |
| `action` | string | always | — | One of the actions below. |
| `browser_id` | string | no | — | Optional. Resolve the session inside this browser only, for example `browser-2`. Omit it when exactly one live browser holds the id. |
| `owner` | string | no | — | Optional. Restrict the lookup to browsers with this owner label, 1 to 64 characters of `A-Z`, `a-z`, `0-9`, `.`, `_`, `-`. |
| `selector` | string | `click`, `input`, `type`, `hover`, `select`, `set_style`, `upload_file` | — | CSS selector or XPath. Optional for `press_key` and the drag source. For `fill_form` it names the `<form>` and may be omitted when `fields` carries full selectors. |
| `handle` | string | alternative to `selector` | — | Handle from `page_fetch` `what=interactives\|dom\|links`. Wins over `selector`. |
| `value` | string | `eval`, `navigate`, `input`, `type`, `select`, `set_style`, `replay`, `post_message`, `poc_bundle`, `encode` | — | Payload text, JavaScript source, URL, option value, CSS declaration list, message payload, `poc_bundle` finding title, or the `encode` payload. For `set_style` an empty value removes the inline style. For `replay` it replaces the request body and is sent only when the field is present. For `inject_canary` it is a custom canary. Omit it to mint `wm-canary-<8 hex>`. For `dialog_accept` it is the prompt text an accept supplies. For `http_auth` it is `user:password`, split at the first colon, and an empty value clears the credential. |
| `context` | string | `encode` | — | Where the payload lands, which selects the variant set: `html-text`, `attr-double`, `attr-single`, `attr-unquoted`, `js-string`, `js-template`, `css-value`, `url-query`, or `url-path`. An unknown context fails as `invalid_args` and lists every accepted value. |
| `variants` | array | `encode` (optional) | full set | Narrows the returned labels. `raw` is always present. A label the context does not offer fails as `invalid_args`. |
| `targets` | array | `inject_canary` (optional) | all four | Injection points: `query`, `hash`, `postmessage`, `name`. Each entry may be a comma-separated list. |
| `target` | string | `replay`, `replay_fuzz`, `poc_bundle` | — | Captured network entry: a `seq` number or a substring of the request URL. |
| `method` | string | `replay` (optional) | — | HTTP method override, for example `PUT`. Omit to keep the captured method. |
| `headers` | object | `replay` (optional) | — | `replay` only: each named header replaces the captured value on the resent request, others keep theirs. It does not set headers on page requests; use `browser_create` `headers` for browser-wide request headers or `intercept_add` with `action:"rewrite"` for a per-request rewrite. |
| `query` | object | `replay` (optional) | — | `replay` only: URL parameters to edit. A non-empty value replaces that parameter, and `""` removes it. Every other captured parameter keeps its value. Parameter order can change. `request.url` shows the rebuilt URL. |
| `max_bytes` | integer | `replay` (optional) | `262144` | Cap on the returned replay body. Larger values are clamped. A cut body returns `truncated:true`. |
| `raw` | boolean | no | `false` | Disable humanized timing for this call: one instant pointer move and no pre/post pause. Applies to `click`, `click_at`, `input`, `hover`, `hover_at`, and `press_key`. |
| `points` | array | `replay_fuzz` | — | Injection points, each `{param,location}` with `location` one of `query`, `body`, `header`, `cookie`. At most 20. A stringified JSON array is accepted. |
| `payloads` | array | `replay_fuzz` | — | Values injected at each point, in plan order. At most 50 of 200 runes each. A stringified JSON array is accepted. |
| `strategy` | string | no | `sniper` | `replay_fuzz` plan: `sniper` (points × payloads), `pitchfork` (nth point with nth payload), or `clusterbomb` (payloads ^ points). |
| `key` | string | `press_key` | — | Key or combo, for example `Enter` or `Control+A`. `value` is a fallback. |
| `button` | string | no | `left` | `left`, `right`, or `middle` for `click` and `click_at`. |
| `click_count` | integer | no | `1` | Click count for `click` and `click_at`. `2` is a double click. |
| `x` | number | `click_at`, `hover_at` | `0` | Horizontal pixel coordinate. Also the drag source and `scroll` amount. |
| `y` | number | `click_at`, `hover_at` | `0` | Vertical pixel coordinate. Also the drag source and `scroll` amount. |
| `target_x`, `target_y` | number | no | `0` | Drag target pixel coordinates. |
| `target_selector` | string | no | — | Drag target element, or the `post_message` destination: `top` (default), `self`, `parent`, or a CSS selector naming an iframe. Its center overrides `target_x`/`target_y` for a drag. |
| `origin` | string | no | `*` | Target origin for `post_message`. Pass the receiver's origin, for example `https://widget.example`. |
| `paths` | array | `upload_file`, `poc_bundle` | — | Absolute local file paths. For `poc_bundle`, `paths[0]` is the absolute output directory, created with mode `0700` when absent; a relative path or a non-empty directory is refused. `value` is the single-path fallback for `upload_file`. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. For `wait` `ms` mode it is the sleep time. |
| `wait_mode` | string | no | `ms` | `load`, `idle`, `stable`, `interactable`, or `ms`. |
| `viewport` | object | `set_viewport` | — | `{"width":<int>,"height":<int>}` in CSS pixels. Both must be positive. |
| `device` | string | `emulate` | — | go-rod preset name, for example `IPhoneX`. `clear` resets. |
| `rules` | array | `intercept_add` | — | Rules, each `{id?,url_pattern?,method?,action,headers?,status?,body?,delay_ms?}`. At most 50 per page across calls, and `action` is required. See [Request interception](#request-interception). |
| `site` | string | `auth_save`, `auth_restore` (optional for `auth_list`) | — | Host the snapshot is scoped to, for example `www.saucedemo.com`. Accepts a bare host or a URL. For `auth_list` it is a filter. |
| `name` | string | `auth_save`, `auth_restore`, `set_attr` | — | Snapshot name, unique per site, for the auth actions. For `set_attr` it is the attribute name to write, for example `href`, `src`, `onerror`, `srcdoc`, or `formaction`. |
| `mode` | string | no | `inner` | `set_html` write: `inner` assigns `innerHTML`, `outer` assigns `outerHTML`, `append` calls `insertAdjacentHTML("beforeend")`. |
| `fields` | object | `fill_form` (one shape) | — | `fill_form` field selector → value. Mutually exclusive with `values`. Without a form selector the selectors resolve against the document; with one they resolve inside that form. |
| `values` | object | `fill_form` (one shape) | — | `fill_form` field name → value, resolved inside the form named by `selector`. Requires `selector`. Mutually exclusive with `fields`. |
| `submit` | string | no | `requestSubmit` | `fill_form` submit mode: `requestSubmit` calls `form.requestSubmit()`, `click` clicks the form's submit control, `none` only fills. A submit other than `none` requires a form. |
| `force` | boolean | no | `false` | `auth_restore` only. Restore across a user-agent mismatch. Without it the restore is refused and both agents are named. |

`value` is required for `eval`, `navigate`, and `post_message`, and the server rejects an empty value for the first two before it touches the page. An empty `value` is valid for `set_style`: it removes the element's inline style. For `upload_file`, pass `paths` (a single `value` path also works). `encode` needs `context`; an empty payload is encoded as empty variants.

## Actions

| `action` | Required arguments | Effect | Return text | State delta |
| :--- | :--- | :--- | :--- | :--- |
| `click` | `selector` or `handle` | Waits for the element to be interactable, then clicks. | `clicked selector="…"` | Yes, when navigation commits. |
| `input` | `selector` or `handle`, `value` | Selects all text, then types. The field ends exactly as `value`. | `set input selector="…" length=N`, plus `value_redacted=true` for a password or hidden field. | No. |
| `type` | `selector` or `handle`, `value` | Focuses, then inserts at the caret. It does not clear. | `typed selector="…" length=N`, plus `value_redacted=true` for a password or hidden field. | No. |
| `press_key` | `key` (or `value`) | Presses a key or combo. Focuses a target when given. | `pressed key "Enter" in selector="…"` | Yes, if the key navigates. |
| `click_at` | `x`, `y` | Moves the mouse to the point and clicks. | `clicked at (34.0, 33.0) button=left count=1` | Yes, if a click navigates. |
| `hover_at` | `x`, `y` | Moves the mouse to the point. | `hovered at (512.0, 33.0)` | No. |
| `drag_and_drop` | source and target | Mouse drag. See the combinations below. | `dragged from (97.0, 532.2) to (170.0, 533.0)` | No. |
| `upload_file` | `selector` or `handle`, plus `paths` | Sets files on an `<input type="file">`. | `uploaded files [/abs/path] to selector="…"` | Yes, when state changes. |
| `eval` | `value` | Runs a JavaScript function in the page. | Compact JSON of the return value. | Suppressed, so the result stays parseable JSON. |
| `navigate` | `value` | Navigates and waits for load. | `navigated to "…" (title "…")` | Suppressed. |
| `back` | — | Goes back and waits for load. | `navigated back to "…"` | Yes. |
| `forward` | — | Goes forward and waits for load. | `navigated forward to "…"` | Yes. |
| `reload` | — | Reloads and waits for load. | `reloaded page` | Yes, when state changes. |
| `hover` | `selector` or `handle` | Moves the mouse over the element. | `hovered selector="…"` | No. |
| `set_style` | `selector` or `handle`, `value` | Writes inline CSS declarations from `value` onto the element, split only at bracket depth zero so `url('a;b')` and `rgb(1,2,3)` survive. An empty `value` removes the inline style attribute. See [Inline styles](#inline-styles). | Compact JSON `{target,cleared,applied,rejected,style}`. | Yes, when state changes. |
| `set_attr` | `selector` or `handle`, `name`, `value` | Sets one attribute from `name` and `value`. An empty `value` removes the attribute. There is no allowlist, so `javascript:` and `onerror` reach the page unchanged. See [Attribute and markup injection](#attribute-and-markup-injection). | Compact JSON `{target,name,value,removed,nodes_added}` plus `url_delta` only when the URL changed. | Yes, when state changes. |
| `set_html` | `selector` or `handle`, `value`, optional `mode` | Writes markup into the element: `inner` (default), `outer`, or `append`. No sanitisation. See [Attribute and markup injection](#attribute-and-markup-injection). | Compact JSON `{target,mode,nodes_added,elements,scripts,executed}`. | Yes, when state changes. |
| `encode` | `value`, `context`; optional `variants` | Page-free helper: encodes `value` for the target context without opening, locking, or touching a page. See [Payload encoding](#payload-encoding). | Compact JSON `{context,variants:[{label,value}]}`. | No. |
| `scroll` | `x`, `y` (default `0`) | Calls `window.scrollBy(x, y)`. | `scrolled to [\n  0,\n  400\n]` | No. |
| `select` | `selector` or `handle`, `value` | Selects an `<option>` by exact `value` attribute. | `selected option value="2" in selector="…"` | Yes, when state changes. |
| `fill_form` | `fields`, or `selector` plus `values`; optional `submit`, `wait_mode` | Fills every field through its native setter with the events React and Vue listen for, then optionally submits. See [Filling a form](#filling-a-form). | JSON object `{form,filled:[{name,selector}],skipped:[{selector,reason}],submit,url_after}`. | Suppressed: `url_after` carries the change. |
| `wait` | `wait_mode` | Waits for a readiness level or sleeps. | `waited 250ms`, `waited for "load"`, or `element "…" is interactable` | No. |
| `set_viewport` | `viewport` | Applies a manual width and height override. | `set viewport to 800x600` | No. |
| `emulate` | `device` | Applies a device preset, or clears it. | `emulated device: IPhoneX` | No. |
| `clear_logs` | — | Empties the network, console, websocket, dialog/auth, and CSP-violation rings and resets their `dropped` counters. | `{"network":132,"console":1,"websocket":0,"dialogs":2,"violations":0}` | No. |
| `sinks_install` | — | Arms the source-to-sink tracer on the current document and every later navigation. Idempotent. Capture is opt-in and changes page behavior. | `{"installed":true}`. A failure returns an error, never `installed:false`. | No. |
| `sinks_clear` | — | Empties the tracer's hit ring and resets `dropped`. Safe on a page that was never armed. | `{"cleared":N}`, `0` when never armed. | No. |
| `replay` | `target`; optional `method`, `headers`, `query`, `value`, `max_bytes` | Resends one captured network entry through a Go HTTP client, outside the browser. See [Replay with what=network as the source](#replay-with-whatnetwork-as-the-source). | Compact JSON `{status,headers,body\|base64,bytes,body_bytes,truncated,body_redactions,body_uncaptured,ms,request,chain}`; secrets are `<redacted>`. | No. |
| `replay_fuzz` | `target`, `points`, `payloads`; optional `strategy`, `max_bytes` | Runs a bounded matrix of variants over one captured entry. The size is computed and refused before any request. See [Replay fuzz](#replay-fuzz). | Compact JSON `{runs:[{seq,payload,point,status,ms,bytes,body_cut,body_redactions}],summary:{baseline:{status,bytes,cold_ms,ms},anomalies}}`. | No. |
| `inject_canary` | optional `targets`, `value` | Injects `wm-canary-<8 hex>` (or `value`) into `query`, `hash`, `postmessage`, and `name`, default all four. `query` reloads, `hash` does not, and work is capped at 20 frames. Run `sinks_install` first. | Compact JSON `{canary,injected:[{target,where}],hits:[{sink,context,selector,value,stack}],installed}`. | No. |
| `postmessage_install` | — | Arms the postMessage recorder on every reachable window. | `{"installed":true}` | No. |
| `postmessage_clear` | — | Empties every recorder ring and resets `dropped`. | `{"cleared":N}` | No. |
| `post_message` | `value` | Sends a message to `top` or the iframe in `target_selector`. Arms the recorder first. | `{origin,target}` | No. |
| `wait_open` | — | Blocks until this page opens a popup or tab. | Compact JSON `{session_id,url,opener}`. | No. |
| `intercept_add` | `rules` | Arms the in-flight interceptor and adds or replaces rules. | Compact JSON `{rules,count,remaining,active}`. | No. |
| `intercept_list` | — | Lists the page's rules, with sensitive values redacted. | Compact JSON `{rules,count,remaining,active}`. | No. |
| `intercept_clear` | — | Removes every rule and disables interception. | Compact JSON `{rules_removed,active:false}`. | No. |
| `auth_save` | `site`, `name` | Captures the page's cookies for `site` plus local storage, session storage, and IndexedDB names. | Compact JSON counts plus `warning`. | No. |
| `auth_restore` | `site`, `name` | Writes the snapshot into the page's browser context: cookies, then storage for the live origin. | Compact JSON counts plus `warnings` and `warning`. | No. |
| `auth_list` | `site` (optional) | Lists saved snapshots. Needs no live session. | Compact JSON `{snapshots,count,warning}`. | No. |
| `poc_bundle` | `value`, `paths`; optional `target` | Packages one finding as an evidence directory: `meta.json`, `request.http`, `response.http`, `screenshot.png`, `dom.html`, `storage.json`, `README.md`, plus `sinks.json` when the tracer is installed. See [PoC bundle](#poc-bundle). | Compact JSON `{directory,files,omitted}`. | Suppressed. |
| `dialog_accept` | optional `value` | Switches the page dialog policy to accept, answering later prompts with `value` as the prompt text. Set it before the step that opens the dialog. | `{policy,prompt_text,count,dropped,last}` with `policy:"accept"`. | No. |
| `dialog_dismiss` | optional `value` | Switches the dialog policy to dismiss. This is the default. | Same shape with `policy:"dismiss"`. | No. |
| `http_auth` | `value` | Arms a per-page HTTP basic-auth credential. `value` is `user:password`, split at the first colon, and an empty `value` clears it. Arm it before the navigation that answers a 401. It refuses while `intercept_add` holds the Fetch domain, and `intercept_add` refuses while auth is armed. | `{http_auth:"armed"\|"cleared"}`, plus `note` when the origin already holds a cached credential. | No. |

A non-positive `click_count` becomes `1`. An unknown `button` is rejected before the click. The server rejects a missing `x` or `y` key for `click_at` and `hover_at`, and an explicit `0` is valid.

## PoC bundle

`poc_bundle` turns one live page into a self-contained directory for a report. One call is `{"tool":"page_act","arguments":{"session_id":"page-6","action":"poc_bundle","value":"Stored XSS in search","paths":["/tmp/poc-xss"]}}`.

- `value` is the finding title written to `meta.json` and `README.md`.
- `paths[0]` is the absolute output directory. It is created with mode `0700` when absent. A relative path is refused with `poc_bundle: "…" must be absolute`, and a non-empty directory with `poc_bundle: … is not empty — pass a new directory or remove its contents`.
- `target` optionally selects the captured request by `seq` or URL substring. Without it the main document request is used.

Every artifact is mode `0600`:

| File | Content |
| :--- | :--- |
| `meta.json` | `url`, `title`, RFC 3339 `timestamp`, `finding`, `web_mcp_version`, `session_id`. |
| `request.http`, `response.http` | Raw HTTP pair: method, path, version, sorted headers, blank line, body. |
| `screenshot.png` | Viewport capture through the shared screenshot seam. |
| `dom.html` | Outer HTML, capped at 2 MB with a trailing `<!-- truncated -->`. |
| `storage.json` | The storage report, values `<redacted>` unless secret capture is on. |
| `sinks.json` | The sink ring, written only when `sinks_install` was run. |
| `README.md` | Finding title, URL, timestamp and a file table with byte counts. |

The result is `{"directory":"…","files":[{"name","bytes"}],"omitted":[{"name","reason"}]}`. `omitted` names every artifact or value left out: `not installed` for an unarmed tracer, `no network entry` when the request was not observed, `no body` when a body could not be fetched, `cap reached` for a cut DOM, and `secrets hidden` when a request or response header value was redacted; a redacted `storage.json` value adds no row.

Cookie, Set-Cookie, Authorization and X-CSRF-Token values are `<redacted>` unless the page opted into secret capture. Nothing is overwritten: each file is written with `O_EXCL`, and the page lock is never held across a file write.

## Returns

The confirmation and the state delta arrive as one JSON string inside `data`. A live form submit on 2026-10-26 returned `{"ok":true,"data":"clicked selector=\"form button\"\n→ url=https://httpbin.org/post title=\"httpbin.org/post\""}`. The delta starts on a new line and lists only changed fields. `navigate`, `eval`, and `poc_bundle` never append one, so `eval` returns its own JSON only and stays parseable; `back` and `forward` do append one. `eval`, `set_style`, `set_attr`, `set_html`, `fill_form` (a JSON object), `clear_logs`, `sinks_install`, `sinks_clear`, `inject_canary`, `replay`, `replay_fuzz`, `postmessage_install`, `postmessage_clear`, `post_message`, `wait_open`, the three `intercept_*` actions, `dialog_accept`, `dialog_dismiss`, and `http_auth` return compact JSON in `data` instead of prose. A DOM node from `eval` comes back as `{}`. `click`, `click_at`, `press_key`, `upload_file`, `eval`, and `select` poll up to 900 ms for a navigation.

## Behavior and limits

### Handles

A handle has the form `g<document>-b<backendNodeId>`, for example `gcq7sm3ng-b165`. Get handles from `page_fetch` with `what=interactives`, `dom`, or `links`. A handle wins over `selector`. A live `input` with a text-field handle and a password-field selector wrote to the text field, and the password field stayed empty. The `g` token names the document that minted the handle: any navigation, reload, redirect, or document replacement changes it, and the old handle then fails as stale, so re-read `interactives` after every navigation. An `interactives` entry from a same-process child iframe works the same way: its `frame_url` names the frame, the backend node id is renderer-global, and its `handle` still acts through `page_act`.

### Element actions

`click`, `click_at`, `input`, `hover`, `hover_at`, and `press_key` are humanized by default: the pointer steps to the target with small jitter, and a randomized 100-500 ms pause precedes and follows the action. Measured on vuejs.org on 2026-10-27, the default mean was 1.908 s and `--raw` 0.946 s. Any one hatch disables it: per-call `raw:true`, `WEB_MCP_RAW_ACTIONS=1` in the server environment, or a server started with `browsers.WithRawActions(true)`. The pause is skipped when it does not fit the remaining budget, never clipped, and a budget that expires first returns `timeout` with the action unperformed.

`input` replaces the field contents: it selects all text first, then types `value`. `type` does not clear, it focuses the element and inserts at the caret. A live `input` of `555-0100` then a `type` of `" EXT"` left the field at `555-0100 EXT`, confirmed by `eval`. Some controls reject synthetic text — a live `input` on `<input type="time">` left the value empty, so use `eval` to set those values. `click` and `wait` `interactable` check interactability, and `input` and `type` do not, so a hidden element can still receive text.

### Inline styles

`set_style` writes arbitrary inline CSS declarations onto the element in `selector` or `handle` (`handle` wins when both are given). `value` is a declaration list such as `background-image:url('/a.png');--page-bg:red`, split only at bracket depth zero and outside quotes, so `url('a;b')`, quoted strings, `rgb(1,2,3)` and `!important` survive. There is no property allowlist: a `url(` value reaches the page unchanged, so a style-only payload can trigger a network request. A non-empty `value` merges onto the element's existing inline `cssText`: declarations from earlier calls persist, and only the properties you pass are overwritten. An empty `value` removes the whole `style` attribute and is the reset path.

The result is compact JSON `{target,cleared,applied,rejected,style}`. `target` is `selector="…"` or `handle="…"`, and `cleared` is `true` only for an empty `value`. `applied` names the properties *this call set*, not the serialized shorthand list. `style` is the element's `cssText` after the change, clipped at 2000 runes, so a shorthand that already existed can serialize differently: live, `applied:["background-image"]` while `style` rendered `background: url(...)`, because a `background` shorthand was already present. `applied` is the call's input list and `style` is the CSSOM's serialization of the whole declaration.

`rejected` holds the declarations it dropped (an invalid value or a missing colon, never silently swallowed). Each declaration is validated against a detached probe first, so a rejected value is reported even when the element already held that property. A selector that matches nothing fails as `element_not_found` without waiting for `timeout_ms`.

**CLI and IPC.** `browser page act set_style '.site-footer' 'background-image:url("/favicon.ico?x=1")'` is the positional form. With a handle, pass the declarations through the flag: `browser page act set_style --handle g3k9x2ab-b6 --value 'color:red !important'`, because a lone positional always fills `selector`. IPC `page.act` carries the existing `action`, `selector`, `handle`, and `value` fields, so no new wire field is needed.

### Attribute and markup injection

`set_attr` and `set_html` are the raw write pair beside `set_style`. Neither has an allowlist or sanitises: `javascript:` and `onerror` values reach the page unchanged, and injected markup is parsed as markup. Parameterise every value you do not control.

`set_attr` writes `name` on the element targeted by `selector` or `handle` (`handle` wins). A non-empty `value` calls `setAttribute`; an empty `value` removes the attribute and returns `removed:true`. The result is `{target,name,value,removed,nodes_added}`. `nodes_added` counts nodes the browser created: an attribute-only write is `0`, while `name:"srcdoc"` on an iframe reports the frame node as `1`. A `url_delta:true` field appears only when the write changed the top-level URL.

`set_html` writes `value` into the target. `mode` is `inner` (default, `innerHTML`), `outer` (`outerHTML`), or `append` (`insertAdjacentHTML("beforeend")`). The result is `{target,mode,nodes_added,elements,scripts,executed}`. `elements` lists the created top-level elements as `{tag,attributes}` and caps at 50; `nodes_added` and `scripts` still count the whole write. `executed` is `true` only when the tracer recorded a hit beyond the write itself, so arm `sinks_install` first; markup inserted through these sinks does not execute its `<script>` children in Chromium, so `scripts:1` with `executed:false` is the normal result.

**CLI and IPC.** `browser page act set_attr 'a' href 'javascript:void(0)'` and `browser page act set_html 'body' append '<b id=wm1>hi</b>'` are the positional forms. With a handle, use `--handle` plus `--attr-name`/`--value` or `--mode`/`--value`. IPC `page.act` carries `attr_name` and `mode` beside the existing `action`, `selector`, `handle`, and `value` fields, so `name` stays the auth snapshot field.

### Filling a form

`fill_form` fills a form and optionally submits it in one call, replacing the five-call `input` per field plus `click` loop. Pick one input shape: `fields` is a selector → value object, or `selector` names the `<form>` and `values` is a name → value object resolved inside it. `fields` and `values` are mutually exclusive, and `values` requires a form selector. When a form selector is given, `fields` selectors resolve inside that form; otherwise they resolve against the document.

Each control is written through its native prototype setter, so a framework that shadows `value` still sees the change, and then receives the events it listens for: a checkbox or radio is checked and gets `change`, a select takes the option value and gets `change`, a textarea takes the value and gets `input`, and every other control takes the value and gets `input` and `change`. `submit` is `requestSubmit` (default, `form.requestSubmit()`), `click` (the form's submit control), or `none`. `wait_mode` `load`, `idle`, or `stable` waits after the submit; `ms` and `interactable` are ignored, because the action never sleeps.

The result is a JSON object, not a JSON string: `{form,filled:[{name,selector}],skipped:[{selector,reason}],submit,url_after}`. No state delta is appended, because `url_after` already carries the landed URL. `form` is the resolved form selector or `""`. A field that does not resolve is reported in `skipped` with a reason, never as an error. A form selector that matches nothing is `element_not_found`. A `submit` other than `none` with no form (no selector and no field inside a `<form>`) is `invalid_args`. The action never moves the pointer or presses a key, so no humanized pause applies.

**CLI and IPC.** `browser page act fill_form 'form#login' --field '#user=a' --field '#pass=b' --submit requestSubmit` fills by selector; `browser page act fill_form 'form#search' --form-value q=wm1 --submit click` fills by name inside the form. `--field` and `--form-value` split at the first `=`, so a selector must not contain `=`. IPC `page.act` carries the two object arguments as `fields` and `values` beside `selector`, `submit`, and `wait_mode`; it does not smuggle JSON through `value`.

### Payload encoding

`encode` is a pure helper with no page. It never validates, blocks, or encodes into the page: it returns the mechanically equivalent spellings of one payload so a bypass test can paste each one. It holds no page lock and sends no request, so it works with no browser open.

`value` is the payload and `context` is where it lands. Each context offers a fixed variant set, and `raw` is always first:

| `context` | Variants |
| :--- | :--- |
| `html-text` | `raw`, `entity`, `html5-attr` |
| `attr-double` | `raw`, `entity`, `url`, `html5-attr` |
| `attr-single` | `raw`, `entity`, `url`, `html5-attr` |
| `attr-unquoted` | `raw`, `entity`, `url`, `html5-attr` |
| `js-string` | `raw`, `js`, `js-hex`, `unicode` |
| `js-template` | `raw`, `js`, `js-hex`, `unicode` |
| `css-value` | `raw`, `url`, `unicode` |
| `url-query` | `raw`, `url`, `url-double` |
| `url-path` | `raw`, `url`, `url-double` |

| Label | What it emits |
| :--- | :--- |
| `raw` | The payload unchanged. |
| `entity` | HTML entities for `&`, `<`, `>`, `"`, and `'` (`&amp;`, `&lt;`, `&gt;`, `&#34;`, `&#39;`). |
| `html5-attr` | Every character HTML5 forbids in an unquoted attribute value as an entity, so it is safe in quoted and unquoted attributes. |
| `url` | Percent-encoding: query encoding for `url-query`, path encoding elsewhere. |
| `url-double` | The `url` variant percent-encoded a second time. |
| `js` | JavaScript string escaping of `\`, both quotes, control whitespace, `<`, `>`, `&`, and `/`. `js-template` also escapes the backtick and `$`. |
| `js-hex` | Every rune as `\xHH` (2 hex digits) or `\uHHHH` above `0xFF`. |
| `unicode` | Every rune as `\uHHHH`, or as a six-digit CSS escape in `css-value`. |

The result is `{context,variants:[{label,value}]}`. `variants` narrows the labels; an unknown label for the context fails as `invalid_args`. An unknown `context` fails with `page_act: encode: unknown context "…" (must be one of attr-double, attr-single, attr-unquoted, css-value, html-text, js-string, js-template, url-path, url-query)`.

**CLI and IPC.** `browser page act encode attr-double '<script>alert(1)</script>'` is the positional form (`context` then payload), and `--context`, `--value`, and `--variants` are the flag fallbacks. IPC `page.act` carries `action`, `value`, `context`, and `variants`; the handler resolves `encode` before the session lookup, so no `session_id` is needed.

### Keyboard combos

Join modifiers and the main key with `+`, so `Control+A`. Modifiers are `Control`/`Ctrl`, `Alt`/`Option`, `Shift`, and `Meta`/`Cmd`/`Command`/`Super`/`Windows`/`Win`. Main keys include `Enter`, `Escape`, `Tab`, `Backspace`, `Delete`, `Space`, the arrows, `Home`, `End`, `PageUp`, `PageDown`, `F1` to `F12`, letters, digits, and common punctuation. Names are case-insensitive and the last token is the main key. `press_key` focuses the element when you pass `selector` or `handle`, and a live `Enter` in the httpbin name field submitted the form.

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

`timeout_ms` defaults to `30000` and bounds the whole call. For `ms` mode the server adds a 5 second margin. If you omit `timeout_ms`, an `ms` wait sleeps 30 seconds, so always set it.

**CLI and IPC.** `--timeout-ms` no longer bounds the CLI socket read. The daemon allows the action 5 s of grace inside its 30 s transport budget, and the client reads for 15 s beyond the deadline, so a completed action returns its result with a trailing `(completed after its deadline: … elapsed, deadline …)` note instead of an i/o timeout. Live: a 200 ms click completed at 0.65-1.78 s.

### Emulation and viewport

`set_viewport` applies a manual override with DPR 1, and both values must be positive. `emulate` applies a go-rod preset with user agent, metrics, and touch, and a later `emulate` wins over an earlier `set_viewport`. Live result: `1024x768` then `IPadPro` gave `1024x1366` at DPR 2. `emulate` with `clear` resets the preset and drops a manual override. Apply emulation before layout work, because a metrics change can reflow the page.

### Logs

Network, console, websocket, dialog/auth, and CSP-violation capture is cumulative from page creation. `clear_logs` empties all five rings, resets `dropped`, and returns `{"network":N,"console":M,"websocket":K,"dialogs":D,"violations":V}`. The `seq` counter does not reset. The recipe is `clear_logs`, then one action, then `page_fetch` `network`, `console`, `websocket`, `dialogs`, or `security` for violations.

### Request interception

`intercept_add` arms a page-scoped CDP `Fetch` hook and installs rules (`code://pkg/browsers/intercept.go#L360`). Rules match the full request URL in insertion order, first match wins, and a request that matches none continues immediately. `url_pattern` is an anchored, case-sensitive wildcard glob: `*` matches any run, `?` matches one rune, and every other character, dots included, is literal. It defaults to `*`. `method` narrows the rule to one HTTP method and is compared case-insensitively.

| `action` | Outcome |
| :--- | :--- |
| `block` | Fails the request with `BlockedByClient`, so the page sees a network error. |
| `allow` | Continues the request unchanged. Put it before a broader rule to exempt one URL. |
| `rewrite` | Continues it with `headers` merged onto the captured set and `body` replacing the payload when non-empty. |
| `fulfill` | Answers from memory with `status` (default 200, range 100-599), `headers` and `body`, with no network fetch. |

A rule carries at most 32 headers, an empty header name is refused, and `delay_ms` is clamped to 10000. Reusing an `id` replaces that rule in place. An omitted `id` is minted as `r1`, `r2`, and so on. The page holds at most 50 rules, so the 51st add is refused (`code://pkg/browsers/intercept.go#L27`). `intercept_add` returns `{"rules":[…],"count":N,"active":true}` with sensitive header values and secret-like body values replaced by `<redacted>`. `intercept_list` returns the same shape, or `{"rules":[],"count":0,"active":false}` on an unarmed page. `intercept_clear` continues any paused request, disables the hook and returns `{"rules_removed":N,"active":false}`.

Interception is opt-in per session and changes page behaviour, so clear it when the step is done. It shares the CDP `Fetch` domain with proxy authentication, so do not arm it on a browser created with `proxy` plus credentials in `proxy.json`: arm one or the other. Rules never outlive the page. Every matched request is tagged on the network ring, so `page_fetch what=network` shows `intercepted` (`block`, `allow`, `rewrite`, or `fulfill`) and `intercept_rule_id`. A `block` entry also carries `failed:true` and an `error` naming the rule. A `fulfill` entry carries the synthetic `status` and `mime`.

Three limits apply. A response served from the HTTP cache bypasses the hook entirely, so a rule for a cached CSS file has no effect. Use a fresh context or clear the cache before arming rules, and expect WEB-212 to change this. `rewrite` cannot change the HTTP method, because `method` is a match filter only. A header set by `rewrite` does not appear in the ring's `req_headers`, so the ring is not proof of what was sent.

**Every surface.** `intercept_add`, `intercept_list`, and `intercept_clear` work on MCP, CLI, and IPC. `browser page act last intercept_add --rules '<json>'` reaches the same engine, and IPC `page.act` carries `action` plus `rules`. The same rule is installed whichever surface you use.

### Auth snapshots

`auth_save` captures the live page's cookies whose domain matches `site`, plus local storage, session storage, and IndexedDB database/store names, and persists them under `name` in the local state database. It returns counts only. `auth_restore` writes the named snapshot into the page's browser context — cookies first, then storage for the live origin — so a new browser and a new `identity` both render the login. Live on 2026-10-27: a saved `www.saucedemo.com` login restored into a fresh browser and a fresh identity, and the inventory page rendered. Host-only cookies stay host-only. Live, `session-username` stayed scoped to `www.saucedemo.com` after restore and was not broadened to `.saucedemo.com` or to a sibling host. A stored user agent that differs from the live page is refused unless `force:true`: `stored user agent "…" does not match the live page user agent "…" — re-capture on this browser, or pass force=true to restore anyway`. A forced restore adds the mismatch to `warnings`. `auth_list` does not resolve `session_id`, so a stale id and no running browser are both harmless. It returns `{snapshots:[{site,name,cookie_count,origin_count,user_agent,updated_at}],count,warning}` and never a cookie or storage value. Every auth result carries `warning`: `auth snapshots are stored unencrypted in the local state database (mode 0600) — treat that file as a credential; no result, error or log line prints a cookie or storage value`. The store does not encrypt, so that `warning` is the only signal that the file is a credential. The copyable login, save, close, restore, and verify recipe is in [page_act_example.md](../examples/page_act_example.md#11-save-and-restore-a-login). Source: `code://pkg/cli/pageops/snapshot.go#L20-L179`, `code://pkg/browsers/cookies.go#L247-L290`.

### Dialogs and HTTP auth

`dialog_accept` and `dialog_dismiss` return `{policy,prompt_text,count,dropped,last}`. `last` is `null` while `count` is `0`, and otherwise the newest recorded dialog or auth challenge. The accepted policy answers every later dialog at once, so nothing blocks the page. Set the policy before the step that opens the dialog, because the engine already answered an earlier one with the policy then in effect.

`http_auth` arms a per-page basic-auth credential from `value` (`user:password`, split at the first colon), and an empty `value` clears it. It applies to later 401 challenges, never echoes the credential, and returns `{http_auth:"armed"|"cleared"}` plus a `note` when Chromium already cached a credential for the origin. A cached credential means a new page is needed to test a different one. `http_auth` and `intercept_add` share the CDP `Fetch` domain: auth refuses while interception is armed, and interception refuses while auth is armed. Arm the credential before the navigation that answers a 401. Source: `code://pkg/cli/pageops/dialogs.go#L41-L134`, `code://pkg/cli/pageops/pageops.go#L76-L84`.

### Replay with what=network as the source

`replay` resends one captured network entry through a real Go HTTP client, outside the browser (`code://pkg/browsers/replay.go#L112-L235`). It bypasses CORS, carries the page's cookies for the target host in a private jar without writing anything back, and never touches the live page. Recipe: read `page_fetch` `what=network`, pick one entry, then replay it.

- `target` picks the entry: a `seq` number or a unique substring of the request URL, and an ambiguous substring is refused as `invalid_args` with every candidate listed as `seq=N METHOD url`. `method` overrides the captured method, `headers` replaces captured headers per name, and framing headers (`Host`, `Cookie`, `Connection`, `Accept-Encoding`, `Content-Length`, and kin) are never copied because the client owns them and the cookie jar owns `Cookie`.
- `query` rebuilds the URL before the request. A non-empty value replaces that parameter, and `""` removes it. Every other parameter keeps its captured value, and the fragment is kept. Parameter order can change, so `request.url` is the proof of the edit.
- `value` replaces the request body, but it is sent only when the `value` field itself is present. Without it the captured body is fetched from the browser, and a body the browser no longer retains reports `body_uncaptured: true` instead of being sent empty. `GET` and `HEAD` never send a body.
- Each URL, redirects included, is checked against the browser's scope allowlist before any request runs (`code://pkg/mcp/tools/act.go#L504-L516`, `code://pkg/browsers/replay.go#L453`). Up to 5 redirects are followed and reported in `chain`, and a 301, 302, or 303 switches the next hop to `GET` and drops the body.

The result is compact JSON inside `data` (`code://pkg/browsers/replay.go#L86-L110`). See [page_act_example.md](../examples/page_act_example.md) §7 for the live replay result. **CLI and IPC.** `browser page act [id] replay <target>` reaches the same engine and prints the same JSON. The first positional slot is the target, and `--target` is the flag fallback. `--method` overrides the method, `--header NAME=VALUE` is repeatable, `--max-bytes` caps the body (0 means the 256 KB ceiling), and the body rides `--value` (omit it to keep the captured body, pass `--value ''` for an empty one). A malformed header fails with `page act: invalid --header "X" — use NAME=VALUE, e.g. X-Bug-Bounty=your-handle`. IPC `page.act` carries `target`, `method`, `headers`, `value` with `value_set`, and `max_bytes`. `query` works on MCP, on the CLI with `--query NAME=VALUE`, and on IPC. An empty value or null removes a parameter. Without `value_set`, a non-empty `value` still sets the body. A missing target fails with `page act: replay requires target=<seq> or target=<url substring> from page_fetch what=network`, and a negative cap with `page act: replay max_bytes must be >= 0` (the IPC copies say `page.act:`). Fields: `status`, `headers` (final response headers as string arrays), `body` for UTF-8 text or `base64` for binary, `bytes`, `body_bytes` (the full body size measured before the cap), `truncated`, `body_redactions`, `body_uncaptured`, `ms`, `request` (first-hop method, url, and echoed headers), and `chain` (each hop's `status` and `url`, always present and `[]` for a single hop). `truncated` and `chain` are always present. The response body is capped at 256 KB on a rune boundary, and `truncated: true` marks the cut. Sensitive request and response header values (`Cookie`, `Set-Cookie`, `Authorization`, and kin) show `<redacted>` unless the page opted into secrets capture, and no surface exposes that switch. Text-body secrets are replaced with `<redacted>` and counted in `body_redactions`. Binary bodies are not scanned. The whole replay is bounded by the call's `timeout_ms` deadline (engine cap 30 seconds).

### Replay fuzz

`replay_fuzz` runs a bounded matrix of variants over one captured network entry, reusing `replay`'s `target` rule (`seq` or a unique URL substring). `points` names what to inject: each `{param,location}`, where `location` is `query`, `body`, `header`, or `cookie`, and a `body` point needs a captured request body. `payloads` is the value list. Both also accept a stringified JSON array.

| `strategy` | Requests | Plan |
| :--- | :--- | :--- |
| `sniper` (default) | points × payloads | One point at a time, every payload. |
| `pitchfork` | min(points, payloads) | The nth point with the nth payload. |
| `clusterbomb` | payloads ^ points | Every combination. |

The size is computed and refused before any request: at most 100 matrix requests, 20 points, 50 payloads, and 200 runes per payload. The baseline probe is extra. Runs execute 5 in flight with 10 s each. A refusal is `invalid_args`, for example live on 2026-10-27: `page_act: replay_fuzz: requests computed 243, over the 100 bound`.

One unmodified baseline request is sent twice. The payload is `{runs:[{seq,payload,point,status,ms,bytes,body_cut,body_redactions}],summary:{baseline:{status,bytes,cold_ms,ms},anomalies:[{seq,rule,why}]}}`. Every run body is redacted like `replay`. `cold_ms` is the first sample and `ms` the second warm sample that each run compares against. Three rules compare each run with the baseline: `status_changed` (a different status), `size_delta` (over 10% either way), and `time_delta` (over 3× either way). A `time_delta` inside the cold-to-warm spread is suppressed.

## Failure modes and recovery

An absent selector for `click`, `input`, `hover` or `select` fails immediately with type `element_not_found` and names the selector, and it does not consume `timeout_ms`. Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first: it always opens with `page_act: `, and the table shows the content after it. This tool can produce every type (`code://pkg/mcp/tools/envelope.go#L194-L227`). One live failure envelope, tested on 2026-10-26 against an ambiguous replay target:

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_act: replay: target \"news.ycombinator.com/news\" matches 2 network entries — pass a seq or a longer URL substring: seq=1 GET https://news.ycombinator.com/news; seq=2 GET https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb"}}
```

| Situation | Message at `error.message` | Recovery |
| :--- | :--- | :--- |
| `click` without a selector | `page_act: click requires selector (or a handle from page_fetch what=interactives)` | Add `selector` or a fresh `handle`. |
| `set_style` without a selector | `page_act: set_style requires selector (or a handle from page_fetch what=interactives)` | Add `selector` or a fresh `handle`. |
| `set_style` on an unmatched selector | `page_act: set_style: element "#wm-no-such-element": cannot find element` (`element_not_found`) | Fix the selector; it does not wait for `timeout_ms`. The envelope hint is `no element matched the selector — read what=dom or what=links and retry with an exact selector`. |
| `set_attr` without `name` | `page_act: set_attr requires name (the attribute to set, e.g. href)` (`invalid_args`) | Pass the attribute name. |
| `set_html` with an unknown mode | `page_act: set_html: unknown mode "replace" (must be inner, outer or append)` (`invalid_args`) | Use `inner`, `outer`, or `append`. |
| `fill_form` without a shape | `page_act: fill_form: missing required argument — pass fields (selector to value) or values (name to value)` (`invalid_args`) | Send `fields`, or `selector` plus `values`. |
| `fill_form` with both shapes | `page_act: fill_form: fields and values are mutually exclusive; pass one` (`invalid_args`) | Drop one of them. |
| `fill_form` `values` without a form | `page_act: fill_form: values requires a selector (the form whose fields are resolved by name)` (`invalid_args`) | Add the form `selector`. |
| `fill_form` unknown submit | `page_act: fill_form: invalid arguments: unknown submit "post" (must be requestSubmit, click or none)` (`invalid_args`) | Use `requestSubmit`, `click`, or `none`. |
| `fill_form` form selector matches nothing | `page_act: fill_form: element "#wm-no-form": cannot find element` (`element_not_found`) | Fix the form selector; a missing field is `skipped`, not an error. |
| `click_at` without `x`/`y` | `page_act: click_at requires x and y coordinates (e.g. {"action":"click_at","x":100,"y":200})` | Send both fields. |
| Bad key name | `page_act: press_key: unknown key name "Banana"` | Use a listed key. |
| `eval` without `value` | `page_act: eval requires value (JavaScript function source, e.g. "() => document.title")` | Pass an arrow function. |
| Bad JavaScript | `page_act: evaluate js: eval js error: SyntaxError: Unexpected token ')' <nil>` | Fix the syntax. |
| Stale handle after reload | `page_act: handle "gzlaqy9xe-b10" is stale — element no longer in the document (re-run page_fetch what=interactives to re-mint handles): {-32000 Node with given id does not belong to the document }` | Re-read `interactives`. |
| Bad handle format | `page_act: invalid handle "x" (expected g<document>-b<backendNodeId> minted by page_fetch what=interactives\|dom\|links)` | Copy a handle exactly. |
| Bad device or viewport input | `page_act: unknown device "Nokia3310" (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)`, `page_act: emulate requires a device`, `page_act: set_viewport requires a viewport`, or `page_act: viewport width and height must be positive` | Use a real preset or `clear`, and send positive width and height. |
| Unknown session | `page_act: session "page-999" not found` | Use `page_list` or `fetch_page`. |
| Unknown action | `page_act: unknown action="konami" (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, set_style, set_attr, set_html, fill_form, encode, scroll, select, wait, set_viewport, emulate, clear_logs, sinks_install, sinks_clear, replay, inject_canary, postmessage_install, postmessage_clear, post_message, wait_open, intercept_add, intercept_list, intercept_clear, auth_save, auth_restore, auth_list, replay_fuzz, poc_bundle, dialog_accept, dialog_dismiss, http_auth, encode)` | Use a listed action. |
| `encode` with an unknown context | `page_act: encode: unknown context "html" (must be one of attr-double, attr-single, attr-unquoted, css-value, html-text, js-string, js-template, url-path, url-query)` (`invalid_args`) | Use a listed context. |
| `encode` with a variant the context does not offer | `page_act: encode: variant "js" is not available for context "attr-double" (must be one of raw, entity, url, html5-attr)` (`invalid_args`) | Drop `variants` or name a label the context offers. |
| `upload_file` without paths | `page_act: upload_file: no file paths provided` | Pass `paths` or `value`. |
| `replay_fuzz` without `target` | `page_act: replay_fuzz requires target=<seq> or target=<url substring> from page_fetch what=network` (`invalid_args`) | Read `what=network` and set `target`. |
| `replay_fuzz` over a bound | `page_act: replay_fuzz: requests computed 243, over the 100 bound` (`invalid_args`) | Lower the payload or point count, or use `sniper`. |
| `replay_fuzz` unknown strategy | `page_act: replay_fuzz: unknown strategy "spray" — use sniper, pitchfork or clusterbomb` (`invalid_args`) | Use a listed strategy. |
| `replay` without `target` | `page_act: replay requires target=<seq> or target=<url substring> from page_fetch what=network` (`invalid_args`) | Read `what=network` and set `target`. |
| `replay` with an unknown seq | `page_act: replay: no network entry with seq %d (read page_fetch what=network for the current seq values)` | Re-read `network` for live `seq` values. |
| `replay` with an unmatched substring | `page_act: replay: no network entry matches target %q (target is a seq number or a substring of the request URL; read page_fetch what=network first)` | Pass a seq or a different substring. |
| `replay` of a non-http entry | `page_act: replay: captured URL %s uses scheme %q — only http and https requests can be replayed` | Replay only http and https entries. |
| `replay` network failure | `page_act: replay: %s %s: <error> (the host is unreachable or rejected the request — verify the URL, DNS and TLS trust)` | Verify the URL, DNS, and TLS trust. |
| Replay outside the scope | `page_act: replay: <url> refused: <scope reason>` | Pick an entry inside the browser's scope allowlist. |
| `inject_canary` before `sinks_install` | `page_act: inject_canary needs the sink tracer — run action "sinks_install" first` (`unknown`) | Run `sinks_install`, then retry. |
| `post_message` without `value` | `page_act: post_message requires value (the message payload, e.g. {"t":"wm"})` | Add `value`. |
| `post_message` bad target | `page_act: post_message target "iframe#missing": cannot find element` or `page_act: post_message target "div#x" is not a frame — pass "top", "self", "parent", or a CSS selector for an <iframe> or <frame>` | Pass a CSS selector for a live iframe, or `top`. |
| `wait_open` timeout | The message names the opener session and the wait budget. | Click the link first, then raise `timeout_ms`. |
| `auth_save` or `auth_restore` without `site`/`name` | `page_act: auth_save: auth: empty name — pass a snapshot name such as main` | Pass `site` as a bare host and a non-empty `name`. |
| `auth_restore` user-agent mismatch | `page_act: auth_restore: stored user agent "…" does not match the live page user agent "…" — re-capture on this browser, or pass force=true to restore anyway` | Re-save on this browser, or pass `force:true`. |
| `intercept_add` without `rules` | `page_act: intercept_add requires rules (e.g. [{"url_pattern":"*api*","action":"block"}])` (`unknown`) | Send at least one rule. |
| Bad intercept action or status | `page_act: intercept add: intercept: rule "r1" has action "deny" (must be block, allow, rewrite or fulfill)` or `… has status 700 (must be 100-599)` (`unknown`) | Fix the rule. Status is 100-599. |
| Intercept rule cap | `page_act: intercept add: intercept: page already holds 50 rules; adding 1 more exceeds the 50-rule limit (clear rules first)` (`unknown`) | Reuse an id to replace a rule, or run `intercept_clear`. |
| `http_auth` while interception is armed | `page_act: http auth: page page-N has request interception armed, which owns the CDP Fetch domain — call intercept_clear, then set the credential` (`unknown`) | Run `intercept_clear`, then arm the credential. |
| `intercept_add` while HTTP auth is armed | `page_act: intercept: page page-N has HTTP basic-auth credentials armed, which owns the CDP Fetch domain — clear the credentials first` (`unknown`) | Run `http_auth` with an empty `value`, then add rules. |
| `http_auth` a second time | `page_act: http auth: credentials are already armed on page page-N — clear them first to replace them` (`unknown`) | Clear with an empty `value`, then arm the new credential on a fresh page. |
| Closed page | `page_act: page session is closed` | Open a new session. |
| Page locked by a running action | `page_act: <action>: page page-N is still busy after 0s — a previous operation on this page did not return; retry, or close it with session_close` | Run `session_close` on that session, then `fetch_page` a fresh page. |
| Page renderer stopped answering | `page_act: <action>: page page-N is unusable — the tab's renderer stopped answering CDP calls; close it with session_close` | Run `session_close` on that session, then `fetch_page` a fresh page. |
| Page closed after a forced lock recovery | `page_act: <action>: page page-N did not answer the recovery probe after the page watchdog forced its lock open — page session is closed: the renderer never answered again; close it with session_close` | Run `session_close` on that session, then `fetch_page` a fresh page. |
| `select` with an unknown value | `MCP error -32001: Request timed out` at the transport, not the envelope | Use an exact option `value`. A live test wedged the whole server until its browser was terminated. Treat as a defect. |

On the CLI, run `browser page close <id>` in place of `session_close`.

## When not to use it

Do not use `page_act` to open a URL in a new tab, use `fetch_page`. Do not use it to read content, use `page_fetch`. Do not use `select` with an unverified value — read the option list with `eval` first. Do not use `drag_and_drop` for native HTML5 drag-and-drop. Do not use `eval` to return a DOM node, it returns `{}`. Do not omit `timeout_ms` on an `ms` wait. Do not use `replay` instead of `navigate`: a replay sends a fresh HTTP request outside the browser and cannot set cookies, storage, or page state. Do not leave interception armed after the step, and never arm it on a browser whose proxy needs credentials, because both own the CDP `Fetch` domain. Do not arm `sinks` or inject a canary for normal browsing, because instrumentation changes page behavior. Do not arm `postmessage` on a page you do not need to inspect, because every install, read, and send walks up to 20 frames.

## Tested calls

- 2026-10-25 — input, type, press_key, select, upload, clicks, hover, scroll, keyboard combo, drag, navigate, back/forward/reload, eval, clear_logs, viewport, emulate, and miss-path errors.
- 2026-10-26 — replay ambiguity failure, submit-click with a state delta, and `wait_open` returning `{session_id,url,opener}` after a `target="_blank"` click, where closing the opener closed the popup. Also `sinks_install`, a payload, and `what=sinks` on `https://www.postgresql.org/`: the arm survived a full navigation and `sinks_clear` returned `{"cleared":N}`.
- 2026-10-26 — `postmessage_install`, `post_message` to `top` and to an iframe, and `page_fetch what=postmessage` on https://www.python.org/. The read returned both messages with `origin` and `frames[].probed:true`.
- 2026-10-27 — source-verified against the shipped code and its tests: `intercept_add`/`list`/`clear` and the `intercepted` network tag (commit `037138c`), and replay over CLI and IPC (commit `ee64f65`). The running daemon predated `037138c` and rejected `intercept_add`, so these two paths have no live call yet.
- 2026-10-27 — `replay_fuzz` live on `https://www.debian.org/`: sniper over one `query` point with two payloads returned two runs plus the baseline and `anomalies:[]`, and a five-point `clusterbomb` with three payloads was refused before any request with `page_act: replay_fuzz: requests computed 243, over the 100 bound`. The live MCP enum listed all 36 actions then, `intercept_*` included, and the CLI/IPC vocabularies carry the same `intercept_*` names. Humanized timing and `raw` were read from the live schema.
- 2026-10-27 — `auth_save`, `auth_list`, and `auth_restore` on `www.saucedemo.com`: the snapshot restored a login into a new browser and a new identity, `auth_list` answered with no live browser and a stale `session_id`, and a host-only `session-username` cookie stayed scoped to `www.saucedemo.com`.
- 2026-10-28 — live on `https://www.freebsd.org/?probe=one`. `replay` with target `1` and `query:{"probe":"two"}` returned status 200 and `request.url` `https://www.freebsd.org/?probe=two`. `query:{"probe":""}` returned `https://www.freebsd.org/`. `replay_fuzz` over the same entry returned `summary.baseline` `{"status":200,"bytes":22060,"cold_ms":30,"ms":44}` with `anomalies:[]`.
- 2026-10-28 — source-verified `dialog_accept`, `dialog_dismiss`, and `http_auth` against the action table (`code://pkg/cli/pageops/actions.go#L55-L57`) and the dispatch list (`code://pkg/cli/pageops/pageops.go#L73`). The table now holds 39 rows and this reference documents all 39. `clear_logs` returns the `dialogs` and CSP `violations` counts (`code://pkg/cli/pageops/websocket.go#L24-L47`), and `last` is `null` on an empty ring (`code://pkg/cli/pageops/dialogs.go#L155-L163`).
- 2026-10-30 — `set_style` live on `https://www.php.net/manual/en/language.basic-syntax.php` and `https://htmx.org/docs/`. A two-call merge probe left a `background` from the first call in place after two later calls, and a `background-image:url(...)` payload produced a `favicon.ico?wmsetstyle=1` network hit. An empty `value` returned `cleared:true`, an invalid declaration came back in `rejected` instead of being swallowed, `!important` was handled without landing in the value, and a `url('data:…;base64,…')` value survived the semicolon. The handle path and the selector path both wrote, an unmatched selector failed as `element_not_found` with `page_act: set_style: element "#wm-no-such-element": cannot find element`, and `browser page act set_style …` reached the same engine. The table now holds 40 rows.
- 2026-10-30 — source-verified and unit-tested the page-free `encode` action (`pkg/cli/pageops/encode_test.go`) across all nine contexts with `<script>alert(1)</script>` and an `& " ' space` payload. The `attr-double` `entity` variant contains no bare `"`, the `js` variant escapes both quotes and the angle brackets, and both `url` variants decode back to the payload. The action table now holds 41 rows. Not yet live-called on a running server, so the header date stays 2026-10-30.
- 2026-10-30 — added `set_attr` and `set_html` beside `set_style`, source-verified against the action table and dispatch list. The mode mapping is unit-tested in `pkg/cli/pageops/inject_test.go`; the live server was not restarted, so no live call is recorded here. The action table now holds 43 rows.
- 2026-10-30 — added `fill_form` beside `select`. The value coercion per control type is unit-tested in `pkg/cli/pageops/forms_test.go`; the field resolver reuses the `cssPath` builder `what=forms` reports. The live server was not restarted, so no live call is recorded here. The action table now holds 44 rows.

## Related tools

- `fetch_page` — opens the session and returns `session_id`.
- `page_fetch` — reads `interactives`, `dom`, `links`, `network`, `console`, and more.
- `page_list`, `session_list`, `session_close` — list and close pages.
- `browser_create`, `browser_list`, `browser_close` — manage the owning browser.

Copyable calls with expected output are in [page_act_example.md](../examples/page_act_example.md).
