> Tool: `caido_sitemap` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# caido_sitemap

Lists the endpoints the Caido sitemap recorded for one host, one row per method and path shape. Use it
to pick a candidate endpoint before you probe it.

It reads the proxy sitemap over GraphQL. It never changes, replays or blocks traffic.

Each row collapses by method and shape. Numeric and UUID segments become `{id}`, so `/user/1` and
`/user/2` share one row. `requests` sums those hits and the latest status is kept.

The full reply is `{"ok":true,"data":<result>}`.

## Inputs

| Input | Type | Required | Default | Limits | Behavior |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `host` | string | yes | — | a bare host | Such as `www.gnu.org`. No scheme and no path. A scheme or a `/` fails as `invalid_args`. |
| `path` | string | no | `""` | any prefix | Keep only endpoints whose path starts with this prefix. Empty keeps every path. The walk still covers the whole tree, so `scanned` stays high. |
| `max_depth` | integer | no | `5` | `1`..`5` | Tree depth to walk. A value below `1` becomes `1`, a value above `5` becomes `5`. |
| `scope_id` | string | no | `""` | `""`, `"*"`, or an id | `""` uses the configured focus scope. `"*"` reads every scope. Any other value is a scope id from `caido_status`. |

## Returns

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `host` | string | The host you asked for. |
| `project` | string | The project the walk read. |
| `scope_id` | string | The scope that ran. `"*"` means every scope. |
| `rows` | array | The endpoint rows, sorted by `requests` from high to low, then `path`. Always present, `[]` on no match. |
| `count` | integer | The rows returned. Capped at 200. |
| `scanned` | integer | The tree nodes walked, capped at 2000. |
| `dropped` | integer | The rows collapsed by shape. |
| `truncated` | integer | The rows over the 200 row cap. |
| `note` | string | The reason for an empty row list, or the unvisited child count at the depth cap. Empty otherwise. |

Each row holds:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `method` | string | The HTTP method of the latest hit. |
| `path` | string | One concrete path, not the shape. |
| `kind` | string | `api`, `page` or `static`. |
| `status` | integer | The status of the latest hit. |
| `mime` | string | The MIME type guessed from the path extension. Absent when the guess is empty. |
| `requests` | integer | The hit count across the collapsed shape. |
| `params` | array | The sorted query parameter names, at most 10. |
| `shape` | string | The collapsed path, with `{id}` in place of a numeric or UUID segment. |

A depth cap is not a full answer. Read `note`. Raise `max_depth` when it names an unvisited count.

## Two live examples

List every endpoint shape on a host:

```json
{"host":"docs.python.org"}
```

Real trimmed output:

```json
{"ok":true,"data":{"host":"docs.python.org","project":"webmcp","scope_id":"","rows":[{"method":"GET","path":"/3/_static/basic.css","kind":"static","status":200,"mime":"text/css","requests":1,"params":[],"shape":"/{id}/_static/basic.css"},{"method":"GET","path":"/3/_static/classic.css","kind":"static","status":200,"mime":"text/css","requests":1,"params":["v"],"shape":"/{id}/_static/classic.css"}],"count":18,"scanned":28,"dropped":0,"truncated":0,"note":""}}
```

Read it like this:

- `count` 18 and `scanned` 28 mean the walk saw 28 nodes and kept 18 shapes.
- `note` is empty, so the depth cap hid no node.
- The `/3/` segment became `{id}` in `shape`. Pick `path` when you need the real one.

Read one subtree. Watch the collapse:

```json
{"host":"www.rew.ca"}
```

Real trimmed output:

```json
{"ok":true,"data":{"host":"www.rew.ca","project":"webmcp","scope_id":"","rows":[{"method":"POST","path":"/graphql/rew-portal","kind":"page","status":403,"requests":4,"params":[],"shape":"/graphql/rew-portal"}],"count":9,"scanned":28,"dropped":3,"truncated":0,"note":"depth 5 reached — 2 child nodes not walked"}}
```

`dropped` 3 is three rows folded into a shape that already had a row. `note` names 2 child nodes the
depth cap left alone, so a deeper read can still add rows.

## Failure modes

| Exact message | Type | Fix |
| :--- | :--- | :--- |
| `caido_sitemap: invalid arguments: host is required` | `invalid_args` | Send a bare host. |
| `caido_sitemap: invalid arguments: host must be a bare host such as www.gnu.org — no scheme and no path` | `invalid_args` | Strip `https://` and any `/`, `?` or `#` tail. |
| `caido: invalid arguments: scope "999" — no scope with that id` | `invalid_args` | Call `caido_status`. Use a real id. The hint reads `call caido_status to list scopes`. |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |
| `caido: no project is selected — select one in the Caido UI` | `unknown` | Select a project. Then call again. |
| `caido: sitemapRootEntries failed: <server message>` | `unknown` | Read the message. Check the instance version. |
| `caido: sitemapDescendantEntries failed: <server message>` | `unknown` | Read the message. Then retry with a lower `max_depth`. |
| `caido: sitemap: parent id is empty` | `unknown` | A tree node came back without an id. Retry. Then read the host in the UI. |
| `caido_sitemap: unknown argument "<key>" — valid arguments are host, max_depth, path, scope_id` | `invalid_args` | Remove the key. |

An empty row list is not an error. It returns `rows:[]` with a `note` that names the host. A host outside
the focus scope records no sitemap entry. An empty result then points at the project or the scope first.

A `path` prefix that matches nothing also returns `rows:[]` with the host note, even when the host has
rows. Check `path` before you read the note as an empty history.

## Related tools

- `caido_status` for the project and the scopes.
- `caido_search` for the full requests behind one row.
- `caido_finding` to record a finding against one request id.

## Maintainer anchors

- Tool, description and validation: `code://pkg/mcp/tools/caido.go#L358-L500`
- Sitemap result and row types: `code://pkg/caido/client.go#L396-L450`
- Sitemap walk and collapse: `code://pkg/caido/client.go#L1102-L1224`
