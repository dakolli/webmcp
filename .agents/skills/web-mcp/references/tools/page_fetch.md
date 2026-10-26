> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-26

# page_fetch

Read one aspect of a live page that `fetch_page` opened. It does not navigate, click, or type. The `session_id` picks the tab, `what` picks the aspect.

The payload rides inside the result envelope, so the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope rules and the `{"ok":false,"error":…}` shape. The payload is one JSON object or array. `html`, `text`, `css`, and `js` arrive as JSON strings inside `data` with no further wrapper. `screenshot` and `marks` return one text block plus one image block. Read the image result rule below.

## Inputs

The live schema is `code://pkg/mcp/tools/page.go#L90`. The aspect registry is `code://pkg/mcp/tools/aspects.go#L67`. A session held outside the declared owner is refused as `not_found`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | yes | — | Tab to read, for example `page-1`. Comes from `fetch_page`. |
| `browser_id` | string | no | — | Optional. Resolve the session inside this browser only, for example `browser-2`. Omit it when exactly one live browser holds the id. |
| `owner` | string | no | — | Optional. Restrict the lookup to browsers with this owner label, 1 to 64 characters of `A-Z`, `a-z`, `0-9`, `.`, `_`, `-`. |
| `what` | string | yes | — | Aspect name. See the list below. |
| `selector` | string | no | — | CSS selector. Only `html` and `text` use it. |
| `max_bytes` | integer | no | `50000` | Output cap. A value of `0` or less means `50000`, not unbounded. |
| `target` | string | no | — | Resource URL for `css`/`js`. For `body`: a `seq` number or a substring of the request URL, newest match wins. |
| `full_page` | boolean | no | `false` | Capture the whole scrollable page. Only `screenshot` uses it. |
| `include_values` | boolean | no | `false` | With `what=storage`, return real cookie and storage values instead of `<redacted>`. `true` exposes live session tokens. |

The live `what` enum has 20 values, in registration order: `html`, `text`, `links`, `meta`, `dom`, `css`, `js`, `screenshot`, `interactives`, `a11y`, `network`, `console`, `body`, `article`, `marks`, `security`, `storage`, `sinks`, `postmessage`, `har`. The tool has no `timeout_ms` input, every call has a fixed 30 second bound, and a full-page screenshot gets its own 90 second budget.

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
| `interactives` | JSON `{interactives:[...],truncated}`, each item with `x`, `y`, `width`, `height`. `x`/`y` are the element centre and `left = x - width/2`. Whole values serialize without a decimal. `marks` reports top-left instead. | no | **no** |
| `a11y` | JSON `{nodes:[...],count,truncated}`. | no | **no** |
| `network` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `body` | JSON `{seq,method,url,status,content_type,bytes,truncated}` plus `body` or `base64`. | no | **no** |
| `console` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `article` | JSON `{title,byline?,excerpt?,text,words,truncated,url}`. | no | **no** |
| `marks` | One text block `{marks,viewport_width,viewport_height,truncated}`, then one PNG image block. | no | **no** |
| `security` | JSON `{url,checks:[{id,status,evidence,detail}],count,truncated}`. | no | **no** |
| `sinks` | JSON `{installed,hits:[{source,sink,context,value,value_cut?,stack?,selector?,ts}],dropped,truncated}`. | no | **no** |
| `postmessage` | JSON `{installed, messages:[{origin, source_kind, data, data_type, ts, stack}], dropped, truncated, frames:[{url, probed}]}`. | no | **no** |
| `storage` | JSON `{cookies,local_storage,session_storage,indexed_db,caches}`, each with a `*_truncated` counter. | no | **no** |
| `har` | JSON `{log:{version,creator,pages,entries,comment?,truncated?}}`. Tested: 2026-10-26. | no | **no** |

### Image results

`screenshot` and `marks` return two content blocks in `result.content`. The first is text and holds the standard envelope inside `data`. The second is the image, beside the text block and never inside `data`. The `screenshot` text block holds `mime_type` (`image/png`), `bytes`, `full_page`, and `width`/`height` only when the PNG header decodes. A live text block looks like this:

```json
{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}
```

The image block is a sibling, not wrapped by `ok` or `data`:

```json
{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}
```

### Text-only clients

Image blocks are on by default. A client opts out at `initialize` with `capabilities.imageContent:false` or `capabilities.experimental.imageContent:false`, and then gets one text block. For `screenshot`, `data` is the data URI string `{"ok":true,"data":"data:image/png;base64,<payload>"}`. For `marks`, `data.screenshot` holds that data URI and other fields are unchanged. The opt-out is read per SSE session.

### Tree, list, and report shapes

- **`dom`** returns one entry per document with `title`, `url`, `frame_id`, and `root`. A node has `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`, and `children`. A node without `handle` is structural.
- **`links`** is deduplicated by `href`, first wins. It drops `javascript:`, `mailto:`, `data:`, `tel:`, empty hrefs, exactly `#`, and anchors with no text. `text` falls back to `aria-label`, `title`, then `img[alt]`.
- **`meta`** holds `title`, `url`, and a `meta` array with any of `name`, `property`, `http-equiv`, `content`, `charset`. **`css`** and **`js`** list resources, with inline `<style>`/`<script>` blocks appearing as `inline-style[1]`. Both lists end with a hint naming `what=<css|js>&target=<url>`.
- **`interactives`** holds `handle`, `role`, `text`, `href`, `type`, `x`, `y`, `width`, `height`, `visible`. `visible: true` means the node has a layout box, not that it is in the viewport.
- **`a11y`** holds `handle`, `depth`, `role`, `name`, `value`, `states`, plus `count` and `truncated`. Names and values are clipped at 200 runes. Root frame only.
- **`network`** entries hold `method`, `url`, `type`, `status`, `mime`, `bytes`, `ms`, `failed`, `error`, `seq`, `req_headers`, `resp_headers`. Each header map keeps at most 32 headers per direction at 512 bytes per value under a 4 KB name+value budget, and a cut set adds `req_headers_truncated` or `resp_headers_truncated`. No bodies. **`console`** entries hold `level`, `text`, `source`, `line`, `seq`.
- **`body`** adds `body` for UTF-8 text or `base64` plus `content_type` for binary, both exclusive, with `bytes` as the pre-cap size and `truncated` on the 256 KB cut. **`marks`** items hold `index`, `role`, `text`, `x`, `y`, `width`, `height` in viewport CSS pixels that can be negative, and the top-left `x`/`y` feed `page_act` `click_at` directly. The `screenshot` field appears only for a text-only client.

## Behavior and limits

### Handles and staleness

Every handle has the form `g<document>-b<backendNodeId>`, for example `gy1g0vstq-b162`. Pass a handle to `page_act` with the `handle` input. Any navigation, reload, or redirect changes the document token, so the old handle fails as stale. Re-read the aspect after navigating.

### Caps

- Caps: `interactives` 200 entries, `a11y` 400 nodes, `article` 40000 runes, `marks` 60 viewport boxes, and `network`/`console` 200-entry rings that emit the newest 100. A `truncated` count reports the rest, and `dropped` counts true ring loss.

### Network and console capture

Capture starts before the first document loads. Both rings are cumulative from page creation. `clear_logs` (a `page_act` action) empties both rings and resets `dropped`, for example `{"network":39,"console":0}`. The `seq` counter does not reset. The recipe is `clear_logs`, then one action, then read.

#### Header redaction

`Cookie` and `Set-Cookie` are absent, not redacted, because Chrome omits them from CDP events. `Authorization` and `X-CSRF-Token` values show `<redacted>` by default, and the name match is case-insensitive (`code://pkg/browsers/redact.go#L24`). The engine switch `Page.CaptureSecrets(true)` keeps the values of entries captured after the switch, and entries already captured are not rewritten (`code://pkg/browsers/observe.go#L400`). No surface exposes the switch today.

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

`sinks` reads a source-to-sink tracer. Arm it first with `page_act action=sinks_install`. The arm step is opt-in and changes page behavior because it wraps page setters and constructors. Keep it off for normal page use.

The payload is `{installed, hits:[{source, sink, context, value, value_cut?, stack?, selector?, ts}], dropped, truncated}`. `installed` is true when the tracer is live in the current document. `hits` holds the newest 100 records, oldest first, and is `[]` and never null on an unarmed page. `dropped` counts hits evicted by the 200-hit ring. `truncated` counts buffered hits older than the emitted 100. `max_bytes` does not apply.

`source` names the tagged read, for example `location.hash`, `message`, `localStorage.getItem`, or `unknown`. `sink` names the instrumented API, for example `innerHTML`, `eval`, `setAttribute:href`, or `location.assign`. `context` is `html`, `attr`, `url`, or `js`. `stack` keeps 5 frames. `selector` is a short CSS path to the node that received the value. `ts` is Unix milliseconds. `value` and each stack or selector string clip at 200 runes, and `value_cut` marks a clipped value. `stack` and `selector` are omitted when empty.

An unarmed page returns `{"installed":false,"hits":[],"dropped":0,"truncated":0}` with no error. A read is not a state change.

The ring is per document. A full navigation starts a fresh ring, because the arm step re-injects the script before the new document's own scripts, so `installed` stays true. The new document's own scripts can write hits at once, so a post-navigation read can be non-empty. A client-side route change keeps the same ring and appends to it. Clear with `page_act action=sinks_clear`, which returns `{"cleared":N}`.

### postMessage recorder with what=postmessage

`postmessage` reads the `message` event ring. Arm it with `page_act action=postmessage_install`. The payload is `{installed, messages:[{origin, source_kind, data, data_type, ts, stack}], dropped, truncated, frames:[{url, probed}]}`. `messages` is `[]` and never null. `source_kind` is `window`, `frame`, `worker`, or `unknown`. `data_type` is `string`, `json`, `object`, or `other`. `stack` keeps 5 receive-side frames and is omitted when empty.

The ring holds 100 messages per window. One read emits the newest 100 across the root window and every iframe, oldest first. `dropped` counts ring eviction. `truncated` counts buffered messages above the emit cap. `data` clips at 500 runes. `frames` lists each window the read reached, up to 20, and `probed` reports whether the recorder was armed there. There is no per-frame ring and no count per frame: `messages` is one merged list and `frames` lists probed URLs only, so a frame-targeted send cannot be attributed to a specific frame element from the payload alone. An unarmed page returns `{"installed":false,"messages":[],"dropped":0,"truncated":0,"frames":[{"url":"…","probed":false}]}` with no error. `target=clear` empties every ring and returns `{"cleared":N}`. `max_bytes` does not apply.

### Storage snapshot with what=storage

`storage` is a read-only snapshot of the browser context. `cookies` hold `name`, `value`, `domain`, `path`, `secure`, `http_only`, `same_site`, `expires`, and `size`. `local_storage` and `session_storage` items hold `key`, `value_length`, and `value`. `indexed_db` items hold `name` and `stores`, and `caches` is a name list. Every section is present even when empty. Caps are 200 cookies, 200 keys per store, 20 databases, and 40 caches, each reported by its own `<section>_truncated`. Values are the literal `<redacted>` unless `include_values: true` (`code://pkg/cli/pageops/storage.go#L135-L156`). `max_bytes` does not apply.

### HAR export with what=har

**Tested: 2026-10-26 on `python.org` and `httpbingo.org`.** The shape comes from `code://pkg/har/har.go`. `har` exports the observed network ring as a HAR 1.2 document. It is read-only and never arms capture. The payload is `{log:{version:"1.2",creator:{name:"web-mcp",version:"1.0"},pages:[{startedDateTime,id:"page_1",title,pageTimings:{onContentLoad:-1,onLoad:-1}}],entries:[...],comment,truncated?}}`. Entries hold `request` and `response` with headers, query strings, status, sizes, and `time`. `startedDateTime` is the export time, because the ring keeps monotonic timestamps only. `time` is the measured round trip and is booked as `timings.wait`. Each redirect hop is its own entry. The 3xx entry sets `response.redirectURL` from its `Location`. The final response is a separate entry. `Cookie` and `Set-Cookie` are absent, not redacted, because Chrome omits them from CDP events. Only `Authorization` and `X-CSRF-Token` read `<redacted>`. The document is capped at 512 KiB. The oldest entries drop first, and `log.truncated` reports the byte-cap and ring drops and reads `0` when nothing dropped. `max_bytes` does not apply.

### Screenshots and timeouts

`screenshot` returns PNG only, as an image block plus a metadata text block. There is no format input. `full_page: false` captures the visible viewport. `full_page: true` captures the whole scrollable page on a 90 second budget that is independent of the 30 second bound, and a very tall page can still time out. Large images are not cut by `max_bytes` — a 400x300 full-page capture produced over 87 KB of base64. Prefer a small viewport for `marks`.

With `--bind 0.0.0.0`, the server prints `http://[::]:<port>`. Put a reachable host in the client URL.

## Truncation matrix

| Group | When `max_bytes` is exceeded | Parseable | Signal |
| :--- | :--- | :--- | :--- |
| `html`, `text`, `css`, `js` | Cut on a rune boundary. A note is appended. | No. A `css`/`js` list is cut inside its JSON. | Text note |
| `dom`, `links` | Array root becomes an envelope object. | Yes | `truncated: true`, `omitted` |
| `meta` | Object keeps its fields, its first array field shrinks. | Yes | `truncated: true`, `omitted` |
| `screenshot`, `interactives`, `a11y`, `network`, `body`, `console`, `article`, `marks`, `security`, `sinks`, `postmessage`, `storage`, `har` | Not cut. | Yes | Shape's own `truncated` or `count` |

The text note is exact: `… (truncated at max_bytes=600 bytes; raise max_bytes — dom/links/meta do not use selector)`. The array envelope is `{"items":[...],"max_bytes":N,"omitted":N,"truncated":true}`.

**`dom` is nearly all-or-nothing.** The envelope cuts whole documents, not nodes. The Wikipedia document exceeded 400000 bytes. At `max_bytes: 50000` the live result was `{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`. Use `article`, `text`, or `html` with a `selector` for large pages, never a megabyte `max_bytes`.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The prefix `page_fetch: ` opens the message. This tool produces the types `invalid_args`, `element_not_found`, `page_closed`, `timeout`, `nav_failed`, and `unknown` (`code://pkg/mcp/tools/envelope.go#L67-L130`). One full envelope below was recorded live on 2026-10-26.

**Unknown session.** Type `page_closed`. Message `page_fetch: session "page-999" not found`. Recovery: re-read `session_list` or `page_list`, because the idle sweep removes pages.

**Unknown `what`.** Type `invalid_args`. `message` is `page_fetch: unknown what="<value>" (must be one of <the live enum>)`. Recovery: send one listed value. `canary` is not on the tool surface.

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
- 2026-10-26 — `sinks` on `https://www.postgresql.org/`: `sinks_install` returned `{"installed":true}`, the arm survived a full navigation, `sinks_clear` returned `{"cleared":N}`, and 250 hits reported `hits:100`, `dropped:50`, `truncated:100`. A `postmessage` read on https://www.python.org/ returned the message list, `frames:[{url,probed}]`, and `target=clear` returned `{"cleared":N}`.

## When not to use it

Do not use `page_fetch` to open a page. Use `fetch_page`. Do not use it to click, type, scroll, or navigate. Use `page_act`. Do not use `dom` for prose on a large page. Use `article` or `text`. Do not use `interactives` to test whether an element is on screen — use `marks` for viewport boxes. Do not expect `selector` to scope `dom`, `meta`, `links`, or `security`. They ignore it. `security` is a passive audit of what the browser already holds, not an exploit scanner.

## Related tools

- `fetch_page` opens the session. `page_act` acts on it. `page_list`, `session_list`, `session_close`, and `browser_list` manage pages and browsers.

For copyable calls with expected output, see the example file at `.agents/skills/web-mcp/references/examples/page_fetch_example.md`.
