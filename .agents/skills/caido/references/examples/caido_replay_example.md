# caido_replay example

Run live on 2026-10-30. Caido must run (`make caido`) and a browser must reach the proxy. Read
[caido_replay.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_replay.md) for every field and error.

## 1. Replay one captured request unchanged

Take an id from a `caido_search` row. This row came from `docs.python.org`.

```json
{"request_id":"2115"}
```

Observed output:

```json
{"ok":true,"data":{"request_id":"2117","entry_id":"30","session_id":"20","method":"GET","url":"https://docs.python.org/3/library/asyncio-task.html","status":200,"ms":1213,"response_bytes":177735}}
```

`request_id` 2117 is the new history row. `caido_search` with `since:0` finds it. A `GET` replay keeps
the captured body and framing headers.

## 2. Replay with an override

```json
{"request_id":"2115","method":"HEAD","query":{"nonexistent":"1"}}
```

Observed output:

```json
{"ok":true,"data":{"request_id":"2119","entry_id":"32","session_id":"22","method":"HEAD","url":"https://docs.python.org/3/library/asyncio-task.html?nonexistent=1","status":200,"ms":1197,"response_bytes":582}}
```

A new query name appends its parameter. An empty query value removes its parameter. A `HEAD` method
drops the body and the framing headers. Use this shape to test one new header, one new parameter or one
new body.

## 3. Prove the not_found branch

```json
{"request_id":"999999999"}
```

Observed output:

```json
{"ok":false,"error":{"type":"not_found","message":"caido: no request with id \"999999999\" — take a request id from caido_search","hint":"read caido_search for a live request id, then replay that id"}}
```

The exact message is `caido: no request with id "…" — take a request id from caido_search`.
