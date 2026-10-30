> Tool: `recon_http` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_http example

Goal: probe `example.com` for status, title and technology. One host gives one entry.

## Call

```sh
browser recon run recon_http --json '{"authorized":true,"targets":["example.com"],"status_code":true,"title":true,"tech":true,"max_entries":25}'
```

## Result

```json
{
  "entries": [
    {
      "url": "https://example.com",
      "status": 200,
      "title": "Example Domain",
      "tech": ["Cloudflare", "HTTP/3"]
    }
  ],
  "count": 1,
  "truncated": 0,
  "dropped": 0,
  "out_of_scope": 0
}
```

The final URL can differ from the target after a redirect. An out-of-scope redirect is dropped and counted in `out_of_scope`.

## Failure and fix

A target outside the allowlist:

```sh
browser recon run recon_http --json '{"authorized":true,"targets":["example.com"],"scope":["httpbin.org"]}'
```

```
Error: recon_http: invalid arguments: host "example.com" is outside the scope allowlist (allowed: httpbin.org) — add it to scope
hint: the target is valid — add "example.com" to scope, or pass scope:["example.com"], then retry
```

Fix: add the target to `scope`, or omit `scope` to default to the literal targets.

An empty run is not proof of a silent host. The engine may never pass the argv to httpx, or the target port may be closed.

Verified: 2026-10-30 example.com rate_limit 5 max_entries 25
