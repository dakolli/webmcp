# Changelog

## 0.2.0 - 2026-10-29

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

## Features
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

## Bug fixes
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

## Refactoring
- refactor(mcp): share the page cap constant with pageops
- refactor(pageops): derive every action list from one table
- refactor(mcp): replace page_fetch switch with an aspect registry

## Documentation
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

## Other
- this is fine
- chore: fix ignore rules for _trash/_examples and gofmt root.go
- chore(examples): add fetch-example make target and examples ignore rules
- chore: stop running serve daemon on install; add launch target
- chore: fix AGENTS.md gitignore pattern and untrack the file
- test(cli): add ipc wire-contract and pageops truncation unit tests
- init
