# web-mcp

A Go browser engine. It has two front-ends. One is an MCP server for LLM agents. The other is a
`browser` CLI for humans. Both drive one live, stateful Chromium through
[go-rod](https://go-rod.github.io/).

The engine answers granular structural questions about a live page. It reports the DOM tree, the
accessibility tree, every interactive element, the loaded CSS and JS, the network traffic, and the
console. An agent can build a scraper or an automation for a site without a guess.

---

## Install

Go 1.26 or later is necessary. The first run downloads Chromium (~100 MB) unless Brave, Chrome or
Chromium is on `PATH`. The probe order is `brave-browser`, `brave`, `google-chrome`, `chrome`,
`chromium`.

```sh
go install ./cmd/browser     # installs the browser binary
make install                 # same, and it stops a running daemon first
```

## Add the agent skill

The skill helps when a tool description is not clear or a call fails. One command copies it into a
harness.

```sh
browser skill claude         # install into ~/.claude/skills/web-mcp
browser skill opencode       # install into ~/.config/opencode/skills/web-mcp
browser skill codex          # install into ~/.codex/skills/web-mcp
browser skill pi             # install into ~/.pi/skills/web-mcp
```

Other targets:

| Command | Result |
| :--- | :--- |
| `browser skill` | Print the preset table. Write nothing. |
| `browser skill .` | Install into `./.agents/skills/web-mcp`. This needs `./.agents/skills` to exist. |
| `browser skill ./some/dir` | Install into that directory. |
| `browser skill claude --dir ./x` | Same, and the flag wins over the preset. |
| `browser skill claude --dry-run` | Print every path. Write nothing. |
| `browser skill claude --force` | Overwrite. A second run refuses without this flag. |

The command writes `SKILL.md` and the `references` tree. It never writes `AGENTS.md`. Start a new
agent session after the install so the harness reads the skill.

---

## Start the server

```sh
browser serve mcp --port 4099
# daemonized: CLI, Unix socket, and MCP HTTP/SSE on http://127.0.0.1:4099/sse
```

- The default port is `4099`.
- `serve mcp` starts no browser. An agent calls `browser_create`, or a first `fetch_page` creates one.
- The server binds `127.0.0.1` by default and it has no authentication.
- Pass `--bind 0.0.0.0` to listen on all interfaces. The server prints a warning when it does.
- A wildcard bind prints `http://[::]:<port>`. Put a reachable host in the client URL.

```sh
browser serve mcp --port 4099 --profile brave --bin brave-browser   # persistent profile
browser serve mcp --port 4099 --headless                            # headless
browser serve stop                                                  # stop the daemon
```

Connect an MCP client:

```json
"web-mcp": {
    "type": "remote",
    "url": "http://127.0.0.1:4099/sse",
    "enabled": true
}
```

---

## What it does

### Engine and lifecycle

- One Chromium process for each browser. Many tabs.
- A profile is ephemeral by default and the directory is deleted on close.
- `persist:true` with a name keeps the profile and its logins.
- An `owner` label claims a browser. The same owner and the same options reuse one browser.
- An idle browser closes after 30 minutes. A dead Chromium relaunches with the same id.
- `purge` deletes one owned profile. `prune` removes orphan ephemeral profiles.
- Stealth applies to every page. Device, viewport, window position and user agent are per page.
- The default User-Agent tracks the launched Chromium major, with matching `Sec-CH-UA` hints and an `en-US` language, and an explicit `user_agent` wins.
- The page lock is context-bound. A slow action cannot block the daemon.

### Isolation and control

- Named incognito identities share one Chromium process. Each identity keeps its own cookies and
  storage.
- A scope allowlist bounds navigation, replay, every redirect hop, and intercepted subresources. The engine refuses an off-scope request before it is sent.
- A per-host rate limit (`rps` 5, `burst` 10 by default) spaces requests. A request that cannot get a token before its deadline fails as `timeout`.
- Auth snapshots save and restore a login across browsers and identities. Results carry counts only, never a cookie or storage value.
- Extra headers go on every request of a browser.
- Proxy support includes credentials, a bypass list, and `ignore_certs`.
- Replay sends a captured request through Go. It carries the page cookies and follows redirects.

### Reads: `page_fetch` has 23 aspects

- Document: `html`, `text`, `dom`, `links`, `meta`.
- Sources: `css`, `js`, and `body` for one resource.
- Pixels: `screenshot`, and `marks` for numbered boxes over a screenshot.
- Action map: `interactives` reports 200 visible elements with handles, centre points, and size.
- Accessibility: `a11y` reports 400 nodes with role, name, value, state flags, and handles.
- Runtime: `network` with request and response headers. `console` for logs.
- Text: `article` returns the readable body as markdown.
- Security: `security` audits headers, CSP, cookie flags, mixed content, and form targets, and probes the CSP for eight bypass patterns. It is passive: no injection, execution, or load.
- State: `storage` reports cookies with flags, local and session storage, IndexedDB and caches. Values
  are redacted by default.
- Tracer: `sinks` reports source-to-sink hits. It is read-only. Arm it with `page_act sinks_install`.
- Export: `har` returns HAR 1.2 for the observed network traffic.

### Actions: `page_act` has 34 actions

- Mouse: `click`, `click_at`, `hover`, `hover_at`, `drag_and_drop`.
- Keyboard: `input`, `type`, `press_key` with combos such as `Control+a`.
- Forms: `select`, `upload_file`.
- Navigation: `navigate`, `back`, `forward`, `reload`.
- View and time: `scroll`, `wait`, `set_viewport`, `emulate`.
- Code and logs: `eval`, `clear_logs`.
- Tracer: `sinks_install`, `sinks_clear`. Both take no payload.
- Network: `replay`, `intercept_add`, `intercept_list`, `intercept_clear`.
- Auth: `auth_save`, `auth_restore`, `auth_list`. Results carry counts only.

### Surfaces

- 9 MCP tools. Every result is `{"ok":true,"data":…}` or `{"ok":false,"error":{…}}`.
- 8 error types: `invalid_args`, `element_not_found`, `not_interactable`, `anti_bot`, `page_closed`,
  `timeout`, `nav_failed`, `unknown`.
- The CLI mirrors the tools and adds the daemon lifecycle.
- IPC is JSON-RPC 2.0 over a Unix socket with mode `0600`. `page.fetch` gets a 180s per-request
  budget so a raised `timeout_ms` survives the daemon; every other method stays at 30s.

### Security lane

Use these on a target you are authorized to test.

1. `page_act sinks_install` arms the source-to-sink tracer.
2. Send a payload. For example, put a canary in the URL hash and push it into a sink.
   ```js
   () => { document.body.innerHTML = location.hash }
   ```
3. `page_fetch what=sinks` reports `source`, `sink`, `value`, the call stack, and the selector.
4. `page_act sinks_clear` empties the ring. `browser_close` ends the test.

The arm survives a full navigation. The hit ring restarts for each document.
`page_fetch what=har` exports the observed traffic for Burp, Caido, or ZAP.
`page_fetch what=security` audits the response headers and the cookies.

---

## Profiles and browser lifecycle

- Every profile lives under `~/.config/browser-mcp/profiles/`. A bare name maps to `profiles/<name>`.
  An absolute path is used as given. A relative path with a separator is refused.
- The MCP path and a bare `browser create` are ephemeral. The plain human `browser serve` daemon keeps
  its `browser-1` profile.
- `browser close <id>` closes one browser and its pages. `--purge` also deletes that profile.
  `--prune` closes all browsers and removes orphan ephemeral directories. Named profiles are never
  deleted by a prune.
- `--owner <label>` labels a browser and joins the create key.
- `--header NAME=VALUE` sends a header on every request. Repeat the flag for more headers.
- Idle browsers expire after 30 minutes. Set `browser_ttl` in `~/.config/browser-mcp/config.json` as a
  Go duration, for example `"15m"` or `"0"` to disable. Pages have a separate 15-minute TTL.
- `--identity <name>` opens a page in an incognito context. `default` is the browser's own jar. A new
  name is created on first use. Every identity dies with its parent browser. An unknown identity is
  refused with the live names.

## Scope and headers

- `--scope HOST` confines requests. A bare host covers its subdomains. A leading `*.` glob and
  `host:port` are accepted. `*` allows all.
- No scope means no limit. `browser_list` reports the live state as `scope` plus `scope_strict`.
- `--scope-strict` is opt-in. It makes an empty allowlist deny every request.
- A refusal names the host, the allowed list, and the fix.
- The limit covers navigation, replay, every redirect hop, and intercepted subresources. An off-scope
  request is refused before it is sent, and a refused replay redirect names the hop and the host.
- `rps` and `burst` are MCP create inputs only: the CLI has no flags for them. The default is 5/s with
  a burst of 10 per host, and the effective policy appears as `limits` on `browser_list`.

## Proxy and TLS

Use a local intercept proxy such as Burp, Caido, or ZAP:

```sh
browser create --proxy http://127.0.0.1:8080 --ignore-certs
browser fetch https://example.org --wait load    # the request appears in the proxy history
```

- `--proxy-bypass` takes a comma-separated host list, for example `localhost,*.internal`.
- `--ignore-certs` accepts an invalid TLS certificate, for example the proxy CA. It is independent of
  the proxy. Set it explicitly.
- Chromium receives `--proxy-server`, `--proxy-bypass-list`, and `--ignore-certificate-errors`. These
  are start flags, so they are per browser.
- Proxy credentials never go in a flag or a tool argument. Put a remote proxy credential in
  `~/.config/browser-mcp/proxy.json` with mode `0600`, keyed by proxy `host:port`:

```json
{ "proxies": { "proxy.example.com:8080": { "username": "user", "password": "pass" } } }
```

A local intercept proxy needs no credentials. Point an agent at a remote proxy only when the user
asks for it.

---

## MCP tools

| Tool | Purpose |
| :--- | :--- |
| `browser_create` | Create or reuse a browser. Inputs: `bin`, `profile`, `persist`, `owner`, `headless`, `stealth`, `position`, `proxy`, `proxy_bypass`, `ignore_certs`, `headers`, `scope`, `scope_strict`, `rps`, `burst`. |
| `browser_list` | List live browsers with `owner`, `persist`, `ephemeral`, `created_at`, `idle_for`, `ttl`, `scope`, `scope_strict`, `limits`, `profile_dir`, and their pages. |
| `browser_close` | Close one browser, or all when `browser_id` is empty. `purge` deletes one profile. `prune` removes orphan ephemeral profiles. |
| `fetch_page` | Open a URL in a new session. Returns `session_id` and a summary. `wait_for` defaults to `idle`. `identity` selects an incognito context. |
| `page_list` | List the pages of a browser with the live URL, title, and `context`. |
| `session_list` | List all live sessions with the live URL, title, and `context`. |
| `session_close` | Close one session. |
| `page_fetch` | Read one aspect of a live page. `timeout_ms` bounds the call (default 30000, clamped to 1..120000). |
| `page_act` | Run one action on a live page. |

Five MCP prompts are registered: `fetch_and_summarize`, `inspect_page_decomposition`,
`form_search_and_submit`, `browser_setup_and_emulate`, and `manage_session_lifecycle`. A prompt is a
template. It names the tools. You still call them.

## Page aspects

`what` accepts 23 values. `selector` scopes `html` and `text`. `max_bytes` defaults to 50000.
`full_page` applies to `screenshot`. `timeout_ms` bounds each call: it defaults to 30000 and is
clamped to 1..120000. A full-page screenshot keeps a 90s floor unless `timeout_ms` is larger.

| `what` | Returns |
| :--- | :--- |
| `dom` | The DOM tree with frames and shadow roots. Each element has `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`, and `children`. |
| `interactives` | `{interactives:[{handle, role, text, href, type, x, y, width, height, visible}], truncated}`. Capped at 200 visible elements. `x`/`y` are the element centre, so `left = x - width/2`. `marks` reports the top-left instead. |
| `a11y` | `{nodes:[{handle?, depth, role, name?, value?, states?}], count, truncated}`. DFS order, capped at 400. Root frame only. |
| `network` | `{entries:[{method, url, type?, status?, mime?, bytes?, ms?, failed?, error?, seq}], count, dropped, truncated}`. Headers included. One entry per redirect hop. Newest 100 of a 200-entry ring. |
| `console` | The same envelope over `{level, text, source?, line?, seq}`. |
| `article` | `{title, byline?, excerpt?, text, words, truncated, url}` as markdown. Capped at 40000 runes. |
| `tables` | `{tables:[{index, caption?, headers, rows, span_info, selector}], count, truncated}`. Capped at 20 tables and 500 rows per table. |
| `forms` | `{forms:[{index, action, method, enctype, fields, submits, csrf_candidates}], count, truncated}`. Field values are never returned; only `has_value` and a hidden-token signal. |
| `marks` | `{marks:[{index, role, text?, x, y, width, height}], viewport_width, viewport_height, truncated}` plus an annotated PNG. `x`/`y` are the top-left. Capped at 60 marks. |
| `security` | `{checks:[{id, status, evidence, detail}], count}`. A passive audit. |
| `sinks` | `{installed, hits:[{source, sink, context, value, stack, selector, ts}], dropped, truncated}`. Read-only. Capped at 100 hits from a 200-hit ring. |
| `har` | HAR 1.2 `{log:{version, creator, pages, entries[]}}` for the observed traffic. Capped at 512 KiB. `Authorization` and `X-CSRF-Token` read `<redacted>`. |
| `storage` | `{cookies, local_storage, session_storage, indexed_db, caches}`. Values are `<redacted>` unless `include_values` is true. |
| `links` | `[{text, href, handle?}]`. Deduped, non-navigable schemes removed. |
| `meta` | `{title, url, meta:[{name/property/http-equiv/content/charset}]}`. |
| `text` | Visible `innerText`. |
| `html` | `outerHTML`. |
| `css`, `js` | Loaded resources as `[{url, type, size}]`. Pass `target=<url>` for one raw source. |
| `body` | One captured response body. |
| `screenshot` | A PNG image block plus `{mime_type, bytes, width, height, full_page}`. |

`screenshot` and `marks` return the PNG as a separate image content block. A text-only client can opt
out at `initialize` with `capabilities.experimental.imageContent: false`. It then receives the base64
data URI in the text block.

`dom`, `links` and `meta` always return parseable JSON. When the result does not fit `max_bytes`, the
tool returns `truncated`, the applied `max_bytes`, and `omitted`. Raise `max_bytes` to read the whole
result. `interactives`, `a11y`, `network`, `console`, `article`, `marks`, `security` and `storage`
report their own `truncated` flag and are never cut mid-value.

## Page actions

`press_key` accepts named keys and combos: `Enter`, `Escape`, `Tab`, `ArrowDown`, `F5`, `Control+a`,
`Meta+c`, `Shift+Tab`. `input` replaces the value. `type` inserts at the caret. `wait_mode` is one of
`load`, `idle`, `stable`, `interactable`, and `ms`.

Element actions accept a `handle` from `page_fetch` instead of a selector. The handle wins when both
are given. A handle looks like `g<document>-b<backendNodeId>`. It goes stale when the element leaves
the page or the page navigates. Re-run the fetch for a new handle.

When an action changes the address or the title, the result gains one final line:

```
clicked selector="a.next"
→ url=https://example.com/next title="Next page"
```

Network and console capture is cumulative from page creation. The per-step recipe is `clear_logs`,
then the action, then `page_fetch` with `network` or `console`.

`replay` resends one captured request through a Go HTTP client. It carries the page cookies for the
target host and follows up to 5 redirects. It returns `{status, headers, body, bytes, truncated, ms,
request, chain}`. Sensitive response headers are redacted.

`page_act` can also intercept requests in flight with `intercept_add`, `intercept_list`, and
`intercept_clear`. A URL-matched rule blocks, allows, rewrites, or fulfills a request, and every match
is tagged on the `network` ring. Rules are per page, capped at 50, and must not run beside proxy
authentication. The CLI and IPC have no `intercept_*` action yet. A response served from the HTTP
cache bypasses the rules, `rewrite` cannot change the HTTP method, and a rewritten header does not
appear in the ring's `req_headers`.

`page_act auth_save` captures the live page's cookies for `site` plus local storage, session storage,
and IndexedDB names. `auth_restore` writes the snapshot into the page's browser context, so a new
browser and a new `identity` both render the login. `auth_list` needs no live session. Results carry
counts and a `warning` only; the store is plaintext with mode 0600, so that warning is the only
confidentiality signal. Host-only cookies stay host-only. The CLI mirrors them as
`browser page auth save <site> <name>`, `browser page auth restore <site> <name>` (with `--force`),
and `browser page auth list [site]`.

---

## CLI

```sh
browser fetch https://news.ycombinator.com --wait stable
browser page fetch last meta
browser page fetch last interactives            # handles, roles, text, x/y
browser page fetch last storage --include-values
browser page fetch last security
browser page fetch last screenshot --full-page --timeout-ms 60000
browser page fetch https://app.test/login --identity attacker
browser page act click ".titleline a"
browser page act replay "api.example.com/session" --method PUT --value '{"role":"admin"}'
browser page act last clear_logs
browser page close last
```

| CLI | MCP |
| :--- | :--- |
| `browser fetch <url>` | `fetch_page` |
| `browser page list` | `page_list` / `session_list` |
| `browser page fetch [id\|url] [what]` | `page_fetch` |
| `browser page act [id] [action] [payload...]` | `page_act` |
| `browser page auth save\|restore\|list` | `page_act` `auth_*` |
| `browser page close [id]` | `session_close` |
| `browser create` / `list` / `close` | `browser_create` / `browser_list` / `browser_close` |
| `browser skill <harness>` | — (CLI-only: install the agent skill) |
| `browser focus` | — (CLI-only: raise the window) |
| `browser serve` / `stop` | — (CLI-only: daemon lifecycle) |

CLI-only conveniences: `--json` on `fetch`, page-id shorthand (`last`, `5` becomes `page-5`), a single
open page as the default target, one-step `page fetch <url> [what]`, and `--max-bytes 0` for unbounded
output.

---

## Operating notes

- A session expires after 15 minutes idle. Re-open the page when a `session_id` disappears.
- A browser expires after 30 minutes idle by default. Close it explicitly when the work is done.
- `wait_for` defaults to `idle`. Use `load` or `stable` on a site with heavy JavaScript.
- Output is capped at 50 KB by default. Narrow it with `selector` or `target`. Raise `max_bytes` for
  `dom`, `links` and `meta`.
- `page_fetch` and `browser page fetch` accept `timeout_ms` / `--timeout-ms` (default 30000, clamped
  to 1..120000). The daemon gives `page.fetch` a 180s transport budget, so a raised value is not cut
  at 30s; every other IPC method keeps the 30s cap.
- The server has no authentication, permissive CORS, and `eval` plus arbitrary `upload_file` paths.
  Treat the port as local-only.

## License

MIT License.
