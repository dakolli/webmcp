> Tool: `recon_subdomains` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_subdomains example

Goal: list a few subdomains of `example.com` from one passive source. This call took about 57 seconds. A first call can also install subfinder.

## Call

```sh
browser recon run recon_subdomains --json '{"authorized":true,"target":"example.com","all_sources":false,"sources":["crtsh"],"max_entries":3,"timeout_ms":90000}'
```

## Result

Trimmed to two entries:

```json
{
  "entries": [
    {"host": "dev.example.com", "sources": ["crtsh"]},
    {"host": "emudhra-lab.example.com", "sources": ["crtsh"]},
    {"host": "m.example.com", "sources": ["crtsh"]}
  ],
  "count": 3,
  "truncated": 1,
  "dropped": 3,
  "out_of_scope": 0
}
```

`truncated: 1` means `max_entries: 3` cut the list. `dropped: 3` is the number omitted. Raise `max_entries` or pass `output_file` for the full set.

## Failure and fix

A call without the consent gate:

```sh
browser recon run recon_subdomains --json '{"target":"example.com"}'
```

```
Error: recon_subdomains: invalid arguments: authorized:true is required — passive enumeration still queries public sources for a domain you must be permitted to assess, so set it explicitly
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: add `"authorized": true` for a domain you may enumerate.

Verified: 2026-10-30 example.com
