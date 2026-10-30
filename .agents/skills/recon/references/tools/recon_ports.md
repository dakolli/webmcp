> Tool: `recon_ports` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_ports

Scan hosts for open ports with naabu. Return host, IP and port.

## What it does

`recon_ports` runs the pinned naabu binary as a child process. It accepts hosts, CIDRs and ASNs. It returns one compact entry per unique `host`, `ip` and `port`. CONNECT is the default scan type on this build.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `targets` | array | yes | — | Hosts, CIDRs or ASNs. 500 expanded hosts per call. | `-host` |
| `ports` | string | no | top 100 | Comma list or ranges, for example `80,443,8000-8100`. Max 200 ports. | `-p` or `-top-ports 100` |
| `mode` | string | no | `connect` | `connect`, `udp` or `syn`. `syn` is unavailable on this build. | `-scan-type connect` or `-u` |
| `exclude_ports` | string | no | empty | Ports or ranges to skip. | `-exclude-ports` |
| `exclude_cdn` | boolean | no | `false` | Skip hosts behind a CDN. | `-exclude-cdn` |
| `passive` | boolean | no | `false` | Shodan-backed lookup. It sends no packets. | `-passive` |
| `rate` | integer | no | naabu default | Packets per second. | `-rate` |
| `threads` | integer | no | naabu default | Worker concurrency. | `-c` |
| `scope` | array | no | literal targets | Host allowlist. | guard only |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |
| `max_entries` | integer | no | `200` | Maximum entries returned. | runner |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |
| `output_file` | string | no | — | Absolute path for the raw JSONL report, mode 0600. | runner |
| `extra_args` | array | no | empty | naabu flags appended verbatim. | passthrough |

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"host":"scanme.sh","ip":"2400:6180:0:d0::91:1001","port":80},{"host":"scanme.sh","ip":"128.199.158.128","port":80}],"count":2,"truncated":0,"dropped":0}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].host` | The scanned host. |
| `entries[].ip` | The resolved address. |
| `entries[].port` | One open port. |
| `count`, `truncated`, `dropped` | Entry count and the cap cut. |
| `output_file` | Absolute raw report path, when requested. |

The tool returns one entry per unique `host`, `ip` and `port`. A host with an IPv4 and an IPv6 answer returns two entries for that port. Drop no entry on your side.

## Behavior and limits

- The pin is naabu `v2.6.1`. v2.7.0 needs a newer Go than this host.
- `mode: syn` needs libpcap and a CGO build. It fails with a named error.
- `mode: connect` is the default. It works on this build.
- A CIDR wider than `/24` is refused before any packet. A list that expands above 500 hosts is refused.
- An explicit port list above 200 ports is refused.
- The first call installs naabu into the private GOBIN. Size `timeout_ms` for that install.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | A bad argument, an over-cap call, an out-of-scope target or `mode: syn`. | Use `mode: connect` and shrink the target set. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or reduce `ports`. |
| `unknown` | The scanner binary failed. | Read the message and retry. |

Exact message for SYN:

```
recon_ports: invalid arguments: mode syn needs a libpcap-capable naabu build, but this binary is installed with CGO_ENABLED=0 — use mode connect (default) or mode udp, both of which work without libpcap
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for hosts you are permitted to scan.

## Example call

```sh
browser recon run recon_ports --json '{"authorized":true,"targets":["scanme.sh"],"ports":"80","rate":5}'
```

See [recon_ports_example.md](../examples/recon_ports_example.md) for the full result.

## Related

- `recon_http` probes the ports that answer.
- `recon_run` runs this stage after `http`.
- `recon_templates` scans live URLs, not raw ports.
