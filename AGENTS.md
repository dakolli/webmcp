# AGENTS.md — web-mcp

A Go MCP server that gives LLM agents a **live, stateful browser** built on
[go-rod](https://go-rod.github.io/). The LLM opens a page, reads its full decomposition (DOM tree,
HTML, CSS, JS, text, screenshot), and drives it (click, input, navigate, run JS) across multiple
tool calls. Read `go-rod-primer.md` before writing any rod code.

## Current state (2026-08-13)

- Scaffold compiles (`go build ./...` green): `pkg/mcp/tools` has an interface-compliant `Tool`
  skeleton (fetch/page/act/session tools, `models.InputSchema`); `cmd/mcp/main.go` serves SSE with
  imports fixed to `webmcp/pkg/...`. No real browser work yet — `SessionManager` is a stub.
- Module `webmcp`, Go 1.26.4. rod + launcher deps present; stealth not yet added.
- Next: sketch the `pkg/browsers` core engine (see Architecture) and wire tools to import it.

## Architecture (decided — do not redesign without asking)

**Browser-first paradigm.** The model operates on a two-level entity tree: **Browsers** contain
**Pages** (a.k.a. sessions — live tabs). Browsers can be listed (with their pages), and the model
creates, closes, and interacts with pages *inside* a browser. This is the operating paradigm.

1. **Browser** is a first-class MCP entity: `browser_create` lazily launches a Chromium instance
   (via `launcher`) and returns a `browser_id`; `browser_list` lists live browsers with their
   open pages/sessions; `browser_close` shuts one down (and all its pages).
2. **Pages (sessions) live inside a browser.** `fetch_page { browser_id, url }` opens a URL as a
   new tab in that browser, waits for load, and returns `session_id` + a page summary.
   `page_list { browser_id }` lists the browser's open pages.
3. Agent calls `page_fetch { session_id, what: html|dom|text|css|js|links|screenshot|meta }` to
   read any aspect of the *same live page*.
4. Agent calls `page_act { session_id, action: click|input|eval|navigate|back|forward|reload|hover|scroll|select, ... }`
   to interact. `eval` (JS via `PageEval`) is the primary navigation mechanism; rod built-ins
   (`Element.Click` etc.) are the fast path.
5. `session_close` closes a page; `browser_close` closes its pages and the browser. A
   `BrowserManager` owns the `browser_id → Browser` map; each `Browser` owns its own
   `session_id → Session` map, a per-session mutex (rod Pages are not thread-safe), and idle-TTL
   cleanup.

**Core engine lives outside MCP.** All rod/Chromium logic lives in `pkg/browsers` (package
`browsers`): `manager.go` (`BrowserManager`), `browser.go` (the `Browser` type — holds pointers to
rod pages), and `session.go` (page sessions). `pkg/mcp/tools` only **imports** `pkg/browsers` and
translates MCP calls; the MCP package never touches rod directly.

**Defaults (decided):** the service starts with a **single `Browser`** (its own type) whose pages
are the MCP-accessible sessions; MCP creates pages in that browser. **Headful by default**,
designed so `headless: true` is a clean option. **Stealth** (`github.com/go-rod/stealth`) is applied
to every page by default, with a per-browser/per-page toggle to turn it off.

**Lighter-weight fetcher (go colly) is on the roadmap**, for headless-less HTML/static extraction —
not now.

## Repository layout

- `cmd/mcp/main.go` — MCP server entrypoint (SSE). Keep server logic; fix imports to `webmcp/pkg/...`.
- `pkg/mcp/models/` — JSON-RPC / MCP types (`InputSchema`, `CallToolRequest`, …).
- `pkg/mcp/tools/` — the `Tool` interface, `Registry`, and one file per tool: `fetch.go`, `page.go`,
  `act.go`, `session.go`, `browser.go`. Imports `pkg/browsers`; no direct rod usage.
- `pkg/browsers/` — **core browser engine** (rod only, zero MCP imports): `manager.go`
  (`BrowserManager`), `browser.go` (`Browser` type owning rod pages), `session.go` (page sessions).
- `go-rod-primer.md` — rod quick reference (read before writing rod code).

## Tool interface (mandatory contract)

Every tool is a struct implementing `pkg/mcp/tools.Tool` (`registry.go`); see `pkg/mcp/tools/fetch.go`
for the canonical layout — the demo `AppendNote` interface shape:

```go
type Tool interface {
    Name() string                    // e.g. "fetch_page"
    Description() string             // what it does + when an agent should call it
    InputSchema() models.InputSchema // JSON Schema (properties + required)
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

- `InputSchema` returns `models.InputSchema` (package `models`, **not** `types`).
- Tool names: `browser_create`, `browser_list`, `browser_close`, `fetch_page`, `page_list`,
  `page_fetch`, `page_act`, `session_close`.
- `Execute` parses args → calls `browsers.Manager` (resolve browser → resolve page session) →
  returns a string (text/JSON) for the LLM. Truncate/dehydrate huge outputs (e.g. "first 50KB; use
  `what=css&target=URL` to fetch more").

## Roadmap

1. **Phase 0 — Foundation (now)**: fix `registry.go` (Tool interface + empty registry, drop stale
   todo tools), rewrite `fetch.go` as the interface-compliant skeleton, sketch the `pkg/browsers`
   core (`manager.go` + `Browser` type + page sessions; single browser, headful default, stealth on
   by default), wire tools to import it, fix `main.go` imports, add rod + launcher + stealth deps,
   `go build ./...` green. *No real browser work.*
2. **Phase 1 — Fetch core**: real `browser_create`/`browser_list`/`browser_close` +
   `fetch_page` (launch, navigate, wait, summary) + `page_fetch` for `html`, `text`, `meta`,
   `links`, and `page_list`/`session_close`. `BrowserManager` with TTL + graceful shutdown.
3. **Phase 2 — Full decomposition**: `dom` (via `CaptureDOMSnapshot`, flattened + string table),
   `css`/`js` (via `GetResource` + `proto.PageGetResourceTree`), `screenshot`, per-resource
   fetching. Produce LLM-friendly shaped output.
4. **Phase 3 — Interact**: `page_act` — click/input/hover/select via selectors, `eval` escape
   hatch, navigate/back/forward/reload, wait helpers (`WaitInteractable`, `WaitDOMStable`).
5. **Phase 4 — Browser realism**: viewport/device emulation, user-agent, cookies, request
   hijacking (capture CSS/JS in-flight), auth dialogs, incognito sessions, page/browser pools.
6. **Phase 5 — Scale & fallback**: colly-based lightweight fetcher for static pages, Docker
   (launcher manager image), session limits, observability.

## Go conventions (mandatory)

- **Idiomatic, production-grade Go.** `gofmt`/`gofmt -s`, `go vet`, `go build ./...`, and
  `go test ./...` must pass before finishing any task.
- **Godoc comments** on every exported symbol (and meaningful unexported ones), starting with the
  name (`// Fetch opens a URL in a new browser session.`). Also `// TODO(web-mcp): …` for stubs.
- **No `Must*` rod methods in production code** — only error-returning forms; never panic across
  the MCP boundary. Wrap a Must-block with `rod.Try` only if unavoidable.
- Always thread `context.Context` (from `Execute`) through to rod calls; set explicit timeouts
  (e.g. 30s per tool call) so a hung page cannot block the server.
- Wrap errors with `%w`; return LLM-actionable error strings from `Execute`.
- One live page per session; serialize per-session access with the session mutex.

## Commands

- `go build ./...` · `go test ./...` · `go vet ./...` · `gofmt -l .`
- `go get github.com/go-rod/rod@latest github.com/go-rod/rod/lib/launcher`
- First `fetch_page` run downloads Chromium (~100MB) via the launcher; in containers set
  `launcher.New().NoSandbox(true)`.
