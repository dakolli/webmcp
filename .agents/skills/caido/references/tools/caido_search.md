> Tool: `caido_search` | Server: web-mcp | Surface: MCP | Tested: 2026-10-29

# caido_search

## What it does

Reads the recorded HTTP history of a local Caido instance over its GraphQL API at `http://127.0.0.1:8080/graphql`. It returns the newest matching transactions, newest first, with bounded bodies. It never changes, replays, blocks or deletes traffic.

The Caido proxy must listen on `127.0.0.1:8081`. Only traffic that passed through that proxy is in the history. A human starts the instance with `make caido` and the tool never starts it.

## Inputs

All inputs are optional. An unknown key fails as `invalid_args` and names the valid keys.

| Input | Type | Default | Limits | Behavior |
| :--- | :--- | :--- | :--- | :--- |
| `url` | string | `""` | any string | Case-insensitive substring of the composed URL. Applied client-side over the scanned rows. Empty means every URL. |
| `method` | string | `""` | `GET`, `POST`, `""` | Server-side filter. The value is trimmed and uppercased. Any other value fails. |
| `status` | integer | `0` | `>= 0` | Server-side exact response code. `0` means every code. A negative value fails. |
| `since` | integer | `900` | `>= 0` | Lookback window in seconds from now. `0` disables the time filter. A negative value fails. |
| `limit` | integer | `20` | `0` to `100` | Rows returned. `0` becomes `20` with no marker. A value above `100` clamps to `100` and sets `limit_applied:100`. |
| `body_bytes` | integer | `2000` | `0` to `65536` | Cap per returned body in bytes. `0` returns headers with empty bodies. A value above `65536` clamps and sets `body_bytes_applied:65536`. |
| `part` | string | `both` | `request`, `response`, `both` | Which side carries a body. The value is trimmed and lowercased. Empty means `both`. |
| `raw` | boolean | `false` | `true`, `false` | Disable credential redaction. The `body_bytes` cut still applies. |

A quoted number that parses is accepted, for example `"limit":"50"`. A quoted boolean that parses is accepted too.

## Returns

The success envelope is `{"ok":true,"data":<result>}`. `result` holds:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `entries` | array | The returned transactions, newest first. Empty on no match. |
| `count` | integer | The rows returned. Equals `len(entries)`. |
| `total` | integer | The server count for the `method` and `status` filter only. It ignores `url` and `since` and can exceed 500. |
| `scanned` | integer | The rows examined, up to 500. |
| `dropped` | integer | The rows rejected by `url` or `since`. |
| `limit_applied` | integer | Present only when `limit` was clamped. Always `100`. |
| `body_bytes_applied` | integer | Present only when `body_bytes` was clamped. Always `65536`. |

Each entry holds:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The Caido request id. Use it to find the row in the Caido UI. |
| `method` | string | The request method. |
| `url` | string | The composed URL `scheme://host[:port]path[?query]`. Redacted unless `raw:true`. |
| `host` | string | The request host. |
| `path` | string | The path without the query. |
| `status` | integer | The response status code. `0` when no response was captured. |
| `mime` | string | The response `Content-Type` without parameters. Absent when the response side was not fetched. |
| `bytes` | integer | The request size that Caido recorded. |
| `response_bytes` | integer | The raw captured response size, headers included. It can exceed `content-length`. |
| `ms` | integer | The response roundtrip time. |
| `created_at` | string | RFC3339 UTC. Absent when Caido reported no time. |
| `request_headers` | object | Lowercased header names. Each value is an array. The first captured value wins. |
| `response_headers` | object | Same shape as `request_headers`. |
| `request_body` | string | The request body after redaction and cut. Absent when empty. |
| `response_body` | string | The response body after redaction and cut. Absent when empty. |
| `request_cut` | boolean | Present only when the returned request body was shortened, or dropped with its headers because the message exceeds 8 MiB. |
| `response_cut` | boolean | Same rule for the response. |
| `url_redacted` | boolean | Present only when a credential-shaped query parameter was masked. Header values that hold a URL, such as `referer` and `location`, lose their credential parameters without setting this flag. |

Empty fields are omitted, not set to `""` or `null`. Read a missing `request_body` as no body on that side.

## Scan window and ordering

1. The tool requests 100 rows per page with `order: {by: CREATED_AT, ordering: DESC}`.
2. It reads at most 5 pages, so the scan depth is 500 rows whatever `limit` is.
3. `method` and `status` become the HTTPQL filter `req.method.eq:"POST" AND resp.code.eq:200` inside `requestsByOffset`. `url` and `since` never reach the server.
4. Each row is tested against `url` and `since`. A rejected row increments `dropped`.
5. A row older than the `since` cutoff stops the scan at once, because the order is newest first.
6. The scan also stops as soon as `limit` rows are kept, so `scanned` can be below 500.
7. Only the kept rows get a body fetch. One aliased GraphQL query fetches every kept raw message.

`total` comes from page 0 only, before any `url` or `since` test. That is why it can show a large number while `count` is small.

## Bodies, cuts and part

- `part:"request"` fetches only the request side. `mime` and `response_headers` are then absent.
- `part:"response"` fetches only the response side. `request_headers` and `request_body` are absent.
- `body_bytes:0` still fetches the raw message, then returns an empty body. It sets no cut flag.
- A body longer than `body_bytes` is cut on a rune boundary and sets `request_cut` or `response_cut`.
- A raw message whose declared length exceeds 8 MiB is not fetched. Its headers and body are dropped and the side is marked cut.

## Redaction contract

Redaction is on unless `raw:true`. It runs before the cut, so a JSON body is not trimmed into an unparseable fragment first.

- **Headers.** A value becomes `<redacted>` when the lowercased name contains `token`, `secret`, `auth`, `key`, `cookie`, `session`, `csrf`, `signature`, `password` or `credential`.
- **Readable headers.** These stay readable even if their name matches: `content-type`, `content-length`, `accept`, `user-agent`, `host`, `referer`, `origin`, `date`, `server`, `etag`, `cache-control`, the `access-control-*` set, `strict-transport-security`, `content-security-policy`, `permissions-policy`, `x-frame-options`, `x-content-type-options`, `vary`, `expires` and `last-modified`.
- **Boolean headers.** A header whose every value is `true` or `false` stays readable, so a policy flag is not hidden.
- **JSON bodies.** A valid JSON object is walked by key. A key that contains `token`, `secret`, `password`, `passw`, `authorization`, `cookie`, `session`, `apikey`, `api_key`, `api-key`, `csrf`, `signature` or `credential` gets the value `<redacted>` at any depth.
- **Form, XML and plain bodies.** A pattern pass then masks `key=value`, `key: value` and `<key>value</key>` when the key has the same credential shape. This pass covers form, XML and plain text.
- **URL.** A credential-shaped query parameter becomes `<redacted>` and sets `url_redacted:true`.
- **Known gaps.** A credential nested inside a JSON-encoded string value is not matched, because the escaped quotes break the key/value pattern. A query credential echoed in a readable header such as `referer` stays visible. `raw:true` removes every mask and exposes live cookies and tokens.

## Failure modes and recovery

| Exact message | Type | Fix |
| :--- | :--- | :--- |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. The tool never starts it. |
| `caido: token rejected — set a PAT or enable guest access` | `unknown` | Set `[caido] pat` in `webmcp.toml`, or enable guest access in Caido. |
| `caido: requestsByOffset failed: <server messages>` | `unknown` | The instance rejected the history query. Read the message. Check the instance version. |
| `caido: raw bodies failed: <server messages>` | `unknown` | The instance rejected the body query. Retry with a smaller `limit`. |
| `caido: request failed: <transport error>` | `unknown` | The call reached the host but failed. Retry. |
| `caido: raw message from request <id> is not base64: <error>` | `unknown` | Caido returned a malformed raw message. Read that row in the UI. |
| `caido: redacting JSON body: <error>` | `unknown` | The redactor could not re-encode the body. Retry with `raw:true`. |
| `caido_search: invalid arguments: method must be GET, POST, or empty` | `invalid_args` | Use `GET`, `POST` or `""`. |
| `caido_search: invalid arguments: status must be >= 0` | `invalid_args` | Use `0` or a real code. |
| `caido_search: invalid arguments: since must be >= 0` | `invalid_args` | Use `0` to disable the window. |
| `caido_search: invalid arguments: limit must be >= 0` | `invalid_args` | Use `1` to `100`, or `0` for the default. |
| `caido_search: invalid arguments: body_bytes must be >= 0` | `invalid_args` | Use `0` for headers only. |
| `caido_search: invalid arguments: part must be request, response, or both` | `invalid_args` | Use one of the three values. |
| `caido_search: unknown argument "<key>" — valid arguments are body_bytes, limit, method, part, raw, since, status, url` | `invalid_args` | Remove the key. Only the 8 inputs exist. |

A `caido_search: invalid arguments: ...` failure also carries `hint: fix the arguments per the tool InputSchema (required fields, enum values)`. An unknown argument and every `unknown` failure carry no hint.

An empty history is not an error. It returns `entries:[]` with `count:0`.

## Tested calls

| Call | Result |
| :--- | :--- |
| `{"url":"httpbin.org","limit":1,"body_bytes":4000,"since":0}` | `count:1`, `total:1747`, `scanned:13`, `dropped:12`, one POST with redacted `api_key` |
| `{"url":"anything?token","limit":2,"body_bytes":4000,"since":0,"part":"request"}` | `count:1`, `url_redacted:true`, `scanned:500`, `dropped:499` |
| `{"url":"/post","method":"POST","limit":6,"body_bytes":300,"since":3600,"part":"request"}` | `count:3`, `total:612`, `scanned:500`, `dropped:497`, form body `user=demo&password=<redacted>` |
| `{"url":"no-such-host","limit":200,"body_bytes":70000,"since":0}` | `entries:[]`, `limit_applied:100`, `body_bytes_applied:65536` |
| `{"method":"PUT"}` | `invalid_args` with the method message |
| `{"bogus":1}` | `invalid_args` with `caido_search: unknown argument "bogus" — valid arguments are body_bytes, limit, method, part, raw, since, status, url` |

## When not to use it

- To see the live traffic of a page you control. Use `page_fetch what=network`, which needs no proxy.
- To replay a request. Use `page_act replay` or the Caido UI.
- To block or edit a request. Use `page_act intercept_add`.
- To find traffic older than the newest 500 rows. Use the Caido UI.
- To read a body over 8 MiB. Use the Caido UI.

## Related tools

- `page_fetch` `what=network` for live capture without a proxy.
- `page_act` `replay` and `replay_fuzz` for resending a captured browser entry.
- `browser_create` `proxy`, `ignore_certs` and `proxy_bypass` to record into Caido.

## Maintainer anchors

- Schema and validation: `code://pkg/mcp/tools/caido.go#L66-L174`
- Result and entry types: `code://pkg/caido/client.go#L99-L129`
- Search loop and scan window: `code://pkg/caido/client.go#L163-L280`
- Body fetch and cut: `code://pkg/caido/client.go#L294-L356`, `code://pkg/caido/client.go#L431-L443`
- Server filter: `code://pkg/caido/client.go#L540-L549`
- Transport and error mapping: `code://pkg/caido/client.go#L586-L641`
- Redaction: `code://pkg/caido/redact.go#L13-L74`, `code://pkg/caido/redact.go#L99-L182`
