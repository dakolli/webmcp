# Changelog

## Unreleased

### Added
- `page_act set_style` writes a CSS declaration list onto one element and reports which declarations the browser kept and which it dropped. An empty value clears the inline style. This turns a CSS injection probe into two calls instead of five.
- `page_act set_attr` and `page_act set_html` write an attribute or markup into the page and report the nodes the browser created, the scripts it parsed and whether one ran.
- `page_act fill_form` fills a form from a selector-to-value map and submits it in one call, with React and Vue seeing each write.
- `page_act encode` returns one payload in the encodings a given context needs, so a bypass test does not hand-write entities or escapes.
- `page_fetch what=reflections` finds a marker in the page and reports where it landed, the context that received it, which special characters survived, and whether that is dangerous, escaped or inert.
- `page_fetch what=security` now parses the effective CSP into directives, nonce and hashes, and records `securitypolicyviolation` events. The policy and the response-header checks come from a page-side capture, so they survive `clear_logs` and a header over 512 bytes parses whole.
- The sink tracer names more DOM XSS sources: `window.name`, `document.referrer`, `document.cookie` (value redacted), local and session storage reads, `history.state`, `message` events and `URLSearchParams` reads.
- `page_act eval` no longer appends the `→ url=… title="…"` change line. Its result is parseable JSON again.
- Nine recon tools wrap pinned external scanners: `recon_subdomains` (subfinder), `recon_dns` (dnsx), `recon_http` (httpx), `recon_ports` (naabu), `recon_urls` (katana), `recon_fuzz` (ffuf), `recon_js` (jsluice), `recon_templates` (nuclei), plus the `recon_run` pipeline. Each returns compact entries with honest `truncated` and `dropped` counters, and can write the raw report to an absolute `output_file`.
- Every recon call needs `authorized: true`. `scope` sits beside it as a host allowlist that defaults to the literal target, so one missing argument does not mean the whole internet.
- The scanners install on first use into `<config>/recon/bin`. `PATH` is never used, so the Python `httpx` on this host cannot be run by mistake.
- `recon_run` chains stages over one registrable domain in a fixed order, adds the stage a later one needs, and records a failed stage without ending the run.
- `browser recon list` and `browser recon run <tool>` run in process, never through the serve daemon, so a scan is not cut at 30 seconds. `--json` reaches every typed field.

### Fixed
- `page_fetch what=reflections` returned a decode error on every call instead of a result.
- `page_act fill_form` returned its result as a JSON string, not an object.
- `csp_unsafe_inline` warned on a `style-src` token while `script-src` was hash-locked. It now tests the effective script directive only.
- `page_fetch what=security` read the `cors` check from the network ring, so `clear_logs` erased it. It now reads the page-side document record and quotes the headers it judged on every status.
- `page_act fill_form` ignored the value in `values`, so a radio group's first member got checked. It now matches by name and value, and names the candidates when several match.
- The CLI `page act --field` split at the first `=`, so a selector that contained one wrote the wrong element with no error. It now splits outside brackets, and `--fields` takes a JSON map.
- `page_fetch what=dom` silently ignored `selector`. It now fails and points at `what=html` for a scoped read.

## 0.3.0 - 2026-10-30

### Highlights
- Six Caido tools now sit beside the browser tools: `caido_search`, `caido_status`, `caido_sitemap`,
  `caido_finding`, `caido_replay` and `caido_intercept`. An agent reads recorded proxy traffic,
  replays one request and records the finding.
- `browser serve` matches the Caido instance at launch from `[caido]`: project, scope and capture
  filter, one log line each.
- `browser_create.deny_hosts` keeps browser telemetry out of the proxy, so history shows the target.
- Chromium switches are configurable, and a quiet set is on by default.

### Added
- Chromium switches are now configurable. `[browser] chromium_flags` in `webmcp.toml`, `browser create --chromium-arg`, and `browser_create.chromium_args` all add to one resolved list.
- A built-in quiet set is on by default for `browser serve` and every created browser. It stops Brave and Chromium background traffic, component updates, sync, breakpad and first-run noise. `[browser] quiet = false` turns it off.
- `browser config show` lists the resolved switches.
- `caido_search` reads the recorded HTTP history of a local Caido instance: method, URL, status,
  request and response headers and bodies, timing, and sizes. This is the half `page_fetch` cannot
  show. It is registered only when `~/.config/browser-mcp/webmcp.toml` sets `[caido] enabled = true`.
- Credentials are masked by default in headers, bodies and query strings. `raw: true` prints the
  live values.
- `browser config init|path|show` creates and inspects that file with mode 0600. `config show` prints
  the token as `set` or `empty`, never the value.
- `browser serve mcp --caido` and `--caido-pat` enable the tool and override the token, without
  editing the file.
- `caido_status` reports the selected project, every scope, the capture mode and the history count.
  Call it first when a search returns nothing. `browser caido status` prints the same facts.
- `caido_search` takes a raw `httpql` expression or a typed `host` key, plus a `scope_id`. The instance
  filters, so a target is found without scanning a 500-row window. Each call reports the expression it
  ran and the scope it read.
- `caido_search` reports `since_seconds` and `dropped_since`, and explains `count 0` beside a larger
  `total` instead of returning a bare empty list.
- The MCP `initialize` response names the Caido project and its scopes, so an agent knows which scope
  it reads before it searches.
- `caido_sitemap` lists the endpoints a host exposes: method, path, kind, status, request count and
  query parameters. Paths that differ only by a number or a UUID collapse into one row with a count.
  Depth defaults to 5, and a cut depth reports the children it did not walk.
- `browser caido sitemap <host>` prints the same rows as a table.
- `CAIDO_LIVE=1 go test ./pkg/caido/` runs one test against the real instance and skips on a plain
  run, so the unit suite stays offline.
- `caido_finding` lists, reads and creates Caido findings. A finding links to a captured request id,
  so a proven bug becomes a record in the project instead of a line in a chat.
- `browser caido finding list|get|create` does the same from the terminal.
- `caido_replay` resends a captured request through the instance with an edited method, headers,
  query or body. The replay lands in project history beside the original, and the result carries the
  new request id so the next `caido_search` finds it. One request per call. Caido records a session
  row beside each exchange, so one replay adds two history rows.
- `browser caido replay <id>` does the same from the terminal, with `--method`, `--header`, `--set`,
  `--remove` and `--body`.
- `caido_intercept` lists, arms and clears proxy-level response rewrites: a status code, a body and
  header overrides. An arm needs an HTTPQL `url_filter`, so one target changes and other traffic
  passes untouched. `clear --confirm` is the kill switch and removes only rules this tool created.
  A rule outlives the process, so call clear before you stop.
- `browser caido intercept list|add|clear` does the same from the terminal.
- `browser_create.deny_hosts` keeps chosen hosts out of the proxy. A proxied visit now stays clean of
  browser telemetry, so Caido history shows the target and not the browser.
- `[caido] access_token` takes a direct instance token, and `[caido] allow_guest` (default true)
  allows the local guest session. `config show` now reports which one worked, for example
  `auth: pat (rejected), guest`.
- `[caido] project`, `scope_name`, `scope_allow` and `scope_deny` make `browser serve` match the
  instance at launch: it selects the named project and creates or updates one scope. The scope id
  stays stable, and the tool never deletes a scope.
- `browser serve` prints one line each for the auth mode, the project, the scope with its id and
  allowlist, and the capture mode. A focus scope with an empty allowlist warns that it matches all
  traffic.
- `[caido] capture = "in-scope"` makes the proxy record only hosts in the scope allowlist.
  `capture = "all"` records everything. Proven on Caido 0.59.0. An empty value leaves the instance
  unchanged.
- `.agents/skills/caido/` documents the tool for an agent.

## 0.2.5 - 2026-10-28

### Highlights
- A page that opens a dialog no longer blocks every later read. The engine answers it at once and
  records it.
- `page_fetch what=mutations` shows exactly what one action changed, without diffing the page.
- A page behind HTTP basic auth is one action away. The error names the action.

### Added
- `page_fetch what=websocket` returns the WebSocket frames the page sent and received, oldest first,
  with socket id, direction, opcode, payload, and handshake status. Text payloads clip at 2000 runes.
  A binary frame reports size only. The newest 100 of a 200-frame ring emit, and `count`, `truncated`,
  and `dropped` add up to the frames observed.
- `page_fetch what=mutations` returns DOM change batches. Read `last_seq`, run an action, then read
  again with `since` to see exactly what that action changed, without diffing the whole page.
- `page_fetch what=dialogs` returns the recorded `alert`, `confirm`, `prompt`, `beforeunload`, and
  auth challenges, with the policy that answered each one.
- `page_act dialog_accept` and `dialog_dismiss` set the answer for later dialogs and return the ring.
- `page_act http_auth` takes `user:password` for a page behind HTTP basic auth, and an empty value
  clears it.

### Fixed
- A page that opens `alert`, `confirm`, `prompt`, or `beforeunload` no longer blocks every later
  read. The engine answers the dialog at once, records it, and the page stays usable.
- A 401 page with no credential used to fail as a raw navigation error. It now keeps the session
  open and the error names `http_auth` and the exact command to run before you navigate again.

### Changed
- The install script lives at `scripts/install.sh`. The README install command matches it.
- The release notes and the bump script stay off the git record.
- The `security` tool description now states that the audit is passive and read-only, and that it
  reports policy weakness, not exploitability.

## 0.2.0 - 2026-10-28

### Highlights
- One command configures an MCP client. `browser mcp add claude` writes the entry, keeps your other
  servers, and never overwrites the file.
- A page that stops answering now fails in about half a second with a recovery line. Before, it
  waited out the whole call timeout.
- `network` and `sinks` reads take a filter. A normal page no longer buries the signal under images
  and framework boot.
- `replay` edits URL query parameters.

### Added
- `browser mcp add claude|opencode|codex|pi`. Flags: `--url`, `--dir`, `--dry-run`, `--force`,
  `--remove`. It merges and copies the file to `.bak` first. With no config file, it prints the path
  and the snippet to paste.
- `page_act replay` takes `query`. A value replaces a parameter. An empty string or `null` removes it.
- The CLI and IPC carry the same two arguments: `--query NAME=VALUE` (repeatable) on `page act`, and
  `--filter` on `page fetch`.
- `page_fetch filter` narrows `network` by 18 request types with a keep form and a `-drop` form, and
  narrows `sinks` by `canary` or `known`. Both report `filtered_out`.
- An argument outside a tool schema fails as `invalid_args` and names the key plus the valid keys. A
  quoted number is accepted for a numeric field.
- `replay_fuzz` reports `cold_ms` beside `ms`, so the first sample is visible.

### Fixed
- A navigate that never commits left the page unusable for the rest of the call budget. Reads,
  actions, and page listings now return in about 0.5 s with one line that names the close command for
  your surface.
- An action on such a page could burn the full 30 s. It now fails in about 0.5 s.
- An error names the operation you ran, not an earlier one. A nested argument error names the key,
  for example `query["probe"]`.
- The ambiguous-target error for `replay` dropped from about 1.6 KB to 365 bytes.
- Wedge recovery on the CLI fell from 1.5 s to about 0.5 s.

### Changed
- The README is now four parts: install, connect an MCP client, install the agent skill, and a tool
  and feature reference. Release instructions moved to `RELEASING.md`.
- The agent skill documents `filter`, replay `query`, strict arguments, and recovery bounds split by
  read and action.

### Known limits
- WebSocket frames are captured and cleared by `clear_logs`, but no read aspect returns them yet.
- `codex` and `pi` document streamable HTTP. The server serves SSE only.

## 0.1.0 - 2026-10-27

### Features
- feat(build): add version plumbing and a GoReleaser pipeline
- feat(pageops): bundle a finding into a PoC directory
- feat(cli): save a HAR export to a file with --out
- feat(har): export bodies, request data, cookies and protocol
- feat(browsers): tag network entries with their document identity
- feat(browsers): humanize pointer and keyboard timing
- feat(browsers): add a bounded replay fuzz matrix
- feat(browsers): make the interception contract true
- feat(cli): give interception and rate limits full CLI and IPC parity
- feat(browsers): enforce request scope and per-host rate limits
- feat(store): save and restore named auth snapshots
- feat(browsers): coherent UA defaults and stealth verification
- feat(pageops): add CSP bypass probes to the security audit
- feat(cli): replay over CLI and IPC
- feat(browsers): intercept and rewrite requests in flight
- feat(browsers): add sqlite store, websocket capture, and iframe interactives
- feat(extract): tables, forms, request timeout, and replay redaction
- feat(browsers): popup tracking, page cap typing, and close logging
- feat(mcp): canary injection, postMessage capture, declarative page lookup
- feat(mcp): sinks tracer, HAR export, element geometry, loopback bind
- feat(pageops): add sinks read, arm, and clear helpers
- feat(cli): add browser skill to install the agent skill
- feat(browser): image blocks, incognito identities, storage, and honest schemas
- feat(browser): scope, headers, security, replay, and result envelope
- feat(browser): ephemeral profiles, owners, idle TTL, and profile purge
- feat(browser): proxy, network bodies, and browser targeting fixes
- feat(browsers): resolve profiles under config dir with ephemeral secondaries and persist flag
- feat(dissect): add article and marks extraction, act state delta, MCP fixes
- feat(dissect): add a11y, network and console dissection
- feat(browser): page_act handles scoped to document
- feat(browsers): multi-browser manager, shared pageops interaction primitives, profile-aware config
- feat(page_fetch): add interactives-only snapshot mode (what=interactives)
- feat(mcp): add MCP prompts protocol with co-located prompt templates
- feat(tools): implement page_act type action with CLI mirror
- feat(mcp): deterministic tools/list ordering and silent notification handling
- feat(cli): make page fetch accept a URL and open the page
- feat(cli): make page commands human-usable with positional args and examples
- feat(browsers): apply device presets pre-navigation; wire emulate action and --device parity
- feat(browsers): consolidate viewport seam behind engine SetViewport; ctx-bind launch
- feat(cli): add browser focus command to raise the live window
- feat(cli): mirror new page_fetch and page_act surfaces in web-cli
- feat(mcp): add screenshot, css/js resources, and DOM decomposition to page_fetch
- feat(mcp): implement page_act interactions, navigation, and wait modes
- feat(cli): print MCP client config instructions from serve mcp
- feat(cli): daemonize serve with serve stop and serve mcp
- feat(pkg/browsers): add manager TTL sweep and graceful shutdown
- feat(cli): add long-lived serve command and IPC forwarding for persistent browser sessions
- feat(cli): add webmcp-cli with launch, fetch, page, and browser commands
- feat(browsers): wire real fetch_page, browser lifecycle tools, and page_fetch extractors
- feat(browsers): scaffold pkg/browsers core engine and wire MCP tools

### Bug fixes
- fix(build): pin GoReleaser v2.17.0 and add a real CHANGELOG
- fix(mcp): omit the hint on a runtime refusal
- fix(mcp): classify poc_bundle refusals as invalid_args
- fix(cli): bound the action with the caller's deadline
- fix(mcp): classify action errors and name the real fix
- fix(browsers): hold and release the page lock on every path
- fix(pageops): scope the security audit to the audited document
- fix(security): redact every secret occurrence in a body
- fix(browser): fail fast on a missing selector and report live URLs
- fix(browsers): rod-native page creation, shared teardown budget, and input/wait fixes
- fix(browsers): decouple page lifetime ctx from per-call ctx
- fix(cli): make serve stop idempotent when no daemon is running
- fix(browsers): decouple browser default ctx from tool calls; harden close paths
- fix(browsers): probe browser liveness and auto-relaunch after Chromium death
- fix(browsers): close sweep TOCTOU race and double-launch orphan; add injectable logger
- fix(mcp): improve links extraction quality in page_fetch

### Refactoring
- refactor(mcp): share the page cap constant with pageops
- refactor(pageops): derive every action list from one table
- refactor(mcp): replace page_fetch switch with an aspect registry

### Documentation
- docs(mcp,skill): note the network ring dependency for poc_bundle
- docs: split the install paths and add the release manual
- docs(mcp,skill): document the PoC bundle loop
- docs(mcp,skill): describe HAR fields and replay header scope
- docs: document tables, forms, popups, timeout_ms, and replay redaction
- docs(tools): describe live listings and fail-fast selectors
- docs(skills): mark browser_close and page_list as tested
- docs(skills): add web-mcp tool references and examples
- docs: add web-mcp skill reference for the MCP browser tools
- docs: rewrite README for the implemented page_fetch/page_act toolset

### Other
- this is fine
- chore: fix ignore rules for _trash/_examples and gofmt root.go
- chore(examples): add fetch-example make target and examples ignore rules
- chore: stop running serve daemon on install; add launch target
- chore: fix AGENTS.md gitignore pattern and untrack the file
- test(cli): add ipc wire-contract and pageops truncation unit tests
- init
