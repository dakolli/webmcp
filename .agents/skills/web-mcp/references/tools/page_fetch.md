> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-28

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
| `selector` | string | no | — | CSS selector. `html`, `text`, and `interactives` use it. On `interactives`, a container scopes the map to that subtree, and an `<iframe>` selector returns that frame's entries with page-absolute coordinates. `reflections` scopes the marker scan to the matched subtree. |
| `max_bytes` | integer | no | `50000` | Output cap. A value of `0` or less means `50000`, not unbounded. |
| `target` | string | no | — | File resource URL for `css`/`js`, resolved against a real resource URL, never an `inline-style[n]` label. For `body`: a `seq` number or a substring of the request URL, newest match wins. For `reflections`: the marker string to hunt, for example `wm-probe-1a2b`. |
| `full_page` | boolean | no | `false` | Capture the whole scrollable page. Only `screenshot` uses it. |
| `include_values` | boolean | no | `false` | With `what=storage`, return real cookie and storage values instead of `<redacted>`. `true` exposes live session tokens. |
| `filter` | string | no | — | Narrows `network` or `sinks`. `network`: comma list of request types, case-insensitive, a bare name keeps and a `-` prefix drops (`document,script,xhr`, `-image,-font`). An unknown name fails as `invalid_args`. `sinks`: `canary` or `known`. A filtered read adds `filtered_out`, the dropped count. An empty value is unfiltered. |
| `since` | integer | no | `0` | With `what=mutations`, keep only entries whose `seq` is greater than this. Read `last_seq`, act, then pass it back to see exactly what the action changed. `0` or absent returns the whole ring, and a numeric string is accepted. |
| `timeout_ms` | integer | no | `30000` | Bound for the whole call. Clamped to 1..120000. A full-page screenshot keeps its 90 s floor unless you pass a larger value. |

The live `what` enum has 26 values, in registration order: `html`, `dom`, `text`, `css`, `js`, `links`, `screenshot`, `meta`, `interactives`, `a11y`, `network`, `body`, `console`, `websocket`, `dialogs`, `article`, `tables`, `forms`, `marks`, `security`, `sinks`, `postmessage`, `storage`, `har`, `mutations`, `reflections`.

## Returns

### Aspect return shapes

| `what` | Payload at `data` | Uses `selector` | Capped by `max_bytes` |
| :--- | :--- | :--- | :--- |
| `html` | `outerHTML` string, full page or matched element. | yes | yes, text cut |
| `dom` | JSON array of document trees. | no | yes, JSON envelope |
| `text` | `document.body.innerText` string, or the matched element's `innerText`. | yes | yes, text cut |
| `css` | JSON array `[{url,type,size}]` of file stylesheets plus `inline-style[n]` labels, or one file resource's source with `target`. | no | yes, text cut |
| `js` | JSON array `[{url,type,size}]` of file scripts, or one file resource's source with `target`. | no | yes, text cut |
| `links` | JSON array `[{text,href,handle?}]`. | no | yes, JSON envelope |
| `screenshot` | One text block `{mime_type,bytes,full_page,width?,height?}`, then one PNG image block. | no | **no** |
| `meta` | JSON object `{title,url,meta:[...]}`. | no | yes, JSON envelope |
| `interactives` | JSON `{interactives:[...],truncated,frames:[{url,count,truncated}],frames_truncated}`, each item with `x`, `y`, `width`, `height`, `box {x,y,w,h}`, and `frame_url`. `box` is the layout bounds as top-left plus size, `frame_url` names the same-process child frame that owns a framed entry, and every coordinate is page-absolute, so `box.x + width/2 == x` exactly. Whole values serialize without a decimal. `marks` reports viewport top-left instead. | yes | **no** |
| `a11y` | JSON `{nodes:[...],count,truncated}`. | no | **no** |
| `network` | JSON `{entries,count,dropped,truncated,filtered_out?}`. | no | **no** |
| `body` | JSON `{seq,method,url,status,content_type,bytes,truncated}` plus `body` or `base64`. | no | **no** |
| `console` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `websocket` | JSON `{entries,count,dropped,truncated}`; each entry `{seq,socket_id,url,direction,opcode,payload,payload_bytes,payload_cut,status,ms}`. | no | **no** |
| `dialogs` | JSON `{entries,count,dropped,truncated,policy}`, the newest 100 of a 50-record ring, so `truncated` is always `0`; entries hold `seq`, `type` (`alert`, `confirm`, `prompt`, `beforeunload`, `auth`), `message`, `message_cut`, `default_prompt`, `url`, `policy`, `prompt_text` and `ms`. The engine answers every dialog at once with the current policy (default `dismiss`, set by `page_act` `dialog_accept`/`dialog_dismiss`), so a dialog never blocks a read, and `clear_logs` empties the ring. | no | **no** |
| `article` | JSON `{title,byline?,excerpt?,text,words,truncated,url}`. | no | **no** |
| `tables` | JSON `{tables:[{index,caption,headers,rows,span_info,selector}],count,truncated}`. `rows` is a list of string lists. `span_info` lists `{row,col,colspan,rowspan}`. Caps are 20 tables, 500 rows and 50 columns. | no | **no** |
| `forms` | JSON `{forms:[{index,action,method,enctype,fields,submits,csrf_candidates}],count,truncated,forms_dropped,fields_dropped}`. `forms_dropped` is the form cap remainder, and `fields_dropped` is the summed field-cap remainder across the reported forms. A field holds `name`, `type`, `label`, `required`, `has_value`, `options[]`, `selector`. A control value never appears. A submit holds `name`, `type`, `label`, `selector`. A CSRF candidate holds `name`, `selector`, `match`. | no | **no** |
| `marks` | One text block `{marks,viewport_width,viewport_height,truncated}`, then one PNG image block. | no | **no** |
| `security` | JSON `{url,policy,violations,checks:[{id,status,evidence,detail}],count,truncated}`. Passive and read-only: it reads held response headers, the cookie jar, one DOM pass and an in-memory CSP violation ring, and never reloads, injects, executes or sends a request. It reports weakness, not exploitability. `policy` is the parsed effective CSP, `violations` a 50-event `securitypolicyviolation` ring, and `checks` the findings. Each finding quotes the real header or meta tag, and `csp_bypass_trusted_host` is an example id. The eight `csp_bypass_*` ids are `unsafe_inline`, `unsafe_eval`, `script_wildcard`, `object_src`, `base_uri`, `frame_ancestors`, `report_only` and `trusted_host`. | no | **no** |
| `sinks` | JSON `{installed,hits:[{source,sink,context,value,value_cut?,stack?,selector?,ts}],dropped,truncated,filtered_out?}`. A 200-hit per-document ring emits the newest 100, oldest first, where `dropped` counts evictions and `truncated` counts buffered hits above the emit cap. `page_act` `sinks_install` arms it, `sinks_clear` empties it, and `filter` is `canary` or `known`. | no | **no** |
| `postmessage` | JSON `{installed, messages:[{origin, source_kind, data, data_type, ts, stack}], dropped, truncated, frames:[{url, probed}]}`. | no | **no** |
| `storage` | JSON `{cookies,local_storage,session_storage,indexed_db,caches}`, each with a `*_truncated` counter, and every section is present even when empty. Caps are 200 cookies, 200 keys per store, 20 databases and 40 caches. Every value is `<redacted>` until `include_values: true`, and the read never writes a cookie. | no | **no** |
| `har` | JSON `{log:{version,creator,pages,entries,comment,truncated,bodies_skipped}}`. It exports the observed network ring read-only, up to 200 entries, with a 512 KiB cap by default: the oldest entries drop first, and `log.truncated` counts the cap drops plus the ring loss and reads `0` on an uncapped export. The CLI `--out <path>` writes the document to a file instead of stdout. | no | **no** |
| `mutations` | JSON `{entries,count,dropped,truncated,last_seq}`, the newest 100 batches of a 500-batch ring; entries hold `seq`, `ts`, `kind` (`attributes`, `characterData`, `childList`), `target`, `attribute`, `added`, `removed` and `text`. Read `last_seq`, act, then pass `since` to bracket the action, because the sequence stays monotonic across a client-side route change. | no | **no** |
| `reflections` | JSON `{marker,count,truncated,hits:[{where,selector?,attribute?,context,snippet,survived,risk}],sinks_correlated?}`. `where` is `dom-text`, `attr`, `comment`, `script-text`, `style-text` or `url`; `context` is `html-text`, `attr-double`, `attr-single`, `attr-unquoted`, `js-string`, `js-template`, `css-value`, `url` or `comment`; `survived` lists the marker's own special characters that stayed unescaped; `risk` is `dangerous`, `escaped` or `inert`. Capped at 200 hits. `target` carries the marker and `selector` scopes the scan. | yes | **no** |

### Image results

`screenshot` and `marks` return two content blocks in `result.content`. The first is text and holds the standard envelope inside `data`. The second is the image, beside the text block and never inside `data`. The `screenshot` text block holds `mime_type` (`image/png`), `bytes`, `full_page`, and `width`/`height` only when the PNG header decodes. A live text block looks like this:

```json
{"ok":true,"data":{"mime_type":"image/png","bytes":91543,"full_page":false,"width":1280,"height":800}}
```

The image block is a sibling, not wrapped by `ok` or `data`: `{"type":"image","data":"<base64 PNG>","mimeType":"image/png"}`.

### Text-only clients

Image blocks are on by default. A client opts out at `initialize` with `capabilities.imageContent:false` or `capabilities.experimental.imageContent:false`, and then gets one text block. For `screenshot`, `data` is the data URI string `{"ok":true,"data":"data:image/png;base64,<payload>"}`. For `marks`, `data.screenshot` holds that data URI and other fields are unchanged. The opt-out is read per SSE session.

### Tree, list, and report shapes

- **`dom`** returns one entry per document with `title`, `url`, `frame_id`, and `root`. A node has `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`, and `children`. A node without `handle` is structural.
- **`links`** is deduplicated by `href`, first wins. It drops `javascript:`, `mailto:`, `data:`, `tel:`, empty hrefs, exactly `#`, and anchors with no text. `text` falls back to `aria-label`, `title`, then `img[alt]`.
- **`meta`** holds `title`, `url`, and a `meta` array with any of `name`, `property`, `http-equiv`, `content`, `charset`.
- **`css`** appends inline `<style>` blocks to the file-stylesheet list as label-only rows, `inline-style[1]`, `inline-style[2]`, each with no real URL. **`js`** lists file scripts only and never appends an inline `<script>` block. Both lists end with a hint naming `what=<css|js>&target=<url>`.
- **`target`** fetches a file resource by URL only. It never resolves an `inline-style[n]` label, so `what=css&target=inline-style[1]` fails with `resource "inline-style[1]" not found in the page's resource tree … No resource with given URL found`. Read an inline block through `page_act` `eval` instead, using `'style'` for a `<style>` block or `'script'` for a `<script>` block:

  ```json
  {"tool":"page_act","arguments":{"session_id":"page-1","action":"eval","value":"() => document.querySelectorAll('style')[0].textContent"}}
  ```
- **`interactives`** holds `handle`, `role`, `text`, `href`, `type`, `x`, `y`, `width`, `height`, `box {x,y,w,h}`, `frame_url`, `visible`. `box` is the layout bounds as top-left plus size, `frame_url` names the same-process child frame that owns a framed entry (absent on the top document), and every x/y/box value is page-absolute CSS px, so `box.x + box.w/2 == x`. `visible: true` means the node has a layout box, not that it is in the viewport. The payload also carries `frames`, one row per child frame and `[]` on a frameless page, and `frames_truncated`, the child-frame remainder, which is `0` on a frameless page. `truncated` is the root remainder only.
- **`a11y`** holds `handle`, `depth`, `role`, `name`, `value`, `states`, plus `count` and `truncated`. Names and values are clipped at 200 runes. Root frame only.
- **`network`** entries hold `method`, `url`, `type`, `status`, `mime`, `protocol`, `remote_ip`, `bytes`, `ms`, `failed`, `error`, `seq`, `frame_id`, `loader_id`, `top_document`, `req_headers`, `resp_headers`, and, when `page_act` `intercept_*` resolved the request, `intercepted` (`block`, `allow`, `rewrite`, or `fulfill`) plus `intercept_rule_id`. `protocol` is the CDP HTTP protocol, for example `h2` or `http/1.1`, and `remote_ip` is the peer IP, and both are omitted when Chrome did not report them. A `block` also sets `failed` and an `error` naming the rule, and a `fulfill` sets the synthetic `status` and `mime`. Each header map keeps at most 32 headers per direction at 512 bytes per value under a 4 KB name+value budget, and a cut set adds `req_headers_truncated` or `resp_headers_truncated`. `frame_id` and `loader_id` name the CDP frame and document loader that issued the request, and `top_document: true` marks the one document request that produced the page's current top-level document, so current-document traffic is separable from an earlier navigation's. Only the current document is marked. A header set by a `page_act` `intercept_*` `rewrite` rule is absent from `req_headers`, so the ring is not proof of the header set that was sent. No bodies. **`console`** entries hold `level`, `text`, `source`, `line`, `seq`.
- **`body`** adds `body` for UTF-8 text or `base64` plus `content_type` for binary, both exclusive, with `bytes` as the pre-cap size and `truncated` on the 256 KB cut. **`marks`** items hold `index`, `role`, `text`, `x`, `y`, `width`, `height` in viewport CSS pixels that can be negative, and the top-left `x`/`y` feed `page_act` `click_at` directly. The `screenshot` field appears only for a text-only client.

## Behavior and limits

### Handles and staleness

Every handle has the form `g<document>-b<backendNodeId>`, for example `gy1g0vstq-b162`. Pass a handle to `page_act` with the `handle` input. Any navigation, reload, or redirect changes the document token, so the old handle fails as stale. Re-read the aspect after navigating.

### Caps

- Caps: `interactives` returns up to 200 root entries, 50 per child frame, and 150 across all child frames, for a 350 ceiling. `truncated` is the root remainder only. `frames_truncated` is the child-frame remainder. A frame-scoped read returns only that frame's row. `a11y` caps at 400 nodes, `article` at 40000 runes, `marks` at 60 viewport boxes, `tables` at 20 tables/500 rows/50 columns, `forms` at 20 forms/500 fields/100 options/50 submits/50 CSRF candidates, `network`/`console`/`websocket` and `sinks` at 200-entry rings that emit the newest 100, `dialogs` at a 50-record ring that emits all 50, `mutations` at a 500-batch ring that emits the newest 100, `security` violations at a 50-event ring that emits all 50, and `reflections` at 200 emitted hits with a 500-hit in-page safety bound. A `truncated` count reports the rest, and `dropped` counts true ring loss.
### Network, console, and websocket capture

Capture starts before the first document loads. The network, console, and WebSocket rings are cumulative from page creation. `clear_logs` (a `page_act` action) empties those three rings, the dialog ring and the CSP violation ring, and resets their `dropped` counts, for example `{"network":39,"console":0,"websocket":0,"dialogs":0,"violations":0}`. The `seq` counters do not reset. The recipe is `clear_logs`, then one action, then read.

`filter` narrows the network read after the ring is read. It is a comma list of request types matched case-insensitively against each entry's `type`. The CLI emits the CDP casing, for example `Document` and `Stylesheet`. The filter accepts either case. A bare name keeps that type and a `-` prefix drops it, so `filter: "document,script,xhr"` keeps those three and `filter: "-image,-font"` drops images and fonts. Unknown names fail as `invalid_args` and list the accepted values: `document`, `stylesheet`, `image`, `media`, `font`, `script`, `texttrack`, `xhr`, `fetch`, `prefetch`, `eventsource`, `websocket`, `manifest`, `signedexchange`, `ping`, `cspviolationreport`, `preflight`, `other`. A filtered read keeps `count` as the returned count and adds `filtered_out`, the number dropped, so `count:0` with `filtered_out:11` means nothing matched rather than nothing captured. An unfiltered read never carries `filtered_out`.

#### Header redaction

`Cookie` and `Set-Cookie` are absent, not redacted, because Chrome omits them from CDP events. `Authorization` and `X-CSRF-Token` values show `<redacted>` by default, and the name match is case-insensitive (`code://pkg/browsers/redact.go#L24`). The engine switch `Page.CaptureSecrets(true)` keeps the values of entries captured after the switch, and entries already captured are not rewritten (`code://pkg/browsers/observe.go#L483`). No surface exposes it today.

#### Response bodies with what=body

`body` fetches one observed response on demand. Read `network` first, then call with `target` set to the entry's `seq` or a substring of its `url`. The payload is capped at 256 KB and flagged with `truncated: true`, `max_bytes` does not apply, and the browser evicts response bodies quickly, so read right after the request. Live evidence from commit `819ba12`: a form POST to `https://httpbin.org/post` returned a 1143 byte `application/json` body.

#### Security audit with what=security

`security` returns one passive audit. The policy and the header checks (`hsts`, `x-content-type-options`, `x-frame-options`, `referrer-policy`, `permissions-policy`, `cross-origin-*`) read a page-side capture of the top-document response, so they survive `clear_logs` and are not clipped at 512 bytes. The rest of the observed response headers come from the network ring, beside the cookie jar for the current URL, one DOM evaluation and the page's CSP violation ring. The audit starts at the marked top-document request (`top_document`) and reads from there onward, so evidence from a previous navigation is excluded, and empty evidence means that document request is no longer in the ring. `csp_unsafe_inline` is script-scoped: only the effective `script-src`, or its `default-src` fallback, raises it, and the detail names that directive. Source: `code://pkg/cli/pageops/security.go#L179-L237`. The report holds `url`, `policy`, `violations`, `checks`, `count`, and `truncated`. Each check holds `id`, `status` — `ok`, `warn`, `fail`, or `info` — evidence, and detail. Check ids and the status you get when the protection is missing (`code://pkg/cli/pageops/security.go#L346-L1088`):

| `id` | Meaning and status when the protection is missing |
| :--- | :--- |
| `headers` | `info` only: the document response left the ring, or the captured set was cut at 32 headers / 4 KB, so every header check below reads absent. |
| `header_conflict` | `warn` when one audited response header arrived with different values. A single value, or an equal repeat, produces no finding. |
| `csp` | `warn` when no `Content-Security-Policy` header and no `<meta http-equiv>` policy exists. A meta-only policy is a warning too. |
| `csp_unsafe_inline`, `csp_unsafe_eval` | `warn` when the policy allows the directive. Present only when any policy was found. |
| `csp_bypass_unsafe_inline`, `csp_bypass_unsafe_eval` | `fail` when the effective `script-src` or its `default-src` fallback allows the token. `unsafe-inline` is `ok` when a nonce or hash neutralizes it. |
| `csp_bypass_script_wildcard` | `fail` for `*` or a scheme source (`http:`, `https:`, `data:`, `blob:`, `filesystem:`), `warn` for a `*.host` subdomain wildcard. |
| `csp_bypass_object_src`, `csp_bypass_base_uri`, `csp_bypass_frame_ancestors` | `warn` when `object-src` has no direct or `default-src` value, when `base-uri` is absent, or when `frame-ancestors` accepts `*`, `http:`, or `https:`. An absent `frame-ancestors` is `info`. |
| `csp_bypass_report_only`, `csp_bypass_trusted_host` | `info` for any `Content-Security-Policy-Report-Only` policy, quoting its first directive. `warn` when a script-src host is a known JSONP or user-host domain. |
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

Evidence names at most 10 offending values, then `(+N more)`. The script, form, password, and cookie lists are each cut at 200 entries and every omission is reported in `truncated`.

The eight probes (`code://pkg/cli/pageops/security.go#L801`) read the enforced policy text — the response header, or a meta policy when the header is absent — plus the report-only header. `csp_bypass_trusted_host` uses a closed list: google.com, googleapis.com, cloudflare.com, jsdelivr.net, unpkg.com, github.io, herokuapp.com, firebaseapp.com, netlify.app, vercel.app, s3.amazonaws.com, cloudfront.net, azureedge.net, azurewebsites.net, and appspot.com, subdomains included.

#### The parsed policy

`policy` turns the raw header into the facts that decide whether a payload can run:

| Field | Meaning |
| :--- | :--- |
| `present` | The page declares a policy. `false` means no `Content-Security-Policy` header, no `<meta http-equiv>` policy and no report-only header. |
| `source` | `header` or `meta`, empty when `present` is `false`. |
| `report_only` | `true` when the only policy is `Content-Security-Policy-Report-Only`, which reports violations and blocks nothing. |
| `directives` | `map` of directive name to its source tokens, always `{}` rather than `null`. Only the first occurrence of a repeated directive is kept. |
| `script_src`, `style_src`, `default_src` | The declared sources as one string, empty when the directive is absent. |
| `unsafe_inline`, `unsafe_eval` | `true` only when the token appears in the effective `script-src`, falling back to `default-src`. A co-listed nonce or hash does not flip these to `false`. |
| `nonce` | The script nonce without its quotes or `nonce-` prefix, case preserved. Empty when there is none. |
| `hashes` | The `sha256-`/`sha384-`/`sha512-` tokens from the effective `script-src`, without quotes, always `[]` rather than `null`. |

#### CSP violations

`violations` is `{entries,count,dropped}`. Capture starts at document start, before the first load, so a violation from the first document is caught. Each entry holds `seq`, `directive`, `blocked_uri`, `source_file`, `line`, `column` and `disposition`. The engine keeps the newest 50 events, oldest first, and `dropped` counts evictions. `clear_logs` empties the ring with the other rings. A blocked inline script produces one entry whose `directive` is `script-src` and whose `blocked_uri` is `inline`.

### WebSocket frames with what=websocket

`websocket` reads the WebSocket frame ring (`code://pkg/browsers/websocket.go#L26-L51`). The payload is `{entries,count,dropped,truncated}`, oldest first. Each entry holds `seq`, `socket_id` (the CDP request id, shared by every frame of one connection), `url`, `direction` (`sent` or `received`), `opcode`, `payload`, `payload_bytes`, `payload_cut`, `status`, and `ms`.

`count` is the emitted count, at most 100, and `count + truncated + dropped` equals the frames observed. `dropped` counts evictions when the 200-frame ring turns over and can exceed 200, and `truncated` counts buffered frames the read held back at the 100-emit cap. A 500-frame run returned `count:100, dropped:300, truncated:100`.

| `opcode` | Frame |
| :--- | :--- |
| 1 | text |
| 2 | binary |
| 8 | close |
| 9 | ping |
| 10 | pong |

A text frame (opcode 1) carries readable `payload`, not base64, clipped at 2000 runes with `payload_cut` set; secret-like values show `<redacted>` before the clip. Every other frame carries an empty `payload` and its true size in `payload_bytes`. `status` is `101` on an observed handshake and `0` when the open event was missed. `ms` is `0` on a frame that arrived before the socket metadata, and it can be `0` even when `status` is `101`. `clear_logs` empties this ring with the network, console and dialog rings.

The aspect is read-only and takes no `filter` and no `target`. `max_bytes` does not apply. A live socket can land a frame between `clear_logs` and the read, so a zero `count` needs a quiet page or a repeat.

### Sinks tracer with what=sinks

`sinks` reads a source-to-sink tracer. Arm it first with `page_act action=sinks_install`. The arm step is opt-in and changes page behavior because it wraps page setters and constructors. Keep it off for normal page use.

The payload is `{installed, hits:[{source, sink, context, value, value_cut?, stack?, selector?, ts}], dropped, truncated}`. `installed` is true when the tracer is live in the current document. `hits` holds the newest 100 records, oldest first, and is `[]` and never null on an unarmed page. `dropped` counts hits evicted by the 200-hit ring. `truncated` counts buffered hits older than the emitted 100. `max_bytes` does not apply.

`source` names the tagged read, for example `location.hash`, `message`, `localStorage.getItem`, or `unknown`. `sink` names the instrumented API, for example `innerHTML`, `eval`, `setAttribute:href`, or `location.assign`. `context` is `html`, `attr`, `url`, or `js`. `stack` keeps 5 frames. `selector` is a short CSS path to the node that received the value. `ts` is Unix milliseconds. `value` and each stack or selector string clip at 200 runes, and `value_cut` marks a clipped value. `stack` and `selector` are omitted when empty.

An unarmed page returns `{"installed":false,"hits":[],"dropped":0,"truncated":0}` with no error. A read is not a state change. The ring is per document. A full navigation starts a fresh ring, because the arm step re-injects the script before the new document's own scripts, so `installed` stays true. The new document's own scripts can write hits at once, so a post-navigation read can be non-empty. A client-side route change keeps the same ring and appends to it. Clear with `page_act action=sinks_clear`, which returns `{"cleared":N}`.

`filter=canary` keeps only hits whose `value` holds the injected `wm-canary-…` marker, and `filter=known` drops hits whose `source` is `unknown`. Both add `filtered_out` to the envelope, so `filter: "known"` on a page whose only hits have an unknown source returns an empty `hits` list with `filtered_out` equal to the number dropped, not an error. `filter=canary` matches only the auto-minted `wm-canary-…` value; a custom `inject_canary` value outside that prefix is not matched, so read unfiltered for it. `inject_canary` can return an empty `hits` list with no error, because no instrumented sink received the canary in the current document; that is a normal result, not a failure, and the ring read is the fallback.

### postMessage recorder with what=postmessage

`postmessage` reads the `message` event ring. Arm it with `page_act action=postmessage_install`. The payload is `{installed, messages:[{origin, source_kind, data, data_type, ts, stack}], dropped, truncated, frames:[{url, probed}]}`. `messages` is `[]` and never null. `source_kind` is `window`, `frame`, `worker`, or `unknown`. `data_type` is `string`, `json`, `object`, or `other`. `stack` keeps 5 receive-side frames and is omitted when empty.

The ring holds 100 messages per window. One read emits the newest 100 across the root window and every iframe, oldest first. `dropped` counts ring eviction, and `truncated` counts buffered messages above the emit cap. `data` clips at 500 runes. `frames` lists each window the read reached, up to 20, and `probed` reports whether the recorder was armed there. There is no per-frame ring and no count per frame: `messages` is one merged list and `frames` lists probed URLs only, so a frame-targeted send cannot be attributed to a specific frame element from the payload alone. An unarmed page returns `{"installed":false,"messages":[],"dropped":0,"truncated":0,"frames":[{"url":"…","probed":false}]}` with no error. `target=clear` empties every ring and returns `{"cleared":N}`. `max_bytes` does not apply.

### Reflections with what=reflections

`reflections` answers one XSS question: did my marker survive into a live context, and did its special characters survive with it? It is read-only and never sends a request. The agent sets the marker, usually by opening `?q=wm-probe-1a2b` with `fetch_page`, then passes the same string as `target`. An empty marker fails as `invalid_args`. A marker with no hits returns `{...,"count":0,"hits":[]}`, not an error.

The payload is `{marker,count,truncated,hits:[{where,selector?,attribute?,context,snippet,survived,risk}],sinks_correlated?}`. The scan reads three sources: the live DOM (text nodes, attributes, comments, `<script>`/`<style>` text), the serialized `document.documentElement.outerHTML`, and `document.URL`. One hit is produced per matching attribute, text node, script or style text node, comment and URL occurrence. It is root-frame only; a child iframe is a separate document and is not scanned. `count` is the emitted count and `truncated` is true when the total exceeds the 200-hit cap.

| `where` | Source |
| :--- | :--- |
| `dom-text` | A text node outside script and style. |
| `attr` | An element attribute whose value carries the marker; `attribute` names it. |
| `comment` | A comment node. |
| `script-text` | The text node of a `<script>` element. |
| `style-text` | The text node of a `<style>` element. |
| `url` | `document.URL`. |

`context` comes from the node kind and the delimiter the serializer observed: `html-text`, `attr-double`, `attr-single`, `attr-unquoted`, `js-string`, `js-template`, `css-value`, `url`, or `comment`. A marker inside a double-quoted attribute reports `attr-double` and names the attribute.

`snippet` is the serialized content within 40 characters on each side of the marker, with the enclosing tag and delimiter quotes removed and an ellipsis marking a cut. The serialized form is authoritative: escaped text shows `&lt;` and `&quot;`, script and style text stay raw, and attribute values are read from the element's serialized start tag. Reading the serialized form is how an escaped reflection is told apart from a raw one, because the DOM value itself decodes both to the same character.

`survived` lists the special characters the marker itself carries (`<`, `>`, `"`, `'`, `&`, `/`) that appear literally in the serialized snippet rather than as an entity. Survival is scoped to the marker, so surrounding markup or a JS string's own quotes never count.

`risk` is `dangerous` only when `<` or a matching quote survives in an executable context: `<` in `html-text`, `css-value`, `js-string` or `js-template`, the enclosing quote in `js-string`, the delimiter quote in `attr-double`/`attr-single`/`attr-unquoted`. It is `escaped` when the entity form of a marker character is present, and `inert` otherwise. `url` and `comment` are never `dangerous`.

When `page_act action=sinks_install` armed the tracer, `sinks_correlated` lists each tracer hit whose `value` carries the marker as `{sink,context,selector?,value}`, linking the reflection to execution. It is omitted when the tracer is uninstalled or no hit matches.

Source: `code://pkg/cli/pageops/reflections.go#L126-L155`. The in-page probe is `code://pkg/cli/pageops/reflections.go#L391`, and the classifier is `code://pkg/cli/pageops/reflections.go#L256-L305`.

### Storage snapshot with what=storage

`storage` is a read-only snapshot of the browser context. `cookies` hold `name`, `value`, `domain`, `path`, `secure`, `http_only`, `same_site`, `expires`, and `size`. `local_storage` and `session_storage` items hold `key`, `value_length`, and `value`. `indexed_db` items hold `name` and `stores`, and `caches` is a name list. Every section is present even when empty. Caps are 200 cookies, 200 keys per store, 20 databases, and 40 caches, each reported by its own `<section>_truncated`. Values are the literal `<redacted>` unless `include_values: true` (`code://pkg/cli/pageops/storage.go#L135-L156`). `max_bytes` does not apply.

### HAR export with what=har

**Tested: 2026-10-26 on `python.org` and `httpbingo.org`.** The shape comes from `code://pkg/har/har.go`. `har` exports the observed network ring as a HAR 1.2 document. It is read-only and never arms capture. The payload is `{log:{version:"1.2",creator:{name:"web-mcp",version:"1.0"},pages:[{startedDateTime,id:"page_1",title,pageTimings:{onContentLoad:-1,onLoad:-1}}],entries:[...],comment,truncated?,bodies_skipped}}`. Entries hold `request` and `response` with headers, query strings, status, sizes, and `time`; `request.httpVersion` and `response.httpVersion` carry the CDP protocol, `HTTP/1.1` when it is unknown (`code://pkg/har/har.go#L744-L747`). `content.text` carries the response body, `content.encoding: "base64"` marks a binary body, and `content.size` is the decoded byte count (`code://pkg/har/har.go#L236-L243`). `request.postData` carries `{mimeType,text}` on a request with a body (`code://pkg/har/har.go#L189-L195`). `serverIPAddress` names the peer IP Chrome reported and is omitted when Chrome reported none (`code://pkg/har/har.go#L158-L160`). `request.cookies` is snapshotted from the browser cookie jar and matched to each request URL by domain, path, and Secure scheme, while `response.cookies` is always `[]` because Chrome omits `Set-Cookie` from CDP events (`code://pkg/har/har.go#L531-L543`). `startedDateTime` is the export time, because the ring keeps monotonic timestamps only. `time` is the measured round trip and is booked as `timings.wait`. Each redirect hop is its own entry. The 3xx entry sets `response.redirectURL` from its `Location`. The final response is a separate entry. Redaction covers header values and cookie values only: sensitive header values such as `Authorization` and `X-CSRF-Token` read `<redacted>`, and so do cookie values unless secret capture is on. Response bodies are exported verbatim, so a body can echo a request cookie. The MCP `page_fetch` export defaults to a 512 KiB cap (`code://pkg/har/har.go#L23-L25`), while `browser page fetch <id> har --out traffic.har` requests no cap (`code://pkg/cli/cmd/page.go#L332-L338`) and its `log.comment` says `Cap: none` (`code://pkg/har/har.go#L696-L700`). For a capped export the oldest entries drop first, and `log.truncated` reports the byte-cap and ring drops and reads `0` when nothing dropped; the uncapped export reports `0` and states any ring loss in `log.comment`. `max_bytes` does not apply. The CLI can write the same document to a file: `browser page fetch <id> har --out traffic.har` prints `wrote traffic.har (<n> entries)` and never echoes the body. `--out` needs the `har` aspect, the parent directory must already exist, the file is mode 0600, and `--max-bytes N` caps the write (without it the CLI asks for the uncapped export).

### Screenshots and timeouts

`screenshot` returns PNG only, as an image block plus a metadata text block. There is no format input. `full_page: false` captures the visible viewport. `full_page: true` captures the whole scrollable page and keeps a 90 s floor unless `timeout_ms` is larger. `timeout_ms` bounds every call, defaults to 30000, and clamps to 1..120000. A timeout names the aspect and the value, for example `page_fetch: what="screenshot" timed out after 30s (timeout_ms=30000, max 120000) …`. Large images are not cut by `max_bytes`. Prefer a small viewport for `marks`.

## Truncation matrix

| Group | When `max_bytes` is exceeded | Parseable | Signal |
| :--- | :--- | :--- | :--- |
| `html`, `text`, `css`, `js` | Cut on a rune boundary. A note is appended. | No. A `css`/`js` list is cut inside its JSON. | Text note |
| `dom`, `links` | Array root becomes an envelope object. | Yes | `truncated: true`, `omitted` |
| `meta` | Object keeps its fields, its first array field shrinks. | Yes | `truncated: true`, `omitted` |
| `screenshot`, `interactives`, `a11y`, `network`, `body`, `console`, `websocket`, `dialogs`, `article`, `tables`, `forms`, `marks`, `security`, `sinks`, `postmessage`, `storage`, `har`, `mutations`, `reflections` | Not cut. | Yes | Shape's own `truncated` or `count` |

The text note is exact: `… (truncated at max_bytes=600 bytes; raise max_bytes — dom/links/meta do not use selector)`. The array envelope is `{"items":[...],"max_bytes":N,"omitted":N,"truncated":true}`.

**`dom` is nearly all-or-nothing.** The envelope cuts whole documents, not nodes. The Wikipedia document exceeded 400000 bytes, and at `max_bytes: 50000` the live result was `{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`. Use `article`, `text`, or `html` with a `selector` for large pages, never a megabyte `max_bytes`.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The prefix `page_fetch: ` opens the message. This tool produces the types `invalid_args`, `element_not_found`, `page_closed`, `timeout`, `nav_failed`, and `unknown` (`code://pkg/mcp/tools/envelope.go#L67-L130`). One full envelope below was recorded live on 2026-10-26.

**Unknown session.** Type `page_closed`. Message `page_fetch: session "page-999" not found`. Recovery: re-read `session_list` or `page_list`, because the idle sweep removes pages.

**Unknown `what`.** Type `invalid_args`. `message` is `page_fetch: unknown what="<value>" (must be one of <the live enum>)`. Recovery: send one listed value. `canary` is not a `what` value; it is the `filter` value on `what=sinks`.

**Unknown `filter` value.** Type `invalid_args`. A `network` name outside the accepted list returns `invalid arguments: what=network filter "<value>" names unknown request type "<name>" — accepted: document, stylesheet, …`, and a `sinks` value other than `canary` or `known` returns `invalid arguments: what=sinks filter "<value>" is not accepted — use canary … or known …`. Recovery: send one accepted value.

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
- 2026-10-26 — `tables` and `forms` on a live page. `forms` reported `has_value:false`, then `has_value:true` after an input, and never returned the value. `timeout_ms` bounded a slow read and the failure named `what` and the value.
- 2026-10-27 — live on `https://www.debian.org/`: every `network` entry carried `frame_id` and `loader_id`, and the document request carried `top_document:true`, `dropped:0`, `truncated:0`. `security` returned `count:21` with no `header_conflict`, which is correct for a page whose audited headers each carry one value.
- 2026-10-27 — source-verified against commit `83a6797` and its tests: the eight `csp_bypass_*` checks, their `fail`/`warn`/`info` statuses, and the trusted-host list. The running daemon predated the commit and returned the 13-check report above, so there is no live call for the new ids yet.
- 2026-10-28 — live on `https://www.freebsd.org/?probe=one`. `filter:"document,stylesheet,script"` returned `count:5` with `filtered_out:14`. `filter:"-image,-font"` returned `count:7` with `filtered_out:12`. The unfiltered read returned `count:19` with no `filtered_out`. `filter:"banana"` returned `invalid_args` with all 18 names. `filter:"canary"` kept one sink hit with `filtered_out:0`.
- 2026-10-30 — live: `reflections` works on the running server. A marker with no hits returned `{"marker":"…","count":0,"truncated":0,"hits":[]}`, not an error. Each hit carried `where` (`dom-text`, `attr`, `comment`, `script-text`, `style-text`, `url`), `selector`, `attribute`, `context`, `snippet`, `survived` and `risk`.

## When not to use it

Do not use `page_fetch` to open a page, use `fetch_page`. Do not use it to click, type, scroll, or navigate, use `page_act`. Do not use `dom` for prose on a large page, use `article` or `text`. Do not use `interactives` to test whether an element is on screen — use `marks` for viewport boxes. Do not expect `selector` to scope `dom`, `meta`, `links`, `a11y`, `marks`, or `security`. They ignore it, while `interactives` and `reflections` are scoped by it. `security` is a passive audit of what the browser already holds, not an exploit scanner. `reflections` reports what the parsed DOM holds after normalization, so a payload that the browser already turned into a live element may leave no literal marker to find; use the sink tracer for execution proof.

## Related tools

`fetch_page` opens the session, `page_act` acts on it, and `page_list`, `session_list`, `session_close`, and `browser_list` manage pages and browsers. For copyable calls with expected output, see the example file at `.agents/skills/web-mcp/references/examples/page_fetch_example.md`.
