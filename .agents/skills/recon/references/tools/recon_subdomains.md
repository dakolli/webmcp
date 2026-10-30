> Tool: `recon_subdomains` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_subdomains

Enumerate subdomains with passive sources. The target never sees a request.

## What it does

`recon_subdomains` runs the pinned subfinder binary. It queries public passive sources for one registrable domain. It returns each host with the sources that reported it. Use it first, before DNS or HTTP work.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `target` | string | yes | — | Registrable domain, for example `example.com`. A URL, host:port or IP is refused. | `-d` |
| `all_sources` | boolean | no | `true` | Use every passive source. | `-all` |
| `recursive` | boolean | no | `false` | Use recursive-capable sources. | `-recursive` |
| `sources` | array | no | empty | Restrict to these sources. The engine joins them with a comma. | `-sources` |
| `scope` | array | no | `[target]` | Host allowlist. A bare host covers its subdomains. | guard only |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 600000. | runner |
| `max_entries` | integer | no | `500` | Maximum hosts returned. | runner |
| `output_file` | string | no | — | Absolute path for the raw JSONL report, mode 0600. | runner |
| `extra_args` | array | no | empty | subfinder flags appended verbatim. | passthrough |

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"host":"dev.example.com","sources":["crtsh"]}],"count":3,"truncated":1,"dropped":3,"out_of_scope":0}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].host` | One discovered hostname. |
| `entries[].sources` | Passive sources that reported the host. |
| `count` | Entries returned. |
| `truncated` | `1` when `max_entries` cut the list. |
| `dropped` | In-scope hosts the cap omitted. |
| `out_of_scope` | Hosts the allowlist dropped. |
| `output_file` | Absolute raw report path, when requested. |

## Behavior and limits

- The run is passive. It sends no packet to the target.
- A run that finds nothing returns `count: 0`, not an error.
- `max_entries` defaults to 500. The response still honors that cap.
- `all_sources: true` is the default. Restrict `sources` to a small set for a fast call.
- The first call installs subfinder into the private GOBIN. Size `timeout_ms` for that install.
- An input target outside `scope` fails as `invalid_args` before the scan.
- A discovered host outside `scope` is dropped and counted in `out_of_scope`.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | Missing `authorized`, a bad `target`, a relative `output_file` or an out-of-scope target. | Pass `authorized: true` and a bare domain. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or restrict `sources`. |
| `unknown` | subfinder failed. The message carries its stderr. | Read the message and retry. |

Exact message for a missing gate:

```
recon_subdomains: invalid arguments: authorized:true is required — passive enumeration still queries public sources for a domain you must be permitted to assess, so set it explicitly
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for domains you are permitted to enumerate.

## Example call

```sh
browser recon run recon_subdomains --json '{"authorized":true,"target":"example.com","all_sources":false,"sources":["crtsh"],"max_entries":3}'
```

See [recon_subdomains_example.md](../examples/recon_subdomains_example.md) for the full result.

## Related

- `recon_dns` resolves the hosts this tool finds.
- `recon_run` runs this stage first.
- `recon_http` probes the live hosts.
