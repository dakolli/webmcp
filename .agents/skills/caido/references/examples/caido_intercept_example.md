# caido_intercept example

Run live on 2026-10-30. Caido must run (`make caido`). A local test server answered `127.0.0.1:8765`
with `baseline-block`. Read
[caido_intercept.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_intercept.md) for every field and error.

The filter targets a loopback host that this run owns. Do not point a rule at a host you do not own.
A rule rewrites responses for every browser that uses the proxy.

## 1. Baseline before the rule

Start a local test server on `127.0.0.1:8765` first. The response body does not matter.

```sh
curl -s -o /dev/null -w 'direct:%{http_code}\n' http://127.0.0.1:8765/
curl -s --noproxy '' -x http://127.0.0.1:8081 -o /dev/null -w 'proxy:%{http_code}\n' http://127.0.0.1:8765/
```

Observed output:

```
direct:200
proxy:200
```

The `--noproxy ''` flag makes curl use the proxy on `8081` for a loopback address. Without the flag,
curl can skip the proxy for `127.0.0.1`.

## 2. Arm one rule

```json
{"action":"add","url_filter":"req.host.eq:\"127.0.0.1\"","status":599,"body":"wm-intercept"}
```

Observed output:

```json
{"ok":true,"data":{"rules":[{"id":"21","name":"webmcp-intercept status 599","enabled":true,"sources":["INTERCEPT"]},{"id":"22","name":"webmcp-intercept body","enabled":true,"sources":["INTERCEPT"]}],"count":2,"ids":["21","22"],"count_before":0,"status":"RUNNING","queued":0,"filter":"req.host.eq:\"127.0.0.1\"","request":{"enabled":false},"response":{"enabled":true,"filter":"req.host.eq:\"127.0.0.1\""},"stream_ws":{"enabled":false}}}
```

`count` is 2. `status` and `body` are two sections, and Caido allows one section per rule. `ids` holds
both new ids. `count_before` is 0 because no rule was armed.

## 3. Send one request through the proxy

```sh
curl -s --noproxy '' -x http://127.0.0.1:8081 -w '\nstatus:%{http_code}\n' http://127.0.0.1:8765/
```

Observed output:

```
wm-intercept
status:599
```

The rule replaced the body and the status code. The server still answered `200` and `baseline-block`
on a direct request.

## 4. List while armed

```json
{"action":"list"}
```

Observed output:

```json
{"ok":true,"data":{"status":"RUNNING","rules":[{"id":"21","name":"webmcp-intercept status 599","enabled":true,"sources":["INTERCEPT"]},{"id":"22","name":"webmcp-intercept body","enabled":true,"sources":["INTERCEPT"]}],"count":2,"ids":["21","22"],"queued":0,"request":{"enabled":false},"response":{"enabled":true,"filter":"req.host.eq:\"127.0.0.1\""},"stream_ws":{"enabled":false}}}
```

`response.enabled` is `true` and `response.filter` names the expression. A matching request now pauses
in interception before the rewrite.

## 5. Clear the rule

```json
{"action":"clear","confirm":true}
```

Observed output:

```json
{"ok":true,"data":{"rules":[],"count":0,"ids":["21","22"],"existing":[{"id":"21","name":"webmcp-intercept status 599","enabled":true,"sources":["INTERCEPT"]},{"id":"22","name":"webmcp-intercept body","enabled":true,"sources":["INTERCEPT"]}],"count_before":2,"status":"DISABLED","queued":0,"filter":"","request":{"enabled":false},"response":{"enabled":false},"stream_ws":{"enabled":false}}}
```

`ids` holds the rules the call targeted. `existing` holds the rules that were armed before the write.
`filter` is empty, and `status` is back to `DISABLED`.

The no-id clear is the kill switch. It deletes every `webmcp-intercept` rule in the project, including
one another caller armed. Read the reference before you use it on a shared instance.

## 6. Confirm the rule is gone

```json
{"action":"list"}
```

Observed output:

```json
{"ok":true,"data":{"status":"DISABLED","rules":[],"count":0,"ids":[],"queued":0,"request":{"enabled":false},"response":{"enabled":false},"stream_ws":{"enabled":false}}}
```

The run left no rule armed. `list` reports `rules:0` and `status:DISABLED`.

## 7. Refusals

Each call below failed without changing the live state.

```json
{"action":"add","status":599}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"caido_intercept: invalid arguments: add needs url_filter — an HTTPQL expression such as req.host.eq:\"example.com\"","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

```json
{"action":"add","url_filter":"req.host.eq:\"127.0.0.1\""}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"caido_intercept: invalid arguments: add needs status, body, or headers","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

```json
{"action":"clear"}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"caido_intercept: invalid arguments: clear needs confirm:true or an id","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```
