# caido_finding example

Run live on 2026-10-30. Read
[caido_finding.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_finding.md) for every field and error.

## Record what you proved

```json
{"action":"create","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","notes":"Verifying the caido_finding create example."}
```

```json
{"ok":true,"data":{"id":"4","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","created_at":"2026-10-30T03:39:43Z","description":"Verifying the caido_finding create example."}}
```

`request_id` 2087 comes from a `caido_search` row. A URL there fails with
`caido: createFinding failed: UnknownIdUserError (UNKNOWN_ID)`.

## Read the project findings

```json
{"action":"list"}
```

```json
{"ok":true,"data":{"findings":[{"id":"4","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","created_at":"2026-10-30T03:39:43Z","description":"Verifying the caido_finding create example."}],"count":1}}
```
An empty list returns `{"findings":[],"count":0}` and that is not an error.

## Read one finding

```json
{"action":"get","id":"4"}
```

```json
{"ok":true,"data":{"id":"4","title":"Doc check: analytics beacon on docs.python.org","request_id":"2087","created_at":"2026-10-30T03:39:43Z","description":"Verifying the caido_finding create example."}}
```
