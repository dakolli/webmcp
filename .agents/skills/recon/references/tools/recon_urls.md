> Tool: `recon_urls` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_urls

Crawl seed URLs with katana. Return deduplicated URLs and endpoints.

## What it does

`recon_urls` runs the pinned katana binary in standard or headless mode. It crawls seed URLs and parses JavaScript for endpoints. It returns one compact entry per in-scope URL.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `targets` | array | yes | — | Seed URLs or hosts, at most 20. Each seed is scope-checked first. | `-u` per target |
| `depth` | integer | no | `3` | Crawl depth. Ceiling 10. A negative value is refused. | `-d` |
| `js_crawl` | boolean | no | `true` | Parse JavaScript for endpoints. | `-jc` |
| `form_extraction` | boolean | no | `false` | Emit form, input, textarea and select elements. | `-fx` |
| `known_files` | string | no | empty | `all`, `robotstxt` or `sitemapxml`. Empty disables it. | `-kf` |
| `headless` | boolean | no | `false` | Run katana's headless Chromium crawler. | `-hl` |
| `concurrency` | integer | no | `10` | Concurrent fetchers. | `-c` |
| `rate_limit` | integer | no | `5` | Per-host requests per second. | `-hrl` |
| `max_entries` | integer | no | `200` | Maximum entries returned. The crawl parses at most 5000. | runner |
| `output_file` | string | no | — | Absolute path for the raw JSONL report, mode 0600. | runner |
| `extra_args` | array | no | empty | katana flags appended verbatim. | passthrough |
| `scope` | array | no | literal seed hosts | Host allowlist. | guard and `-cs` |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[{"url":"https://scanme.sh","method":"GET","status":200}],"count":1,"truncated":0,"dropped":0,"out_of_scope":0}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].url` | One discovered URL. |
| `entries[].method` | HTTP method, when katana reported it. |
| `entries[].status` | HTTP status, when katana reported it. |
| `count`, `truncated`, `dropped` | Entry count and the cap cut. |
| `out_of_scope` | URLs the allowlist dropped. |
| `output_file` | Absolute raw report path, when requested. |

## Behavior and limits

- `depth: 0` means the default 3. A negative depth is refused.
- `known_files` expects depth 3 or more. The tool does not enforce that rule.
- `headless: true` starts katana's own Chromium. It needs a browser binary on `PATH` and costs more time.
- The seed cap is 20. A seed outside scope fails before katana starts.
- A crawl that finds nothing returns `count: 0`, not an error.
- The first call installs katana into the private GOBIN. Size `timeout_ms` for that install.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | More than 20 seeds, a depth above 10 or below 0, a bad `known_files`, a seed outside scope or a relative `output_file`. | Fix the named argument and retry. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or lower `depth`. |
| `unknown` | katana failed. The message carries its stderr. | Read the message and retry. |

Exact message for a negative depth:

```
recon_urls: invalid arguments: depth -1 must be >= 0 — omit depth to use the default 3
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for targets you are permitted to crawl. Crawling sends active traffic.

## Example call

```sh
browser recon run recon_urls --json '{"authorized":true,"targets":["https://scanme.sh"],"depth":1,"js_crawl":true,"rate_limit":5,"max_entries":10}'
```

See [recon_urls_example.md](../examples/recon_urls_example.md) for the full result.

## Related

- `recon_http` supplies the live URLs this tool consumes.
- `recon_js` parses the scripts this crawl finds.
- `recon_fuzz` fuzzes one of the found URLs.
