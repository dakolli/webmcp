> Tool: `recon_urls` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_urls example

Goal: crawl one page on `scanme.sh` at depth 1. The crawl returns the seed URL.

## Call

```sh
browser recon run recon_urls --json '{"authorized":true,"targets":["https://scanme.sh"],"depth":1,"js_crawl":true,"rate_limit":5,"max_entries":10}'
```

## Result

```json
{
  "entries": [
    {"url": "https://scanme.sh", "method": "GET", "status": 200}
  ],
  "count": 1,
  "truncated": 0,
  "dropped": 0,
  "out_of_scope": 0
}
```

A crawl that finds nothing returns `count: 0`, not an error. A shallow crawl of a small page can return only the seed.

## Failure and fix

A negative depth:

```sh
browser recon run recon_urls --json '{"authorized":true,"targets":["httpbin.org"],"depth":-1}'
```

```
Error: recon_urls: invalid arguments: depth -1 must be >= 0 — omit depth to use the default 3
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: omit `depth` for the default 3, or pass a value from 0 to 10. `0` also means the default 3.

`known_files` expects depth 3 or more. The tool does not enforce that rule. A shallow depth can make `known_files` return nothing.

Verified: 2026-10-30 scanme.sh
