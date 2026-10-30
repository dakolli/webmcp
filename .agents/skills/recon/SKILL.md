---
name: recon
description: Run the pinned external recon scanners (subfinder, dnsx, httpx, naabu, katana, ffuf, jsluice, nuclei) over MCP or the browser CLI. Use for host, DNS, port, URL, JavaScript and template recon before the first page load. Consent and scope are required.
---

# recon — external scanner lane

The recon lane answers questions about a host, not a page. It runs eight pinned scanner binaries as child processes. It never starts a browser. Use it before the browser lane on a target you may test. The browser lane reads one live page. The recon lane maps the surface around that page.

## Read order

1. Read the live tool description and input schema first. They are authoritative.
2. Read `references/tools/<tool>.md` when a call is unclear or a call failed.
3. Read `references/examples/<tool>_example.md` for a copyable call.

Do not invent an argument. Run `make schema` when a field looks wrong.

## Consent

- Every call requires `"authorized": true`.
- The field is never defaulted.
- A call without it fails as `invalid_args` and names the reason.
- Set it only for hosts you are permitted to scan.
- A `/24` scan with nuclei is a denial-of-service, not a scan.

## Scope

- Every tool takes `scope`, a host allowlist.
- A bare host covers its subdomains.
- A leading `*.` is accepted.
- `*` allows all hosts.
- An empty scope defaults to the literal targets.
- A target outside scope fails as `invalid_args` before any scan.
- `recon_subdomains`, `recon_http` and `recon_urls` also drop out-of-scope results and report `out_of_scope`.
- `recon_js` returns no `out_of_scope` field. jsluice parses bytes, not hosts.
- `recon_fuzz` checks the base host only. ffuf sends the remaining requests itself. A redirect is not re-checked.
- `recon_js` and `recon_templates` add `scope_strict`. When true, an empty scope denies every host.

## Caps

| Cap | Value |
| :-- | :-- |
| Hosts per call | 500 |
| URL seeds | 20 for katana, 200 for nuclei |
| Ports | 200 |
| Entries | `max_entries`, default 200 |
| Run budget | `timeout_ms`, default 120 s, ceiling 1800 s |
| Pipeline budget | 600 s default, 3600 s ceiling |

- `recon_subdomains` caps `max_entries` at 500 and its budget at 600 s.
- `recon_run` defaults `max_hosts` to 100 and `max_urls` to 500 for each stage input.
- These are defaults, not ceilings. A caller can raise either value.
- `output_file` is the escape hatch for a larger report.
- The report keeps the full raw output. The response still honors `max_entries`.

## Bootstrap

- The engine resolves each binary from a private GOBIN at `~/.config/browser-mcp/recon/bin`.
- It never trusts `PATH`. An `httpx` on `PATH` can be a Python script.
- The first use of a tool runs `go install <module>@<pin>` into that GOBIN.
- That install can take minutes.
- Size `timeout_ms` to cover it.
- A slow first call is not a hang.

## Binaries and pins

| Tool | Binary | Pin |
| :-- | :-- | :-- |
| `recon_subdomains` | subfinder | v2.17.0 |
| `recon_dns` | dnsx | v1.3.1 |
| `recon_http` | httpx | v1.12.0 |
| `recon_ports` | naabu | v2.6.1 |
| `recon_urls` | katana | v1.8.0 |
| `recon_fuzz` | ffuf | v2.3.0 |
| `recon_js` | jsluice | v0.0.0-20240110145140-0ddfab153e06 |
| `recon_templates` | nuclei | v3.11.1 |

`recon_run` has no binary. It calls the eight scanners in one process.

## Tool coverage

| Tool | Use | Reference | Example |
| :-- | :-- | :-- | :-- |
| `recon_subdomains` | Passive subdomain enumeration | [recon_subdomains.md](references/tools/recon_subdomains.md) | [recon_subdomains_example.md](references/examples/recon_subdomains_example.md) |
| `recon_dns` | Resolve or brute-force DNS records | [recon_dns.md](references/tools/recon_dns.md) | [recon_dns_example.md](references/examples/recon_dns_example.md) |
| `recon_http` | Probe status, title, tech and TLS | [recon_http.md](references/tools/recon_http.md) | [recon_http_example.md](references/examples/recon_http_example.md) |
| `recon_ports` | Scan TCP, UDP or passive ports | [recon_ports.md](references/tools/recon_ports.md) | [recon_ports_example.md](references/examples/recon_ports_example.md) |
| `recon_urls` | Crawl URLs and endpoints | [recon_urls.md](references/tools/recon_urls.md) | [recon_urls_example.md](references/examples/recon_urls_example.md) |
| `recon_fuzz` | Find paths and fuzz parameters | [recon_fuzz.md](references/tools/recon_fuzz.md) | [recon_fuzz_example.md](references/examples/recon_fuzz_example.md) |
| `recon_js` | Parse JavaScript for URLs and secrets | [recon_js.md](references/tools/recon_js.md) | [recon_js_example.md](references/examples/recon_js_example.md) |
| `recon_templates` | Scan with nuclei templates | [recon_templates.md](references/tools/recon_templates.md) | [recon_templates_example.md](references/examples/recon_templates_example.md) |
| `recon_run` | Chain the eight stages | [recon_run.md](references/tools/recon_run.md) | [recon_run_example.md](references/examples/recon_run_example.md) |

## Stage order

`recon_run` always uses this order. A selected stage pulls in what it needs. The `added` list on the caused stage shows the chain that ran.

| Order | Stage | Needs |
| :-- | :-- | :-- |
| 1 | `subdomains` | seed domain |
| 2 | `dns` | hosts, or the seed domain |
| 3 | `http` | hosts |
| 4 | `ports` | hosts |
| 5 | `urls` | live URLs |
| 6 | `js` | live URLs |
| 7 | `fuzz` | live URLs |
| 8 | `templates` | live URLs |

- A failed stage records the error. The run continues.
- `stop_on_empty` defaults to false. When true, an empty stage ends the run.
- `recon_run` does not appear in `browser recon list`. That table lists scanner forks only.

## extra_args

- Every scanner tool takes `extra_args`, an array of strings.
- The engine appends it verbatim after the typed arguments.
- Any scanner CLI flag is reachable.
- The typed arguments are the fast path.
- `recon_run` takes `extra_args` as an object keyed by stage, for example `{"http":["-threads","20"]}`.

## output_file

- Pass an absolute path.
- A relative path fails as `invalid_args`.
- The eight scanner tools write the raw report with mode 0600.
- `recon_run` writes one JSON object per stage record with mode 0600.
- The result returns the path in `output_file`.
- Use it when the raw report matters or the entry cap would hide data.

## Result envelope

Every MCP tool returns one JSON envelope. On the CLI, `browser recon run` prints the `data` payload directly.

```json
{"ok":true,"data":{"entries":[],"count":0,"truncated":0,"dropped":0}}
{"ok":false,"error":{"type":"invalid_args","message":"recon_ports: …","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Every recon tool raises three types: `invalid_args`, `timeout` and `unknown`. Each reference states its own detail. Read the tool reference for the exact message.

## CLI

```sh
browser recon list
browser recon run recon_dns --json '{"authorized":true,"targets":["example.com"]}'
browser recon run recon_urls --authorized --target https://example.com -- -d 2
```

- `browser recon list` shows the eight scanner forks and their install state.
- `browser recon run` executes in the current process. It never uses the serve daemon.
- The daemon IPC socket caps a request at 30 s. A scan can take longer, so the CLI runs locally.
- Everything after `--` becomes `extra_args`.

## Test hosts

Use only public consenting hosts. Never reuse one host for two calls in a row.

- `example.com`
- `httpbin.org`
- `scanme.sh`
- `scanme.nmap.org`

A site quirk is not a code defect. Repeat a failed call once on a second host.

## Known lane limits

- `naabu` uses v2.6.1. v2.7.0 needs a newer Go than this host.
- `naabu` `mode: syn` needs libpcap and a CGO build. It fails with a named error. `mode: connect` is the default.
- `jsluice` needs CGO. The engine builds it with `CGO_ENABLED=1` and the other scanners with `0`. Do not change your toolchain.
- `httpx` takes each target as `-u`. `recon_http` does this for you. A scan with no answer is an error, not an empty success.
- `recon_fuzz` `input` is the matched wordlist value. A rate below 1 falls back to the default 20. Pass an explicit rate.
- `recon_templates` needs a template repository. A nonexistent absolute `templates` path is refused. Zero findings is a real answer.
- `recon_urls` refuses a negative depth. `0` means the default 3. `known_files` expects depth 3 or more, and the tool does not enforce that.
- `recon_dns` reports a null MX as `mx_null: true` with no empty value.
