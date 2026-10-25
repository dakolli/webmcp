# web-mcp 🚀

A Go **browser engine** with two front-ends — an MCP server for LLM agents and a `browser` CLI for
humans — over one live, stateful Chromium driven by [go-rod](https://go-rod.github.io/).

Two things it is for:

1. **Browsing for agents.** Open pages, click, type, press keys, scroll, upload, run JS, take
   screenshots — all against a real browser that keeps state across tool calls.
2. **Dissecting pages.** Ask granular structural questions about a live page: the full DOM tree with
   bounds and visibility, every interactive element with a stable handle, the loaded CSS/JS resources,
   links, meta, iframes and shadow DOM. The goal is for an LLM to be able to *build* a scraper or
   automation for a site (or edit that site) without guessing.

---

## Features

- 🌐 **Live, stateful browser**: sessions are real tabs that survive navigations, redirects, and
  `back`/`forward` between tool calls.
- 📐 **Granular decomposition**: `dom` (frames + shadow DOM + bounds + visibility), `interactives`
  (dense action map with `g<document>-b<backendNodeId>` handles), `css`/`js` resource trees with
  source fetch,
  `meta`, `links`, `text`, `html`, `screenshot`.
- 🖱️ **19 interaction actions**: click, input, type, key combos (`Control+a`, `Meta+c`), coordinate
  mouse (`click_at`/`hover_at`), drag & drop, file upload, eval, navigate/history, hover, scroll,
  select, wait modes (`load`/`idle`/`stable`/`interactable`/`ms`), viewport and device emulation.
- 🧭 **Multi-browser aware**: a manager owns a map of browsers, each with its own pages; `browser-1`
  is the default.
- 🥷 **Stealth on by default** (`go-rod/stealth`) and **persistent profiles** under
  `~/.config/browser-mcp/profiles/<name>`, so cookies and logins survive restarts.
- 📱 **Opt-in device emulation** (`IPhoneX`, `IPadPro`, `LaptopWithHiDPIScreen`), applied before
  navigation; pages otherwise launch plain at your real display metrics.
- ⚡ **Dual interface**: the `browser` CLI mirrors the MCP tools 1:1, plus a daemon over a Unix socket
  so the same browser survives between shell commands.

---

## Installation

Requires Go 1.26+. First run downloads Chromium (~100 MB) unless a host Brave/Chrome/Chromium is
found on `PATH` (probe order: `brave-browser` → `brave` → `google-chrome` → `chrome` → `chromium`).

```sh
go install ./cmd/browser     # installs the `browser` binary
make install                 # same, after stopping any running daemon
```

---

## Quick Start

### 1. Launch the server

```sh
browser serve mcp --port 4089
# daemonized: CLI + Unix socket + MCP HTTP/SSE on http://127.0.0.1:4089/sse
```

```sh
browser serve mcp --port 4089 --profile brave --bin brave-browser   # persistent Brave profile
browser serve mcp --port 4089 --headless                            # headless
browser serve stop                                                  # or: browser stop
browser serve --foreground                                          # stay attached (debug)
```

`--port` defaults to `4099` when omitted; the Makefile and the examples here use `4089`.
The MCP server binds all interfaces and has **no authentication** — keep it local-only.

### 2. Use it from the CLI

```sh
browser fetch https://news.ycombinator.com --wait stable
# -> session_id page-1, title, url, text snippet, html size, load time

browser page fetch last meta
browser page fetch last links
browser page fetch last interactives            # handles + roles + text + x/y
browser page fetch last dom --max-bytes 30000
browser page act click ".titleline a"
browser page act click --handle g3k9x2ab-b6      # handle from the same document only
browser page act eval "() => document.title"
browser page close last
```

### 3. Connect an MCP client

```json
"web-mcp": {
    "type": "remote",
    "url": "http://127.0.0.1:4089/sse",
    "enabled": true
}
```

---

## MCP Tools

| Tool | Purpose |
| :--- | :--- |
| `browser_create` | Launch/reuse a browser (`bin`, `profile`, `headless`, `stealth`, `position`). |
| `browser_list` | List live browsers and their pages. |
| `browser_close` | Close one browser, or all when `browser_id` is empty. |
| `fetch_page` | Open a URL in a new session and return `session_id` + summary. |
| `page_list` | List the pages of a browser. |
| `session_list` | List all live sessions. |
| `session_close` | Close a session (tab). |
| `page_fetch` | Read one aspect of a live page (see below). |
| `page_act` | Perform one interaction action on a live page. |

---

## Page Extractions (`page_fetch`)

`what` accepts nine values. `selector` scopes `html`, `text`, and `dom`; `max_bytes` defaults to
50000; `full_page` applies to screenshots.

| `what` | Returns |
| :--- | :--- |
| `dom` | Re-nested DOM tree: every document/frame, `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`, `children`. Every element node carries a `g<document>-b<backendNodeId>` handle. Shadow DOM included, script/style bodies elided. |
| `interactives` | `{interactives:[{handle, role, text, href, type, x, y, visible}], truncated}` — visible clickable/typeable elements, capped at 200. Handles are `g<document>-b<backendNodeId>`, usable as `page_act`'s `handle`. |
| `links` | `[{text, href, handle?}]`, deduped by href, non-navigable schemes filtered; `handle` is the first anchor carrying that href. |
| `meta` | `{title, url, meta:[{name/property/http-equiv/content/charset}]}`. |
| `text` | Visible `innerText`. |
| `html` | `outerHTML`. |
| `css` / `js` | Loaded stylesheet/script resources as `[{url, type, size}]`; pass `target=<url>` to fetch one resource's raw source. |
| `screenshot` | Base64 PNG data-URI; `full_page: true` captures the whole scrollable page. |

`dom`, `links`, `meta`, and the css/js list are byte-truncated at `max_bytes`; `interactives` and
`screenshot` are never truncated (interactives reports `truncated` as an omitted count).

---

## Page Actions (`page_act`)

```
click · input · type · press_key · click_at · hover_at · drag_and_drop · upload_file
eval · navigate · back · forward · reload · hover · scroll · select · wait
set_viewport · emulate
```

`press_key` accepts named keys and combos: `Enter`, `Escape`, `Tab`, `ArrowDown`, `F5`,
`Control+a`, `Meta+c`, `Shift+Tab`. `input` selects-all then types (value is replaced); `type`
inserts at the caret. `wait_mode` is one of `load`, `idle`, `stable`, `interactable`, `ms`.

Element actions (`click`, `input`, `type`, `press_key`, `hover`, `select`, `wait` in `interactable`
mode, `upload_file`) also accept a `handle` from `page_fetch` (`interactives`, `dom`, `links`) instead
of a selector; the handle wins when both are supplied. Handles look like
`g<document>-b<backendNodeId>` and go stale when the element is removed or re-rendered, or when the
page navigates, reloads or redirects (the handle carries the document that minted it) — re-run the
fetch to mint fresh ones.

---

## CLI ⇄ MCP Parity

The CLI exposes the same capabilities with shell conveniences:

| CLI | MCP |
| :--- | :--- |
| `browser fetch <url>` | `fetch_page` |
| `browser page list` | `page_list` / `session_list` |
| `browser page fetch [id\|url] [what]` | `page_fetch` |
| `browser page act [id] [action] [payload...] [--handle g<doc>-b<id>]` | `page_act` |
| `browser page close [id]` | `session_close` |
| `browser create` / `list` / `close` | `browser_create` / `browser_list` / `browser_close` |
| `browser focus` | — (CLI-only: raise the window) |
| `browser serve` / `stop` | — (CLI-only: daemon lifecycle) |

CLI-only conveniences: `--json` on `fetch`, page-id shorthand (`last`, `5` → `page-5`, single open
page by default), one-step `page fetch <url> [what]`, and `--max-bytes 0` for unbounded output.

---

## Operating Notes

- **Sessions expire** after 15 minutes idle. If a `session_id` disappears or the browser restarted,
  re-open the page; the error message says so.
- **Always pass `wait_for`** to `fetch_page` (`load` or `stable`); omitting it currently fails rather
  than defaulting.
- **50 KB default output cap.** Narrow with `selector` / `target` rather than raising it blindly.
- **Local-only by default.** No auth, permissive CORS, arbitrary `eval` and `upload_file` paths.
- Known gaps and the roadmap: `improvements.md` in this repo, plus the mininote ROADMAP.

---

## License

MIT License.
