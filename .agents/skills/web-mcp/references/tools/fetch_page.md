> Tool: `fetch_page` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# fetch_page

Open a URL in a new live session and return the `session_id` that `page_fetch` and
`page_act` need.

## What it does

`fetch_page` opens one URL in a new tab of one browser. It waits for a readiness level.
It returns the title, the final URL, a short text snippet, the HTML size, and the load
time. Every call creates a new session, even for the same URL.

The call reuses a live browser. It applies stealth, viewport, device, and User-Agent
settings **before** navigation. The loaded page sees those settings. You do not need to
navigate again.

## Inputs

`url` is the only required input. The source is `code://pkg/mcp/tools/fetch.go#L49`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `url` | string | yes | — | Absolute URL. The schema says http or https. The code does not enforce a scheme. |
| `browser_id` | string | no | default browser | Target browser, for example `browser-16`. A dead id gets relaunched. An **unknown** id gets created. |
| `wait_for` | string | no | `idle` | Readiness mode: `load`, `idle`, or `stable`. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. `0` means the default. A negative value is rejected. |
| `viewport` | object | no | none | Manual `{"width":<int>,"height":<int>}` in CSS pixels. Both values must be positive. |
| `device` | string | no | none | go-rod preset name or Title, case-insensitive. Examples: `IPhoneX`, `iphone x`, `IPadPro`. `clear` resets emulation. |
| `user_agent` | string | no | browser default | User-Agent override for this page, applied before navigation. |

**Precedence.** `device` beats `viewport`. Live test: `IPadPro` plus `viewport`
`400x400` gave a `1024x1366` viewport at DPR 2. A `user_agent` wins over a device preset's
UA when you pass both. That rule comes from the source and was not run live.

The schema has no `persist` input, and this MCP client omits it. A lone `profile` name on
`browser_create` therefore yields a persistent profile. See the example's isolation note.

## Returns

The tool returns one line of compact JSON. It has no wrapper and no `ok` field.

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `session_id` | string | New tab id, for example `page-4`. Use it with `page_fetch` and `page_act`. |
| `title` | string | Document title at return time. It can be empty. |
| `url` | string | Final URL after redirects. |
| `text_snippet` | string | First 500 characters of `document.body.innerText`. |
| `html_bytes` | integer | Byte length of the full outer HTML. |
| `load_ms` | integer | Time from call start through the wait and the summary reads. It includes browser launch or relaunch. |

Live result:

```json
{"session_id":"page-1","title":"Web browser - Wikipedia","url":"https://en.wikipedia.org/wiki/Web_browser","text_snippet":"Jump to content\nMain menu\nSearch\n…","html_bytes":597043,"load_ms":1185}
```

The snippet is cut at 500 characters. `html_bytes` can change between fetches because
scripts inject content. Live test: 597043, 614822, and 614807 bytes on one page.

## Behavior and limits

**Wait modes.** Each value maps to one rod wait (`code://pkg/cli/pageops/pageops.go#L111`).

| `wait_for` | Returns when | Stalls on |
| :--- | :--- | :--- |
| `load` | `window.onload` fires. | A subresource that never finishes. |
| `idle` | No network request for 500 ms. | Polling, websockets, beacons, or endless loaders. The page may never go idle. |
| `stable` | DOM mutation stays under a 0.05 ratio for 500 ms. | Timers, animations, carousels, and infinite scrollers. |

`idle` is the default and the safest general choice. Use `load` for a fast first look.
Use `stable` for client-rendered pages such as `react.dev`. Each mode can stall. The
timeout ends the stall.

**Live timings.** Wikipedia with a 60 s cap: `load` 1185 ms, `idle` 1534 ms, `stable`
1479 ms. `react.dev/learn` with `stable` and an 8 s cap: 2570 ms. `example.com` with the
default `idle`: 687 ms.

**Timeout covers everything.** The bound starts before the browser lookup. It includes a
cold Chromium download. A short timeout on a first run fails before any page loads.

**A wait timeout closes the tab.** The error names the closed session:
`fetch_page: context deadline exceeded (session page-10 closed)`. Do not reuse that id.

**Lifecycle.** The engine sweeps idle pages after 15 minutes. A later `page_fetch` with an
expired id returns a not-found error. If Chromium dies, the next call relaunches it under
the same `browser_id`. Old session ids then fail with a "browser was restarted" error.

**`browser_id` is not checked.** Live test: `{"browser_id":"browser-999"}` did not fail.
It launched a browser with the literal id `browser-999` and an ephemeral profile. A typo
spends a full Chromium process. Confirm the id with `browser_list` first.

**Session ids collide across browsers.** Every browser numbers its tabs from `page-1`.
The manager resolves a page id by scanning browsers, not by ownership. Keep one browser
per task to avoid closing or reading the wrong tab.

**The summary is small by design.** It never returns the full HTML. Use `page_fetch` with
`what:"html"` or `max_bytes` to read content. Two calls for one URL make two tabs, so
close sessions you no longer need.

## Failure modes and recovery

The server prefixes tool errors with `MCP error -32603: `. The strings below show the
message after that prefix.

**Invalid URL.** Live test: `"not-a-url"`.
`fetch_page: browsers: navigate: {-32000 Cannot navigate to invalid URL }`

**DNS failure.** Live test: `https://this-host-does-not-exist-96.invalid/`.
`fetch_page: browsers: navigate: navigation failed: net::ERR_NAME_NOT_RESOLVED`

Recovery for both: send an absolute URL with a resolvable host.

**Non-http scheme.** The schema says http or https. `file://` loaded in the live test with
no error, so the code does not enforce the scheme. Do not rely on that. FTP failed with
`fetch_page: browsers: navigate: navigation failed: net::ERR_ABORTED` on
`ftp://example.com/file`. Recovery: serve the content over http or https.

**Timeout before the page loads.** Live test with `timeout_ms: 1` created no session:
`fetch_page: browsers: apply stealth: context deadline exceeded`. Recovery: raise
`timeout_ms` and retry. Allow more time on a cold start.

**Timeout during the wait.** The tab is closed for you. Live test: a 1200 ms cap on
Wikipedia with `stable`.

```
fetch_page: context deadline exceeded (session page-10 closed)
```

Recovery: retry with a larger cap or a cheaper wait mode.

**Close also failed.** Rare. This format comes from the source and was not run live. The message appends the close error:
`fetch_page: context deadline exceeded (session page-7 closed, but close reported: …)`.
The session is already evicted. Do not call `session_close` for it.

**Unknown device.** Live test with `device:"Nokia3310"`:
`fetch_page: unknown device "Nokia3310" (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)`.
Recovery: use a real preset name or `clear`.

**Bad viewport.** Live test with `height:0`: `fetch_page: viewport width and height must be positive`.
Recovery: send positive integers.

**Negative timeout.** Live test with `timeout_ms:-5`: `fetch_page: timeout_ms must be >= 0`.
Recovery: omit the field or send a non-negative value.

**Missing url or bad enum.** The client schema usually blocks these before the server. The
server strings are `fetch_page: missing required argument "url"` and
`fetch_page: invalid wait_for "<value>" (must be one of load, idle, stable)`.

**Bad JSON shape.** `fetch_page: invalid arguments: json: cannot unmarshal …`. Recovery:
send one JSON object with the documented keys.

**Server stopping.** `fetch_page: browsers: manager has been shut down`. Recovery: start a
new `browser serve mcp` process.

## When not to use

Do not use `fetch_page` to re-read an open page. Use `page_fetch` with the `session_id`.

Do not use it to click, type, or scroll. Use `page_act`.

Do not pass a guessed `browser_id`. An unknown id creates a browser.

Do not use it as a stateless HTTP client for static pages. Every call pays a tab and a full navigation.

Do not use it to close a tab. Use `session_close` for the default browser, or `browser_close` for a browser that you own.

## Related tools

- `page_fetch` — read html, dom, text, links, meta, and other aspects of the session.
- `page_act` — click, type, press keys, evaluate JS, and navigate inside the session.
- `page_list` — list the pages of one browser. Confirm your new page here.
- `session_list` — list every live session across browsers.
- `session_close` — close one page.
- `browser_create` — create an isolated browser with its own profile.
- `browser_list` — list live browsers, their absolute `profile_dir`, and their pages.
- `browser_close` — close one browser and every page in it.

See [fetch_page_example.md](../examples/fetch_page_example.md) for copyable call sequences with expected
output.
