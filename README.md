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
  (dense action map with `g<document>-b<backendNodeId>` handles), `a11y` (computed accessibility
  tree: roles, accessible names, values, state flags), `article` (readable body as markdown),
  `marks` (numbered overlay boxes + annotated screenshot), `css`/`js` resource trees with
  source fetch,
  `meta`, `links`, `text`, `html`, `screenshot`.
- 🖱️ **20 interaction actions**: click, input, type, key combos (`Control+a`, `Meta+c`), coordinate
  mouse (`click_at`/`hover_at`), drag & drop, file upload, eval, navigate/history, hover, scroll,
  select, wait modes (`load`/`idle`/`stable`/`interactable`/`ms`), viewport and device emulation,
  and `clear_logs`.
- 🧭 **Multi-browser aware**: a manager owns a map of browsers, each with its own pages; `browser-1`
  is the default.
- 🥷 **Stealth on by default** (`go-rod/stealth`) and **profiles under the config dir**
  (`~/.config/browser-mcp/profiles/`). `browser-1` keeps its profile and logins across restarts.
  Secondary browsers are ephemeral by default and their profile is removed on close.
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

### Profiles

- A bare profile name resolves under `~/.config/browser-mcp/profiles/<name>`.
- An absolute path is used as given. A relative path with a separator is rejected.
- `browser-1` with no profile uses the persistent default profile.
- A secondary browser gets an ephemeral profile under `profiles/.ephemeral/`.
  The manager removes that profile on close, shutdown, or launch failure.
- Add `--persist` to keep a named profile and its logins after close.
  The MCP equivalent is `"persist": true` on `browser_create`.

### 2. Use it from the CLI

```sh
browser fetch https://news.ycombinator.com --wait stable
# -> session_id page-1, title, url, text snippet, html size, load time

browser page fetch last meta
browser page fetch last links
browser page fetch last interactives            # handles + roles + text + x/y
browser page fetch last dom --max-bytes 30000
browser page fetch last article                  # readable body as markdown
browser page fetch last marks                    # numbered boxes + annotated screenshot
browser page fetch last network                 # newest 100 requests + counters
browser page act click ".titleline a"
browser page act last clear_logs                # reset both log buffers
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
| `browser_create` | Launch/reuse a browser (`bin`, `profile`, `persist`, `headless`, `stealth`, `position`). |
| `browser_list` | List live browsers and their pages. |
| `browser_close` | Close one browser, or all when `browser_id` is empty. An unknown id fails with `browser "browser-42" not found (running: browser-1)`. |
| `fetch_page` | Open a URL in a new session and return `session_id` + summary. `wait_for` defaults to `idle`. |
| `page_list` | List the pages of a browser. `browser_id` is required, and an unknown id fails with `browser "browser-42" not found (running: browser-1)`. |
| `session_list` | List all live sessions. |
| `session_close` | Close a session (tab). |
| `page_fetch` | Read one aspect of a live page (see below). |
| `page_act` | Perform one interaction action on a live page. |

### MCP Prompts

`prompts/list` returns five prompt templates. They are templates, not tools: each one returns a short
step-by-step message that names the tools to call.

| Prompt | Purpose | Arguments |
| :--- | :--- | :--- |
| `fetch_and_summarize` | Open one URL, read `meta` and `text`, then summarize the page. | `url` (required) |
| `inspect_page_decomposition` | Decompose a live session into `meta`, `links` and `dom`. | `session_id` (required) |
| `form_search_and_submit` | Fill a search or form field, submit it, and wait for the result. | `session_id` and `search_query` (required), `selector` (optional) |
| `browser_setup_and_emulate` | Create a browser, open a page, and apply an optional device preset. | `device` (optional) |
| `manage_session_lifecycle` | Audit live sessions and close the stale ones. | none |

Call `prompts/get` with the prompt name and its arguments to receive the message. The prompt only
guides the workflow. You still run `fetch_page`, `page_fetch` and `page_act` yourself.

---

## Page Extractions (`page_fetch`)

`what` accepts fourteen values. `selector` scopes `html` and `text`. `max_bytes` defaults to
50000. `full_page` applies to screenshots.

| `what` | Returns |
| :--- | :--- |
| `dom` | Re-nested DOM tree: every document/frame, `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`, `children`. Every element node carries a `g<document>-b<backendNodeId>` handle. Shadow DOM included, script/style bodies elided. |
| `interactives` | `{interactives:[{handle, role, text, href, type, x, y, visible}], truncated}` — visible clickable/typeable elements, capped at 200. Handles are `g<document>-b<backendNodeId>`, usable as `page_act`'s `handle`. |
| `a11y` | `{nodes:[{handle?, depth, role, name?, value?, states?}], count, truncated}` — flattened accessibility tree in DFS order: computed roles, accessible names, values, and the state flags that are true (checked, expanded, selected, disabled, required, pressed, invalid, focused; `flag=mixed` when the value is mixed). Capped at 400. Handles on DOM-backed nodes are usable as `page_act`'s `handle`. Root frame only. Text-fragment roles (StaticText, InlineTextBox, paragraph, code, …) and unnamed images are omitted, and `name`/`value` are clipped at 200 runes — use `text` for prose. |
| `network` | `{entries:[{method, url, type?, status?, mime?, bytes?, ms?, failed?, error?, seq}], count, dropped, truncated}` — observed requests/responses. Emits the newest 100 buffered entries in chronological order; `dropped` counts entries lost to the 200-entry ring, `truncated` counts buffered older entries omitted by the 100 cap. Capture is cumulative from page creation. `data:` URLs are skipped; `url`, `error` and `text` are clipped at 300, 200 and 500 bytes. |
| `console` | Same envelope over `{level, text, source?, line?, seq}` — `console.*` calls, uncaught exceptions and browser log entries. Cumulative from page creation. |
| `article` | `{title, byline?, excerpt?, text, words, truncated, url}` — the readable article body rendered as markdown (headings, paragraphs, lists, code, quotes, links, images, tables) with nav/aside/footer/forms/scripts stripped. `text` is capped at 40000 runes and `truncated` reports the cut. Prefer it over `text` for prose; never byte-truncated. |
| `marks` | `{marks:[{index, role, text?, x, y, width, height}], screenshot, viewport_width, viewport_height, truncated}` — a numbered box over every visible interactive element plus the annotated viewport as a base64 PNG data-URI (`screenshot`). `x`/`y` are the box's top-left in viewport CSS px, so `x + width/2`, `y + height/2` feed `page_act`'s `click_at`. Capped at 60 marks; `truncated` counts the remainder. The overlay is removed again before returning. |
| `links` | `[{text, href, handle?}]`, deduped by href, non-navigable schemes filtered; `handle` is the first anchor carrying that href. |
| `meta` | `{title, url, meta:[{name/property/http-equiv/content/charset}]}`. |
| `text` | Visible `innerText`. |
| `html` | `outerHTML`. |
| `css` / `js` | Loaded stylesheet/script resources as `[{url, type, size}]`; pass `target=<url>` to fetch one resource's raw source. |
| `screenshot` | Base64 PNG data-URI; `full_page: true` captures the whole scrollable page. |

`dom`, `links` and `meta` always return parseable JSON. When the result does not fit `max_bytes`,
the tool returns an envelope with `truncated: true`, the applied `max_bytes`, and `omitted` (the
number of entries dropped). `links` returns `{items, omitted, truncated, max_bytes}`. `dom` and
`meta` keep their fields and trim their first array field. Raise `max_bytes` to read the whole
result. `selector` does not apply to these three. `html`, `text` and the css/js list are cut at
`max_bytes` as plain text.

`interactives`, `a11y`, `network`, `console`, `article`, `marks`, and `screenshot` are never
byte-truncated: `interactives`, `a11y` and `article` report `truncated` as an omitted-node/cut-body
signal, `network` and `console` report `truncated` (buffered older entries left out) plus `dropped`
(lost to the 200-entry ring), and `marks` carries a screenshot that must stay decodable.

---

## Page Actions (`page_act`)

```
click · input · type · press_key · click_at · hover_at · drag_and_drop · upload_file
eval · navigate · back · forward · reload · hover · scroll · select · wait
set_viewport · emulate · clear_logs
```

`press_key` accepts named keys and combos: `Enter`, `Escape`, `Tab`, `ArrowDown`, `F5`,
`Control+a`, `Meta+c`, `Shift+Tab`. `input` selects-all then types (value is replaced); `type`
inserts at the caret. `wait_mode` is one of `load`, `idle`, `stable`, `interactable`, `ms`.
`clear_logs` takes no parameters and returns `{"network":N,"console":M}`. Network and console
capture is cumulative from page creation, so the per-step recipe is
`clear_logs` → act → `page_fetch` `network`/`console` → filter on the client side.

Element actions (`click`, `input`, `type`, `press_key`, `hover`, `select`, `wait` in `interactable`
mode, `upload_file`) also accept a `handle` from `page_fetch` (`interactives`, `dom`, `links`,
`a11y`) instead
of a selector; the handle wins when both are supplied. Handles look like
`g<document>-b<backendNodeId>` and go stale when the element is removed or re-rendered, or when the
page navigates, reloads or redirects (the handle carries the document that minted it) — re-run the
fetch to mint fresh ones.

Arguments are checked before the page is touched, so a missing one fails fast with a one-line hint.
`click`, `input`, `type`, `hover`, `select` and `upload_file` need `selector` or `handle`:

```
page_act: click requires selector (or a handle from page_fetch what=interactives)
```

`click_at` and `hover_at` need `x` and `y`. `eval` and `navigate` need `value`. `set_viewport` needs
`viewport`, and `emulate` needs `device`.

When an action changes the page address or title, the result gains one final line. Only the fields
that changed appear. A click that navigates reports the new destination:

```
clicked selector="a.next"
→ url=https://example.com/next title="Next page"
```

Nothing is added when the page did not change. `navigate` never adds the line because it reports the
destination itself.

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
- **`wait_for` defaults to `idle`.** Omit it on a normal page. Pass `load` or `stable` on JS-heavy
  sites, where the idle network-wait can stall.
- **50 KB default output cap.** Narrow with `selector` / `target` where those apply. Raise
  `max_bytes` for `dom`, `links` and `meta`, which return a truncated envelope instead of cutting
  the JSON.
- **Local-only by default.** No auth, permissive CORS, arbitrary `eval` and `upload_file` paths.
- Known gaps and the roadmap: `improvements.md` in this repo, plus the mininote ROADMAP.

---

## License

MIT License.
