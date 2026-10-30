> Tool: `recon_run` · Server: web-mcp · Surface: MCP + CLI · Tested: 2026-10-30

# recon_run

Chain the eight recon stages over one domain. Return one record per stage.

## What it does

`recon_run` runs the scanners in one fixed dependency order. A stage consumes the artifact of the stage before it. Each stage record reports its status, count and time. A failed stage records its error and the run continues.

## Inputs

| Name | Type | Required | Default | Meaning |
| :-- | :-- | :-- | :-- | :-- |
| `domain` | string | yes | — | Registrable seed domain, for example `example.com`. |
| `stages` | array | no | `subdomains,dns,http` | A subset of the eight stage names. |
| `authorized` | boolean | yes | — | Consent gate. Must be `true`. |
| `scope` | array | no | `[domain]` | Host allowlist. A bare host covers its subdomains. |
| `stop_on_empty` | boolean | no | `false` | When true, an empty stage ends the run. |
| `max_hosts` | integer | no | `100` | Default host limit per stage input. A caller can raise it. |
| `max_urls` | integer | no | `500` | Default URL limit per stage input. A caller can raise it. |
| `max_entries` | integer | no | `200` | Total entries across all stages. |
| `timeout_ms` | integer | no | `600000` | Whole-run budget. Ceiling 3600000. |
| `output_file` | string | no | — | Absolute path for a JSONL report, mode 0600. |
| `extra_args` | object | no | empty | Per-stage scanner flags, for example `{"http":["-threads","20"]}`. |
| `templates` | array | no | empty | Each entry is an absolute template directory or file. It applies only to the templates stage. A missing path fails as `invalid_args`. |
| `rps` | number | no | `5` | Shared per-host request rate. |
| `burst` | integer | no | `10` | Shared per-host token bucket depth. |

`recon_run` has no `targets` field. It takes one `domain`. A URL, host:port or wildcard is refused.

## Stage order

The order is fixed. A selection runs in this order whatever order you pass.

1. `subdomains`
2. `dns`
3. `http`
4. `ports`
5. `urls`
6. `js`
7. `fuzz`
8. `templates`

A stage pulls in what it needs. `http` needs `dns`. `urls`, `js`, `fuzz` and `templates` need `http`. The caused stage carries an `added` list. The run carries a matching `warnings` entry.

## Returns

One envelope. The payload lives in `data`.

```json
{"ok":true,"data":{"domain":"example.com","stages":[{"stage":"dns","status":"ran","count":1,"truncated":0,"dropped":0,"ms":815},{"stage":"http","status":"ran","count":1,"truncated":0,"dropped":0,"added":["dns"],"ms":1668}],"truncated":0,"dropped":0,"warnings":["http requires dns; added it to the run"]}}
```

| Field | Meaning |
| :-- | :-- |
| `domain` | The seed domain. |
| `stages[].stage` | One stage name. |
| `stages[].status` | `ran`, `skipped` or `failed`. |
| `stages[].count` | Entries that stage produced. |
| `stages[].truncated`, `dropped` | That stage's cap cut. |
| `stages[].added` | Dependencies added because of this stage. |
| `stages[].error` | The failure or skip reason. |
| `stages[].ms` | Stage time in milliseconds. |
| `truncated`, `dropped` | The run-wide cap cut. |
| `warnings` | The dependency chain. Also a notice when a declared argument is ignored because its stage is not selected. |
| `output_file` | Absolute report path, when requested. |

The run returns per-stage counts, not entries. `output_file` writes one JSON object per stage record, not the raw scanner artifacts.

## Behavior and limits

- `stop_on_empty` defaults to false. A failed stage does not stop the run.
- A stage with no input is skipped with a reason.
- A failed stage records its error. The later stages still run.
- `max_hosts` trims each stage input to 100 by default. `max_urls` trims to 500.
- `max_entries` is a run-wide total. A stage can drop entries after the total fills.
- `extra_args` is an object keyed by stage id, not an array.
- `recon_run` does not appear in `browser recon list`. That table lists scanner forks only.
- The whole run shares one `rps` and `burst` budget.

## Failure modes and recovery

| Type | Cause | Fix |
| :-- | :-- | :-- |
| `invalid_args` | Missing consent, a bad `domain`, an unknown stage, an out-of-scope target or a relative `output_file`. | Fix the named argument and retry. |
| `timeout` | The whole-run budget expired. | Raise `timeout_ms` or select fewer stages. |
| `unknown` | A scanner failed. The stage record carries the error. | Read the stage record and retry that stage. |

Exact message for an unknown stage:

```
recon_run: invalid arguments: unknown stage "bogus" — use one of subdomains, dns, http, ports, urls, js, fuzz, templates
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

## Consent

Pass `authorized: true` only for domains you are permitted to assess.

## Example call

```sh
browser recon run recon_run --json '{"authorized":true,"domain":"example.com","stages":["http"],"max_entries":25,"rps":2,"burst":2}'
```

See [recon_run_example.md](../examples/recon_run_example.md) for the full result.

## Related

- `recon_subdomains`, `recon_dns` and `recon_http` are the default stages.
- `recon_urls`, `recon_js`, `recon_fuzz` and `recon_templates` need the `http` stage.
- `browser recon list` shows the eight scanner forks, not this pipeline.
