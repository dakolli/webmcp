> Tool: `recon_templates` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_templates

Scan targets with pinned nuclei v3 templates. Return one finding per match.

## What it does

`recon_templates` runs the pinned nuclei binary. It scans URLs or hosts with the selected templates. It returns one compact finding per match. It never downloads a template repository.

## Inputs

| Name | Type | Required | Default | Meaning | Scanner flag |
| :-- | :-- | :-- | :-- | :-- | :-- |
| `targets` | array | yes | — | URLs or hosts to scan, at most 200. | positional |
| `severity` | array | no | `critical,high,medium` | Severities to scan. | `-severity` |
| `tags` | array | no | empty | Template tags to include. | `-tags` |
| `templates` | array | no | empty | Absolute, existing template directory or file. | `-t` |
| `exclude_tags` | array | no | empty | Template tags to exclude. | `-etags` |
| `rate_limit` | integer | no | `20` | Per-second request budget. Ceiling 100. | `-rl` |
| `concurrency` | integer | no | `10` | Template concurrency. | `-c` |
| `headless` | boolean | no | `false` | Run headless templates. | `-headless` |
| `max_entries` | integer | no | `200` | Maximum findings returned. | runner |
| `output_file` | string | no | — | Absolute path for the raw JSONL report, mode 0600. | runner |
| `extra_args` | array | no | empty | nuclei flags appended verbatim. | passthrough |
| `scope` | array | no | literal target hosts | Host allowlist. | guard only |
| `scope_strict` | boolean | no | `false` | When true, an empty scope denies every target. | guard only |
| `timeout_ms` | integer | no | `120000` | Run budget. Ceiling 1800000. | runner |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. | guard only |

## Returns

One envelope. The payload lives in `data`.

```json
{"entries":[],"count":0,"truncated":0,"dropped":0}
```

| Field | Meaning |
| :-- | :-- |
| `entries[].template` | The template id that matched. |
| `entries[].name` | The template name, when present. |
| `entries[].severity` | Finding severity. |
| `entries[].url` | The matched URL. |
| `entries[].matcher`, `type`, `host`, `ip` | Matcher and target details, when present. |
| `entries[].tags` | Template tags. |
| `entries[].description` | Template description, when present. |
| `entries[].extracted` | Extracted values, clipped and redacted. |
| `count`, `truncated`, `dropped` | Finding count and the cap cut. |
| `output_file` | Absolute raw report path, when requested. |

## Behavior and limits

- The tool needs a template repository. It never downloads one.
- A nonexistent absolute `templates` path is refused as `invalid_args`.
- A missing template repository fails as `unknown`. The intended fix maps this case to `invalid_args`.
- Zero findings is a real answer, not an error.
- A template that exploits runs only when you name it in `templates`.
- Interactsh callbacks stay off unless you pass their flags in `extra_args`.
- The first scan needs the nuclei template repository. Run `nuclei -update-templates` once if it is absent.
- The first call installs nuclei into the private GOBIN. Size `timeout_ms` for that install.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | Missing consent, an out-of-scope target, more than 200 targets or a relative template path. | Fix the named argument. Pass an existing absolute template path. |
| `timeout` | The run budget expired. | Raise `timeout_ms` or lower `rate_limit`. |
| `unknown` | nuclei exited with its own error text. A missing template repository also raises this type. | Read the message and retry. The intended fix maps a missing repository to `invalid_args`. |

Exact message for a missing template path:

```
recon_templates: invalid arguments: templates entry "/no/such/dir" does not exist or is not readable: stat /no/such/dir: no such file or directory — pass an existing absolute template directory or file, or run "nuclei -update-templates" once
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for targets you are permitted to scan.

## Example call

```sh
browser recon run recon_templates --json '{"authorized":true,"targets":["https://example.com"],"max_entries":25}'
```

See [recon_templates_example.md](../examples/recon_templates_example.md) for the full result.

## Related

- `recon_http` supplies live URLs to scan.
- `recon_urls` finds more URLs to scan.
- `recon_run` runs this stage last, after `http`.
