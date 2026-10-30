> Tool: `recon_js` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_js example

Goal: extract URLs from a real Swagger bundle on `httpbin.org`. The bundle is large, so `max_entries` cuts the list.

## Call

```sh
browser recon run recon_js --json '{"authorized":true,"mode":"urls","urls":["https://httpbin.org/flasgger_static/swagger-ui-bundle.js"],"max_entries":25}'
```

## Result

Trimmed to three entries:

```json
{
  "entries": [
    {
      "context": "stringLiteral",
      "filename": "https://httpbin.org/flasgger_static/swagger-ui-bundle.js",
      "kind": "absolute",
      "method": "",
      "url": "https://httpbin.org/dist"
    },
    {
      "context": "stringLiteral",
      "filename": "https://httpbin.org/flasgger_static/swagger-ui-bundle.js",
      "kind": "absolute",
      "method": "",
      "url": "https://httpbin.org/flasgger_static/index.js"
    },
    {
      "context": "stringLiteral",
      "filename": "https://httpbin.org/flasgger_static/swagger-ui-bundle.js",
      "kind": "absolute",
      "method": "",
      "query_params": ["url"],
      "url": "https://httpbin.org/debug?url="
    }
  ],
  "count": 25,
  "truncated": 1,
  "dropped": 82
}
```

`dropped: 82` means the bundle held 107 parsed entries. Raise `max_entries` or pass `output_file` for the full list.

## Failure and fix

Both input kinds at once:

```sh
browser recon run recon_js --json '{"authorized":true,"urls":["https://httpbin.org/x.js"],"js_files":["/tmp/x.js"]}'
```

```
Error: recon_js: invalid arguments: urls and js_files are mutually exclusive — pass one input kind
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: pass `urls` for a remote script, or `js_files` for a local file. Never both.

`jsluice` needs CGO. The engine builds it with `CGO_ENABLED=1`. Do not change your toolchain.

Verified: 2026-10-30 httpbin.org
