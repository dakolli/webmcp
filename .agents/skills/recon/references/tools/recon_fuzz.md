> Tool: `recon_fuzz` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_fuzz

Find paths and fuzz parameters with ffuf. This tool is loud.

## What it does

`recon_fuzz` runs the pinned ffuf binary. It sends one request per wordlist entry. It supports directory, vhost and parameter modes. When you omit `mode`, it infers the placement from the URL and `params`.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `url` | string | yes | — | Target URL. The placement needs `FUZZ` or `params`. | `-u` |
| `mode` | string | no | inferred | `dir`, `vhost` or `param`. | placement |
| `wordlist` | string | no | embedded 40-entry list | Absolute path to a wordlist. | `-w` |
| `params` | string | no | empty | Query parameter name to fuzz in param mode. | URL rewrite |
| `method` | string | no | `GET` | HTTP method. | `-X` |
| `headers` | array | no | empty | Request headers, one `-H` per entry. | `-H` |
| `match_codes` | string | no | `200,204,301,302,307,401,403` | Comma list of status codes to keep. | `-mc` |
| `filter_size` | string | no | empty | Comma list of response sizes to drop. | `-fs` |
| `rate` | integer | no | `20` | Requests per second. Ceiling 100. | `-rate` |
| `threads` | integer | no | `40` | Worker count. Ceiling 200. | `-t` |
| `max_entries` | integer | no | `200` | Maximum hits returned. | runner |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |
| `output_file` | string | no | — | Absolute path for ffuf's raw JSON report, mode 0600. | runner |
| `extra_args` | array | no | empty | ffuf flags appended verbatim. | passthrough |
| `scope` | array | no | target host | Host allowlist. Base host only. | guard only |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"content_type":"text/plain","duration_ms":66,"input":"robots.txt","length":30,"lines":3,"status":200,"url":"https://httpbin.org/robots.txt","words":3}],"count":2,"truncated":0,"dropped":0,"mode":"dir"}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].url` | The matched URL. |
| `entries[].input` | The matched wordlist value. |
| `entries[].status` | HTTP status. |
| `entries[].length` | Response length. |
| `entries[].words` | Word count. |
| `entries[].lines` | Line count, when ffuf reported it. |
| `entries[].content_type` | Response content type, when reported. |
| `entries[].redirect` | Redirect location, when reported. |
| `entries[].duration_ms` | Request time in milliseconds. |
| `count`, `truncated`, `dropped` | Hit count and the cap cut. |
| `mode` | The placement that ran: `dir`, `vhost` or `param`. |
| `output_file` | Absolute raw report path, when requested. |

## Behavior and limits

- `input` is the matched wordlist value. It is not the full URL.
- A rate below 1 falls back to the default 20. Pass an explicit rate.
- `threads` defaults to 40. A value above 200 is clamped to 200.
- `dir` mode needs the literal `FUZZ` marker in the path or query.
- `vhost` mode needs `FUZZ.` in the host. `param` mode needs `params` or `FUZZ`.
- `scope` checks the base host only. ffuf sends the remaining requests itself. A redirect is not re-checked.
- The embedded wordlist has 40 entries. The engine writes it to a 0600 temp file and removes it afterwards.
- The first call installs ffuf into the private GOBIN. Size `timeout_ms` for that install.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | A bad `FUZZ` placement, a relative or missing wordlist, missing `authorized` or an out-of-scope host. | Fix the URL or the named argument. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or lower `rate`. |
| `unknown` | ffuf failed. The message carries its stderr. | Read the message and retry. |

Exact message for a missing marker:

```
recon_fuzz: invalid arguments: dir mode requires the literal FUZZ marker in the url path or query (example: https://example.com/FUZZ) — got "https://httpbin.org/"
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for targets you are permitted to test. Fuzzing is loud.

## Example call

```sh
browser recon run recon_fuzz --json '{"authorized":true,"url":"https://httpbin.org/FUZZ","rate":5,"threads":10,"max_entries":25}'
```

See [recon_fuzz_example.md](../examples/recon_fuzz_example.md) for the full result.

## Related

- `recon_urls` supplies a base URL to fuzz.
- `recon_http` gives the status and tech of a hit.
- `recon_run` runs this stage after `urls`.
