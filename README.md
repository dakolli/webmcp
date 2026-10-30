# web-mcp

A Go browser engine for LLM agents and for people. It gives an agent one live Chromium with real
state, real interaction, and a detailed picture of how a page is built.

Best for:

- Agent research and browsing that must keep a login, a cart, or a wizard step across calls.
- Page dissection: the DOM, the accessibility tree, interactive elements with coordinates, loaded
  assets, network traffic, and console output.
- Authorised security testing on a target you own. Replay, fuzz, trace sinks, and package the
  evidence.

---

## Install

One of three lines. Go 1.26 or later is required for the first.

```sh
go install github.com/dakolli/webmcp/cmd/browser@latest
```

```sh
curl -fsSL https://raw.githubusercontent.com/dakolli/webmcp/main/scripts/install.sh | sh
```

```sh
git clone https://github.com/dakolli/webmcp && cd webmcp && make install
```

The first method writes to `$(go env GOPATH)/bin`. The second writes to `~/.local/bin`. Put that
directory on `PATH`.

The first run downloads Chromium (about 100 MB). It uses a host Brave, Chrome, or Chromium when one
is on `PATH`.

Check the install:

```sh
browser --version
```

---

## Connect an MCP client

Start the server:

```sh
browser serve mcp --port 4099
```

Then write the client config with one command:

```sh
browser mcp add claude
browser mcp add opencode
browser mcp add codex
browser mcp add pi
```

Each command merges an entry into that harness config file. It keeps your other servers, writes the
URL `http://127.0.0.1:4099/sse`, and makes a `.bak` copy first.

| Command | Config file | Entry |
| :--- | :--- | :--- |
| `browser mcp add claude` | `~/.claude.json` | `mcpServers."web-mcp"` |
| `browser mcp add opencode` | `~/.config/opencode/opencode.json` | `mcp."web-mcp"` |
| `browser mcp add codex` | `~/.codex/config.toml` | `[mcp_servers.web-mcp]` |
| `browser mcp add pi` | `~/.pi/agent/mcp.json` | `mcpServers."web-mcp"` |

| Flag | Effect |
| :--- | :--- |
| `--url <url>` | Write a different server URL. |
| `--dir <dir>` | Write into that directory instead of the preset path. |
| `--dry-run` | Print the merged file. Write nothing. |
| `--force` | Replace an entry that already exists. |
| `--remove` | Delete the entry. Keep every other server. |

When the harness has no config file on this host, the command prints the path and the snippet to
paste. It writes nothing.

`browser mcp add` with no harness prints the table above and writes nothing.

The server speaks SSE (protocol `2024-11-05`, planning on updating to streamable HTTP soon). A harness that requires streamable HTTP will not
connect until that transport lands. Claude Code and OpenCode work today.

Add the entry by hand when you prefer:

```json
{
  "mcp": {
    "web-mcp": {
      "type": "remote",
      "url": "http://127.0.0.1:4099/sse",
      "enabled": true
    }
  }
}
```

Notes:

- `serve mcp` starts no browser. An agent creates one with `browser_create`, or a first `fetch_page`
  creates one.
- The server binds `127.0.0.1` and has no authentication. Keep the port local. Never expose it.
- Stop the server with `browser serve stop`.

---

## Install the agent skill

The skill is a fallback. Every tool description already carries its own instructions. Load the skill
when a call fails or a parameter is unclear.

```sh
browser skill claude
browser skill opencode
browser skill codex
browser skill pi
```

| Command | Result |
| :--- | :--- |
| `browser skill` | Print the preset table. Write nothing. |
| `browser skill ./some/dir` | Install into that directory. |
| `browser skill .` | Install into `./.agents/skills/web-mcp`. |
| `browser skill claude --dry-run` | Print every path. Write nothing. |
| `browser skill claude --force` | Overwrite an existing install. |

Start a new agent session after the install.

---

## MCP tools

| Tool | Purpose |
| :--- | :--- |
| `browser_create` | Create or reuse a Chromium. Options cover profile, headless, stealth, proxy, scope, headers, and rate limit. |
| `browser_list` | List live browsers with pages, owner, idle time, scope, and limits. |
| `browser_close` | Close one browser or all. `purge` deletes one profile. `prune` clears orphan profiles. |
| `fetch_page` | Open a URL in a new session. Returns `session_id` and a page summary. |
| `page_list` | List the pages of one browser. |
| `session_list` | List every live session. |
| `session_close` | Close one session. |
| `page_fetch` | Read one aspect of a live page. |
| `page_act` | Run one action on a live page. |

Every result is `{"ok":true,"data":…}` or `{"ok":false,"error":{"type","message","hint"}}`. Eight
error types: `invalid_args`, `element_not_found`, `not_interactable`, `anti_bot`, `page_closed`,
`timeout`, `nav_failed`, `unknown`.

Five prompts are registered: `fetch_and_summarize`, `inspect_page_decomposition`,
`form_search_and_submit`, `browser_setup_and_emulate`, `manage_session_lifecycle`.

## `page_fetch` aspects

| `what` | Returns |
| :--- | :--- |
| `dom` | The DOM tree with frames and shadow roots. Each node carries `tag`, `attrs`, `text`, `handle`, `visible`, `bounds`, and `children`. |
| `interactives` | Visible interactive elements with `handle`, `role`, `text`, `href`, `type`, centre `x`/`y`, and width. Capped at 200. |
| `a11y` | The accessibility tree as `{handle, depth, role, name, value, states}`. Capped at 400 nodes. |
| `network` | Requests and responses with method, URL, type, status, MIME, size, timing, and `<redacted>` headers. `filter` selects types, `top_document` marks the page load. |
| `console` | Console output, uncaught exceptions, and log entries with level, text, and source line. |
| `article` | The readable body as markdown. Chrome and consent blocks are removed. Capped at 40 000 runes. |
| `tables` | Tables with caption, headers, rows, and span information. |
| `forms` | Forms with action, method, fields, and CSRF candidates. Field values are never returned. |
| `marks` | Numbered boxes over one screenshot, plus the mark table. The coordinates feed `click_at`. |
| `screenshot` | A PNG image block, viewport or full page. |
| `security` | A passive audit of headers, CSP, cookie flags, mixed content, and form targets. |
| `sinks` | Source-to-sink tracer hits with stack and selector. Read-only. Arm it with `sinks_install`. |
| `postmessage` | Recorded `postMessage` traffic per window and frame. Arm it with `postmessage_install`. |
| `websocket` | WebSocket frames the page sent and received, oldest first, with socket id, direction, opcode, payload, and handshake status. Text payloads clip at 2000 runes. A binary frame reports size only. The newest 100 of a 200-frame ring. |
| `dialogs` | Recorded `alert`, `confirm`, `prompt`, `beforeunload`, and basic-auth challenges with the policy that answered each one. The default is dismiss, so a dialog never blocks the page. Ring of 50. |
| `mutations` | DOM change batches with `seq`, `kind`, `target`, `attribute`, and added and removed node counts. Read `last_seq`, act, then read again with `since` to see exactly what the action changed. Ring of 500. |
| `reflections` | Hunts a marker (passed as `target`) across the live DOM, the serialized `outerHTML`, and `document.URL`. Each hit reports `where`, `context`, `survived` special characters, a 40-char `snippet`, and a `risk` of `dangerous`, `escaped`, or `inert`. Capped at 200 hits; `sinks_correlated` links tracer hits. |
| `storage` | Cookies with flags, local and session storage, IndexedDB, and caches. Values are redacted unless `include_values`. |
| `har` | HAR 1.2 export of the observed traffic for Burp, Caido, or ZAP. |
| `html`, `text` | Raw `outerHTML`, or visible `innerText`. `selector` narrows both. |
| `links` | Links with text and href. Deduped. |
| `meta` | Title, URL, and meta tags. |
| `css`, `js` | Loaded resources with sizes. Pass `target` for one raw source. |
| `body` | One captured response body. |

`dom`, `links`, and `meta` always return parseable JSON. When a result exceeds `max_bytes`, the tool
reports `truncated` and `omitted` instead of cutting an object in half.

## `page_act` actions

| Group | Actions |
| :--- | :--- |
| Mouse | `click`, `click_at`, `hover`, `hover_at`, `drag_and_drop` |
| Keyboard | `input`, `type`, `press_key` with combos such as `Control+a` |
| Forms | `select`, `upload_file`, `fill_form` (fill fields and submit in one call) |
| Style | `set_style` (inline CSS declarations, or clear with an empty value) |
| Injection | `set_attr` (one attribute, or remove it with an empty value), `set_html` (inner, outer, or appended markup) |
| Encoding | `encode` (page-free: payload variants for a target context) |
| Navigation | `navigate`, `back`, `forward`, `reload` |
| View and time | `scroll`, `wait`, `set_viewport`, `emulate` |
| Code and logs | `eval`, `clear_logs` |
| Network | `replay`, `replay_fuzz`, `intercept_add`, `intercept_list`, `intercept_clear` |
| Session and auth | `auth_save`, `auth_restore`, `auth_list`, `http_auth` |
| Windows | `wait_open`, `post_message`, `dialog_accept`, `dialog_dismiss` |
| Security | `sinks_install`, `sinks_clear`, `postmessage_install`, `postmessage_clear`, `inject_canary`, `poc_bundle` |

Element actions take a `handle` from `page_fetch`, or a CSS selector. A handle wins when both are
given.

`set_attr` writes one attribute (`name`, `value`; an empty `value` removes it) and returns
`{target,name,value,removed,nodes_added}`. `set_html` writes markup with `mode` `inner` (default),
`outer`, or `append` and returns `{target,mode,nodes_added,elements,scripts,executed}`; `elements`
caps at 50 and `executed` is true only when the sink tracer saw a hit beyond the write itself.
Neither action has an allowlist or sanitises: `javascript:` and `onerror` values reach the page
unchanged.

`fill_form` fills a form and optionally submits it in one call. Pass `fields` (a field selector to
value object) or `selector` naming the `<form>` plus `values` (a field name to value object resolved
inside that form). `submit` is `requestSubmit` (default, `form.requestSubmit()`), `click`, or `none`;
`wait_mode` `load`, `idle`, or `stable` waits after the submit. Each control is written through its
native setter and then receives the `input` and `change` events React and Vue listen for: a checkbox
or radio is checked, a select takes the option value, a textarea takes the value, and every other
control takes the value. It returns `{form,filled:[{name,selector}],skipped:[{selector,reason}],submit,url_after}`.
An unresolved field is reported in `skipped`, never as an error; a missing form is
`element_not_found`. The action never moves the pointer or presses a key.

A pointer or keyboard action is humanised by default. Pass `raw:true` to skip it.

When an action changes the address or the title, the result ends with one line:

```
clicked selector="a.next"
→ url=https://example.com/next title="Next page"
```

`eval` never appends a delta, so its result is always parseable JSON.

`encode` is a pure helper: it needs `value` and `context` but no `session_id`, opens no page and
sends no request. It returns `{context,variants:[{label,value}]}` with the mechanically equivalent
payloads `raw`, `entity`, `url`, `url-double`, `js`, `js-hex`, `unicode`, and `html5-attr`. The
contexts are `html-text`, `attr-double`, `attr-single`, `attr-unquoted`, `js-string`, `js-template`,
`css-value`, `url-query`, and `url-path`. Pass `variants` to narrow the set; `raw` is always present.

## CLI

The CLI mirrors the tools and adds the daemon lifecycle.

```sh
browser fetch https://news.ycombinator.com --wait stable
browser page fetch last interactives
browser page fetch last network --filter=-image,-font
browser page act click ".titleline a"
browser page act set_attr ".titleline a" href "javascript:void(0)"
browser page act set_html "body" append "<b id=wm1>hi</b>"
browser page act fill_form "form#login" --field "#user=a" --field "#pass=b" --submit requestSubmit
browser page act fill_form "form#search" --form-value q=wm1 --submit click
browser page act replay "api.example.com/session" --method PUT --query ref=5.0
browser page act last poc_bundle --value "Stored XSS" --paths /tmp/poc-1
browser page act encode attr-double '<script>alert(1)</script>'
browser serve mcp --port 4099
browser serve stop
```

| CLI | MCP |
| :--- | :--- |
| `browser fetch <url>` | `fetch_page` |
| `browser page list` | `page_list`, `session_list` |
| `browser page fetch [id\|url] [what]` | `page_fetch` |
| `browser page act [id] [action] [payload...]` | `page_act` |
| `browser page close [id]` | `session_close` |
| `browser create`, `list`, `close` | `browser_create`, `browser_list`, `browser_close` |
| `browser skill <harness>` | none. Installs the agent skill. |
| `browser mcp add <harness>` | none. Writes the client config. |
| `browser serve`, `stop` | none. Runs the daemon. |

CLI conveniences: `--json` on `fetch`, page-id shorthand (`last`, `5` for `page-5`), one open page as
the default target, and `--max-bytes 0` for unbounded output.

## Recon lane

The recon lane adds nine MCP tools: eight scanners and one pipeline. Every recon tool call
requires `authorized: true`. Set it only for targets you may scan. `scope` defaults to the literal target.
The caps are 500 hosts, 5000 URLs and 200 ports per call. An absolute `output_file` receives the
raw report with mode 0600.

| Tool | Purpose |
| :--- | :--- |
| `recon_subdomains` | Passive subdomain enumeration with subfinder. |
| `recon_dns` | Resolve records or brute-force names with dnsx. |
| `recon_http` | Probe hosts and URLs with httpx. |
| `recon_ports` | Scan TCP and UDP ports with naabu. |
| `recon_urls` | Crawl seeds with katana and return in-scope URLs. |
| `recon_fuzz` | Run ffuf for content discovery and parameter tests. |
| `recon_js` | Parse JavaScript with jsluice for URLs, paths and secrets. |
| `recon_templates` | Scan targets with nuclei templates and return findings. |
| `recon_run` | Chain the selected stages over one bare registrable domain. |

`recon_run` runs the selected stages in this fixed order:
`subdomains → dns → http → ports → urls → js → fuzz → templates`.
It adds a stage that a selected stage needs. It records a failed stage and continues the run.
`stop_on_empty` defaults to false.

```sh
browser recon list
browser recon run recon_http --authorized --target https://example.com --json '{"title":true,"tech":true}'
```

`browser recon list` prints each scanner, its pin and its install state. `browser recon run` runs
one scanner in the CLI process. It never uses the serve daemon, because the IPC socket caps a
request at 30 seconds. The friendly flags include `--target`, `--scope`, `--authorized`,
`--output-file`, `--max-entries` and `--timeout-ms`. Pass `--json` for any typed field. Arguments
after `--` reach the scanner.

The first call to a scanner installs its pinned binary with `go install` into `<config>/recon/bin`
(mode 0700).
A cold install can take minutes. The lane never uses `PATH`, because `httpx` on this host is a
Python script. `recon_js` builds with CGO. The other scanners build without it. `recon_ports`
uses CONNECT by default. `mode: syn` needs libpcap and fails with a named error.

For agent usage, read `.agents/skills/recon/SKILL.md`.

---

## Configuration

- Profiles live in `~/.config/browser-mcp/profiles/`. A bare name maps to `profiles/<name>`.
- An MCP browser is ephemeral by default. Pass `persist:true` with a profile name to keep a login.
- Set `browser_ttl` in `~/.config/browser-mcp/config.json` to change the 30-minute idle timeout.
  Pages expire after 15 minutes idle.
- `--scope HOST` bounds navigation, replay, and intercepted subresources to a host list.
- `--proxy http://127.0.0.1:8080 --ignore-certs` sends traffic through Burp, Caido, or ZAP. Remote
  proxy credentials belong in `~/.config/browser-mcp/proxy.json` with mode `0600`, never in a flag.
- Caido settings live in `~/.config/browser-mcp/webmcp.toml` (mode `0600`). `browser config init`
  creates the file once and never overwrites it; `browser config path` prints its location and
  `browser config show` reports the Caido settings, the resolved credential (`auth: access_token`,
  `pat`, `pat (rejected), guest`, `guest` or `none`) and the resolved Chromium switches. On
  `browser serve mcp`, `--caido-token` overrides the file `access_token`, `--caido-pat` overrides
  `CAIDO_PAT`, which overrides the file `pat`, and `--caido` forces the tool on. An `access_token`
  wins over a `pat`; a rejected credential falls back to a guest session unless `allow_guest = false`.
  `browser serve mcp` applies the `[caido]` selection keys below after it connects:

  | Key | Meaning | Empty means |
  | :--- | :--- | :--- |
  | `project` | Engagement project selected at launch. | Use the instance's project and log it. |
  | `scope_name` | Focus scope inside the project; created when absent, updated in place when a list changes. | Write nothing. |
  | `scope_allow` | Host globs the scope records. | Keep the stored allowlist of an existing scope. |
  | `scope_deny` | Host globs the scope skips. | Keep the stored denylist of an existing scope. |
  | `capture` | Passthrough filter: `all` records everything, `in-scope` records only `scope_allow` hosts. | Keep the instance's stored filter (`auto` too). |
- Browser settings live in the same file under `[browser]`. `chromium_flags = ["--flag", …]` adds
  Chromium switches to every browser this process starts, and `quiet = false` drops the built-in
  quiet set that disables background telemetry, update and sync traffic (the default is on). Add a
  one-off switch with `browser create --chromium-arg=--disable-gpu` or the MCP `chromium_args`.

## Caido history

Six `caido_*` tools work with a Caido instance on the same host. Five read the recorded history and one rewrites a live response.

| Listener | Address |
| :--- | :--- |
| Proxy | `127.0.0.1:8081` |
| UI and GraphQL API | `127.0.0.1:8080` |

### Enable the tools

The tools register only when one condition is true:

1. `[caido] enabled = true` in `~/.config/browser-mcp/webmcp.toml` (mode `0600`).
2. `browser serve mcp --caido` runs. The flag forces the tools on.

| Task | Command |
| :--- | :--- |
| Create the file once | `browser config init` |
| Print its path | `browser config path` |
| Show the resolved settings | `browser config show` |

`browser config init` writes the file one time. It never overwrites an existing file.

Two launch errors turn the tools off, even when `enabled = true`:

- `[caido] capture` is not `all`, `in-scope`, or `auto`.
- `[caido] project` names a project that the instance does not have.

A failed login does not turn them off. The tools register, and each call returns the login error.

### Authentication

The client tries each credential in this order:

1. `access_token` as the bearer.
2. `pat` as the bearer.
3. A guest session, when `allow_guest = true`. This is the default.

A rejected credential falls through to the next step. A bad PAT still gives a working guest session.
Guest history lasts only while the instance runs.

Each credential comes from the first source that sets it. The two flags belong to `browser serve mcp`.

| Credential | Flag | Environment | File key |
| :--- | :--- | :--- | :--- |
| Access token | `--caido-token` | `CAIDO_ACCESS_TOKEN` | `access_token` |
| PAT | `--caido-pat` | `CAIDO_PAT` | `pat` |

Copy the access token from the Caido UI console. It expires after about 7 days.

```js
JSON.parse(localStorage.CAIDO_AUTHENTICATION).accessToken
```

With `allow_guest = false` and no accepted credential, the tool returns this error:

```
caido: no session — the instance rejected the credential and guest access is off
```

### Tools

| Tool | Purpose |
| :--- | :--- |
| `caido_status` | Project, scopes, capture mode and row count. Call it first. |
| `caido_sitemap` | The endpoints one host recorded. One row per method and path shape. |
| `caido_search` | The captured requests. Filter with `httpql` or the typed keys. |
| `caido_finding` | Read or create a finding linked to one captured request id. |
| `caido_replay` | Resend one captured request and read the exchange it records. |
| `caido_intercept` | List, add or clear rewrite rules that change a live response. |

### What the proxy records

| Setting | Effect |
| :--- | :--- |
| `[caido] capture = "all"` | The proxy records every host. |
| `[caido] capture = "in-scope"` | The proxy records only `scope_allow` hosts. |
| `[caido] capture = "auto"` | The instance keeps its stored filter. |
| `browser_create` `deny_hosts` | The host skips the proxy, so it never reaches history. |

`deny_hosts` keeps a host out of history. The request still leaves the machine.

### Limits

- `caido_status`, `caido_sitemap`, `caido_search` and `caido_finding` never change captured history.
- `caido_replay` sends one new request. `caido_intercept` changes live responses, not history.
- Credential-shaped header names and body keys return as `<redacted>`. Pass `raw: true` to disable redaction.
- `count` is the rows returned. `total` is the whole matching history, and it ignores `since`.
- A search with no filter scans the newest 500 rows.
- `body_bytes: 0` returns headers with empty bodies.

## Licence

MIT License.
