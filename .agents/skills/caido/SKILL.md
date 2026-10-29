---
name: caido
description: Read the recorded HTTP history of a local Caido instance with the caido_search MCP tool. Use when a task needs the requests and responses that pages sent through the Caido proxy on 127.0.0.1:8081, including headers, bodies, status codes and timings.
---

# caido skill

`caido_search` reads Caido proxy history over GraphQL. It is one read-only search surface. It is not Caido's own MCP server.

Read the tool description and InputSchema first. Read [caido_search.md](references/tools/caido_search.md) for every field, clamp, redaction rule and error string. Read [caido_search_example.md](references/examples/caido_search_example.md) for copyable calls.

## What it is for

- Recover the exact request a page made, so you can write a scraper.
- Read a request or response body that `page_fetch what=network` does not show.
- Compare what the browser sent with what the server returned.
- Find the POST that a form step produced.

## Prerequisite

Caido must run on this host. A human starts it with `make caido`. The tool never starts it.

- Proxy: `127.0.0.1:8081`.
- UI and GraphQL API: `127.0.0.1:8080`.
- A browser records into it only through the proxy. Create one with `browser_create {"proxy":"127.0.0.1:8081","ignore_certs":true}`.
- History is shared. Every client of the instance writes to the same list.
- In guest mode the history dies with the instance.

## Authentication

The tool tries each credential in order and falls back to a guest session. Guest is the normal path on a local instance.

| Credential | Where | Behavior |
| :--- | :--- | :--- |
| `access_token` | `[caido] access_token`, or `--caido-token` on `browser serve mcp` | Sent as the bearer with no exchange. A direct instance token copied from the Caido UI console: `JSON.parse(localStorage.CAIDO_AUTHENTICATION).accessToken`. It expires after about 7 days. |
| `pat` | `[caido] pat`, `CAIDO_PAT`, or `--caido-pat` (flag > env > file) | Tried as the bearer after `access_token`. A PAT the instance rejects falls back to guest when `allow_guest` is true. |
| guest | no credential worked, `allow_guest = true` (the default) | `loginAsGuest`. The history lasts only while the instance runs. |

`allow_guest = false` with no working credential is an error. `browser config show` prints the resolved path as `auth: access_token`, `pat`, `pat (rejected), guest`, `guest` or `none`. Never print or log a token.

## The one tool

`caido_search` takes 8 arguments and returns the newest matching transactions.

| Argument | Type | Default | Range | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `url` | string | `""` | any | Case-insensitive substring of the URL. Client-side filter. |
| `method` | string | `""` | `GET`, `POST`, `""` | Server-side filter. No other method is selectable. |
| `status` | integer | `0` | `0` or a code | Server-side exact response code. `0` means all. |
| `since` | integer | `900` | `0` or more | Lookback in seconds. `0` disables the time filter. |
| `limit` | integer | `20` | `0` to `100` | Rows returned. `0` becomes `20`. Above `100` clamps. |
| `body_bytes` | integer | `2000` | `0` to `65536` | Cap per body. `0` returns headers only. |
| `part` | string | `both` | `request`, `response`, `both` | Which side carries a body. |
| `raw` | boolean | `false` | `true`, `false` | Disable redaction. Exposes live credentials. |

## The envelope

Success:

```json
{"ok":true,"data":{"entries":[{"id":"1792","method":"POST"}],"count":1,"total":1792,"scanned":1,"dropped":0}}
```

`data` holds `entries`, `count`, `total`, `scanned` and `dropped`. `limit_applied` and `body_bytes_applied` appear only when a request was clamped.

Failure:

```json
{"ok":false,"error":{"type":"invalid_args","message":"caido_search: invalid arguments: method must be GET, POST, or empty","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

## Five facts before your first call

1. `url` and `since` do not filter on the server. The tool scans the newest 500 rows (5 pages of 100) and filters them here. Older history is unreachable at any `limit`.
2. `total` is the server count for `method` and `status` only. It ignores `url` and `since`. A `total` of 612 with `count` 3 is normal.
3. `scanned` is the rows examined, up to 500. The scan stops early once `limit` rows are kept. `dropped` is the rows that `url` or `since` rejected.
4. Redaction is on unless `raw` is `true`. It masks credential-shaped header values, body keys and query parameters. A URL-bearing header such as `referer` loses its credential parameters too. A masked URL sets `url_redacted`. `raw:true` prints live cookies and tokens.
5. `body_bytes:0` returns headers with empty bodies and sets no cut flags. `request_cut` and `response_cut` appear only when a returned body was shortened, or dropped with its headers because the message exceeds 8 MiB.

## Four failure modes

1. **Caido is not running.** Exact text: `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido`. Type: `unknown`. Fix: ask the human to run `make caido`. The tool never starts it.
2. **No session.** Exact text: `caido: no session — the instance rejected the credential and guest access is off`. Type: `unknown`. Fix: set a valid `access_token` from the Caido UI console, or set `allow_guest = true`. A rejected credential falls back to guest while `allow_guest` is true.
3. **Guest is refused.** Exact text: `caido: guest access rejected — the instance does not allow guests; set access_token or pat`. Type: `unknown`. Fix: set `access_token` or `pat`.
4. **An argument is outside its schema.** Exact text: `caido_search: invalid arguments: part must be request, response, or both`. Type: `invalid_args`. Fix: read the table above and resend. The same rule covers a bad `method`, `status`, `since`, `limit` or `body_bytes`.

## Worked example

Find the form POST that the browser just made:

```json
{"url":"/post","method":"POST","limit":3,"body_bytes":300,"since":3600,"part":"request"}
```

Real trimmed result:

```json
{"ok":true,"data":{"entries":[{"id":"1694","method":"POST","url":"https://httpbin.org/post","host":"httpbin.org","path":"/post","status":200,"bytes":674,"response_bytes":1279,"ms":193,"created_at":"2026-10-29T09:29:35Z","request_headers":{"content-type":["application/x-www-form-urlencoded"],"content-length":["31"],"origin":["https://httpbin.org"]},"request_body":"user=demo&password=<redacted>"}],"count":3,"total":612,"scanned":500,"dropped":497}}
```

`total` 612 is every POST with status 200 on the server. `count` 3 is the rows this call returned. `dropped` 497 is the rows that failed the `url` substring.

## What this cannot do

- No replay. Use `page_act replay` on a captured browser entry, or the Caido UI.
- No intercept, no request blocking, and no editing or deleting history.
- No findings, sitemaps, scopes or Caido filter syntax. Only `method` and `status` filter.
- No method other than `GET` and `POST`.
- No history older than the newest 500 rows.
- No WebSocket, DNS or non-HTTP records.
