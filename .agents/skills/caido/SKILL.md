---
name: caido
description: Read and replay a local Caido proxy's HTTP history with caido_status, caido_sitemap, caido_search, caido_finding and caido_replay. Use when a task needs the requests and responses that pages sent through the Caido proxy on 127.0.0.1:8081. The record covers headers, bodies, status codes, timings, endpoints and findings.
---

# caido skill

Read the tool description and InputSchema first. This file carries the flow, the rules and every
argument. Read `references/tools/<tool>.md` when a call fails. Read `references/examples/<tool>_example.md`
for a copy-paste call.

## What Caido is here

A Caido instance on this host sits between the browser and the network. It records every request that
passes through its proxy at `127.0.0.1:8081`. One history covers every tab, every navigation and every
browser that points at the proxy. The record stays on the Caido server, so a body is still readable
after the page closes. A human starts the instance with `make caido`. These tools never start it.

- Proxy: `127.0.0.1:8081`. UI and GraphQL API: `127.0.0.1:8080`.
- A page records only through the proxy. Create a browser with `browser_create {"proxy":"127.0.0.1:8081","ignore_certs":true}`.
- The history is shared. Every client of the instance writes to one list.
- A guest session loses its history when the instance stops.
- `caido_status`, `caido_sitemap`, `caido_search` and `caido_finding` create never change or block traffic. `caido_replay` sends one new request and records its exchange. `caido_finding` create writes a finding only.

## The five tools

| Tool | What it answers | Key argument |
| :--- | :--- | :--- |
| `caido_status` | Which project, scopes and capture mode the tools read. | None. It takes no arguments. |
| `caido_sitemap` | Which endpoints one host recorded, one row per method and path shape. | `host` |
| `caido_search` | Which requests match one filter, newest first. | `httpql` |
| `caido_finding` | Which bugs you proved, linked to one captured request id. | `action` |
| `caido_replay` | The exchange one resent request recorded, as a new history row. | `request_id` |

## The read flow

1. Call `caido_status` first. It names the project, the scopes and the capture mode.
2. Call `caido_sitemap` for the host. It lists endpoints, not requests.
3. Call `caido_search` with `httpql` when you know what you want.
4. Call `caido_finding` to record what you proved.
5. Call `caido_replay` when the request lives only in history.

## Every argument

`caido_status` takes no arguments. All other inputs are optional unless the table says required.

| Tool | Argument | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `caido_search` | `httpql` | string | no | `""` | Raw HTTPQL sent to the instance, for example `req.host.eq:"www.gnu.org"`. A non-empty value wins and the typed keys below are ignored. |
| `caido_search` | `host` | string | no | `""` | Typed filter, compiled to `req.host.eq:"H"`. Empty means every host. |
| `caido_search` | `method` | string | no | `""` | `GET`, `POST` or `""` for both. No other method is available. |
| `caido_search` | `status` | integer | no | `0` | Exact response code, compiled to `resp.code.eq:N`. `0` means every code. |
| `caido_search` | `status_gte` | integer | no | `0` | Minimum response code, compiled to `resp.code.gte:N`. `0` disables it. |
| `caido_search` | `path_contains` | string | no | `""` | Path substring, compiled to `req.path.cont:"P"`. Empty disables it. |
| `caido_search` | `scope_id` | string | no | `""` | `""` uses the configured focus scope. `"*"` reads every scope. Any other value is a scope id from `caido_status`. |
| `caido_search` | `since` | integer | no | `900` | Lookback window in seconds over the returned rows. `0` disables the window. |
| `caido_search` | `url` | string | no | `""` | Case-insensitive substring of the composed URL, applied here and not on the server. |
| `caido_search` | `limit` | integer | no | `20` | Rows returned. Clamps at `100`. `0` becomes `20`. |
| `caido_search` | `body_bytes` | integer | no | `2000` | Cap per body. Clamps at `65536`. `0` returns headers with empty bodies. |
| `caido_search` | `part` | string | no | `both` | `request`, `response` or `both`. |
| `caido_search` | `raw` | boolean | no | `false` | Turn redaction off. The body cut still applies. |
| `caido_sitemap` | `host` | string | yes | — | Bare host, such as `www.gnu.org`. No scheme and no path. |
| `caido_sitemap` | `path` | string | no | `""` | Keep only endpoints whose path starts with this prefix. |
| `caido_sitemap` | `max_depth` | integer | no | `5` | Tree depth to walk, clamped `1`..`5`. A shallow read hides deep endpoints. |
| `caido_sitemap` | `scope_id` | string | no | `""` | Same three values as `caido_search`. |
| `caido_finding` | `action` | string | yes | — | `list`, `get` or `create`. |
| `caido_finding` | `id` | string | no | `""` | Finding id. Only `get` reads it. |
| `caido_finding` | `title` | string | no | `""` | Finding title. `create` needs it. |
| `caido_finding` | `request_id` | string | no | `""` | Captured request id from `caido_search`, never a URL. `create` needs it. |
| `caido_finding` | `notes` | string | no | `""` | Finding description, cut at 4096 bytes. |
| `caido_replay` | `request_id` | string | yes | — | Captured request id from `caido_search`, never a URL. |
| `caido_replay` | `method` | string | no | captured | An HTTP token, upper-cased. A change to `GET`, `HEAD` or `DELETE` drops the body. |
| `caido_replay` | `headers` | object | no | `{}` | Each name is a token and each value has no newline. A named header replaces its captured value. |
| `caido_replay` | `query` | object | no | `{}` | A non-empty value replaces one URL parameter. An empty value removes it. |
| `caido_replay` | `body` | string | no | captured | Replaces the body. An empty string sends an empty body and `content-length: 0`. |

## What a search returns

| Field | Meaning |
| :--- | :--- |
| `entries` | The rows, newest first. Each row holds `id`, `method`, `url`, `host`, `path`, `status`, `mime`, `bytes`, `response_bytes`, `ms`, `created_at`, both header maps and both bodies. |
| `count` | The rows returned. Equals `len(entries)`. |
| `total` | The server count for the expression and the scope. It ignores `since`. |
| `scanned` | The rows examined. A no-filter scan stops at 500. |
| `dropped` | The rows that `url` or `since` rejected. |
| `since_seconds` | The window that ran. |
| `dropped_since` | Every row that matches and sits older than the window. |
| `httpql` | The expression that ran. Empty when no filter ran. |
| `httpql_source` | `httpql` for a raw expression, `typed` for the compiled keys, `""` for no filter. |
| `scope_id` | The scope that ran. `"*"` means every scope, and `all_scopes` is `true`. |
| `note` | The fix when a zero count sits beside a non-zero total. |
| `limit_applied`, `body_bytes_applied` | Present only when a value was clamped. |

## Rules

- Call `caido_status` before the first search. It is the cheapest way to spot the wrong project.
- Filter inside the focus scope. The default `scope_id:""` uses it. Pass `scope_id:"*"` only to look wide.
- Never delete a scope. Create your own scope. Or reuse one that matches the target host.
- A search with no `since` reads the newest 900 seconds. Send `since:0` to read older history.
- A search with no expression and no scope scans the newest 500 rows. A filtered search pages until `limit` is met. A narrow expression beats a big `limit`.
- Raise the window when `entries` is empty and `dropped_since` is above zero. Do not conclude that history is empty.
- Read `total` as the server count. It ignores `since`, so count 0 beside a total of 25 is normal.
- Keep redaction on for a report. `raw:true` prints live cookies and tokens into your context.
- Take `request_id` from a `caido_search` row before you create a finding or replay it.
- `caido_replay` sends one request. Read `caido_status` first when the project matters.

## `deny_hosts` and capture

`browser_create` takes `deny_hosts`, a list of host globs. A denied host never reaches the proxy, so it
never enters capture. The request still leaves the machine. Use `deny_hosts` for a telemetry beacon,
such as `["*.bsg.brave.com","usage-ping.brave.com"]`. Leave a target host off that list, or the capture holds
no row for it.

## Caido or `page_fetch`

| Question | Reach for |
| :--- | :--- |
| What did the whole browser send across tabs and navigations? | Caido. The record outlives the page and the bodies stay on the server. |
| What did one page send, with timing, in one call? | `page_fetch what=network`. No proxy is needed. |
| What is one captured body? | `caido_search` with `part` and `body_bytes`, or `page_fetch what=body`. |
| What can I replay? | `caido_replay` for a Caido history row. `page_act replay` for a live browser entry. |

Reach for Caido when the browser has closed, when several tabs matter, or when the body must survive a
navigation. Reach for `page_fetch` when one live page owns the question.

## Failures and the fix

| Exact text | Type | Fix |
| :--- | :--- | :--- |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |
| `caido: no session — the instance rejected the credential and guest access is off` | `unknown` | Set `access_token`. Or set `allow_guest = true`. |
| `caido: guest access rejected — the instance does not allow guests; set access_token or pat` | `unknown` | Set `access_token` or `pat` in `webmcp.toml`. |
| `caido: no project is selected — select one in the Caido UI` | `unknown` | Select a project. Then call `caido_status`. |
| `caido: invalid arguments: scope "999" — no scope with that id` | `invalid_args` | Call `caido_status`. Use a real id. |
| `caido_search: invalid arguments: <reason>` | `invalid_args` | Read the reason and resend. |
| `caido_search: unknown argument "<key>" — valid arguments are …` | `invalid_args` | Remove the key. The message lists all 13. |
| `caido_sitemap: invalid arguments: host must be a bare host such as www.gnu.org — no scheme and no path` | `invalid_args` | Strip the scheme and the path. |
| `caido_finding: invalid arguments: create needs title and request_id — take a request id from caido_search` | `invalid_args` | Take an id from a search row. |
| `caido_replay: invalid arguments: request_id is required — take a request id from caido_search` | `invalid_args` | Send `request_id`. |
| `caido: no request with id "…" — take a request id from caido_search` | `not_found` | Search again and take a live id. |
| `caido: createFinding failed: UnknownIdUserError (UNKNOWN_ID)` | `unknown` | The request id is not on the instance. Search again. |

A `caido_search: invalid arguments` failure also carries `hint: fix the arguments per the tool InputSchema (required fields, enum values)`.
