> Tool: `caido_replay` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# caido_replay

Resends one captured request through Caido's replay lane. The new exchange lands in project history.
It sends exactly one request per call. There is no matrix and no retry.

Use `caido_replay` when the request lives in Caido history and the page is gone. Use `page_act replay`
when a live page holds the captured entry.

The full reply is `{"ok":true,"data":<payload>}`.

## Inputs

| Input | Type | Required | Default | Limit |
| :--- | :--- | :--- | :--- | :--- |
| `request_id` | string | yes | — | A captured request id from a `caido_search` row, never a URL. An empty value fails validation. |
| `method` | string | no | captured | An RFC 7230 token with no length limit. The tool upper-cases it. A change to `GET`, `HEAD` or `DELETE` drops the body and the framing headers. |
| `headers` | object of string to string | no | `{}` | Each name must be an HTTP token. Each value must hold no CR or LF. A repeated name keeps its first captured value. |
| `query` | object of string to string | no | `{}` | A non-empty value replaces one URL parameter. An empty value removes it. Untouched parameters keep their order and encoding. |
| `body` | string | no | captured | Replaces the captured body. An empty string sends an empty body and `content-length: 0`. The tool sets the length to the new size. |

A `method` change to `GET`, `HEAD` or `DELETE` clears the body even when `body` is set.

## Returns

One replay writes two history rows. The first row is the replay session with `status: 0`. The second
row is the exchange. The tool returns only the exchange.

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `request_id` | string | The new history row. `caido_search` with `since:0` finds it. |
| `entry_id` | string | The replay entry id. |
| `session_id` | string | The replay session id. |
| `method` | string | The method that ran, after the override. |
| `url` | string | The target URL, after the `query` edits. |
| `status` | integer | The recorded response code. |
| `ms` | integer | The exchange duration in milliseconds. |
| `response_bytes` | integer | The decoded response body size. `caido_search` reports `bytes` for transfer size and `response_bytes` for the body. |

Two further fields appear only on a problem. `error` names an exchange that did not complete.
`note` explains a row that never appeared. A missing `request_id` means the read-back failed.

## Failures

| Exact message | Type | Fix |
| :--- | :--- | :--- |
| `caido: no request with id "…" — take a request id from caido_search` | `not_found` | Read `caido_search` again and take a live id. |
| `caido_replay: invalid arguments: request_id is required — take a request id from caido_search` | `invalid_args` | Send `request_id`. |
| `caido_replay: invalid arguments: <reason>` | `invalid_args` | Read the reason and resend. |
| `caido_replay: unknown argument "<key>" — valid arguments are body, headers, method, query, request_id` | `invalid_args` | Remove the key. |
| `caido: replay: invalid arguments: method "<value>" must be an HTTP token` | `invalid_args` | Send a token such as `PUT`. |
| `caido: replay: invalid arguments: header "<name>" must be a token and its value must not contain a newline` | `invalid_args` | Use a token name and a single-line value. |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |

The `not_found` failure also carries `hint: read caido_search for a live request id, then replay that id`.

## Related tools

- `caido_search` for the request id and the row that backs it.
- `caido_status` for the project that records the replay.
- `caido_finding` to link a finding to the new row.

## Maintainer anchors

- Tool, description and schema: `code://pkg/caido/tools/replay.go#L16-L82`
- Execute: `code://pkg/caido/tools/replay.go#L94-L131`
- Replay and read-back: `code://pkg/caido/client.go#L965-L1075`
- Rewrite and skipped body: `code://pkg/caido/client.go#L1159-L1230`
