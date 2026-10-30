# caido_search examples

Run live on 2026-10-30. Output is trimmed. Caido must run (`make caido`) and the page must reach the
proxy. For every field and error, read [caido_search.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_search.md).

## 1. Find an API call by host and path

```json
{"host":"docs.python.org","path_contains":"/library/","limit":3,"body_bytes":0,"since":0}
```

```json
{"ok":true,"data":{"entries":[{"id":"2066","method":"GET","url":"https://docs.python.org/3/library/asyncio-task.html","host":"docs.python.org","path":"/3/library/asyncio-task.html","status":200}],"count":1,"total":1,"scanned":1,"dropped":0,"since_seconds":0,"dropped_since":0,"httpql":"req.host.eq:\"docs.python.org\" AND req.path.cont:\"/library/\"","httpql_source":"typed","scope_id":""}}
```

`httpql_source:"typed"` says the tool compiled the keys. `id` 2066 feeds `caido_finding`.

## 2. Read older history that the default window hid

```json
{"host":"www.rew.ca","limit":2,"body_bytes":0}
```

```json
{"ok":true,"data":{"entries":[],"count":0,"total":25,"scanned":1,"dropped":1,"since_seconds":900,"dropped_since":25,"note":"all 25 matching rows are older than since=900s — pass since:0 to read them","httpql":"req.host.eq:\"www.rew.ca\"","httpql_source":"typed","scope_id":""}}
```

Add `"since":0` and the 25 rows arrive.

## 3. Read one request body

```json
{"url":"/api/event","limit":1,"part":"request","raw":true,"since":0,"body_bytes":500}
```

```json
{"ok":true,"data":{"entries":[{"id":"2087","method":"POST","url":"https://analytics.python.org/api/event","status":202,"request_body":"{\"n\":\"engagement\",\"sd\":2,\"d\":\"docs.python.org\"}"}],"count":1,"total":2087,"scanned":1,"dropped":0}}
```
`url` filters here, so `total` 2087 is the whole history. `raw:true` prints live values.
