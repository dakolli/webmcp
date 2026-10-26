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
- 🧭 **Multi-browser aware**: a manager owns a map of browsers, each with its own pages and owner;
  `browser-1` is just the first id minted, not a shared singleton.
- 🥷 **Stealth on by default** (`go-rod/stealth`) and **profiles under the config dir**
  (`~/.config/browser-mcp/profiles/`). Browsers are ephemeral by default — their profile is
  deleted on close. Pass `--persist` / `"persist": true` to keep a named profile and its logins.
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
make install-dev             # DEV RESET: see below
```

`make install-dev` stops the daemon, **deletes the whole config directory**
(`~/.config/browser-mcp`: every profile including saved logins and cookies, `config.json`, and
`proxy.json` with its proxy credentials), reinstalls the binary, and relaunches
`browser serve mcp` on `PORT` (default 4089). It refuses to run when `BROWSER_MCP_CONFIG` or
`CONFIG_DIR` points outside `$HOME`, or when `HOME` is unset. `make install` is unchanged and
non-destructive.

---

## Quick Start

### 1. Launch the server

```sh
browser serve mcp --port 4089
# daemonized: CLI + Unix socket + MCP HTTP/SSE on http://127.0.0.1:4089/sse
# starts no browser — agents create their own with browser_create (or a first fetch_page)
```

```sh
browser serve mcp --port 4089 --profile brave --bin brave-browser   # persistent Brave profile
browser serve mcp --port 4089 --headless                            # headless
browser serve stop                                                  # or: browser stop
browser serve --foreground                                          # stay attached (debug)
```

`--port` defaults to `4099` when omitted; the Makefile and the examples here use `4089`.
The MCP server binds all interfaces and has **no authentication** — keep it local-only.

### Profiles and browser lifecycle

- Every profile lives under `~/.config/browser-mcp/profiles/`. A bare profile name resolves to
  `profiles/<name>`; an absolute path is used as given; a relative path with a separator is rejected.
- **`browser serve mcp` starts no browser.** The daemon binds the IPC socket and the MCP/SSE port
  and waits; `browser_list` returns `[]` until an agent creates a browser with `browser_create` (or
  the first `fetch_page` creates one owned by that caller). Close it when done. Plain
  `browser serve` still pre-launches the persistent `browser-1` for the human CLI path.
- **Browsers are ephemeral by default.** A browser gets a throwaway profile under
  `profiles/.ephemeral/` that is deleted on close or when the browser expires. The MCP
  (`browser serve mcp`) path and a bare in-process `browser create` are ephemeral.
- Add `--persist` (CLI) or `"persist": true` (`browser_create`) to keep a named profile and its
  logins after close. The plain human `browser serve` daemon keeps its lazily launched `browser-1`
  persistent.
- **Close is the normal end.** `browser close <id>` closes one browser and its pages.
  `--purge` also deletes that browser's profile (config-dir guarded). `--prune` closes every browser
  and removes orphan `profiles/.ephemeral/` directories; named persistent profiles are never
  auto-deleted.
- `--owner <label>` (CLI) / `"owner": "<label>"` (`browser_create`) labels a browser and joins the
  create key: the same owner with the same options reuses one browser, a different owner gets a
  separate one.
- **Extra headers.** `--header NAME=VALUE` (repeatable) / `"headers": {"NAME": "VALUE"}`
  (`browser_create`) sends a header with every request from every page of that browser, the first
  navigation included. Use it for a program identity header, e.g.
  `browser create --header X-Bug-Bounty=your-handle`.
- **Scope allowlist.** `--scope HOST` (repeatable) / `"scope": ["HOST", …]` (`browser_create`)
  confines navigation. A bare host also covers its subdomains, a leading `*.` glob is accepted,
  `host:port` is accepted, and `*` allows all. **No scope means no limit**; `browser_list` reports
  the live state as `scope` (`open` when unrestricted) plus `scope_strict`. `--scope-strict` /
  `"scope_strict": true` is opt-in and makes an empty allowlist deny every navigation. A refusal
  names the host, the allowed list and the fix (`browser_create scope=[…]`). **The limit is
  navigation-only:** subresources and redirects are not gated, so a redirect can still land on an
  out-of-scope host (the final URL in the action result shows where you are).
- **Idle browsers expire** after 30 minutes by default. Set `browser_ttl` in
  `~/.config/browser-mcp/config.json` as a Go duration (`"15m"`, `"0"` to disable); pages keep their
  separate 15-minute TTL.
- **Incognito identities** isolate two accounts inside one Chromium process. Pass
  `"identity": "victim"` to `fetch_page` (or `browser page fetch <url> --identity victim`) and the
  page opens in that incognito context, with its own cookies and storage. `default` is the browser's
  own jar; a new name is created on first use and reused afterwards. Every identity dies with its
  parent browser. `session_list` / `page_list` report each session's `context` (the identity name),
  and `session_list {"identity": "victim"}` lists one identity. An unknown identity is refused with
  the live names — nothing is auto-created.

### Proxy & TLS (Burp, Caido, ZAP)

Route the browser through a local intercept proxy with `--proxy` (CLI) or `proxy`
(`browser_create`). Both accept `host:port` or `scheme://host:port`:

```sh
browser create --proxy http://127.0.0.1:8080 --ignore-certs
browser fetch https://example.org --wait load    # the request appears in the proxy history
```

```json
{ "proxy": "http://127.0.0.1:8080", "ignore_certs": true }
```

- `--proxy-bypass` / `proxy_bypass` takes a comma-separated host list
  (`localhost,*.internal`) that skips the proxy.
- `--ignore-certs` / `ignore_certs` accepts invalid TLS certificates, such as the
  proxy's MITM CA. It is independent of the proxy: set it explicitly.
- Chromium receives `--proxy-server=<value>`, `--proxy-bypass-list=<list>` and
  `--ignore-certificate-errors`. These are process-start flags, so they are per
  browser: create a new browser to change them.
- Proxy credentials never go in a flag or tool argument. For a remote proxy, put
  them in `~/.config/browser-mcp/proxy.json` with mode `0600`, keyed by proxy
  `host:port`:

```json
{
  "proxies": {
    "proxy.example.com:8080": { "username": "user", "password": "pass" }
  }
}
```

The engine answers Chromium's proxy auth challenge from that file. A proxy that
demands auth with no matching entry fails with an error naming the file path. A
local intercept proxy needs no credentials; do not point an agent at a remote
proxy unless the user asks.

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
browser page fetch https://app.test/login --identity attacker   # incognito context
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

The agent skill lives at [`.agents/skills/web-mcp/SKILL.md`](.agents/skills/web-mcp/SKILL.md).
The MCP tool descriptions stay primary. The skill covers cross-cutting rules and exact limits.

---

## MCP Tools

| Tool | Purpose |
| :--- | :--- |
| `browser_create` | Create or reuse a browser (`bin`, `profile`, `persist`, `owner`, `headless`, `stealth`, `position`, `proxy`, `proxy_bypass`, `ignore_certs`, `headers`, `scope`, `scope_strict`). Ephemeral by default; `persist: true` keeps the profile. Same owner + same options reuses the same id. `headers` applies to every request; `scope` is the navigation allowlist (no scope = no limit; `scope_strict` makes an empty list deny all). Scope does not gate subresources or redirects. |
| `browser_list` | List live browsers with `owner`, `persist`, `ephemeral`, `created_at`, `idle_for`, `ttl`, `scope`, `scope_strict`, `profile_dir` and their pages. |
| `browser_close` | Close one browser, or all when `browser_id` is empty. `purge: true` also deletes that profile; `prune: true` (no `browser_id`) closes all and removes orphan `.ephemeral` profiles. An unknown id fails with `browser "browser-42" not found (running: browser-1)`. |
| `fetch_page` | Open a URL in a new session and return `session_id` + summary. `wait_for` defaults to `idle`. `identity` opens it in an incognito context (`default` = the browser's own jar); a new identity is created on first use and keeps separate cookies/storage. |
| `page_list` | List the pages of a browser, each with its `context` (identity name). `browser_id` is required, and an unknown id fails with `browser "browser-42" not found (running: browser-1)`. |
| `session_list` | List all live sessions with their `context`. Optional `identity` lists one identity; an unknown name fails with the live names and creates nothing. |
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
| `marks` | A text block `{marks:[{index, role, text?, x, y, width, height}], viewport_width, viewport_height, truncated}` plus the annotated viewport PNG as a separate image content block — a numbered box over every visible interactive element. `x`/`y` are the box's top-left in viewport CSS px, so `x + width/2`, `y + height/2` feed `page_act`'s `click_at`. Capped at 60 marks; `truncated` counts the remainder. The overlay is removed again before returning. |
| `links` | `[{text, href, handle?}]`, deduped by href, non-navigable schemes filtered; `handle` is the first anchor carrying that href. |
| `meta` | `{title, url, meta:[{name/property/http-equiv/content/charset}]}`. |
| `text` | Visible `innerText`. |
| `html` | `outerHTML`. |
| `css` / `js` | Loaded stylesheet/script resources as `[{url, type, size}]`; pass `target=<url>` to fetch one resource's raw source. |
| `screenshot` | A PNG image content block (`mimeType: image/png`) plus a text block `{mime_type, bytes, width, height, full_page}`; `full_page: true` captures the whole scrollable page. |

`screenshot` and `marks` return the PNG as an MCP image content block, so a vision-capable client
renders it without a base64 wall of text. Image blocks are enabled by default (MCP defines no
standard image capability); a text-only client opts out at `initialize` with
`capabilities.experimental.imageContent: false` and then receives the base64 data URI in the text
block instead — under `data` for `screenshot`, under `data.screenshot` for `marks`. The bytes are
sent exactly once either way.

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
| `browser create [--owner --persist --header --scope --scope-strict]` / `list` / `close [--purge\|--prune]` | `browser_create` / `browser_list` / `browser_close` |
| `browser focus` | — (CLI-only: raise the window) |
| `browser serve` / `stop` | — (CLI-only: daemon lifecycle) |

CLI-only conveniences: `--json` on `fetch`, page-id shorthand (`last`, `5` → `page-5`, single open
page by default), one-step `page fetch <url> [what]`, and `--max-bytes 0` for unbounded output.

---

## Operating Notes

- **Sessions expire** after 15 minutes idle. If a `session_id` disappears or the browser restarted,
  re-open the page; the error message says so.
- **Browsers expire** after 30 minutes idle by default (`browser_ttl` in `config.json`; `"0"`
  disables). Closing explicitly is still the normal end: `browser close <id>` or `browser_close`.
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
