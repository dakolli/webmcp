> Tool: `page_act` · Server: web-mcp · Surface: MCP · Tested: 2026-10-27

# page_act

Interact with a live page that `fetch_page` opened.

## What it does

`page_act` performs one action in an open session. The `session_id` picks the tab and the `action` field picks the operation. Actions cover clicking, typing, keys, mouse coordinates, drag, upload, JavaScript, navigation, waiting, waiting for a popup, emulation, log clearing, replaying and fuzzing a captured network request, in-flight request interception, sink control, canary injection, postMessage capture and sending, and auth snapshots.

The confirmation rides the result envelope: the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the `{"ok":false,"error":…}` shape. The payload is one short prose string plus a state delta line. `eval`, `replay`, and `replay_fuzz` return compact JSON instead.

## Inputs

The schema is `code://pkg/mcp/tools/act.go#L125`. Only `session_id` and `action` are required, and a session held outside the declared `owner` is refused as `not_found`. Actions ignore fields they do not use.

| Name | Type | Required by | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | always | — | Tab from `fetch_page`, for example `page-3`. |
| `action` | string | always | — | One of the actions below. |
| `browser_id` | string | no | — | Optional. Resolve the session inside this browser only, for example `browser-2`. Omit it when exactly one live browser holds the id. |
| `owner` | string | no | — | Optional. Restrict the lookup to browsers with this owner label, 1 to 64 characters of `A-Z`, `a-z`, `0-9`, `.`, `_`, `-`. |
| `selector` | string | `click`, `input`, `type`, `hover`, `select`, `upload_file` | — | CSS selector or XPath. Optional for `press_key` and the drag source. |
| `handle` | string | alternative to `selector` | — | Handle from `page_fetch` `what=interactives\|dom\|links`. Wins over `selector`. |
| `value` | string | `eval`, `navigate`, `input`, `type`, `select`, `replay`, `post_message` | — | Payload text, JavaScript source, URL, option value, or message payload. For `replay` it replaces the request body and is sent only when the field is present. For `inject_canary` it is a custom canary. Omit it to mint `wm-canary-<8 hex>`. |
| `targets` | array | `inject_canary` (optional) | all four | Injection points: `query`, `hash`, `postmessage`, `name`. Each entry may be a comma-separated list. |
| `target` | string | `replay`, `replay_fuzz` | — | Captured network entry: a `seq` number or a substring of the request URL. |
| `method` | string | `replay` (optional) | — | HTTP method override, for example `PUT`. Omit to keep the captured method. |
| `headers` | object | `replay` (optional) | — | `replay` only: each named header replaces the captured value on the resent request, others keep theirs. It does not set headers on page requests; use `browser_create` `headers` for browser-wide request headers or `intercept_add` with `action:"rewrite"` for a per-request rewrite. |
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
| `paths` | array | `upload_file` | — | Absolute local file paths. `value` is the single-path fallback. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. For `wait` `ms` mode it is the sleep time. |
| `wait_mode` | string | no | `ms` | `load`, `idle`, `stable`, `interactable`, or `ms`. |
| `viewport` | object | `set_viewport` | — | `{"width":<int>,"height":<int>}` in CSS pixels. Both must be positive. |
| `device` | string | `emulate` | — | go-rod preset name, for example `IPhoneX`. `clear` resets. |
| `rules` | array | `intercept_add` | — | Rules, each `{id?,url_pattern?,method?,action,headers?,status?,body?,delay_ms?}`. At most 50 per page across calls, and `action` is required. See [Request interception](#request-interception). |
| `site` | string | `auth_save`, `auth_restore` (optional for `auth_list`) | — | Host the snapshot is scoped to, for example `www.saucedemo.com`. Accepts a bare host or a URL. For `auth_list` it is a filter. |
| `name` | string | `auth_save`, `auth_restore` | — | Snapshot name, unique per site. A second save with the same site and name updates it. |
| `force` | boolean | no | `false` | `auth_restore` only. Restore across a user-agent mismatch. Without it the restore is refused and both agents are named. |

`value` is required for `eval`, `navigate`, and `post_message`, and the server rejects an empty value for the first two before it touches the page. For `upload_file`, pass `paths` (a single `value` path also works).

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
| `clear_logs` | — | Empties the network, console, and websocket rings. | `{"network":132,"console":1,"websocket":0}` | No. |
| `sinks_install` | — | Arms the source-to-sink tracer. Idempotent. | `{"installed":true}` | No. |
| `sinks_clear` | — | Empties the tracer ring and resets `dropped`. | `{"cleared":N}` | No. |
| `replay` | `target` | Resends one captured network entry. See below. | Compact JSON result. | No. |
| `replay_fuzz` | `target`, `points`, `payloads` | Runs a bounded matrix of variants over one captured entry. See [Replay fuzz](#replay-fuzz). | Compact JSON `{runs,summary}`. | No. |
| `inject_canary` | `targets` (optional) | Injects a canary into client sources and reads the sink hits. Run `sinks_install` first. | Compact JSON: `{canary, injected, hits, installed}`. | No. |
| `postmessage_install` | — | Arms the postMessage recorder on every reachable window. | `{"installed":true}` | No. |
| `postmessage_clear` | — | Empties every recorder ring and resets `dropped`. | `{"cleared":N}` | No. |
| `post_message` | `value` | Sends a message to `top` or the iframe in `target_selector`. Arms the recorder first. | `{origin,target}` | No. |
| `wait_open` | — | Blocks until this page opens a popup or tab. | Compact JSON `{session_id,url,opener}`. | No. |
| `intercept_add` | `rules` | Arms the in-flight interceptor and adds or replaces rules. | Compact JSON `{rules,count,active}`. | No. |
| `intercept_list` | — | Lists the page's rules, with sensitive values redacted. | Compact JSON `{rules,count,active}`. | No. |
| `intercept_clear` | — | Removes every rule and disables interception. | Compact JSON `{rules_removed,active:false}`. | No. |
| `auth_save` | `site`, `name` | Captures the page's cookies for `site` plus local storage, session storage, and IndexedDB names. | Compact JSON counts plus `warning`. | No. |
| `auth_restore` | `site`, `name` | Writes the snapshot into the page's browser context: cookies, then storage for the live origin. | Compact JSON counts plus `warnings` and `warning`. | No. |
| `auth_list` | `site` (optional) | Lists saved snapshots. Needs no live session. | Compact JSON `{snapshots,count,warning}`. | No. |

A non-positive `click_count` becomes `1`. An unknown `button` is rejected before the click. The server rejects a missing `x` or `y` key for `click_at` and `hover_at`, and an explicit `0` is valid.

## Returns

The confirmation and the state delta arrive as one JSON string inside `data`. A live form submit on 2026-10-26 returned `{"ok":true,"data":"clicked selector=\"form button\"\n→ url=https://httpbin.org/post title=\"httpbin.org/post\""}`. The delta starts on a new line and lists only changed fields. `navigate` never appends one, and `back` and `forward` do. `eval`, `clear_logs`, `replay`, `replay_fuzz`, the three `intercept_*` actions, and `wait_open` return compact JSON in `data` instead of prose. A DOM node from `eval` comes back as `{}`. `click`, `click_at`, `press_key`, `upload_file`, `eval`, and `select` poll up to 900 ms for a navigation.

## Behavior and limits

### Handles

A handle has the form `g<document>-b<backendNodeId>`, for example `gcq7sm3ng-b165`. Get handles from `page_fetch` with `what=interactives`, `dom`, or `links`. A handle wins over `selector`. A live `input` with a text-field handle and a password-field selector wrote to the text field, and the password field stayed empty. The `g` token names the document that minted the handle: any navigation, reload, redirect, or document replacement changes it, and the old handle then fails as stale, so re-read `interactives` after every navigation. An `interactives` entry from a same-process child iframe works the same way: its `frame_url` names the frame, the backend node id is renderer-global, and its `handle` still acts through `page_act`.

### Element actions

`click`, `click_at`, `input`, `hover`, `hover_at`, and `press_key` are humanized by default: the pointer steps to the target with small jitter, and a randomized 100-500 ms pause precedes and follows the action. Measured on vuejs.org on 2026-10-27, the default mean was 1.908 s and `--raw` 0.946 s. Any one hatch disables it: per-call `raw:true`, `WEB_MCP_RAW_ACTIONS=1` in the server environment, or a server started with `browsers.WithRawActions(true)`. The pause is skipped when it does not fit the remaining budget, never clipped, and a budget that expires first returns `timeout` with the action unperformed.

`input` replaces the field contents: it selects all text first, then types `value`. `type` does not clear, it focuses the element and inserts at the caret. A live `input` of `555-0100` then a `type` of `" EXT"` left the field at `555-0100 EXT`, confirmed by `eval`. Some controls reject synthetic text — a live `input` on `<input type="time">` left the value empty, so use `eval` to set those values. `click` and `wait` `interactable` check interactability, and `input` and `type` do not, so a hidden element can still receive text.

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

Network, console, and websocket capture is cumulative from page creation. `clear_logs` empties all three rings, resets `dropped`, and returns `{"network":N,"console":M,"websocket":K}`. The `seq` counter does not reset. The recipe is `clear_logs`, then one action, then `page_fetch` `network`, `console`, or `websocket`.

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

### Replay with what=network as the source

`replay` resends one captured network entry through a real Go HTTP client, outside the browser (`code://pkg/browsers/replay.go#L112-L235`). It bypasses CORS, carries the page's cookies for the target host in a private jar without writing anything back, and never touches the live page. Recipe: read `page_fetch` `what=network`, pick one entry, then replay it.

- `target` picks the entry: a `seq` number or a unique substring of the request URL, and an ambiguous substring is refused as `invalid_args` with every candidate listed as `seq=N METHOD url`. `method` overrides the captured method, `headers` replaces captured headers per name, and framing headers (`Host`, `Cookie`, `Connection`, `Accept-Encoding`, `Content-Length`, and kin) are never copied because the client owns them and the cookie jar owns `Cookie`.
- `value` replaces the request body, but it is sent only when the `value` field itself is present. Without it the captured body is fetched from the browser, and a body the browser no longer retains reports `body_uncaptured: true` instead of being sent empty. `GET` and `HEAD` never send a body.
- Each URL, redirects included, is checked against the browser's scope allowlist before any request runs (`code://pkg/mcp/tools/act.go#L504-L516`, `code://pkg/browsers/replay.go#L453`). Up to 5 redirects are followed and reported in `chain`, and a 301, 302, or 303 switches the next hop to `GET` and drops the body.

The result is compact JSON inside `data` (`code://pkg/browsers/replay.go#L86-L110`). See [page_act_example.md](../examples/page_act_example.md) §7 for the live replay result. **CLI and IPC.** `browser page act [id] replay <target>` reaches the same engine and prints the same JSON. The first positional slot is the target, and `--target` is the flag fallback. `--method` overrides the method, `--header NAME=VALUE` is repeatable, `--max-bytes` caps the body (0 means the 256 KB ceiling), and the body rides `--value` (omit it to keep the captured body, pass `--value ''` for an empty one). A malformed header fails with `page act: invalid --header "X" — use NAME=VALUE, e.g. X-Bug-Bounty=your-handle`. IPC `page.act` carries `target`, `method`, `headers`, `value` with `value_set`, and `max_bytes`. Without `value_set`, a non-empty `value` still sets the body. A missing target fails with `page act: replay requires target=<seq> or target=<url substring> from page_fetch what=network`, and a negative cap with `page act: replay max_bytes must be >= 0` (the IPC copies say `page.act:`). Fields: `status`, `headers` (final response headers as string arrays), `body` for UTF-8 text or `base64` for binary, `bytes`, `truncated`, `body_redactions`, `body_uncaptured`, `ms`, `request` (first-hop method, url, and echoed headers), and `chain` (each hop's `status` and `url`, always present and `[]` for a single hop). `truncated` and `chain` are always present. The response body is capped at 256 KB on a rune boundary, and `truncated: true` marks the cut. Sensitive request and response header values (`Cookie`, `Set-Cookie`, `Authorization`, and kin) show `<redacted>` unless the page opted into secrets capture, and no surface exposes that switch. Text-body secrets are replaced with `<redacted>` and counted in `body_redactions`. Binary bodies are not scanned. The whole replay is bounded by the call's `timeout_ms` deadline (engine cap 30 seconds).

### Replay fuzz

`replay_fuzz` runs a bounded matrix of variants over one captured network entry, reusing `replay`'s `target` rule (`seq` or a unique URL substring). `points` names what to inject: each `{param,location}`, where `location` is `query`, `body`, `header`, or `cookie`, and a `body` point needs a captured request body. `payloads` is the value list. Both also accept a stringified JSON array.

| `strategy` | Requests | Plan |
| :--- | :--- | :--- |
| `sniper` (default) | points × payloads | One point at a time, every payload. |
| `pitchfork` | min(points, payloads) | The nth point with the nth payload. |
| `clusterbomb` | payloads ^ points | Every combination. |

The size is computed and refused before any request: at most 100 matrix requests, 20 points, 50 payloads, and 200 runes per payload. The baseline probe is extra. Runs execute 5 in flight with 10 s each. A refusal is `invalid_args`, for example live on 2026-10-27: `page_act: replay_fuzz: requests computed 243, over the 100 bound`.

One unmodified baseline request is sent first. The payload is `{runs:[{seq,payload,point,status,ms,bytes,body_cut,body_redactions}],summary:{baseline:{status,bytes,ms},anomalies:[{seq,rule,why}]}}`, and every run body is redacted like `replay`. Three rules compare each run with the baseline: `status_changed` (a different status), `size_delta` (over 10% either way), and `time_delta` (over 3× either way).

## Failure modes and recovery

An absent selector for `click`, `input`, `hover` or `select` fails immediately with type `element_not_found` and names the selector, and it does not consume `timeout_ms`. Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first: it always opens with `page_act: `, and the table shows the content after it. This tool can produce every type (`code://pkg/mcp/tools/envelope.go#L194-L227`). One live failure envelope, tested on 2026-10-26 against an ambiguous replay target:

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_act: replay: target \"news.ycombinator.com/news\" matches 2 network entries — pass a seq or a longer URL substring: seq=1 GET https://news.ycombinator.com/news; seq=2 GET https://news.ycombinator.com/news.css?EieZrD3Ae1qf3cIQQaRb"}}
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
| Bad device or viewport input | `page_act: unknown device "Nokia3310" (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)`, `page_act: emulate requires a device`, `page_act: set_viewport requires a viewport`, or `page_act: viewport width and height must be positive` | Use a real preset or `clear`, and send positive width and height. |
| Unknown session | `page_act: session "page-999" not found` | Use `page_list` or `fetch_page`. |
| Unknown action | `page_act: unknown action="konami" (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs, sinks_install, sinks_clear, replay, inject_canary, postmessage_install, postmessage_clear, post_message, wait_open, intercept_add, intercept_list, intercept_clear, auth_save, auth_restore, auth_list, replay_fuzz)` | Use a listed action. |
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
| Closed page | `page_act: page session is closed` | Open a new session. |
| `select` with an unknown value | `MCP error -32001: Request timed out` at the transport, not the envelope | Use an exact option `value`. A live test wedged the whole server until its browser was terminated. Treat as a defect. |

## When not to use it

Do not use `page_act` to open a URL in a new tab, use `fetch_page`. Do not use it to read content, use `page_fetch`. Do not use `select` with an unverified value — read the option list with `eval` first. Do not use `drag_and_drop` for native HTML5 drag-and-drop. Do not use `eval` to return a DOM node, it returns `{}`. Do not omit `timeout_ms` on an `ms` wait. Do not use `replay` instead of `navigate`: a replay sends a fresh HTTP request outside the browser and cannot set cookies, storage, or page state. Do not leave interception armed after the step, and never arm it on a browser whose proxy needs credentials, because both own the CDP `Fetch` domain. Do not arm `sinks` or inject a canary for normal browsing, because instrumentation changes page behavior. Do not arm `postmessage` on a page you do not need to inspect, because every install, read, and send walks up to 20 frames.

## Tested calls

- 2026-10-25 — input, type, press_key, select, upload, clicks, hover, scroll, keyboard combo, drag, navigate, back/forward/reload, eval, clear_logs, viewport, emulate, and miss-path errors.
- 2026-10-26 — replay ambiguity failure, submit-click with a state delta, and `wait_open` returning `{session_id,url,opener}` after a `target="_blank"` click, where closing the opener closed the popup. Also `sinks_install`, a payload, and `what=sinks` on `https://www.postgresql.org/`: the arm survived a full navigation and `sinks_clear` returned `{"cleared":N}`.
- 2026-10-26 — `postmessage_install`, `post_message` to `top` and to an iframe, and `page_fetch what=postmessage` on https://www.python.org/. The read returned both messages with `origin` and `frames[].probed:true`.
- 2026-10-27 — source-verified against the shipped code and its tests: `intercept_add`/`list`/`clear` and the `intercepted` network tag (commit `037138c`), and replay over CLI and IPC (commit `ee64f65`). The running daemon predated `037138c` and rejected `intercept_add`, so these two paths have no live call yet.
- 2026-10-27 — `replay_fuzz` live on `https://www.debian.org/`: sniper over one `query` point with two payloads returned two runs plus the baseline and `anomalies:[]`, and a five-point `clusterbomb` with three payloads was refused before any request with `page_act: replay_fuzz: requests computed 243, over the 100 bound`. The live MCP enum lists all 35 actions, `intercept_*` included, and the CLI/IPC vocabularies carry the same `intercept_*` names. Humanized timing and `raw` were read from the live schema.
- 2026-10-27 — `auth_save`, `auth_list`, and `auth_restore` on `www.saucedemo.com`: the snapshot restored a login into a new browser and a new identity, `auth_list` answered with no live browser and a stale `session_id`, and a host-only `session-username` cookie stayed scoped to `www.saucedemo.com`.

## Related tools

- `fetch_page` — opens the session and returns `session_id`.
- `page_fetch` — reads `interactives`, `dom`, `links`, `network`, `console`, and more.
- `page_list`, `session_list`, `session_close` — list and close pages.
- `browser_create`, `browser_list`, `browser_close` — manage the owning browser.

Copyable calls with expected output are in [page_act_example.md](../examples/page_act_example.md).
