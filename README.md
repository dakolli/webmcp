# web-mcp 🚀

A Go MCP server and CLI that gives LLM agents and developers a **live, stateful browser** powered by [go-rod](https://go-rod.github.io/). 

Open pages, read complete page decompositions (DOM snapshots, HTML, text, screenshots, links, CSS/JS resources), and drive pages interactively across multiple tool calls.

---

## Features

- 🌐 **Live Browser Control**: Click, type, scroll, navigate, wait for elements, and evaluate custom JavaScript.
- 📐 **Native Full-Screen Viewport**: Pages automatically render at your physical display resolution with un-emulated metrics.
- 📱 **Opt-In Device Emulation**: Easily emulate mobile or laptop devices (e.g. `IPhoneX`, `LaptopWithHiDPIScreen`, `IPadPro`).
- 🦁 **Custom Browser Support**: Connect to host browsers like **Brave**, **Google Chrome**, or **Edge** via `--bin`.
- 🥷 **Built-in Stealth**: Powered by `go-rod/stealth` to bypass bot checks.
- 👤 **Persistent Profiles**: Store and load user profiles under `~/.config/browser-mcp/profiles/<name>` (`brave`, `chrome`, `firefox`, `default`, or custom path).
- ⚡ **Dual Interface (MCP & CLI)**: The `browser` CLI mirrors every MCP tool 1:1 for fast terminal testing.

---

## Installation

Requires Go 1.26+.

```sh
go install ./cmd/browser
```

*(This installs the `browser` binary into `$(go env GOPATH)/bin`)*

> **Note:** First run automatically downloads Chromium (~100 MB).

---

## Quick Start

### 1. Launch the Server

Start the background daemon with the MCP HTTP/SSE server enabled on port 4089:

```sh
browser serve mcp --port 4089
```

*Launch with a persistent profile (`brave`, `chrome`, `firefox`, `default`, or custom path):*

```sh
browser serve mcp --port 4089 --profile brave --bin brave-browser
```

---

### 2. Command Line Usage (`browser`)

```sh
# Fetch a webpage and get its session ID (e.g. page-1)
browser fetch https://news.ycombinator.com

# Read links, text, meta, or DOM snapshot from the open page
browser page fetch links
browser page fetch dom
browser page fetch screenshot

# Drive the page (click, input, run JavaScript)
browser page act click ".titleline a"
browser page act eval "() => document.title"

# Close the tab or stop the server
browser page close
browser stop
```

---

## MCP Server Configuration

Add `web-mcp` to your MCP client config (e.g., Claude Desktop or `mem-mcp`):

```json
"web-mcp": {
    "type": "remote",
    "url": "http://127.0.0.1:4089/sse",
    "enabled": true
}
```

---

## Available MCP Tools & CLI Commands

The CLI commands mirror the MCP server tools 1-to-1:

| CLI Command | MCP Tool | Description |
| :--- | :--- | :--- |
| `browser serve mcp` | — | Start the persistent browser daemon + MCP HTTP/SSE server. |
| `browser fetch <url>` | `fetch_page` | Open a URL and return `session_id`, title, and summary. |
| `browser page list` | `page_list` | List all open pages in the browser. |
| `browser page fetch <aspect>` | `page_fetch` | Extract `html`, `text`, `links`, `dom`, `meta`, `css`, `js`, or `screenshot`. |
| `browser page act <action>` | `page_act` | Perform `click`, `input`, `type`, `press_key`, `click_at`, `hover_at`, `drag_and_drop`, `upload_file`, `eval`, `navigate`, `back`, `forward`, `reload`, `hover`, `scroll`, `select`, `wait`, `set_viewport`, or `emulate`. |
| `browser page close` | `session_close` | Close an active tab session. |
| `browser browser close` | `browser_close` | Close all tabs and shut down the browser engine. |
| `browser stop` | — | Stop the background daemon process. |

---

## Page Extractions (`page_fetch`)

- **`dom`**: Re-nested DOM tree from CDP snapshot including shadow DOM, visibility, and hoisted IDs/classes.
- **`links`**: Clean array of `{text, href}` links with anchor/javascript links filtered out.
- **`text`**: Visible text on the page (can be scoped with `--selector`).
- **`html`**: Outer HTML of the page (can be scoped with `--selector`).
- **`screenshot`**: Base64 PNG data-URI (`--full-page` supported).
- **`meta`**: Page title, canonical URL, and open graph `<meta>` tags.
- **`css` / `js`**: Resource list of loaded stylesheets and scripts (`--target <url>` fetches source content).

---

## License

MIT License.
