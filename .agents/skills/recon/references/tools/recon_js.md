> Tool: `recon_js` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_js

Parse JavaScript with jsluice. Return URLs, paths and redacted secrets.

## What it does

`recon_js` parses a script body with jsluice's Tree-sitter AST. It finds URLs, paths and secrets that a regex misses. It accepts remote script URLs or absolute local `.js` files.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `urls` | array | no | empty | Absolute http(s) script URLs. Mutually exclusive with `js_files`. | positional |
| `js_files` | array | no | empty | Absolute local `.js` paths. Mutually exclusive with `urls`. | positional |
| `mode` | string | no | `urls` | `urls`, `secrets` or `both`. | mode argument |
| `max_bytes` | integer | no | `5242880` | Per-file ceiling. Hard cap 5242880. | runner |
| `max_entries` | integer | no | `200` | Maximum entries returned. | runner |
| `output_file` | string | no | — | Absolute path for the report, mode 0600. | runner |
| `extra_args` | array | no | empty | jsluice flags appended verbatim. | passthrough |
| `scope` | array | no | literal hosts of `urls` | Host allowlist. Checked before each fetch and redirect. | guard only |
| `scope_strict` | boolean | no | `false` | When true, an empty scope denies every target. | guard only |
| `rps` | number | no | `5` | Sustained per-host request rate. | guard only |
| `burst` | integer | no | `10` | Per-host token bucket depth. | guard only |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |

Pass one input kind. Do not pass both. Do not pass neither.

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"url":"https://httpbin.org/dist","method":"","context":"stringLiteral","kind":"absolute","filename":"https://httpbin.org/flasgger_static/swagger-ui-bundle.js"}],"count":25,"truncated":1,"dropped":82}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].url` | A URL or path from the script. |
| `entries[].method` | HTTP method, when jsluice found one. |
| `entries[].context` | The match type, for example `stringLiteral`. |
| `entries[].kind` | `absolute` or `relative`. |
| `entries[].filename` | The script label. |
| `entries[].query_params` | Query parameter names, when present. |
| `entries[].relative` | `true` for an unresolved relative path from a local file. |
| `count`, `truncated`, `dropped` | Entry count and the cap cut. |
| `output_file` | Absolute report path, when requested. |

A secrets entry carries `kind`, a redacted `value`, `value_prefix`, `value_length`, `redacted: true`, `filename`, `severity` and `context`. A full secret value is never returned.

## Behavior and limits

- jsluice needs CGO. The engine builds it with `CGO_ENABLED=1` and the other scanners with `0`. Do not change your toolchain.
- `mode: both` runs the URL pass and the secret pass, then merges the results.
- A fetched script resolves relative paths against its URL. A local file has no base, so a relative result stays raw.
- Scope is checked before each fetch and on every redirect hop.
- The first call installs jsluice into the private GOBIN. Size `timeout_ms` for that install.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | Missing consent, both input kinds, neither input, a relative `js_files` path or an out-of-scope host. | Pass one input kind and an absolute path. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or analyze fewer scripts. |
| `unknown` | jsluice failed. | Read the message and retry. |

Exact message for both input kinds:

```
recon_js: invalid arguments: urls and js_files are mutually exclusive — pass one input kind
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for scripts you are permitted to analyze.

## Example call

```sh
browser recon run recon_js --json '{"authorized":true,"mode":"urls","urls":["https://httpbin.org/flasgger_static/swagger-ui-bundle.js"],"max_entries":25}'
```

See [recon_js_example.md](../examples/recon_js_example.md) for the full result.

## Related

- `recon_urls` finds the script URLs this tool consumes.
- `recon_fuzz` fuzzes an endpoint this tool reveals.
- `recon_run` runs this stage after `urls`.
