# caido_search examples

Three recipes and four calls that were run live on 2026-10-29 against Caido on `127.0.0.1:8080`. Output is trimmed to the fields that matter. The values are real.

Read [caido_search.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_search.md) for the full field list.

## Before you start

Caido must run. A human starts it with `make caido`.

The page must reach the proxy. Create a browser with:

```json
{"proxy":"127.0.0.1:8081","ignore_certs":true,"proxy_bypass":"127.0.0.1,localhost","headless":true}
```

Then load the page and act. Only proxied traffic is in the history.

## (a) Find the form POST your own browser just made

The page posted a URL-encoded form. This call finds it.

```json
{"url":"/post","method":"POST","limit":3,"body_bytes":300,"since":3600,"part":"request"}
```

Real trimmed output:

```json
{"ok":true,"data":{"entries":[{"id":"1694","method":"POST","url":"https://httpbin.org/post","host":"httpbin.org","path":"/post","status":200,"bytes":674,"response_bytes":1279,"ms":193,"created_at":"2026-10-29T09:29:35Z","request_headers":{"content-type":["application/x-www-form-urlencoded"],"content-length":["31"],"origin":["https://httpbin.org"]},"request_body":"user=demo&password=<redacted>"}],"count":3,"total":612,"scanned":500,"dropped":497}}
```

Read it like this:

- `id` 1694 is the newest matching row. `count` 3 means two older rows also matched.
- `total` 612 is every POST with status 200 on the server. It ignores `url` and `since`.
- `request_body` shows the form fields. The password value is masked.
- `bytes` 674 is the request size. `response_bytes` 1279 includes the response headers.
- `request_cut` is absent because 300 bytes held the whole body.

To see the plain form body, add `"raw":true`. Do that only when the value is the question.

## (b) Read an API call's request body to write a scraper

The page sent a JSON POST with an API key header. This call recovers both.

```json
{"url":"/anything/api/orders","limit":1,"body_bytes":400,"since":0,"part":"request"}
```

Real trimmed output:

```json
{"ok":true,"data":{"entries":[{"id":"1792","method":"POST","url":"https://httpbin.org/anything/api/orders","host":"httpbin.org","path":"/anything/api/orders","status":200,"bytes":665,"response_bytes":1295,"ms":196,"created_at":"2026-10-29T09:31:37Z","request_headers":{"content-type":["application/json"],"content-length":["21"],"origin":["http://127.0.0.1:4099"],"x-api-key":["<redacted>"]},"request_body":"{\"sku\":\"A-1\",\"qty\":2}"}],"count":1,"total":1792,"scanned":1,"dropped":0}}
```

Read it like this:

- `request_body` is the exact JSON to send again: `{"sku":"A-1","qty":2}`.
- `content-type` is the required header. `content-length` 21 matches the body.
- `request_headers.x-api-key` is `<redacted>`. You know the header name and its position, not its value.
- `scanned` 1 means the newest row matched at once. `dropped` 0 means no row failed the filter.
- `mime` is absent because `part:"request"` did not fetch the response.

To reproduce the call, send the body with the same method, path and `content-type`. Pass the key from your own store.

## (c) Check whether a response leaks a token

The page called `https://httpbin.org/anything?token=caido-skill-demo`. The response echoes its input. This call reads the response side.

```json
{"url":"anything?token","limit":1,"body_bytes":1200,"since":0,"part":"response"}
```

Real trimmed output. The `headers` block of the echoed body is removed.

```json
{"ok":true,"data":{"entries":[{"id":"1632","method":"GET","url":"https://httpbin.org/anything?token=<redacted>","host":"httpbin.org","path":"/anything","status":200,"mime":"application/json","bytes":662,"response_bytes":1245,"ms":216,"created_at":"2026-10-29T09:29:27Z","response_headers":{"content-type":["application/json"],"content-length":["958"],"access-control-allow-origin":["*"]},"response_body":"{\"args\":{\"token\":\"<redacted>\"},\"data\":\"\",\"files\":{},\"form\":{},\"json\":null,\"method\":\"GET\",\"origin\":\"172.59.225.150\",\"url\":\"https://httpbin.org/anything?token=<redacted>\"}","url_redacted":true}],"count":1,"total":1749,"scanned":118,"dropped":117}}
```

The redacted body says the response reflects the token, but it hides the value. That is not enough to prove a leak. Repeat with `raw:true`.

```json
{"url":"anything?token","limit":1,"body_bytes":400,"since":0,"part":"response","raw":true}
```

Real trimmed output:

```json
{"ok":true,"data":{"entries":[{"id":"1632","method":"GET","url":"https://httpbin.org/anything?token=caido-skill-demo","status":200,"response_body":"{\n  \"args\": {\n    \"token\": \"caido-skill-demo\"\n  }, ...","response_cut":true}],"count":1,"total":1789,"scanned":158,"dropped":157}}
```

Read it like this:

- `url` now shows the token. `url_redacted` is absent.
- `response_body` now shows `"token": "caido-skill-demo"`. The server echoes the credential.
- `response_cut:true` means 400 bytes did not hold the full body. Raise `body_bytes` to read more.
- This is proof of reflection, not proof of harm. Pair it with `page_fetch what=storage` to see if the value is stored.

## Cleanup

Close the browser you created for the proxy test with `browser_close`. Use its exact id.

```json
{"browser_id":"browser-2"}
```

Call `browser_list` first to confirm the id. Do not close `browser-1`.
