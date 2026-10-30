> Tool: `caido_search` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# caido_search

Reads the Caido proxy history over GraphQL and returns HTTP transactions as JSON. It reads recorded
history only. It never changes, replays or blocks traffic.

The Caido proxy must listen on `127.0.0.1:8081`. Only traffic that passed through that proxy is in the
history. A human starts the instance with `make caido`, and the tool never starts it.

The payload rides inside the result envelope, so the full reply is `{"ok":true,"data":<result>}`.
Read `SKILL.md` for the envelope rules.

## How the filter reaches the server

The tool builds one HTTPQL expression and sends it to the instance.

1. A non-empty `httpql` is sent unchanged, and the typed keys are ignored.
2. Otherwise the typed keys `host`, `method`, `status`, `status_gte` and `path_contains` compile to one
   expression joined with ` AND `. The order is host, method, status, status_gte, path_contains.
3. `scope_id` adds the scope to the same query.
4. `url` and `since` never reach the server. The tool applies both to the returned rows.

The envelope reports the expression as `httpql` and its origin as `httpql_source` (`httpql`, `typed`,
or `""` when no filter ran).

## Inputs

All inputs are optional. An unknown key fails as `invalid_args` and lists the valid keys.

| Input | Type | Default | Limits | Behavior |
| :--- | :--- | :--- | :--- | :--- |
| `httpql` | string | `""` | any expression | Raw HTTPQL sent to the instance, for example `req.host.eq:"www.gnu.org"`. A non-empty value wins and the typed keys are ignored. An instance parse error returns as `unknown`. |
| `host` | string | `""` | any host | Typed filter, compiled to `req.host.eq:"H"`. Empty means every host. |
| `method` | string | `""` | `GET`, `POST`, `""` | Server-side filter. The value is trimmed and uppercased. Any other value fails. |
| `status` | integer | `0` | `>= 0` | Exact response code, compiled to `resp.code.eq:N`. `0` means every code. |
| `status_gte` | integer | `0` | `>= 0` | Minimum response code, compiled to `resp.code.gte:N`. `0` disables it. |
| `path_contains` | string | `""` | any substring | Path substring, compiled to `req.path.cont:"P"`. Empty disables it. |
| `scope_id` | string | `""` | `""`, `"*"`, or an id | `""` uses the configured focus scope. `"*"` reads every scope and sets `all_scopes:true`. Any other value is a scope id from `caido_status`. A rejected scope fails as `invalid_args`. |
| `since` | integer | `900` | `>= 0` | Lookback window in seconds over the returned rows. `0` disables it. |
| `url` | string | `""` | any substring | Case-insensitive substring of the composed URL. Applied over the rows the instance returns. |
| `limit` | integer | `20` | `0` to `100` | Rows returned. `0` becomes `20` with no marker. A value above `100` clamps and sets `limit_applied:100`. |
| `body_bytes` | integer | `2000` | `0` to `65536` | Cap per returned body in bytes. `0` returns headers with empty bodies. A value above `65536` clamps and sets `body_bytes_applied:65536`. |
| `part` | string | `both` | `request`, `response`, `both` | Which side carries a body. The value is trimmed and lowercased. Empty means `both`. |
| `raw` | boolean | `false` | `true`, `false` | Turn redaction off. The `body_bytes` cut still applies. |

A quoted number that parses is accepted, for example `"limit":"50"`. A quoted boolean that parses is
accepted too.

## Returns

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `entries` | array | The rows, newest first. Empty when no row matches. |
| `count` | integer | The rows returned. Equals `len(entries)`. |
| `total` | integer | The server count for the expression and the scope. It ignores `since`, so it can sit above `count`. |
| `scanned` | integer | The rows examined. |
| `dropped` | integer | The examined rows that `url` or `since` rejected. |
| `since_seconds` | integer | The window that ran. `0` when the window was disabled. |
| `dropped_since` | integer | Every row that matches the expression and sits older than the window. |
| `note` | string | Present only when the window emptied the result. It names the fix. |
| `httpql` | string | The expression that ran. Empty when no filter ran. |
| `httpql_source` | string | `httpql`, `typed`, or `""`. |
| `scope_id` | string | The scope that ran. `"*"` means every scope. |
| `scope_name` | string | The focus scope name. Absent when no scope ran. |
| `all_scopes` | boolean | Present and `true` only for `scope_id:"*"`. |
| `limit_applied` | integer | Present only when `limit` was clamped. Always `100`. |
| `body_bytes_applied` | integer | Present only when `body_bytes` was clamped. Always `65536`. |

Each entry holds `id`, `method`, `url`, `host`, `path`, `status`, `mime`, `bytes` and `response_bytes`.
Each entry also holds `ms`, `created_at`, `request_headers`, `response_headers`, `request_body`,
`response_body`, `request_cut`, `response_cut` and `url_redacted`.

- `id` is the Caido request id. Use it for `caido_finding` `request_id`.
- `bytes` is the request size. `response_bytes` is the raw captured response size, headers included.
- `request_headers` and `response_headers` use lowercased names with an array of values.
- Empty fields are omitted, not set to `""` or `null`. An absent `request_body` means no body on that side.
- `request_cut` and `response_cut` appear only when a body was shortened.
- They also appear when the captured message exceeds 8 MiB. The body and its headers are then dropped.
- `url_redacted` appears only when a credential-shaped query parameter was masked.

## Scan window and order

1. The tool reads 100 rows per page, newest first.
2. A search with no expression and no scope reads at most 5 pages, so `scanned` stops at 500.
3. A search with an expression or a scope pages until `limit` is met or the instance runs out of rows.
4. Each row then faces the `url` and `since` tests. A rejected row increments `dropped`.
5. A row older than the `since` cutoff stops the scan at once, because the order is newest first.
6. The scan stops as soon as `limit` rows are kept, so `scanned` can sit below 500.
7. Only the kept rows get a body fetch, in one aliased GraphQL query.

`total` comes from page 0, before any `url` or `since` test. A large `total` beside a small `count` is normal.

## Bodies, cuts and part

- `part:"request"` fetches the request side only. `mime` and `response_headers` are then absent.
- `part:"response"` fetches the response side only. `request_headers` and `request_body` are absent.
- `body_bytes:0` still fetches the raw message, then returns an empty body. It sets no cut flag.
- A body longer than `body_bytes` is cut on a rune boundary and sets `request_cut` or `response_cut`.
- A raw message whose declared length exceeds 8 MiB is not fetched. Its headers and body are dropped
  and the side is marked cut.

## Redaction

Redaction is on unless `raw:true`. It runs before the cut, so a JSON body is not trimmed into an
unparseable fragment first.

- A header value becomes `<redacted>` when the lowercased name holds one of the words below.
- The words are `token`, `secret`, `auth`, `key`, `cookie`, `session`, `csrf`, `signature`, `password`
  and `credential`.
- Readable headers stay readable. The first group is `content-type`, `content-length`, `accept`,
  `user-agent`, `host`, `referer`, `origin`, `date`, `server`, `etag` and `cache-control`.
- The second group is the `access-control-*` set, `strict-transport-security`,
  `content-security-policy`, `permissions-policy`, `x-frame-options`, `x-content-type-options`,
  `vary`, `expires` and `last-modified`.
- A JSON body loses the value of any credential-shaped key at any depth.
- A form, XML or plain body loses `key=value`, `key: value` and `<key>value</key>` when the key is
  credential-shaped.
- A credential-shaped query parameter becomes `<redacted>` and sets `url_redacted:true`.
- `raw:true` removes every mask and exposes live cookies and tokens. Redact before you quote a body.

## Two live examples

Find the JSON POST that a page sent:

```json
{"host":"docs.python.org","path_contains":"/library/","limit":3,"body_bytes":0,"since":0}
```

Real trimmed output:

```json
{"ok":true,"data":{"entries":[{"id":"2066","method":"GET","url":"https://docs.python.org/3/library/asyncio-task.html","host":"docs.python.org","path":"/3/library/asyncio-task.html","status":200}],"count":1,"total":1,"scanned":1,"dropped":0,"since_seconds":0,"dropped_since":0,"httpql":"req.host.eq:\"docs.python.org\" AND req.path.cont:\"/library/\"","httpql_source":"typed","scope_id":""}}
```

Read it like this:

- `httpql_source:"typed"` means the tool compiled `host` and `path_contains` for you.
- `total` 1 matches `count` 1, so the instance holds one row for this expression.
- `body_bytes:0` kept the reply small. Drop it to fetch bodies.

Read older history that the default window hid:

```json
{"host":"www.rew.ca","limit":2,"body_bytes":0}
```

Real trimmed output:

```json
{"ok":true,"data":{"entries":[],"count":0,"total":25,"scanned":1,"dropped":1,"since_seconds":900,"dropped_since":25,"note":"all 25 matching rows are older than since=900s — pass since:0 to read them","httpql":"req.host.eq:\"www.rew.ca\"","httpql_source":"typed","scope_id":""}}
```

`entries` is empty, `dropped_since` is 25, and `note` names the fix. Add `"since":0` to the same call and
the rows arrive.

## Failure modes

| Exact message | Type | Fix |
| :--- | :--- | :--- |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |
| `caido: no session — the instance rejected the credential and guest access is off` | `unknown` | Set `[caido] access_token`. Or set `allow_guest = true`. |
| `caido: guest access rejected — the instance does not allow guests; set access_token or pat` | `unknown` | Set `access_token` or `pat`. |
| `caido: no project is selected — select one in the Caido UI` | `unknown` | Select a project. Then call `caido_status`. |
| `caido: invalid arguments: scope "999" — no scope with that id` | `invalid_args` | Call `caido_status`. Use a real id. The hint reads `call caido_status to list scopes`. |
| `caido: requestsByOffset failed: Invalid HTTPQL query` | `unknown` | Fix the raw expression. Or use the typed keys. |
| `caido: requestsByOffset failed: <server message>` | `unknown` | Read the message. Check the instance version. |
| `caido: raw bodies failed: <server message>` | `unknown` | Retry with a smaller `limit`. |
| `caido: raw message from request <id> is not base64: <error>` | `unknown` | Read that row in the UI. |
| `caido: request failed: <transport error>` | `unknown` | Retry. |
| `caido_search: invalid arguments: method must be GET, POST, or empty` | `invalid_args` | Use `GET`, `POST` or `""`. |
| `caido_search: invalid arguments: status must be >= 0` | `invalid_args` | Use `0` or a real code. |
| `caido_search: invalid arguments: status_gte must be >= 0` | `invalid_args` | Use `0` to disable it. |
| `caido_search: invalid arguments: since must be >= 0` | `invalid_args` | Use `0` to disable the window. |
| `caido_search: invalid arguments: limit must be >= 0` | `invalid_args` | Use `1` to `100`, or `0` for the default. |
| `caido_search: invalid arguments: body_bytes must be >= 0` | `invalid_args` | Use `0` for headers only. |
| `caido_search: invalid arguments: part must be request, response, or both` | `invalid_args` | Use one of the three values. |
| `caido_search: unknown argument "<key>" — valid arguments are body_bytes, host, httpql, limit, method, part, path_contains, raw, scope_id, since, status, status_gte, url` | `invalid_args` | Remove the key. |

A `caido_search: invalid arguments` failure also carries
`hint: fix the arguments per the tool InputSchema (required fields, enum values)`. A scope rejection
carries `hint: call caido_status to list scopes`. An unknown argument carries no hint.

An empty history is not an error. It returns `entries:[]` with `count:0`.

## Related tools

- `caido_status` for the project, the scopes and the capture mode.
- `caido_sitemap` for the endpoint list of one host, without row noise.
- `caido_finding` to record a finding against a row `id`.
- `page_fetch` `what=network` for live capture without a proxy.
- `page_act` `replay` to resend a captured browser entry.
- `browser_create` `proxy`, `ignore_certs`, `proxy_bypass` and `deny_hosts` to shape what reaches Caido.

## Maintainer anchors

- Schema and validation: `code://pkg/mcp/tools/caido.go#L77-L155`
- Execute path: `code://pkg/mcp/tools/caido.go#L157-L265`
- Result and entry types: `code://pkg/caido/client.go#L258-L334`
- Search loop and scan window: `code://pkg/caido/client.go#L514-L688`
- Typed expression: `code://pkg/caido/client.go#L2044-L2062`
- Redaction: `code://pkg/caido/redact.go#L1`
