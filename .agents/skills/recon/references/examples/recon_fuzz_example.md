> Tool: `recon_fuzz` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_fuzz example

Goal: fuzz paths on `httpbin.org` with the embedded wordlist. The run sent 40 requests at rate 5.

## Call

```sh
browser recon run recon_fuzz --json '{"authorized":true,"url":"https://httpbin.org/FUZZ","rate":5,"threads":10,"max_entries":25}'
```

## Result

```json
{
  "entries": [
    {
      "content_type": "text/plain",
      "duration_ms": 66,
      "input": "robots.txt",
      "length": 30,
      "lines": 3,
      "status": 200,
      "url": "https://httpbin.org/robots.txt",
      "words": 3
    },
    {
      "content_type": "text/html; charset=utf-8",
      "duration_ms": 66,
      "input": "static",
      "length": 44,
      "lines": 3,
      "redirect": "/static/",
      "status": 307,
      "url": "https://httpbin.org/static",
      "words": 3
    }
  ],
  "count": 2,
  "truncated": 0,
  "dropped": 0,
  "mode": "dir"
}
```

`input` is the matched wordlist value. Use it to show which word produced the hit.

## Failure and fix

A dir URL with no `FUZZ` marker:

```sh
browser recon run recon_fuzz --json '{"authorized":true,"url":"https://httpbin.org/"}'
```

```
Error: recon_fuzz: invalid arguments: dir mode requires the literal FUZZ marker in the url path or query (example: https://example.com/FUZZ) — got "https://httpbin.org/"
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: put `FUZZ` in the path. For param mode, pass `params`. For vhost mode, put `FUZZ.` in the host.

A rate below 1 falls back to the default 20. A negative rate acts the same way. Pass an explicit rate from 1 to 100.

Verified: 2026-10-30 httpbin.org
