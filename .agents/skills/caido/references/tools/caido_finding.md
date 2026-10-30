> Tool: `caido_finding` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# caido_finding

Reads or creates Caido findings, so a proven bug outlives the chat. A finding links a title and notes to
one captured request id. The Caido UI shows it in the project.

The tool never deletes or updates a finding. A description body comes back redacted, and there is no raw
switch.

The full reply is `{"ok":true,"data":<payload>}`.

## Inputs

| Input | Type | Required | Default | Behavior |
| :--- | :--- | :--- | :--- | :--- |
| `action` | string | yes | — | `list` reads the newest findings. `get` reads one finding. `create` links a title and notes to a request id. |
| `id` | string | for `get` | `""` | Finding id, for example `"4"`. `list` and `create` ignore it. |
| `title` | string | for `create` | `""` | The finding title. |
| `request_id` | string | for `create` | `""` | A captured request id from a `caido_search` row, never a URL. |
| `notes` | string | no | `""` | The description, cut at 4096 bytes. A longer value sets `description_cut`. |

## Returns

`action:list` returns `{"findings":[…],"count":N}`. The list is capped at 100 rows, newest first.

`action:get` and `action:create` return one finding object:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The finding id. |
| `title` | string | The title. |
| `request_id` | string | The captured request id that backs the finding. |
| `created_at` | string | RFC3339 UTC. Absent when the instance reports no time. |
| `description` | string | The notes, with credential-shaped values masked. Absent when empty. |
| `description_cut` | boolean | Present only when `notes` was cut at 4096 bytes. |

## Two live examples

Read every finding in the project:

```json
{"action":"list"}
```

Real output:

```json
{"ok":true,"data":{"findings":[{"id":"4","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","created_at":"2026-10-30T03:39:43Z","description":"Checked the caido_finding create example."}],"count":1}}
```

Read how it works:

- `request_id` 2087 points at one captured row, so the Caido UI can open the exact request.
- `description` is the `notes` value. A credential-shaped value in it would arrive as `<redacted>`.
- An empty project returns `{"findings":[],"count":0}` and that is not an error.

Record what you proved:

This example creates a finding. Each run adds one more.

```json
{"action":"create","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","notes":"Checked the caido_finding create example."}
```

Real output:

```json
{"ok":true,"data":{"id":"4","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","created_at":"2026-10-30T03:39:43Z","description":"Checked the caido_finding create example."}}
```

Take `request_id` from a `caido_search` row first. A wrong id is not a validation error, and the tool
cannot repair it:
`caido: createFinding failed: UnknownIdUserError (UNKNOWN_ID)`.

## Failure modes

| Exact message | Type | Fix |
| :--- | :--- | :--- |
| `caido_finding: invalid arguments: action must be list, get, or create` | `invalid_args` | Use one of the three values. |
| `caido_finding: invalid arguments: get needs id` | `invalid_args` | Send `id` with `get`. |
| `caido_finding: invalid arguments: create needs title and request_id — take a request id from caido_search` | `invalid_args` | Send both fields, with an id from a search row. |
| `caido: no finding with id "999" — call caido_finding action:list` | `unknown` | Read the list. Then use a real id. |
| `caido: createFinding failed: UnknownIdUserError (UNKNOWN_ID)` | `unknown` | The request id is not on the instance. Search again for the row. |
| `caido: createFinding returned no finding — confirm request_id with caido_search` | `unknown` | The instance accepted the call but returned nothing. Search for the row again. |
| `caido: createFinding failed: <server message>` | `unknown` | Read the message. Check the project and its read-only flag. |
| `caido: findings query failed: <server message>` | `unknown` | Read the message. Check the instance version. |
| `caido: finding query failed: <server message>` | `unknown` | Read the message. Check the instance version. |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |
| `caido_finding: unknown argument "<key>" — valid arguments are action, id, notes, request_id, title` | `invalid_args` | Remove the key. |

An empty list usually means the wrong project. Call `caido_status` to confirm `project.selected` before
you create a second finding.

## Related tools

- `caido_status` for the project that holds the finding.
- `caido_search` for the request id and the row that proves the bug.
- `caido_sitemap` for the endpoint list of the host.

## Maintainer anchors

- Tool, description and validation: `code://pkg/mcp/tools/caido.go#L520-L656`
- Finding type: `code://pkg/caido/client.go#L340-L347`
- List and get: `code://pkg/caido/client.go#L690-L740`
- Create: `code://pkg/caido/client.go#L742-L790`
