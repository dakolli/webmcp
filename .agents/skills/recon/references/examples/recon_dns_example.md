> Tool: `recon_dns` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_dns example

Goal: read the A, MX and NS records of `example.com`. This host has a null MX, so the entry carries `mx_null: true`.

## Call

```sh
browser recon run recon_dns --json '{"authorized":true,"targets":["example.com"],"record_types":["a","mx","ns"],"max_entries":25}'
```

## Result

```json
{
  "entries": [
    {
      "a": ["104.20.23.154", "172.66.147.243"],
      "host": "example.com",
      "mx_null": true,
      "ns": ["hera.ns.cloudflare.com", "elliott.ns.cloudflare.com"],
      "resolver": ["1.0.0.1:53", "8.8.8.8:53", "8.8.4.4:53"],
      "status_code": "NOERROR"
    }
  ],
  "count": 1,
  "truncated": 0,
  "dropped": 0
}
```

No `mx` field appears. A null MX becomes `mx_null: true`. Do not read an empty value as a missing record.

## Failure and fix

An unsupported record type:

```sh
browser recon run recon_dns --json '{"authorized":true,"targets":["example.com"],"record_types":["soa"]}'
```

```
Error: recon_dns: invalid arguments: unknown record_type "soa" — use a, aaaa, cname, mx, ns, txt, srv, ptr or caa
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: use one of the nine accepted names.

Verified: 2026-10-30 example.com
