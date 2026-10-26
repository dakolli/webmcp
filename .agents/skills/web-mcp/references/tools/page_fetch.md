> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_fetch

Read one aspect of a live page that `fetch_page` opened. It does not navigate, click, or type. The `session_id` picks the tab, `what` picks the aspect.

The payload rides inside the result envelope, so the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the `{"ok":false,"error":…}` shape. The payload is one JSON object or array. `html`, `text`, `css`, and `js` arrive as JSON strings inside `data` with no further wrapper. `screenshot` and `marks` return one text block plus one image block. Read the image result rule below.

## Inputs

The live schema is `code://pkg/mcp/tools/page.go#L90`. The aspect registry is `code://pkg/mcp/tools/aspects.go#L67`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | yes | — | Tab to read, for example `page-1`. Comes from `fetch_page`. |
| `what` | string | yes | — | Aspect name. See the list below. |
| `selector` | string | no | — | CSS selector. Only `html` and `text` use it. |
| `max_bytes` | integer | no | `50000` | Output cap. A value of `0` or less means `50000`, not unbounded. |
| `target` | string | no | — | Resource URL for `css`/`js`. For `body`: a `seq` number or a substring of the request URL, newest match wins. |
| `full_page` | boolean | no | `false` | Capture the whole scrollable page. Only `screenshot` uses it. |

The live `what` enum has 16 values, in registration order: `html`, `dom`, `text`, `css`, `js`, `links`, `screenshot`, `meta`, `interactives`, `a11y`, `network`, `body`, `console`, `article`, `marks`, `security`. A 17th value, `sinks`, is specified by WEB-157 to WEB-159 but is not registered in the built server yet, so treat it as draft. The tool has no `timeout_ms` input, every call has a fixed 30 second bound, and a full-page screenshot gets its own 90 second budget.

## Returns

### Aspect return shapes

| `what` | Payload at `data` | Uses `selector` | Capped by `max_bytes` |
| :--- | :--- | :--- | :--- |
| `html` | `outerHTML` string, full page or matched element. | yes | yes, text cut |
| `dom` | JSON array of document trees. | no | yes, JSON envelope |
| `text` | `document.body.innerText` string, or the matched element's `innerText`. | yes | yes, text cut |
| `css` | JSON array `[{url,type,size}]`, or one resource's source with `target`. | no | yes, text cut |
| `js` | JSON array `[{url,type,size}]`, or one resource's source with `target`. | no | yes, text cut |
| `links` | JSON array `[{text,href,handle?}]`. | no | yes, JSON envelope |
| `screenshot` | One text block `{mime_type,bytes,full_page,width?,height?}`, then one PNG image block. | no | **no** |
| `meta` | JSON object `{title,url,meta:[...]}`. | no | yes, JSON envelope |
| `interactives` | JSON `{interactives:[...],truncated}`. | no | **no** |
| `a11y` | JSON `{nodes:[...],count,truncated}`. | no | **no** |
| `network` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `body` | JSON `{seq,method,url,status,content_type,bytes,truncated}` plus `body` or `base64`. | no | **no** |
| `console` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `article` | JSON `{title,byline?,excerpt?,text,words,truncated,url}`. | no | **no** |
| `marks` | One text block `{marks,viewport_width,viewport_height,truncated}`, then one PNG image block. | no | **no** |
| `security` | JSON `{url,checks:[{id,status,evidence,detail}],count,truncated}`. | no | **no** |
| `sinks` | JSON `{installed,hits:[{source,sink,context,value,value_cut?,stack?,selector?,ts}],dropped,truncated}`. Draft. | no | **no** |

`dom`, `meta`, and `links` keep their JSON parseable when cut. `css` and `js` do not. Read the truncation matrix below.

### Image results

`screenshot` and `marks` return two content blocks in `result.content`. The first is text and holds the standard envelope inside `data`. The second is the image, beside the text block and never inside `data`. The `screenshot` text block holds `mime_type` (`image/png`), `bytes`, `full_page`, and `width`/`height` only when the PNG header decodes. A live text block looks like this:

```json
{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}
```

The image block is a sibling, not wrapped by `ok` or `data`:

```json
{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}
```

The `marks` text block holds `marks`, `viewport_width`, `viewport_height`, and `truncated`. Its annotated PNG is the image block. Mark `x` and `y` values feed `page_act` `click_at`.

### Text-only clients

Image blocks are on by default. A client opts out at `initialize` with `capabilities.imageContent:false` or `capabilities.experimental.imageContent:false`, and then gets one text block. For `screenshot`, `data` is the data URI string `{"ok":true,"data":"data:image/png;base64,<payload>"}`. For `marks`, `data.screenshot` holds that data URI and other fields are unchanged. The opt-out is read per SSE session.

### Tree, list, and report shapes

- **`dom`** returns one entry per document with `title`, `url`, `frame_id`, and `root`. A node has `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`, and `children`. A node without `handle` is structural.
- **`links`** is deduplicated by `href`, first wins. It drops `javascript:`, `mailto:`, `data:`, `tel:`, empty hrefs, exactly `#`, and anchors with no text. `text` falls back to `aria-label`, `title`, then `img[alt]`.
- **`meta`** holds `title`, `url`, and a `meta` array with any of `name`, `property`, `http-equiv`, `content`, `charset`. **`css`** and **`js`** list resources, with inline `<style>`/`<script>` blocks appearing as `inline-style[1]`. Both lists end with a hint naming `what=<css|js>&target=<url>`.
- **`interactives`** holds `handle`, `role`, `text`, `href`, `type`, `x`, `y`, `visible`. `visible: true` means the node has a layout box, not that it is in the viewport.
- **`a11y`** holds `handle`, `depth`, `role`, `name`, `value`, `states`, plus `count` and `truncated`. Names and values are clipped at 200 runes. Root frame only.
- **`network`** entries hold `method`, `url`, `type`, `status`, `mime`, `bytes`, `ms`, `failed`, `error`, `seq`, `req_headers`, `resp_headers`. Each header map keeps at most 32 headers per direction at 512 bytes per value under a 4 KB name+value budget, and a cut set adds `req_headers_truncated` or `resp_headers_truncated`. No bodies. **`console`** entries hold `level`, `text`, `source`, `line`, `seq`.
- **`body`** adds `body` for UTF-8 text or `base64` plus `content_type` for binary, both exclusive. `bytes` is the pre-cap payload size. `truncated` marks the 256 KB cut.
- **`marks`** items hold `index`, `role`, `text`, `x`, `y`, `width`, `height`. Coordinates are viewport CSS pixels and can be negative. The `screenshot` field appears only for a text-only client.

## Behavior and limits

### Handles and staleness

Every handle has the form `g<document>-b<backendNodeId>`, for example `gy1g0vstq-b162`. Pass a handle to `page_act` with the `handle` input. Any navigation, reload, or redirect changes the document token, so the old handle fails as stale. Re-read the aspect after navigating.

### Caps

- `interactives`: 200 entries, `truncated` counts the rest. Live Wikipedia result had `truncated: 335`.
- `a11y`: 400 nodes. Live Wikipedia result had `count: 400` and `truncated: 299`.
- `network` and `console`: 200-entry ring per page. One call emits the newest 100. `dropped` counts true ring loss, `truncated` counts buffered entries above the emit cap.
- `article`: 40000 runes in `text`, reported by a boolean `truncated`. `marks`: 60 boxes, with `truncated` for the rest. Only nodes inside the viewport are boxed.

### Network and console capture

Capture starts before the first document loads. Both rings are cumulative from page creation. `clear_logs` (a `page_act` action) empties both rings and resets `dropped`, for example `{"network":39,"console":0}`. The `seq` counter does not reset. The recipe is `clear_logs`, then one action, then read.

#### Header redaction

`Cookie`, `Set-Cookie`, `Authorization`, and `X-CSRF-Token` values show `<redacted>` by default. The name match is case-insensitive (`code://pkg/browsers/redact.go#L24`). The engine switch `Page.CaptureSecrets(true)` keeps the values of entries captured after the switch, and entries already captured are not rewritten (`code://pkg/browsers/observe.go#L400`). No surface exposes the switch today.

#### Response bodies with what=body

`body` fetches one observed response on demand. Read `network` first, then call with `target` set to the entry's `seq` or a substring of its `url`. The payload is capped at 256 KB and flagged with `truncated: true`, `max_bytes` does not apply, and the browser evicts response bodies quickly, so read right after the request. Live evidence from commit `819ba12`: a form POST to `https://httpbin.org/post` returned a 1143 byte `application/json` body.

#### Security audit with what=security

`security` returns one passive audit. It never reloads or probes the site. It reads observed response headers from the network ring, the cookie jar for the current URL, and one DOM evaluation. Source: `code://pkg/cli/pageops/security.go#L124-L167`.

The report holds `url`, `checks`, `count`, and `truncated`. Each check holds `id`, `status` — `ok`, `warn`, `fail`, or `info` — evidence, and detail. Check ids and the status you get when the protection is missing (`code://pkg/cli/pageops/security.go#L223-L601`):

| `id` | Meaning and status when the protection is missing |
| :--- | :--- |
| `headers` | `info` only: the document response left the ring and every header check reads absent, or the captured header set was cut at 32 headers / 4 KB. |
| `csp` | `warn` when no `Content-Security-Policy` header and no `<meta http-equiv>` policy exists. A meta-only policy is a warning too. |
| `csp_unsafe_inline`, `csp_unsafe_eval` | `warn` when the policy allows the directive. Present only when any policy was found. |
| `hsts` | `fail` with no header or on plain http, `warn` when a value sets `max-age=0`. |
| `x_frame_options` | `warn` when absent. A CSP `frame-ancestors` directive satisfies it. |
| `x_content_type_options` | `warn` unless the header is exactly `nosniff`. |
| `referrer_policy`, `permissions_policy`, `coop` | `info` when absent (no failure status exists), `ok` when set. |
| `cookie:<name>` | One check per cookie, `warn` when `HttpOnly`, `Secure`, or `SameSite` is missing. Becomes `cookies`, `ok`, when no cookies exist. |
| `mixed_content` | `warn` for an http:// subresource observed on an https page, `info` on plain-http pages. |
| `sri` | `warn` for cross-origin `<script src>` without an `integrity` attribute. |
| `form_target` | `warn` for http form actions and GET forms with password fields. |
| `password_autocomplete` | `warn` for password fields lacking `current-password` or `new-password`. |
| `cors` | `warn` for an observed response pairing `Access-Control-Allow-Origin: *` with credentials, `info` before any response is observed. |

Evidence names at most 10 offending values, then `(+N more)`. The script, form, password, and cookie lists are each cut at 200 entries and every omission is reported in `truncated`. Live run on 2026-10-26 against `https://go.dev/`: `count: 20` (6 cookie checks, 2 `csp_unsafe_*`, 14 header/content/cors checks), `truncated: 0`.

### Sinks tracer with what=sinks

**Draft — the surface is not in the built server yet. Every value below follows the WEB-157 to WEB-159 spec. Do not claim a live test for it.**

`sinks` reads a source-to-sink tracer. Arm it first with `page_act action=sinks_install`. The arm step is opt-in and changes page behavior because it wraps page setters and constructors. Keep it off for normal page use.

The payload is `{installed, hits:[{source, sink, context, value, value_cut?, stack?, selector?, ts}], dropped, truncated}`. `installed` is true when the tracer is live in the current document. `hits` holds the newest 100 records, oldest first, and is `[]` and never null on an unarmed page. `dropped` counts hits evicted by the 200-hit ring. `truncated` counts buffered hits older than the emitted 100. `max_bytes` does not apply.

`source` names the tagged read, for example `location.hash`, `message`, `localStorage.getItem`, or `unknown`. `sink` names the instrumented API, for example `innerHTML`, `eval`, `setAttribute:href`, or `location.assign`. `context` is `html`, `attr`, `url`, or `js`. `stack` keeps 5 frames. `selector` is a short CSS path to the node that received the value. `ts` is Unix milliseconds. `value` and each stack or selector string clip at 200 runes, and `value_cut` marks a clipped value. `stack` and `selector` are omitted when empty.

An unarmed page returns `{"installed":false,"hits":[],"dropped":0,"truncated":0}` with no error. A read is not a state change.

The ring is per document. A full navigation starts a fresh ring, because the arm step re-injects the script before the new document's own scripts, so `installed` stays true and `hits` is empty. A client-side route change keeps the same ring and appends to it. Clear with `page_act action=sinks_clear`, which returns `{"cleared":N}`.

### Screenshots and timeouts

`screenshot` returns PNG only, as an image block plus a metadata text block. There is no format input. `full_page: false` captures the visible viewport. `full_page: true` captures the whole scrollable page on a 90 second budget that is independent of the 30 second bound, and a very tall page can still time out. Large images are not cut by `max_bytes` — a 400x300 full-page capture produced over 87 KB of base64. Prefer a small viewport for `marks`.

## Truncation matrix

| Group | When `max_bytes` is exceeded | Parseable | Signal |
| :--- | :--- | :--- | :--- |
| `html`, `text`, `css`, `js` | Cut on a rune boundary. A note is appended. | No. A `css`/`js` list is cut inside its JSON. | Text note |
| `dom`, `links` | Array root becomes an envelope object. | Yes | `truncated: true`, `omitted` |
| `meta` | Object keeps its fields, its first array field shrinks. | Yes | `truncated: true`, `omitted` |
| `screenshot`, `interactives`, `a11y`, `network`, `body`, `console`, `article`, `marks`, `security`, `sinks` | Not cut. | Yes | Shape's own `truncated` or `count` |

The text note is exact: `… (truncated at max_bytes=600 bytes; raise max_bytes — dom/links/meta do not use selector)`. The array envelope is `{"items":[...],"max_bytes":N,"omitted":N,"truncated":true}`.

**`dom` is nearly all-or-nothing.** The envelope cuts whole documents, not nodes. The Wikipedia document exceeded 400000 bytes. At `max_bytes: 50000` the live result was `{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`. Use `article`, `text`, or `html` with a `selector` for large pages, never a megabyte `max_bytes`.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The prefix `page_fetch: ` opens the message. This tool produces the types `invalid_args`, `element_not_found`, `page_closed`, `timeout`, `nav_failed`, and `unknown` (`code://pkg/mcp/tools/envelope.go#L67-L130`). Two full envelopes below were recorded live on 2026-10-26.

**Unknown session.** Type `page_closed`. Message `page_fetch: session "page-999" not found`. Recovery: re-read `session_list` or `page_list`, because the idle sweep removes pages.

**Unknown `what`.** Type `invalid_args`. Live envelope, tested on 2026-10-26 with `sinks` as the probe before this draft aspect existed:

```json
{"ok":false,"error":{"type":"invalid_args","message":"page_fetch: unknown what=\"sinks\" (must be one of html, dom, text, css, js, links, screenshot, meta, interactives, a11y, network, body, console, article, marks, security)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: send one listed value. `sinks` is draft and `canary` is not on the tool surface.

**No selector match on `text`.** Type `element_not_found`. Live envelope, tested on 2026-10-26:

```json
{"ok":false,"error":{"type":"element_not_found","message":"page_fetch: no element matched selector \"#does-not-exist-xyz\": cannot find element"}}
```

Recovery: check the selector with `interactives` or `a11y` first.

**Unknown `target` on `css`/`js`.** Type `unknown`. Message: `page_fetch: resource "…" not found in the page's resource tree: {-32000 No resource with given URL found }`. Recovery: list `css` or `js` first, then copy a `url` value exactly.

**`body` with no matching entry or an unusable body.** From the source (`code://pkg/cli/pageops/pageops.go#L527`, `code://pkg/browsers/observe.go#L417`), runtime values as `%` templates:

```
body: no network entry matches target %q (read what=network first; target is a seq number or a substring of the request URL, newest match wins)
network body: seq %d (%s %s) failed before a response arrived (%s)
network body: seq %d (%s %s) body is no longer available — the browser keeps response bodies only briefly (evicted, streamed, or served from cache); re-issue the request and read the body immediately: %w
network body: seq %d (%s %s) has an empty body (status %d, mime %q) — the response carried no payload
```

Recovery: re-read `what=network` for the current `seq` values, then read the body at once.

**Missing required input.** Type `invalid_args`. Messages `page_fetch: missing required argument "session_id"` and `..."what"`. Send both fields.

## Tested calls

- 2026-10-25 — `meta`, `text` with selector, `css` with `target`, `interactives`, `network`, `body`, `network` header redaction, and the unknown-`what` failure. Commit `819ba12`.
- 2026-10-26 — `security` on `https://go.dev/` returned `count: 20`, `truncated: 0`. Failure envelopes for `unknown what` and a no-match selector were recorded live. The no-match selector returned `element_not_found`.
- Draft `sinks` has no live test. Its values come from the WEB-157 to WEB-159 spec.

## When not to use it

Do not use `page_fetch` to open a page. Use `fetch_page`. Do not use it to click, type, scroll, or navigate. Use `page_act`. Do not use `dom` for prose on a large page. Use `article` or `text`. Do not use `interactives` to test whether an element is on screen — use `marks` for viewport boxes. Do not expect `selector` to scope `dom`, `meta`, `links`, or `security`. They ignore it. `security` is a passive audit of what the browser already holds, not an exploit scanner.

## Related tools

- `fetch_page` — opens the session that `page_fetch` reads. It returns `session_id`.
- `page_act` — clicks, types, presses keys, evaluates JS, navigates, calls `clear_logs`, and replays `network` entries.
- `page_list` — lists the pages of one browser.
- `session_list` — lists every live session across browsers.
- `session_close` — closes one page.
- `browser_list` — lists browsers and their pages.

For copyable calls with expected output, see the example file at `.agents/skills/web-mcp/references/examples/page_fetch_example.md`.
