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

`serve mcp` starts no browser. If no browser can be resolved for you, `fetch_page`
launches one and the first call pays the launch time. Call `browser_create` first when
you want to control the launch options.

## Inputs

`url` is the only required input. Schema source: `code://pkg/mcp/tools/fetch.go#L55-L102`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `url` | string | yes | — | Absolute URL. The schema says http or https. The code does not enforce a scheme. |
| `browser_id` | string | no | default browser | Target browser, for example `browser-16`. A dead id gets relaunched. An **unknown** id gets created. |
| `wait_for` | string | no | `idle` | Readiness mode: `load`, `idle`, or `stable`. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. `0` means the default. A negative value is rejected. |
| `viewport` | object | no | none | Manual `{"width":<int>,"height":<int>}` in CSS pixels. Both values must be positive. |
| `device` | string | no | none | go-rod preset name or Title, case-insensitive. Examples: `IPhoneX`, `iphone x`, `IPadPro`. `clear` resets emulation. |
| `user_agent` | string | no | browser default | User-Agent override for this page, applied before navigation. |

**Precedence.** `device` beats `viewport`. Live test: `IPadPro` plus `viewport` `400x400`
gave a `1024x1366` viewport at DPR 2. A `user_agent` wins over a device preset's UA when
you pass both. That rule comes from the source and was not run live.

## Returns

Every result is one envelope. The payload lives in `data`. The envelope rule with the
failure branch and the full error-type list is in [SKILL.md](../../SKILL.md) under
Cross-cutting rules.

On success `data` is one page summary object:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `session_id` | string | New tab id, for example `page-4`. Use it with `page_fetch` and `page_act`. |
| `title` | string | Document title at return time. It can be empty. |
| `url` | string | Final URL after redirects. |
| `text_snippet` | string | First 500 characters of `document.body.innerText`. |
| `html_bytes` | integer | Byte length of the full outer HTML. |
| `load_ms` | integer | Time from call start through the wait and the summary reads. It includes a forced launch. |

Live result:

```json
{"ok":true,"data":{"session_id":"page-1","title":"Quick Start – React","url":"https://react.dev/learn","text_snippet":"React\nv19.3\nSearch\nCtrl\nK\n…","html_bytes":285239,"load_ms":2760}}
```

The snippet is cut at 500 characters. `html_bytes` can change between fetches because
scripts inject content.

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

**Timeout covers everything.** The bound starts before the browser lookup. It includes a
cold Chromium download and a launch that no live browser can serve.

**A wait timeout closes the tab.** The envelope names the closed session in `message`:
`fetch_page: context deadline exceeded (session page-10 closed)`. Do not reuse that id.

**Lifecycle.** The engine sweeps idle pages after 15 minutes. A later `page_fetch` with an
expired id returns a `page_closed` envelope. A relaunch after a Chromium death makes old
session ids fail with a "browser was restarted" error, also typed `page_closed`.

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

Every failure is one envelope:

```json
{"ok":false,"error":{"type":<type>,"message":"fetch_page: …","hint":"<one-line fix>"}}
```

The message starts with `fetch_page: `. Types seen by this tool: `invalid_args`,
`nav_failed`, `timeout`, `page_closed`, `anti_bot`, and `unknown`.

**Invalid `wait_for`.** Live result:

```json
{"ok":false,"error":{"type":"invalid_args","message":"fetch_page: invalid wait_for \"bogus\" (must be one of load, idle, stable)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

**DNS failure.** Live result for `https://this-host-does-not-exist-96.invalid/`:

```json
{"ok":false,"error":{"type":"nav_failed","message":"fetch_page: browsers: navigate: navigation failed: net::ERR_NAME_NOT_RESOLVED","hint":"the page navigation failed — verify the URL is reachable, retry, or re-fetch the page"}}
```

**Invalid URL string.** Live result for `"not-a-url"`:

```json
{"ok":false,"error":{"type":"unknown","message":"fetch_page: browsers: navigate: {-32000 Cannot navigate to invalid URL }"}}
```

Recovery for all three: send an absolute http or https URL with a resolvable host, or fix the enum value. The `unknown` type carries no hint.

**Non-http scheme.** `file://` loaded with no error, so the code does not enforce the
schema scheme. Do not rely on that. FTP failed with a `nav_failed` envelope whose message
ends `net::ERR_ABORTED`. Serve the content over http or https.

**Timeout before the page loads.** Live test with `timeout_ms: 1` created no session.
Typed `timeout`, message `fetch_page: browsers: apply stealth: context deadline exceeded`.

**Timeout during the wait.** The tab is closed for you. Typed `timeout`, message
`fetch_page: context deadline exceeded (session page-10 closed)`. Recovery: retry with a
larger cap or a cheaper wait mode.

**Close also failed.** Rare. The message appends the close error and the session is
already evicted: `fetch_page: context deadline exceeded (session page-7 closed, but close
reported: …)`. Do not call `session_close` for it.

**Unknown device.** Typed `invalid_args`. Live message: `fetch_page: unknown device
"Nokia3310" (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)`.

**Bad arguments.** Typed `invalid_args`. Exact messages: `fetch_page: viewport width and
height must be positive`, `fetch_page: timeout_ms must be >= 0`, `fetch_page: missing
required argument "url"`, and `fetch_page: invalid arguments: json: cannot unmarshal …`.

**Bot challenge.** A challenge page or a denied navigation is typed `anti_bot`. The hint
says: retry after a wait, toggle stealth via `browser_create`, or hand off to a human.

**Server stopping.** Typed `unknown`, message `fetch_page: browsers: manager has been
shut down`. Recovery: start a new `browser serve mcp` process.

## When not to use

Do not re-read an open page with it. Use `page_fetch` with the `session_id`.
Do not click, type, or scroll with it. Use `page_act`.
Do not pass a guessed `browser_id`. An unknown id creates a browser.
Do not use it as a stateless HTTP client. Every call pays a tab and a full navigation.
Do not close a tab with it. Use `session_close` or `browser_close`.

## Related tools

- `page_fetch` — read html, dom, text, links, meta, and other aspects of the session.
- `page_act` — click, type, press keys, evaluate JS, and navigate inside the session.
- `page_list` — list the pages of one browser. Confirm your new page here.
- `session_list` — list the live sessions the manager resolves for you.
- `session_close` — close one page.
- `browser_create` — create an isolated browser with its own profile.
- `browser_list` — list live browsers, their absolute `profile_dir`, and their pages.
- `browser_close` — close one browser and every page in it.

See [fetch_page_example.md](../examples/fetch_page_example.md) for copyable call sequences with expected output.
