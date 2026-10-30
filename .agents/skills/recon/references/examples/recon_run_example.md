> Tool: `recon_run` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_run example

Goal: run the `http` stage on `example.com`. The pipeline adds `dns`, because `http` needs it.

## Call

```sh
browser recon run recon_run --json '{"authorized":true,"domain":"example.com","stages":["http"],"max_entries":25,"rps":2,"burst":2}'
```

## Result

```json
{
  "domain": "example.com",
  "stages": [
    {"stage": "dns", "status": "ran", "count": 1, "truncated": 0, "dropped": 0, "ms": 815},
    {"stage": "http", "status": "ran", "count": 1, "truncated": 0, "dropped": 0, "added": ["dns"], "ms": 1668}
  ],
  "truncated": 0,
  "dropped": 0,
  "warnings": ["http requires dns; added it to the run"]
}
```

The `http` record carries `added: ["dns"]`. The run carries the same chain in `warnings`. The run does not return the URLs themselves. Read the per-stage count and the raw `output_file` when you need the artifacts.

## Failure and fix

A URL in `domain`:

```sh
browser recon run recon_run --json '{"authorized":true,"domain":"https://example.com","stages":["http"]}'
```

```
Error: recon_run: invalid arguments: domain "https://example.com" must be a bare registrable domain, not a URL, host:port or wildcard — pass example.com
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: pass a bare domain such as `example.com`.

An unknown stage name:

```sh
browser recon run recon_run --json '{"authorized":true,"domain":"example.com","stages":["bogus"]}'
```

```
Error: recon_run: invalid arguments: unknown stage "bogus" — use one of subdomains, dns, http, ports, urls, js, fuzz, templates
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: pass one of the eight stage names. The order is fixed, so pass them in any order.

`recon_run` does not appear in `browser recon list`. That table lists scanner forks only.

Verified: 2026-10-30 example.com
