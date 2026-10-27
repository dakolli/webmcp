> Tool: `browser_create` · Server: web-mcp · Surface: MCP · Tested: 2026-10-27

# browser_create

Create a live Chromium browser or reuse the one your owner already holds. Return the `browser_id` that every page tool needs.

## What it does

`browser_create` starts one Chromium process and registers it under a `browser_id` such as `browser-2`. It opens no page. Use `fetch_page` to open one.

The call is idempotent per owner. A second call with the same owner and the same launch options returns the live browser with `reused:true`. A different owner, or any changed option, gets a separate browser. The proxy, headers and scope are fixed at creation and never change afterwards.

Browsers are ephemeral by default on the MCP surface. With no `persist`, the profile lives under `~/.config/browser-mcp/profiles/.ephemeral/` and is deleted when the browser closes. That fresh profile is the isolation mechanism: a scoped or header-identity browser is not a second identity in the same profile, it is a whole new profile.

## Inputs

All inputs are optional. The schema declares no required field. Source: `code://pkg/mcp/tools/browser.go#L82-L150`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `bin` | string | no | empty | Browser executable path or command, for example `brave-browser`. Empty probes the default browsers in order. |
| `profile` | string | no | empty | Bare profile name or absolute directory. A bare name resolves under `~/.config/browser-mcp/profiles/<name>` when persistent, or under `profiles/.ephemeral/` when ephemeral. A relative path with a separator is rejected. An absolute path is always persistent. |
| `persist` | boolean | no | `false` | Keep the profile after close. `true` with a `profile` name keeps that named profile. On the MCP surface even `browser-1` is ephemeral unless you pass `persist:true`. |
| `headless` | boolean | no | server setting | Run without a window. The manager default is headful. |
| `stealth` | boolean | no | server setting | Apply go-rod/stealth to every page in the new browser. The manager default is on. |
| `position` | string | no | empty | Headful window preset: `left`, `right`, `max`, or `normal`. |
| `proxy` | string | no | empty | Local proxy for intercept tools, for example `127.0.0.1:8080`. Accepts `host:port` or `scheme://host:port`. Credentials belong in `~/.config/browser-mcp/proxy.json`, never in this argument. |
| `proxy_bypass` | string | no | empty | Comma-separated hosts that skip the proxy, for example `localhost,*.internal`. Passed to Chromium as `--proxy-bypass-list`. |
| `ignore_certs` | boolean | no | `false` | Accept invalid TLS certificates via `--ignore-certificate-errors`. Independent of `proxy`: set it explicitly. |
| `headers` | object of name to string | no | empty | Request headers sent with every request of every page in this browser, first navigation and subresources included. Use it for an identity header, for example `{"X-Bug-Bounty":"your-handle"}`. |
| `scope` | array of strings | no | empty (no limit) | Navigation allowlist. A bare host covers its subdomains. `*.example.com`, `host:port` and `*` are accepted. Omit for no limit. |
| `scope_strict` | boolean | no | `false` | Opt-in strict mode. When `true` with an empty `scope`, every request is refused. |
| `rps` | number | no | `5` | Per-host sustained request rate in requests per second. Must be finite and greater than 0. Every engine-issued request waits for a token. |
| `burst` | integer | no | `10` | Per-host token bucket depth: how many requests may leave at once before the `rps` pace applies. Must be at least 1. |
| `owner` | string | no | session identity, else `unowned` | Owner label of 1 to 64 characters from `A-Z`, `a-z`, `0-9`, `.`, `_`, and `-`. It joins the create key. |

String-encoded arguments are accepted: the registry rewrites `"scope":"[\"wikipedia.org\"]"` into the array and `"true"` into the boolean before the call runs. Source: `code://pkg/mcp/tools/registry.go#L189-L264`.

## Returns

The payload below rides inside the result envelope, so the full reply is `{"ok":true,"data":<payload>}`. See `SKILL.md` for the envelope and for the `{"ok":false,"error":…}` shape.

One line of compact JSON with eight fields. Source: `code://pkg/mcp/tools/browser.go#L238-L261`.

```json
{"ok":true,"data":{"browser_id":"browser-4","owner":"unowned","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}}
```

| Field | Meaning |
| :--- | :--- |
| `browser_id` | The live browser id. Use it with `fetch_page`, `browser_list`, and `browser_close`. |
| `owner` | The explicit `owner`, the SSE session label, or `unowned`. |
| `persist` | `true` when the profile survives close. |
| `ttl` | The effective idle TTL as a Go duration, for example `30m0s`. `0s` disables browser sweeping. |
| `scope` | `open` when unrestricted, `strict` when an empty allowlist denies all, else the patterns joined by `", "`. |
| `scope_strict` | Whether an empty allowlist denies every request. |
| `limits` | The effective per-host budget: `rps`, `burst`, and `hosts` with one entry per host that has used a bucket, each `{host,throttled}` where `throttled` counts the requests that had to wait. `hosts` is `[]` until traffic starts and sorted by host. |
| `reused` | `true` when a live browser matched the owner and launch options. |

## Behavior and limits

**The create key is owner plus every launch option, including headers and scope.** The key holds `owner`, `bin`, `profile`, `persist`, `headless`, `stealth`, `position`, `proxy`, `proxy_bypass`, `ignore_certs`, `headers`, `scope`, `scope_strict`, `rps`, and `burst`. A different owner plus a different scope is therefore a different browser. Scope and header identity is canonical: allowlist order, duplicates and blank entries are ignored, and headers compare as sorted `name=value`. Source: `code://pkg/browsers/manager.go#L447-L482`.

**Owner joins the key but does not authorize.** `browser_close` works by id and never checks the owner. Source: `code://pkg/browsers/manager.go#L128-L140`.

**Proxy.** The value is validated before Chromium starts, so a bad `proxy` never launches a process. Credentials are never accepted in the argument: they come only from `~/.config/browser-mcp/proxy.json` (mode 0600), keyed by lowercase `host:port`. A loopback proxy (`localhost` or a loopback IP) with no `proxy.json` entry arms no auth handler, so a local intercept proxy works without credentials and pays no interception cost. A remote proxy may demand credentials, and a page under it fails until `proxy.json` has the entry. Source: `code://pkg/browsers/proxyauth.go#L20-L70` and `code://pkg/browsers/proxyauth.go#L110-L112`.

**`ignore_certs`.** Legitimate with an intercept proxy whose MITM CA signs the pages you fetch: the proxy re-signs TLS, Chromium refuses it, and `ignore_certs:true` accepts the local CA. Turn it on only for that setup. Source: `code://pkg/browsers/browser.go#L332-L334`.

**`headers` ride every request.** They are installed at page creation, before any navigation, and stay for the life of each page. Verified live: the network capture shows an `X-Doc-Test` header on the document and on every subresource. Source: `code://pkg/browsers/browser.go#L486-L489`.

**Scope matching.** A bare host also covers its subdomains, so `wikipedia.org` admits `en.wikipedia.org`. `*.example.com` means the same as the bare host. `host:port` pins the port, with the scheme default (`80` for http, `443` for https) applied otherwise. `*` allows all. Entries are lowercased and trimmed. A relative navigation (no host) is always allowed. Source: `code://pkg/browsers/policy.go#L96-L121`.

**Scope covers more than navigation.** The engine checks `fetch_page` before the tab is created, `page_act navigate` before it moves, `page_act replay` on the first hop and on every redirect hop, and intercepted subresources. An out-of-scope request is refused before it is sent. A live fetch outside the allowlist failed before the request left, with `host "example.net" is outside the scope allowlist`. A live replay that redirected to `dl.google.com` stopped with `redirect hop 1 …` and named the host. Source: `code://pkg/browsers/browser.go#L455-L459`, `code://pkg/browsers/replay.go#L453-L470`.

**Per-host rate limit.** `rps` defaults to 5 and `burst` to 10, and both join the create key, so a different rate gets a separate browser. Every engine-issued request takes a token for its host. A request whose deadline cannot fit the wait fails as `timeout`: `web-mcp: host "example.org" is rate limited: the next request slot is 6.473s away, beyond this call's deadline — lower rps, raise burst, or retry with a longer timeout`. Live: `rps:0.5, burst:1` spaced three replays about 4 seconds apart, and `rps:0.1` produced the error above. The live budget and the throttled host counters ride `limits` on the create result and on every `browser_list` entry. Source: `code://pkg/browsers/policy.go#L39-L91`.

**Idle TTL is 30 minutes**, overridable by `browser_ttl` in `~/.config/browser-mcp/config.json`. One live browser per profile directory: a second create on the same directory fails before Chromium starts.

## Failure modes and recovery

Every error rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Read `message` first. The tool prefixes it with `browser_create: `.

**Out of scope navigation.** Tested live against a browser created with `scope:["wikipedia.org"]`.

```
web-mcp: host "example.net" is outside the scope allowlist (allowed: wikipedia.org; scope=wikipedia.org, scope_strict=false) — add it with browser_create scope=["example.net"]
```

Strict mode with no scope refuses everything:

```
web-mcp: host "example.com" is outside the scope allowlist (allowed: none; scope=strict, scope_strict=true) — add it with browser_create scope=["example.com"]
```

Recovery: scope is immutable per browser, so create a new browser with the extended allowlist. Source: `code://pkg/browsers/policy.go#L140-L154`.

**Invalid proxy.** Tested live. The argument is rejected before launch, never mid-navigation.

```
browsers: invalid proxy "http://user:pass@127.0.0.1:8080": credentials are not accepted here; put them in <config-dir>/proxy.json and chmod 600 it
browsers: invalid proxy "127.0.0.1:99999": port "99999" must be a number between 1 and 65535
browsers: invalid proxy "127.0.0.1": missing port; pass host:port
```

A proxy with credentials in the URL is refused with the first message. Source: `code://pkg/browsers/proxyauth.go#L20-L70`.

**Profile already in use.** Tested live with `profile: "audit"` held by `browser-2` owned by `client-a`.

```
profile "…/audit" is already in use by browser-2 (owned by client-a) — close it with browser_close {browser_id: "browser-2"} or pick a different profile name
```

Recovery: close the holder, or pick a different `profile`.

**Invalid rate.** Source message, not run live. Validation runs before Chromium starts: `browser_create: invalid arguments: rps must be a finite number greater than 0 (got 0)` and `browser_create: invalid arguments: burst must be at least 1 (got 0)`. Recovery: pass a positive `rps` and a `burst` of at least 1, or omit both for 5 and 10.

**Invalid owner.** Source message, not run live: `browser_create: owner "bad/name" is invalid — use 1 to 64 characters from A-Z, a-z, 0-9, '.', '_' and '-' only`.

**Bad argument shape.** The error names the field, the wanted type and the value it received, then shows one corrected call. A client that sends `"true"` as text is corrected automatically. Source: `code://pkg/mcp/tools/registry.go`.

## Tested calls

Verified 2026-10-25 on the live server, and the `rps`/`burst` and scope paths again on 2026-10-27. The unscoped call ran over MCP. The scoped, strict and proxy calls ran through the CLI against the same shared manager, so the engine path and every error string above are live.

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true}}
```

Payload: `{"browser_id":"browser-4","owner":"unowned","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}`.

```json
{"tool":"browser_create","arguments":{"owner":"doc-web128","headless":true,"stealth":true,"scope":["wikipedia.org"],"headers":{"X-Doc-Test":"web-128"}}}
```

Payload: `scope:"wikipedia.org", scope_strict:false, limits:{"rps":5,"burst":10,"hosts":[]}, reused:false`. A fetch to another host failed with the scoped refusal above. A fetch to a page under the allowed domain succeeded, proving the subdomain inherits. `{"owner":"doc-strict","headless":true,"scope_strict":true}` returned `scope:"strict", scope_strict:true` and refused a request.

Verified 2026-10-27: `{"owner":"doc-rate","rps":0.5,"burst":1}` returned `limits:{"rps":0.5,"burst":1,"hosts":[]}`. Three replays to one host were spaced about 4 seconds apart. At `rps:0.1` the next request failed with `the next request slot is 6.473s away`. `browser_list` showed the host under `limits.hosts` with its `throttled` count.

## When not to use it

Do not call `browser_create` to open a page. Call `fetch_page` with a `browser_id`.

Do not repeat the same owner and options per call. That reuses one browser automatically.

Do not use `owner` as authentication. It is a label, not a permission check.

Do not pass credentials in `proxy`. They belong in `proxy.json` only.

Do not use `scope` as a security boundary. It now refuses replay redirects and intercepted subresources, but it is not authentication, and subresource traffic outside the hook set is not audited. It bounds reach, not trust.

Do not omit `profile` on a second browser that needs the same persistent directory as a live one. The create fails on the profile lock.

## Related tools

- `browser_list` — lists live browsers with `owner`, `persist`, `ephemeral`, `idle_for`, `ttl`, `scope`, and `scope_strict`. An unscoped browser shows `"scope":"open"`.
- `browser_close` — closes one browser. `purge:true` also deletes its profile.
- `fetch_page` — opens a page in a browser and returns a `session_id`.
- `page_list` / `session_list` / `session_close` — pages inside a browser.

See [browser_create_example.md](../examples/browser_create_example.md) for copyable call sequences with expected output.

## Maintainer anchors

- Owner validation: `code://pkg/mcp/tools/browser.go#L17-L33`
- Tool description: `code://pkg/mcp/tools/browser.go#L55-L78`
- Input schema: `code://pkg/mcp/tools/browser.go#L82-L150`
- Execute path: `code://pkg/mcp/tools/browser.go#L170-L234`
- Result shape: `code://pkg/mcp/tools/browser.go#L238-L261`
- Create key: `code://pkg/browsers/manager.go#L447-L482`
- Create and reuse: `code://pkg/browsers/manager.go#L540-L582`
- Profile collision: `code://pkg/browsers/manager.go#L865-L878`
- Launcher proxy and cert flags: `code://pkg/browsers/browser.go#L311-L334`
- Proxy parse and credential key: `code://pkg/browsers/proxyauth.go#L20-L70`
- Loopback check: `code://pkg/browsers/proxyauth.go#L83-L93`
- Proxy auth arming: `code://pkg/browsers/proxyauth.go#L110-L112`
- proxy.json load and mode 0600: `code://pkg/config/proxy.go#L40-L73`
- Scope matching: `code://pkg/browsers/policy.go#L96-L121`
- Scope refusal: `code://pkg/browsers/policy.go#L140-L154`
- Headers install: `code://pkg/browsers/policy.go#L219-L242`
