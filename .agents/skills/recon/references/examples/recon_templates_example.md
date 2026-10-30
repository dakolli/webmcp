> Tool: `recon_templates` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_templates example

Goal: scan one URL with the default severity set. This call returned zero findings, which is a real answer.

## Call

```sh
browser recon run recon_templates --json '{"authorized":true,"targets":["https://example.com"],"max_entries":25}'
```

## Result

```json
{
  "entries": [],
  "count": 0,
  "truncated": 0,
  "dropped": 0
}
```

`count: 0` means no template matched. It is not an error. Keep the result as a clean finding.

## Failure and fix

A nonexistent absolute template path:

```sh
browser recon run recon_templates --json '{"authorized":true,"targets":["https://example.com"],"templates":["/no/such/dir"]}'
```

```
Error: recon_templates: invalid arguments: templates entry "/no/such/dir" does not exist or is not readable: stat /no/such/dir: no such file or directory — pass an existing absolute template directory or file, or run "nuclei -update-templates" once
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: pass an existing absolute template path, or run `nuclei -update-templates` once.

The tool needs a template repository. It never downloads one. Interactsh callbacks stay off unless you pass their flags in `extra_args`.

Verified: 2026-10-30 example.com
