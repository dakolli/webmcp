# web-mcp

A Go MCP server that gives LLM agents a **live, stateful browser** built on
[go-rod](https://go-rod.github.io/). The LLM opens a page, reads its full
decomposition (DOM tree, HTML, CSS, JS, text, screenshot), and drives it
(click, input, navigate, run JS) across multiple tool calls.

> **Status:** this README documents the full **MCP toolset** (see
> [MCP](#mcp)) and its human-facing mirror, the **`web-cli`** CLI. Both drive
> the same `pkg/browsers` core engine, and the full page-reading and
> page-interaction surfaces described here are implemented and live.

## Install

Requires Go 1.26+.

```sh
go install ./cmd/web-cli
```

This installs the `web-cli` binary into `$(go env GOPATH)/bin`.

> First run downloads Chromium (~100 MB) via the go-rod launcher. In
> containers, set `launcher.New().NoSandbox(true)`.

## Quick start

```sh
# 1. Start the shared browser as a background daemon (headful Chromium, stealth
#    on). fetch/page commands also launch it lazily, so this step is optional.
web-cli serve

# 2. Open a URL in a new page — prints session_id, title, url, text snippet,
#    html bytes and load time.
web-cli fetch https://example.com

# 3. Inspect and interact with the live page. With one page open the id is
#    optional; `last` means the most recently opened page; a bare number
#    resolves to page-N. The --id/--what/--action flag forms still work.
web-cli page list
web-cli page fetch links
web-cli page act eval '() => document.title'
web-cli page fetch --id page-1 --what meta

# 4. Close a page, the browser, or the whole serve daemon.
web-cli page close
web-cli browser close
web-cli serve stop
```

## Commands

The CLI mirrors the MCP tools one-to-one, talking directly to the
`pkg/browsers` core engine (never through `pkg/mcp`). Every command accepts
`--browser-id` (default `browser-1`), the id of the single managed browser,
and `--socket` (the path of the serve daemon's IPC socket).

| Command | Mirrors | Description |
| --- | --- | --- |
| `web-cli serve` | `browser_create` | Start the shared browser as a background **daemon**: it binds the IPC socket, launches Chromium, answers the readiness ping and returns to the prompt (status in `$XDG_STATE_HOME/web-cli/serve.log`, else `~/.web-cli/serve.log`). A second `serve` fails fast on the bound socket. Flags `--headless` (default headful), `--stealth` (default on), `--socket`, and `--foreground` (run in the terminal instead; Ctrl-C stops it). Stop the daemon with `web-cli serve stop`. |
| `web-cli serve stop` | — | Stop the serve daemon via the `server.shutdown` IPC message; prints a friendly notice (and exits 0) when no daemon is running. |
| `web-cli serve mcp --port <n>` | — | Daemonize exactly like `serve` and additionally host the MCP HTTP/SSE server on port `<n>` (default 4099) in the same process, so MCP tools and the CLI drive the same live browser. Prints the client-config snippet on startup. |
| `web-cli browser list` | `browser_list` | List the live browser and its open pages (`id\turl` rows). Never launches the browser. |
| `web-cli browser close` | `browser_close` | Close the browser and all of its pages. Idempotent when nothing is open. Note: this closes the *browser*, not the serve daemon — stop the daemon with `web-cli serve stop`. |
| `web-cli fetch <url>` | `fetch_page` | Open `<url>` in a new page and print a summary (`session_id`, title, url, text snippet, html bytes, load ms). Flags: `--json`, `--wait idle\|load\|stable` (default `idle`), `--timeout-ms` (default `30000`). |
| `web-cli page list` | `page_list` | List the browser's open pages (`id\turl`). |
| `web-cli page fetch [id] [what]` | `page_fetch` | Extract an aspect of a live page. `[what]` is one of `html`, `text`, `links`, `meta`, `dom`, `css`, `js`, `screenshot` (default `text`); `[id]` is optional with one page open, `last` = most recently opened, and a bare number (`12`) resolves to `page-12`. The `--id`/`--what` flag forms remain supported. Optional `--selector` (CSS) scopes html/text; `--target <url>` fetches exactly one css/js resource's full content instead of the `{url,type,size}` list; `--full-page` captures the entire scrollable page (screenshot only); `--max-bytes` caps output (default `50000`, truncation is flagged; screenshots are never truncated). `links`, `meta`, `dom` and the css/js lists print JSON; `screenshot` prints a `data:image/png;base64,…` data-URI. |
| `web-cli page act [id] [action] [payload...]` | `page_act` | Perform an action on a live page: `click`, `input`, `eval`, `navigate`, `back`, `forward`, `reload`, `hover`, `scroll`, `select`, `wait` — all implemented; `type` is accepted but still errors "not implemented". `[id]` follows the same conveniences as `page fetch`. Positional payloads fill each action's fields (`click "#x"`, `input "#q" "text"`, `navigate <url>`, `scroll 0 500`, `eval '() => document.title'`, …); the `--action`/`--selector`/`--value`/`--x`/`--y` flag forms remain supported. |
| `web-cli page close [id]` | `session_close` | Close the page, releasing its tab (`[id]` optional with one page open; `last`; `12` → `page-12`; `--id` still works). The session id becomes invalid. |

### Example session

```sh
$ web-cli serve
browser_id: browser-1
socket: /run/user/1000/web-mcp.sock
daemonized (log: /home/dakolli/.web-cli/serve.log); stop with 'web-cli serve stop'

$ web-cli fetch https://go-rod.github.io/
session_id: page-1
title: go-rod
url: https://go-rod.github.io/
text_snippet: go-rod — A high-efficiency driver for Chromium...
html_bytes: 42119
load_ms: 1420

$ web-cli page fetch links --max-bytes 2000     # single open page: no id needed
[{"text":"Github","href":"https://github.com/go-rod/rod"},...]

$ web-cli page fetch dom
[{"title":"go-rod","url":"https://go-rod.github.io/","root":{"tag":"#document","children":[{"tag":"html",...}]}}]

$ web-cli page fetch screenshot --full-page | head -c 120
data:image/png;base64,iVBORw0KGgoAAAANSUhEUgA...

$ web-cli page act eval '() => document.title'
eval result: "go-rod"

$ web-cli page act click 'nav a'
clicked selector="nav a"

# Open a second page — now the id is required (or use `last`).
$ web-cli fetch https://example.org
session_id: page-2
...

$ web-cli page fetch last links                  # 'last' = most recently opened → page-2
[{"text":"Example Domain","href":"https://www.iana.org/domains/example"},...]

$ web-cli page fetch                            # two pages → helpful ambiguity error
page fetch: multiple pages open — pass an explicit page id or 'last':
  page-1	https://go-rod.github.io/
  page-2	https://example.org

$ web-cli page fetch 1 text                     # bare number → page-1
...

$ web-cli page act 1 navigate https://news.ycombinator.com
navigated to https://news.ycombinator.com

$ web-cli page close 1                          # close page-1 (or `page close last`)
session page-1 closed

$ web-cli browser close
browser browser-1 closed

$ web-cli serve stop
stopped
```

Every engine call is bound to a per-call timeout (default 30s) so a hung page
cannot block the CLI; `SIGINT`/`SIGTERM` also triggers a graceful browser
shutdown.

### Human tips

- The page **id is optional when only one page is open** — `web-cli page fetch
  links` reads the single open page, `web-cli page act click "#search"` drives
  it, `web-cli page close` closes it.
- **`last`** means the most recently opened page (`web-cli page fetch last
  links`).
- A **bare number resolves to `page-N`** (`web-cli page fetch 12 dom` →
  `page-12`).
- With several pages open, an omitted or unknown id prints the open pages
  (`id<TAB>url` rows) so you can copy the one you recognize.
- All the legacy `--id`/`--what`/`--action` flag forms still work unchanged for
  scripts and LLMs.

## Testing

Run the full test suite:

```sh
go test ./...
```

Useful variants:

- `go test -count=1 ./...` — bypass the test cache and always run fresh.
- `go test -race ./...` — enable the race detector (good for the IPC
  server's Serve/Close/connection-set concurrency paths).
- `go test -v ./pkg/cli/ipc/...` — verbose output for one package.
- `go test ./pkg/cli/ipc/ ./pkg/cli/pageops/` — just the packages that
  currently have tests (the IPC wire contract + pageops truncation pins);
  other packages report `[no test files]`.

Before finishing any change, the repo conventions require all of these to pass:

```sh
gofmt -l .     # prints nothing (files are formatted; use -s for simplification)
go vet ./...   # static analysis
go build ./... # everything compiles
go test ./...  # all tests pass
```

> Tests never launch a real browser: the IPC suite injects a stub browser
> manager, so `go test ./...` needs no Chromium download and runs in under a
> second.

## MCP

`web-cli serve mcp` hosts the MCP HTTP/SSE server inside the serve daemon, so
LLM agents drive the **same live browser** as the CLI over MCP. The toolset
mirrors the CLI commands one-to-one:

| Tool | Purpose | Key params |
| --- | --- | --- |
| `browser_create` | Lazily launch the shared live browser (one Chromium instance) and return its `browser_id`. | — (optional `headless`/`stealth`, reserved) |
| `browser_list` | List the live browser with its open pages; `[]` when nothing is running (never launches). | — |
| `browser_close` | Close the browser and all of its pages. | `browser_id` |
| `fetch_page` | Open a URL in a new session; returns `session_id` + a page summary. Call FIRST. | `url`, `wait_for`, `viewport`, `user_agent`, `timeout_ms` |
| `page_list` | List a browser's open pages. | `browser_id` |
| `session_list` | List every live session (manager-wide equivalent of `page_list`). | — |
| `page_fetch` | Read any aspect of a live page. | `session_id`, `what`, `selector`, `max_bytes`, `full_page`, `target` |
| `page_act` | Interact with a live page. | `session_id`, `action`, `selector`, `value`, `x`, `y`, `timeout_ms`, `wait_mode` |
| `session_close` | Close a session; its `session_id` becomes invalid. | `session_id` |

Every call is bound to a per-call timeout (default 30s), so a hung page cannot
block the server.

### `page_fetch` — what you can read

`what` selects the aspect:

- `html` — the page's outerHTML (`selector` scopes it to one element).
- `dom` — the full DOM re-nested from a CDP flattened snapshot (via
  `CaptureDOMSnapshot`) into a compact, LLM-friendly tree — iframes, shadow
  DOM and all, with `id`/`class` hoisted and layout info (visibility, bounds)
  attached.
- `text` — the page's visible text (`selector` scopes it).
- `css` / `js` — a JSON list of the page's `{url, type, size}` style/script
  resources; pass `target=<url>` to fetch exactly one resource's full content
  (prefixed with a `// url=… // bytes=…` header).
- `links` — a JSON array of `{text, href}` anchors, deduplicated by href with
  non-navigable links (`javascript:`, `mailto:`, `data:`, `tel:`, `#`)
  dropped.
- `screenshot` — a base64 PNG data-URI (`data:image/png;base64,…`); pass
  `full_page: true` to capture the entire scrollable page instead of the
  visible viewport.
- `meta` — the page title, URL and collected `<meta>` tags as JSON.

`max_bytes` (default `50000`) caps every text extraction: output is cut on a
UTF-8 rune boundary and a truncation note is appended, so the agent can
re-request with a narrower `selector`, a `target` resource URL, or a higher
cap. **Screenshots are the exception** — the data-URI bypasses truncation
entirely, because image size scales with the viewport (or full-page height),
not `max_bytes`, and cutting a data-URI mid-stream would corrupt it.

### `page_act` — what you can do

`action` selects the interaction:

- `click` / `hover` — a CSS or XPath `selector`; click waits until the element
  is interactable first.
- `input` — set a field's value (`selector` + `value`).
- `type` — deferred: still errors "not implemented".
- `eval` — run arbitrary JavaScript, the primary escape hatch. Pass a JS
  function (e.g. `() => document.title`); the JSON result is returned and JS
  exceptions surface verbatim.
- `navigate` — go to a URL and wait for it to load.
- `back` / `forward` / `reload` — history navigation, each waiting for load.
- `scroll` — scroll by `x`/`y` pixels; returns the resulting scroll position.
- `select` — pick an `<option>` by `value` in the element matched by
  `selector`.
- `wait` — block until the page satisfies a readiness level.

`wait` supports these `wait_mode`s: `load` (window.onload), `idle` (no network
requests for ~500ms), `stable` (DOM unchanged for ~500ms), `interactable` (the
element matched by `selector` is clickable), and `ms` (sleep for `timeout_ms`;
the default).

### Session lifecycle

`fetch_page` creates a session (id `page-N`) inside the live browser; every
subsequent `page_fetch` / `page_act` targets it via `session_id`. Sessions live
until `session_close` (or `browser_close`), after which their `session_id`
becomes invalid. The browser itself is launched lazily by
`browser_create`/`fetch_page` and torn down by `browser_close`.

### Browser liveness (self-healing)

The engine probes Chromium's liveness on every call, so a dead process is never
reported as healthy:

- lists go honest — `browser_list` returns `[]` (or `page_list` an empty array)
  instead of stale pages;
- `browser_create` and `fetch_page` silently relaunch a fresh browser;
- a `page_fetch` / `page_act` / `session_close` referencing a pre-restart
  session id fails with an explicit error: the browser process died and was
  restarted, all previous sessions are gone — call `fetch_page` to re-open
  URLs.

### Start the MCP server

```sh
web-cli serve mcp --port 4099
```

This daemonizes exactly like `web-cli serve` (same socket, same log) and
additionally serves the SSE endpoint at `http://127.0.0.1:4099/sse`. It prints
the client config snippet with your actual port on startup.

### Add it to your MCP client config

Add an entry to your MCP client's config file — e.g. Claude Desktop's
`claude_desktop_config.json`, a `mem-mcp` client config, or any SSE-capable MCP
client. The server key is the name you assign to the server (here:
`web-mcp`):

```json
"web-mcp": {
    "type": "remote",
    "url": "http://127.0.0.1:4099/sse",
    "enabled": true
}
```
