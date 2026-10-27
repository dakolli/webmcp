> Tool: `browser_create` · Server: web-mcp · Surface: MCP · Tested: 2026-10-27

# browser_create example

Use this file when tool calling for `browser_create` fails. The tool description is primary. The full contract is in [browser_create.md](../tools/browser_create.md). Every result rides the envelope: `{"ok":true,"data":<shown payload>}`. Errors ride `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Only the payload is shown below.

This sequence creates a persistent browser and an ephemeral browser under two owners, reuses one, tests a profile collision, creates a scoped browser with an identity header, creates a local-proxy browser, then closes with exact ids. Run it on an isolated server. Ids and paths differ on your host.

## 1. Create a persistent browser

```json
{"tool":"browser_create","arguments":{"owner":"client-a","profile":"audit","persist":true,"headless":true,"stealth":true}}
```

Expected payload:

```json
{"browser_id":"browser-2","owner":"client-a","persist":true,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}
```

Record the id. The profile resolves under `<profiles-dir>/audit`.

## 2. Repeat the same call

```json
{"tool":"browser_create","arguments":{"owner":"client-a","profile":"audit","persist":true,"headless":true,"stealth":true}}
```

Expected payload:

```json
{"browser_id":"browser-2","owner":"client-a","persist":true,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":true}
```

The same owner and the same options reuse the live browser.

## 3. Collide on the same profile as another owner

```json
{"tool":"browser_create","arguments":{"owner":"client-b","profile":"audit","persist":true,"headless":true,"stealth":true}}
```

Expected error. The envelope carries the message, and the tool prefixes it with `browser_create: `.

```
profile "…/audit" is already in use by browser-2 (owned by client-a) — close it with browser_close {browser_id: "browser-2"} or pick a different profile name
```

Recovery: pick a different `profile`, or create an ephemeral browser with no `profile`.

## 4. Create an ephemeral browser for another owner

```json
{"tool":"browser_create","arguments":{"owner":"client-b","headless":true,"stealth":true,"persist":false}}
```

Expected payload:

```json
{"browser_id":"browser-3","owner":"client-b","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}
```

The profile sits under `<profiles-dir>/.ephemeral/browser-3-<random>` and is deleted on close.

## 5. Create a scoped browser with an identity header

```json
{"tool":"browser_create","arguments":{"owner":"doc-web128","headless":true,"stealth":true,"scope":["wikipedia.org"],"headers":{"X-Bug-Bounty":"your-handle"}}}
```

Expected payload:

```json
{"browser_id":"browser-4","owner":"doc-web128","persist":false,"ttl":"30m0s","scope":"wikipedia.org","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}
```

Verified live on 2026-10-25 with `scope:["wikipedia.org"]` and a test header:

- A fetch to a page under the allowed domain succeeded. The bare host covers its subdomains.
- A fetch to any other host failed before the request left, with the scoped refusal in the reference.
- The network capture showed the identity header on the document request and on every subresource request.
- Repeating the same call with `scope` sent as the string `"[\"wikipedia.org\"]"` is accepted. The registry rewrites it into the array.

## 6. Create a local-proxy browser with ignore_certs

```json
{"tool":"browser_create","arguments":{"owner":"doc-proxy","headless":true,"proxy":"127.0.0.1:8080","ignore_certs":true}}
```

Expected payload:

```json
{"browser_id":"browser-5","owner":"doc-proxy","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}
```

This pairs the browser with a local intercept proxy on port 8080 and accepts its MITM CA. Verified live on 2026-10-25 for the failure paths, all rejected before Chromium starts:

`browsers: invalid proxy "http://user:pass@127.0.0.1:8080": credentials are not accepted here; put them in <config-dir>/proxy.json and chmod 600 it`, `browsers: invalid proxy "127.0.0.1:99999": port "99999" must be a number between 1 and 65535`, and `browsers: invalid proxy "127.0.0.1": missing port; pass host:port`.

A loopback proxy with no entry in `~/.config/browser-mcp/proxy.json` launches with no auth handler. A page through a dead proxy then fails with `net::ERR_PROXY_CONNECTION_FAILED`.

Credentials never go in the call. See the reference for the `proxy.json` contract.

## 6. Rate-limit the browser

```json
{"tool":"browser_create","arguments":{"owner":"doc-rate","headless":true,"stealth":true,"rps":0.5,"burst":1}}
```

Expected payload:

```json
{"browser_id":"browser-6","owner":"doc-rate","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":0.5,"burst":1,"hosts":[]},"reused":false}
```

Verified live on 2026-10-27. Three replays to one host were spaced about 4 seconds apart. After the buckets saw traffic, `browser_list` reported `"hosts":[{"host":"…","throttled":N}]` in `limits`. At `rps:0.1` the next request failed as `timeout` with `the next request slot is 6.473s away`. `rps` and `burst` join the create key, so a different rate is a different browser.

## 7. Close every browser you created

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-5"}}
```

Repeat for browser-6, browser-4, browser-3, and browser-2. Expected message per call: `browser browser-5 closed`.

Always close the browser you created with its exact id. Never send `{}` or an empty `browser_id` in a shared session.
