> Tool: `recon_http` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_http

Probe hosts and URLs with httpx. Return status, title, technology and TLS.

## What it does

`recon_http` runs the pinned httpx binary for each target. It returns one compact entry per answered URL. Only the fields you request come back. A host that answers always yields one entry.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `targets` | array | yes | — | Hosts or URLs to probe, at most 500. | `-u` per target |
| `ports` | array | no | httpx defaults | Ports to try, for example `["80","443","8080"]`. | `-ports` |
| `status_code` | boolean | no | `false` | Return the HTTP status. | `-status-code` |
| `title` | boolean | no | `false` | Return the page title, clipped at 200 characters. | `-title` |
| `tech` | boolean | no | `true` | Return detected technologies. | `-td` |
| `tls` | boolean | no | `false` | Return the certificate subject, issuer and expiry. | `-tls-grab` |
| `follow_redirects` | boolean | no | `false` | Follow HTTP redirects. | `-fr` |
| `threads` | integer | no | httpx default | Worker threads. | `-threads` |
| `rate_limit` | integer | no | httpx default | Requests per second. | `-rl` |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |
| `max_entries` | integer | no | `200` | Maximum entries returned. | runner |
| `output_file` | string | no | — | Absolute path for the raw JSONL report, mode 0600. | runner |
| `extra_args` | array | no | empty | httpx flags appended verbatim. | passthrough |
| `scope` | array | no | literal target hosts | Host allowlist. | guard only |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"url":"https://example.com","status":200,"title":"Example Domain","tech":["Cloudflare","HTTP/3"]}],"count":1,"truncated":0,"dropped":0,"out_of_scope":0}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].url` | The URL that answered. |
| `entries[].status` | HTTP status, when `status_code` is true. |
| `entries[].title` | Page title, when `title` is true. |
| `entries[].tech` | Detected technologies, when `tech` is true. |
| `entries[].tls` | `{subject,issuer,expiry}`, when `tls` is true. |
| `entries[].raw` | A stdout line that failed to decode. |
| `count`, `truncated`, `dropped` | Entry count and the cap cut. |
| `out_of_scope` | Returned URLs the allowlist dropped. |
| `output_file` | Absolute raw report path, when requested. |

## Behavior and limits

- httpx takes each target as `-u`. It accepts no bare positional target. `recon_http` passes `-u` for you.
- A run that answers nothing has two causes. The engine may never pass the argv to httpx, or the target port may be closed.
- The engine probes the binary identity before the first run. That probe rejects a false `httpx` on `PATH`.
- An out-of-scope URL in a redirect is dropped after the fact and counted in `out_of_scope`.
- The first call installs httpx into the private GOBIN. Size `timeout_ms` for that install.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | Missing `authorized`, an out-of-scope target, an over-cap list or a bad argument. | Fix the named argument and retry. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or lower `threads`. |
| `unknown` | httpx failed, or it exited 0 with no output. | Read the message. Remove a false `httpx` on `PATH` and retry. |

Exact message for an out-of-scope target:

```
recon_http: invalid arguments: host "example.com" is outside the scope allowlist (allowed: httpbin.org) — add it to scope
hint: the target is valid — add "example.com" to scope, or pass scope:["example.com"], then retry
```

## Consent

Pass `authorized: true` only for hosts you are permitted to probe.

## Example call

```sh
browser recon run recon_http --json '{"authorized":true,"targets":["example.com"],"status_code":true,"title":true,"tech":true,"max_entries":25}'
```

See [recon_http_example.md](../examples/recon_http_example.md) for the full result.

## Related

- `recon_dns` supplies hosts that resolve.
- `recon_urls` crawls the live URLs this tool finds.
- `recon_templates` scans the same live URLs.
