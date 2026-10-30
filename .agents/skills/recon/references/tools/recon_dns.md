> Tool: `recon_dns` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_dns

Resolve DNS records, or brute-force names under one domain, with dnsx.

## What it does

`recon_dns` has two modes. Resolve mode queries a list of hostnames. Brute-force mode expands names under one domain from an absolute wordlist. The response keeps only the record values that came back non-empty.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `targets` | array | no | empty | Resolve mode: hostnames to query, at most 500. | `-l` list file |
| `domain` | string | no | — | Brute-force mode: the parent domain. Requires `wordlist`. | `-d` |
| `wordlist` | string | no | — | Absolute path to a wordlist. Its presence selects brute-force mode. | `-w` |
| `record_types` | array | no | `["a","cname"]` | `a`, `aaaa`, `cname`, `mx`, `ns`, `txt`, `srv`, `ptr` or `caa`. | `-a` style flags |
| `resolver` | array | no | empty | Resolver addresses. The engine joins them into `-r`. | `-r` |
| `threads` | integer | no | dnsx default 100 | Concurrent lookups. | `-t` |
| `max_entries` | integer | no | `200` | Maximum entries returned. | runner |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |
| `output_file` | string | no | — | Absolute path for the raw JSONL report, mode 0600. | runner |
| `extra_args` | array | no | empty | dnsx flags appended verbatim. | passthrough |
| `scope` | array | no | literal targets, or `domain` | Host allowlist. A bare host covers its subdomains. | guard only |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |

Do not combine the two modes. Pass `targets` for resolve mode. Pass `domain` and `wordlist` for brute-force mode.

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"a":["104.20.23.154","172.66.147.243"],"host":"example.com","mx_null":true,"ns":["hera.ns.cloudflare.com","elliott.ns.cloudflare.com"],"resolver":["1.0.0.1:53","8.8.8.8:53","8.8.4.4:53"],"status_code":"NOERROR"}],"count":1,"truncated":0,"dropped":0}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].host` | One queried hostname. |
| `entries[].a` … `caa` | Record values for each requested type that answered. |
| `entries[].mx_null` | `true` for a null MX. No empty `mx` value is returned. |
| `entries[].status_code` | dnsx status, for example `NOERROR`. |
| `entries[].resolver` | Resolvers that answered. |
| `count`, `truncated`, `dropped` | Entry count and the cap cut. |
| `output_file` | Absolute raw report path, when requested. |

## Behavior and limits

- A host with no record of a requested type returns `count: 0` for that call, not an error.
- A null MX renders as `mx_null: true` with no empty value.
- `record_types` defaults to `a` and `cname`. An unknown name fails as `invalid_args`.
- The first call installs dnsx into the private GOBIN. Size `timeout_ms` for that install.
- A brute-force wordlist path must be absolute and must exist.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | An unknown `record_type`, a relative or missing wordlist, missing `authorized`, or an out-of-scope target. | Fix the named argument and retry. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or lower `threads`. |
| `unknown` | dnsx failed. The message carries its stderr. | Read the message and retry. |

Exact message for a bad record type:

```
recon_dns: invalid arguments: unknown record_type "soa" — use a, aaaa, cname, mx, ns, txt, srv, ptr or caa
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for hosts you are permitted to resolve.

## Example call

```sh
browser recon run recon_dns --json '{"authorized":true,"targets":["example.com"],"record_types":["a","mx","ns"],"max_entries":25}'
```

See [recon_dns_example.md](../examples/recon_dns_example.md) for the full result.

## Related

- `recon_subdomains` supplies hostnames for resolve mode.
- `recon_http` probes the hosts that answer.
- `recon_run` runs this stage after `subdomains`.
