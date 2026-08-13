# web-mcp

A Go MCP server that gives LLM agents a **live, stateful browser** built on
[go-rod](https://go-rod.github.io/). The LLM opens a page, reads its full
decomposition (DOM tree, HTML, CSS, JS, text, screenshot), and drives it
(click, input, navigate, run JS) across multiple tool calls.

> **Status:** this README currently documents the **CLI** (`web-cli`) — the
> human-facing mirror of the MCP toolset that drives the same `pkg/browsers`
> core engine. MCP server usage and the tool reference will be documented here
> soon.

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
# 1. Launch the shared browser (headful Chromium, stealth on). fetch/page
#    commands also launch it lazily, so this step is optional.
web-cli launch

# 2. Open a URL in a new page — prints session_id, title, url, text snippet,
#    html bytes and load time.
web-cli fetch https://example.com

# 3. Inspect and interact with the live page using its session_id.
web-cli page list
web-cli page fetch --id <session_id> --what text
web-cli page act --id <session_id> --action eval --value '() => document.title'

# 4. Close a page, or the whole browser.
web-cli page close --id <session_id>
web-cli browser close
```

## Commands

The CLI mirrors the MCP tools one-to-one, talking directly to the
`pkg/browsers` core engine (never through `pkg/mcp`). Every command accepts
`--browser-id` (default `browser-1`), the id of the single managed browser.

| Command | Mirrors | Description |
| --- | --- | --- |
| `web-cli launch` | `browser_create` | Lazily start the shared browser and print its `browser_id`. Flags `--headless` / `--stealth` are accepted for parity but reserved. |
| `web-cli browser list` | `browser_list` | List the live browser and its open pages (`id\turl` rows). Never launches the browser. |
| `web-cli browser close` | `browser_close` | Close the browser and all of its pages. Idempotent when nothing is open. |
| `web-cli fetch <url>` | `fetch_page` | Open `<url>` in a new page and print a summary (`session_id`, title, url, text snippet, html bytes, load ms). Flags: `--json`, `--wait idle\|load\|stable` (default `idle`), `--timeout-ms` (default `30000`). |
| `web-cli page list` | `page_list` | List the browser's open pages (`id\turl`). |
| `web-cli page fetch --id <session> --what <what>` | `page_fetch` | Extract an aspect of a live page. `--what` is one of `html`, `text`, `links`, `meta` (implemented) — `dom`, `css`, `js`, `screenshot` are not yet implemented. Optional `--selector` (CSS) scopes html/text; `--max-bytes` caps output (default `50000`, truncation is flagged). `links` and `meta` print JSON. |
| `web-cli page act --id <session> --action eval --value <js>` | `page_act` | Perform an action on a live page. Only `eval` is implemented: run a JS function (e.g. `'() => document.title'`) and print the JSON result. `click`, `input`, `type`, `navigate`, `back`, `forward`, `reload`, `hover`, `scroll`, `select`, `wait` are accepted but not yet implemented. |
| `web-cli page close --id <session>` | `session_close` | Close the page, releasing its tab. The session id becomes invalid. |

### Example session

```sh
$ web-cli launch
browser_id: browser-1

$ web-cli fetch https://go-rod.github.io/
session_id: 30c1c3f17d20cbceb4f9dbb3d0a1ce0c
title: go-rod
url: https://go-rod.github.io/
text_snippet: go-rod — A high-efficiency driver for Chromium...
html_bytes: 42119
load_ms: 1420

$ web-cli page fetch --id 30c1c3f17d20cbceb4f9dbb3d0a1ce0c --what links --max-bytes 2000
[{"text":"Github","href":"https://github.com/go-rod/rod"},...]

$ web-cli page act --id 30c1c3f17d20cbceb4f9dbb3d0a1ce0c --action eval --value '() => document.title'
"go-rod"

$ web-cli page close --id 30c1c3f17d20cbceb4f9dbb3d0a1ce0c
session 30c1c3f17d20cbceb4f9dbb3d0a1ce0c closed

$ web-cli browser close
browser browser-1 closed
```

Every engine call is bound to a per-call timeout (default 30s) so a hung page
cannot block the CLI; `SIGINT`/`SIGTERM` also triggers a graceful browser
shutdown.

## MCP

> MCP server usage (SSE endpoint, tool reference) will be added here.
