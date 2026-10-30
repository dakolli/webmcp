---
name: web-mcp
description: Drive a live, stateful Chromium browser over the web-mcp MCP server. Use for real browsing, page inspection (DOM, accessibility, network, console, article, marks), form filling, and UI testing. The MCP tool description is primary. This skill carries the exact shapes, limits, and errors.
---

# web-mcp — MCP browser skill

This skill covers the 14 `web-mcp` MCP tools: 9 browser tools and 5 Caido tools. It does not replace the tool descriptions.
This file is the single entry. It carries the cross-cutting rules. Tool detail lives in the references.

## Read order

1. Read the tool description and input schema first. They are authoritative.
2. Read `references/tools/<tool>.md` when the description is unclear, incomplete, or a call failed.
3. Read `references/examples/<tool>_example.md` when tool calling itself fails.

Do not invent parameters. The Go source in `pkg/mcp/tools/` is the schema source of truth.

## Install

Install or refresh this skill with `browser skill <harness|path|.>`. With no argument it prints the presets and writes nothing. `AGENTS.md` is never installed.

## Before you call

- Create one browser per agent. Pass a unique `owner` label such as `"owner": "research-a"`. The label joins the create key, so the same owner and options reuse one browser.
- Browsers are ephemeral by default. Pass `persist:true` with a `profile` name only when logins must survive a restart.
- Close your own browser with `browser_close` when the task ends. Never close another agent's browser.

## Prefer a headless browser

- Create browsers with `"headless": true`. Most reads and actions do not need a window.
- Go headful only when a site blocks automation in headless mode, or when the task needs a real display for a visual check, window placement, or an OS dialog.

## Mental model

- **Browser** (`browser_id`, for example `browser-1`). One Chromium process and one profile.
- **Session** (`session_id`, for example `page-1`). One live tab inside a browser.
- **Context** (`identity`). One incognito cookie jar inside a browser. `default` is that
  browser's own jar. Every name dies with its browser.
- `browser-1` is the first id minted. It is not a shared singleton.
- The ephemeral profile lives under `profiles/.ephemeral/` in the config dir and is removed on close.
- A persistent profile needs `persist:true` and a `profile` name.
- A bare `profile` name resolves under the config dir. A relative path with a separator is rejected.
- A browser idle for 30 minutes closes automatically. Set `browser_ttl` in `config.json` in the config dir to change it. `browser_list` shows `idle_for` and `ttl`.
- Idle sessions expire after 15 minutes. A dead Chromium is relaunched under the same `browser_id`.
- Page ids are unique inside one browser only. Two live browsers can both have `page-1`.
- Pages get a coherent default identity: the User-Agent's Chrome major matches the launched Chromium, `Accept-Language` is `en-US,en;q=0.9`, and the `Sec-CH-UA` hints agree with both. An explicit `user_agent` wins, and a `device` preset's own UA wins while that preset is applied.
- With `stealth` on (the default), `navigator.webdriver` is `false` and `window.chrome` exists.

## Coverage

| Tool | Reference | Example | State |
| :--- | :--- | :--- | :--- |
| `browser_create` | [browser_create.md](references/tools/browser_create.md) | [browser_create_example.md](references/examples/browser_create_example.md) | tested |
| `browser_list` | [browser_list.md](references/tools/browser_list.md) | [browser_list_example.md](references/examples/browser_list_example.md) | tested |
| `browser_close` | [browser_close.md](references/tools/browser_close.md) | [browser_close_example.md](references/examples/browser_close_example.md) | tested |
| `fetch_page` | [fetch_page.md](references/tools/fetch_page.md) | [fetch_page_example.md](references/examples/fetch_page_example.md) | tested |
| `page_list` | [page_list.md](references/tools/page_list.md) | [page_list_example.md](references/examples/page_list_example.md) | tested |
| `session_list` | [session_list.md](references/tools/session_list.md) | [session_list_example.md](references/examples/session_list_example.md) | tested |
| `session_close` | [session_close.md](references/tools/session_close.md) | [session_close_example.md](references/examples/session_close_example.md) | tested |
| `page_fetch` | [page_fetch.md](references/tools/page_fetch.md) | [page_fetch_example.md](references/examples/page_fetch_example.md) | tested |
| `page_act` | [page_act.md](references/tools/page_act.md) | [page_act_example.md](references/examples/page_act_example.md) | tested |
| `caido_status` | [caido_status.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_status.md) | [caido_status_example.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/examples/caido_status_example.md) | tested |
| `caido_sitemap` | [caido_sitemap.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_sitemap.md) | [caido_sitemap_example.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/examples/caido_sitemap_example.md) | tested |
| `caido_search` | [caido_search.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_search.md) | [caido_search_example.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/examples/caido_search_example.md) | tested |
| `caido_finding` | [caido_finding.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_finding.md) | [caido_finding_example.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/examples/caido_finding_example.md) | tested |
| `caido_replay` | [caido_replay.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_replay.md) | [caido_replay_example.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/examples/caido_replay_example.md) | tested |

## Cross-cutting rules

1. Call `browser_list` before you use a `browser_id`. Confirm the id and its `profile_dir`.
2. If your client does not show `persist` or `owner`, reconnect it before you create a browser. Reconnect it when a cached `page_fetch` `what` enum lacks `dialogs`, `har`, `mutations`, `postmessage`, `sinks`, `storage`, `tables`, `forms`, or `websocket`, because the live enum has 25 values. A cached client schema can also omit the new `browser_id` and `owner` inputs, so reconnect the client after a server build.
3. Never close a browser you did not create, and close your own by id when done. Never call `browser_close` with an empty `browser_id` in a shared session. Use the exact key `browser_id`. A key outside a tool's schema fails as `invalid_args`. The message quotes the key once and lists the valid keys sorted. So a misspelled `browser` key now fails, and `browser_close` no longer closes every browser on that typo. `page_fetch` and `page_act` take no `identity` argument. `fetch_page` alone accepts `identity`.
4. Use one browser per agent and task. Page ids are unique per browser, so two owners can both hold `page-1`. With one live browser a bare id resolves. With two or more live browsers a bare id returns `invalid_args`, and the message names every browser and owner.
5. `page_fetch`, `page_act`, and `session_close` take optional `browser_id` and `owner`. A session held outside the declared owner returns `not_found`, names the holder, and returns no content. Pass `browser_id` to cross on purpose.
6. `session_list` reads every live browser and identity. Use `page_list` with a required `browser_id` for one browser. Both report the live URL and title, refreshed after navigation and redirects.
7. An unknown `browser_id` can create a new browser. A typo spends a full Chromium process. Verify the id first.
8. `fetch_page` creates a new session on every call. Close sessions you no longer need.
9. `page_fetch` network, console, and websocket capture is cumulative from page creation. Call `page_act` `clear_logs` before the step you want to observe.
10. `page_act` `eval` needs a JavaScript function expression, for example `() => document.title`. A bare expression can fail.
11. `page_act` `select` matches the option `value`, not its text. An invalid value can block the server. Use a value you read with `eval`, or use `eval` to set the field.
12. `page_act` `drag_and_drop` uses mouse events. It does not fire native HTML5 drag events. Use `eval` for HTML5 drag handlers.
13. `page_act` `wait` defaults to `wait_mode:"ms"`. If you omit `timeout_ms`, it sleeps for 30 seconds.
14. Prefer `article` over `text` for prose pages. Prefer `interactives` or `a11y` before you act.
15. `browser_close` is not idempotent. A second close returns a not-found error. Check `browser_list` first.
16. `page_act` `inject_canary` proves sink reachability. Run `sinks_install` first or it returns the guard error. It injects a `wm-canary-<8 hex>` value into `query`, `hash`, `postmessage`, and `name`, capped at 20 frames, and returns the `hits` whose value contains the canary.
17. `page_act` `postmessage_install` arms a `message` recorder on every reachable window and returns `{"installed":true}`. Read it with `page_fetch` `what=postmessage`. Send with `post_message`, which takes `value`, `target_selector` (`top`, `self`, `parent`, or a CSS frame selector), and `origin` (default `*`). Clear with `postmessage_clear` or `target=clear`. The ring holds 100 messages per window and clips `data` at 500 runes. The read merges every window into one `messages` list, each entry carries `source_kind` (`window`, `frame`, `worker`, or `unknown`) as the only frame hint, and `frames` lists probed URLs only, with no per-frame ring or count, so a frame-targeted send cannot be attributed to a specific frame element from the payload alone.
18. `page_act` `wait_open` returns the popup as `{session_id,url,opener}`. `page_list` and `session_list` carry `opener` on a popup. Closing the opener closes its popups. A browser holds at most 20 pages, popups included.
19. `page_act` `intercept_add`, `intercept_list`, and `intercept_clear` work on MCP, CLI, and IPC. `browser page act last intercept_add --rules '<json>'` reaches the same engine. Read [page_act.md](references/tools/page_act.md#request-interception) before your first rule.
20. `page_act` `auth_save`, `auth_restore`, and `auth_list` move a login between browsers and identities. Inputs are `site`, `name`, and `force`. Every result carries counts and a `warning` only, never a cookie or storage value. `auth_list` needs no live session, so a stale `session_id` and no running browser are both fine. The store is plaintext with mode 0600, so the warning is the only confidentiality signal. Host-only cookies stay host-only after restore. See [page_act.md](references/tools/page_act.md#auth-snapshots).
21. `browser_create` rate-limits each host with `rps` (default 5) and `burst` (default 10), and both join the create key. A request that cannot get a token before its deadline fails as `timeout` with `the next request slot is … away`. The effective policy rides `limits:{rps,burst,hosts:[{host,throttled}]}` on every create result and every `browser_list` entry.
22. Scope is not navigation-only. It gates `fetch_page`, `page_act navigate`, `replay` and `replay_fuzz` hops, and intercepted subresources. The engine refuses an out-of-scope request before it is sent. A navigation HTTP redirect is not re-checked, while replay hops and paused subresources are.
23. Pointer and keyboard actions (`click`, `click_at`, `input`, `hover`, `hover_at`, `press_key`) are humanized by default: stepped, jittered pointer movement and a randomized 100-500 ms pause. A sub-second `timeout_ms` can expire in that pause and leave the action unperformed, so pass at least 1000 ms or `raw:true`. Any one hatch disables it: per-call `raw:true`, `WEB_MCP_RAW_ACTIONS=1` in the server environment, or the server started with raw actions.
24. `page_act` `poc_bundle` packages one finding as an evidence directory. Inputs: `value` (the finding title), `paths` (an array whose first entry is an absolute output directory), and optional `target` (a `seq` or request-URL substring; omit it for the main document request). It writes `meta.json`, `request.http`, `response.http`, `screenshot.png`, `dom.html` (2 MB cap), `storage.json`, `README.md`, and `sinks.json` only when the tracer is installed. The result is `{directory,files:[{name,bytes}],omitted:[{name,reason}]}`. A relative path or a non-empty directory is refused, and Cookie, Set-Cookie, Authorization and X-CSRF-Token values stay `<redacted>` unless secret capture is on.
25. `page_fetch` `filter` narrows `network` and `sinks`. For `network`, pass a comma list of request types. A bare name keeps that type, and a `-` prefix drops it. An unknown name fails as `invalid_args`. For `sinks`, pass `canary` or `known`. A filtered read adds `filtered_out` with the dropped count. An unfiltered read omits it. See [page_fetch.md](references/tools/page_fetch.md).
26. `page_act` `replay` `query` edits URL parameters. A non-empty value replaces that parameter, and `""` removes it. Every other parameter keeps its captured value. `request.url` shows the rebuilt URL and is the only proof of the edit. Parameter order can change. See [page_act.md](references/tools/page_act.md).
27. `browser_create` takes `chromium_args` (array of strings, each starting with `--`) to add Chromium switches for that browser only, appended to the built-in quiet set and `[browser] chromium_flags` and deduplicated first-wins. Example: `{"chromium_args":["--disable-gpu"]}`.
27. A quoted number is coerced when it parses. `"max_bytes":"8000"` works, and a non-numeric string fails as `invalid_args`.
28. `page_fetch` `what=websocket` reads a 200-frame ring and emits the newest 100, so `dropped` is ring eviction and `truncated` is the emit-cap remainder. A text frame (`opcode` 1) carries `payload`, while a binary or control frame leaves `payload` empty and reports its size in `payload_bytes`. See [page_fetch.md](references/tools/page_fetch.md).
29. `page_act` `dialog_accept` and `dialog_dismiss` set the page dialog policy, and `dialog_accept` takes optional `value` as the prompt text. Both return `{policy,prompt_text,count,dropped,last}`, and `last` is `null` while `count` is 0. The accepted policy answers every later dialog at once, so nothing blocks the page. `http_auth` takes `user:password` split at the first colon, and an empty value clears it. It and `intercept_add` share the CDP `Fetch` domain, so one must be cleared first, and a different credential needs a new page. See [page_act.md](references/tools/page_act.md#dialogs-and-http-auth).

## Bounty loop

Use this loop on a target you are authorized to test. Find, confirm, bundle, attach.

1. **Find.** `page_fetch` `what=interactives` or `what=security`, or drive the form with `page_act`. Watch `what=network` and `what=console` after `clear_logs`.
2. **Confirm.** Reproduce the trigger in a clean page and prove reachability. Arm `sinks_install` before the payload and read `what=sinks`, or use `inject_canary` for a one-shot proof. Re-read `what=storage` to show what the session exposed.
3. **Bundle.** `page_act` `poc_bundle` with the finding title and an empty absolute directory. It captures the request/response pair, screenshot, DOM, storage and (when armed) the sink ring, and writes a `README.md` index. Pass `target` to pin the one request that proves the finding.
4. **Attach.** Hand the directory to the report. The request/response pair imports directly into a proxy, and `README.md` lists every artifact with its byte count. `omitted` tells you what was not captured: `not installed`, `no body`, `cap reached` or `secrets hidden`.

```sh
browser page act last poc_bundle --value "Stored XSS in search" --paths /tmp/poc-1
```

## Incognito identities

One browser can hold several isolated cookie jars. `fetch_page` `identity` picks one. The
name `default` is the browser's own jar. A new name is created on first use.

**Two-account IDOR recipe.**

1. Call `fetch_page` with `{"url":"<object-url>","identity":"attacker"}`.
2. Record the object id from `page_fetch` `what:"network"` or `what:"interactives"`.
3. Call `fetch_page` with `{"url":"<same-object-url>","identity":"victim"}`.
4. Compare the two responses. A victim response for the attacker's object is an IDOR.
5. Close the browser. Every identity dies with it.

A second identity is cheaper than a second browser. It shares one Chromium process, so it
costs one context and no launch time.

`fetch_page` creates a name on first use. `session_list` with `identity` is lookup only and
refuses an unknown name.

## Result envelope

Every tool returns one JSON envelope. Success puts the payload inside `data`:

```json
{"ok":true,"data":[]}
```

`data` is the JSON object or array shown in each tool reference. Prose results, such as the `browser_close` replies, become a JSON string inside `data`, for example `{"ok":true,"data":"browser browser-2 closed"}`.

An image-bearing result is different. `page_fetch` `screenshot` and `marks` return one text block with `{"ok":true,"data":<metadata>}` and one image block beside it. The image block is `{"type":"image","data":<base64>,"mimeType":"image/png"}`. It is never inside `data`. A client opts out at `initialize` with `capabilities.imageContent:false` or `capabilities.experimental.imageContent:false`. It then gets one text block with the data URI under `data` or `data.screenshot`.

Failure puts the error branch inside `error`:

```json
{"ok":false,"error":{"type":"unknown","message":"browser_close: browser \"browser-2\" not found (running: browser-1 (owner: unowned)): unknown browser"}}
```

Branch on `error.type`. The values are `invalid_args`, `element_not_found`, `not_interactable`, `anti_bot`, `page_closed`, `timeout`, `nav_failed`, and `unknown`. `message` is the human sentence, prefixed with the tool name. `hint` is a one-line fix and is omitted when the type has none, such as `unknown`.

Each tool reference states only its own payload and its failure types, and links here. This section is the only full description of the envelope. Shapes come from `code://pkg/mcp/tools/envelope.go#L43-L56`.

## Known live-server limits

- `page_fetch` large aspects bypass `max_bytes`: `interactives`, `a11y`, `network`, `console`, `websocket`, `article`, `marks`, `screenshot`, `sinks`, `postmessage`, `storage`, and `har`. The client can cut the response.
- `page_fetch` `dom` truncation can return an empty `items` array for one huge document. A larger `max_bytes` does not always help.
- `page_fetch` `css` and `js` lists are cut as plain text. A cut list is not valid JSON.
- `fetch_page` `browser_id` is not checked. It can launch a new browser with a literal unknown id.
- `page_act` `input` on `<input type="time">` can leave the value empty. Use `eval`.
- `page_act` invalid `select` value can time out the request and wedge the server. Close that browser and retry.
- `Accept-Language` currently sends `en-US,en;q=0.9;q=0.9`. The contract is `en-US,en;q=0.9`; WEB-209 fixes the duplicate. Do not treat the doubled value as expected.
- A response served from the HTTP cache bypasses every `intercept_*` rule, so a rule for a cached CSS file has no effect. Use a fresh context or clear the cache before arming rules. WEB-212 may remove this limit.
- `intercept_*` `rewrite` cannot change the HTTP method. `method` is a match filter only.
- A header set by `intercept_*` `rewrite` does not appear in the `network` ring `req_headers`. The ring is not proof of what was sent.
- A humanized pre/post pause that does not fit the remaining budget is skipped, not clipped. A budget that expires first returns `timeout` and performs no action.
- `page_fetch` `har` always returns an empty `response.cookies`, because Chrome omits `Set-Cookie` from CDP events. `request.cookies` is populated from the browser jar and redacted.
- `page_fetch` `har` reports `headersSize` and `bodySize` as `-1` when the size was not measured (`code://pkg/har/har.go#L764-L776`).
- A client caches `tools/list` for its whole connection, so a live argument can look missing. `make schema` is the authority. A name in `make schema` and absent from your client means a stale client cache, not a server defect.

## Unresponsive page recovery

**Symptom.** A `navigate` never commits, or a `page_fetch` or `page_act` call reports that the tab is unusable. The renderer no longer answers CDP calls.

- A read returns within 1.5 s. On 2026-10-28, `page_fetch` `what=meta` took 510 ms and `page_list` took 7 ms.
- An action can use the full per-call timeout. On 2026-10-28, `page_act` `eval` took 30 015 ms and `page_act` `click` took 30 014 ms. WEB-247 adds an act fast-fail.
- `page_act` `replay` stays fast at 9 ms, because it sends a new HTTP request outside the page.
- A higher `timeout_ms` does not help.
- Close the session with `session_close` on MCP, or with `browser page close` on the CLI.
- Run `fetch_page` to open a fresh page.

## Isolated test recipe

```json
{ "tool": "browser_create", "arguments": { "headless": true, "stealth": true, "persist": false } }
{ "tool": "browser_list", "arguments": {} }
{ "tool": "browser_close", "arguments": { "browser_id": "browser-2" } }
```

Use only the id you created. Do not close `browser-1`.

## Test pages

Never reuse one site. No site is the default. Pick a different real site for each run, and rotate across news, documentation, shops, SPAs and government portals.

When a call fails, repeat it once on a second unrelated site before you report a defect. A quirk of one site is not a code bug.

Keep these shapes in the rotation, with a new host each time:

- a large page with hundreds of interactive elements
- a form that posts and returns a body
- an SPA with client-side routes and framework handlers

## Maintainer status

- All 9 tools have a tested reference and an example.
- `poc_bundle` is on the tool surface: one call captures the request/response pair, screenshot, DOM, storage, sink ring (when armed) and a README index for a report.
- The result envelope is documented once here and referenced from every tool page.
- Lifecycle, proxy, scope, headers, `what=body`, `what=security` and `page_act replay` are documented.
- `sinks` is on the tool surface: `sinks_install` → `what=sinks` → `sinks_clear`. `inject_canary` is on the tool surface: `sinks_install` → `inject_canary` → `hits`.
- `postmessage` is on the tool surface: `postmessage_install` → `what=postmessage` → `post_message` → `postmessage_clear`.
- `websocket` is on the tool surface: `what=websocket` reads the frame ring, and `clear_logs` returns its cleared count beside network and console.
- `tables`, `forms`, popups, `page_fetch` `timeout_ms`, and replay `body_redactions` are on the documented surface.
- Request interception (`intercept_add`, `intercept_list`, `intercept_clear`), the eight `csp_bypass_*` security probes, replay over CLI and IPC, and the coherent default UA are documented as of 2026-10-27.
- Auth snapshots (`auth_save`, `auth_restore`, `auth_list`) and the per-host `rps`/`burst` budget with its `limits` report are documented as of 2026-10-27.
- Scope covers replay and `replay_fuzz` hops and intercepted subresources, a navigation HTTP redirect is not re-checked, and the interception limits (cache bypass, method filter, rewritten headers absent from `req_headers`) are documented as of 2026-10-27.
- Humanized pointer and keyboard timing, `replay_fuzz` with its caps and anomaly rules, the `network` identity fields (`frame_id`, `loader_id`, `top_document`), and the `security` top-document boundary plus `header_conflict` are documented as of 2026-10-27.
- `page_fetch` `filter`, `page_act` `replay` `query`, the warm `replay_fuzz` baseline, and the strict-argument gate are documented as of 2026-10-28.
