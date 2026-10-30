> Tool: `caido_status` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# caido_status

Reports which Caido project, which scopes, which capture mode and how many history rows the other
Caido tools read. Call it before the first search, and again whenever a search returns nothing.

An empty search usually means the wrong project, a scope that does not match, or a capture filter.
It rarely means an empty history.

## Inputs

It takes no arguments. Any key fails as `invalid_args` with
`caido_status: unknown argument "<key>" — caido_status takes no arguments`.

## Returns

The full reply is `{"ok":true,"data":<status>}`.

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `auth` | string | The credential that worked: `access_token`, `pat`, `pat (rejected), guest`, `guest` or `none`. |
| `url` | string | The GraphQL endpoint the client dials. |
| `project.id` | string | The project UUID. |
| `project.name` | string | The project name. |
| `project.path` | string | The project directory on disk, under the config dir. |
| `project.status` | string | The instance state, for example `READY`. |
| `project.temporary` | boolean | `true` when the instance holds the project in memory only. |
| `project.read_only` | boolean | `true` when the instance refuses writes to the project. |
| `project.selected` | string | The project the instance serves right now. |
| `project.configured` | string | The `[caido] project` value from `webmcp.toml`. A mismatch against `selected` is worth a look. |
| `scopes` | array | One row per scope: `id`, `name`, `allowlist`, `denylist`, `matches` and `focus`. |
| `scopes[].matches` | string | `all traffic` for an empty allowlist, `allowlist` otherwise. |
| `scopes[].focus` | boolean | `true` for the configured focus scope. |
| `capture` | object | The passthrough options: `capture_list`, `passthrough_list` and `out_of_scope`. |
| `capture.unfiltered` | boolean | `true` when Caido filters nothing, in place of the three lists. |
| `history` | integer | The request count in the project. |

An empty `scopes` array is normal for a fresh project. With no scope, the default `scope_id:""` adds no
scope filter and a search sees every host.

## Two live examples

Read the project, the scopes and the capture mode:

```json
{}
```

Real trimmed output:

```json
{"ok":true,"data":{"auth":"guest","url":"http://127.0.0.1:8080/graphql","project":{"id":"063ae3b3-a703-43be-a9db-af9b39f061e2","name":"webmcp","path":"<config>/caido/projects/063ae3b3-…","status":"READY","temporary":false,"read_only":false,"selected":"webmcp","configured":"webmcp"},"scopes":[],"capture":{"unfiltered":true},"history":2087}}
```

Read it like this:

- `selected` and `configured` both read `webmcp`, so the launch selection matches the instance.
- `scopes` is empty, so no scope narrows a search.
- `capture.unfiltered:true` means Caido records every host that reaches the proxy.
- `history` 2087 is the row count a wide search can match.

The scope shape on a project that holds one:

```json
{}
```

Real trimmed shape, from `scopes[0]` of the same call:

```json
{"id":"1","name":"target","allowlist":[],"denylist":[],"matches":"all traffic","focus":true}
```

Use `id` as `scope_id` to stay inside that scope. `matches:"all traffic"` says the empty allowlist
records every host, so this scope narrows nothing yet.

## Failure modes

| Exact message | Type | Fix |
| :--- | :--- | :--- |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |
| `caido: no credential — set pat, or set access_token, or set allow_guest = true` | `unknown` | Set one credential path in `webmcp.toml`. |
| `caido: no session — the instance rejected the credential and guest access is off` | `unknown` | Set `access_token`. Or set `allow_guest = true`. |
| `caido: guest access rejected — the instance does not allow guests; set access_token or pat` | `unknown` | Set `access_token` or `pat`. |
| `caido: no project is selected — select one in the Caido UI` | `unknown` | Select a project in the UI. Then call again. |
| `caido: status query failed: <server message>` | `unknown` | Read the message. The status query names a field the instance lacks. |
| `caido: scopes failed: <server message>` | `unknown` | Read the message. Check the instance version. |
| `caido: capture filter read failed: <server message>` | `unknown` | Read the message. Check the instance version. |
| `caido_status: unknown argument "<key>" — caido_status takes no arguments` | `invalid_args` | Send `{}`. |

## Related tools

- `caido_search` for rows in the project and scope this call reports.
- `caido_sitemap` for the endpoint list of one host.
- `caido_finding` to read or record a finding.

## Maintainer anchors

- Tool and description: `code://pkg/mcp/tools/caido.go#L296-L352`
- Status types: `code://pkg/caido/client.go#L352-L389`
- Status read: `code://pkg/caido/client.go#L855-L949`
