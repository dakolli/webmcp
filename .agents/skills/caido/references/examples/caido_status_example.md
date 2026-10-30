# caido_status example

Run live on 2026-10-30. The tool takes no arguments. Read
[caido_status.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_status.md) for every field.

## Call it before the first search

```json
{}
```

```json
{"ok":true,"data":{"auth":"guest","url":"http://127.0.0.1:8080/graphql","project":{"id":"063ae3b3-a703-43be-a9db-af9b39f061e2","name":"webmcp","path":"<config>/caido/projects/063ae3b3-…","status":"READY","temporary":false,"read_only":false,"selected":"webmcp","configured":"webmcp"},"scopes":[],"capture":{"unfiltered":true},"history":2087}}
```

## Read it like this

- `auth:"guest"` means no token ran and the session dies with the instance.
- `selected` and `configured` both read `webmcp`, so the launch selection matches the instance.
- `scopes:[]` means no scope narrows a search. The default `scope_id:""` adds no scope filter.
- `capture.unfiltered:true` means Caido records every host that reaches the proxy.
- `history` 2087 is the row count a wide search can match.

## A wrong key fails at once

```json
{"bogus":1}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"caido_status: unknown argument \"bogus\" — caido_status takes no arguments"}}
```
