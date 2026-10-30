> Tool: `caido_intercept` · Server: web-mcp · Surface: MCP · Tested: 2026-10-30

# caido_intercept

> **DANGER.** A rule created here outlives this process. It rewrites response traffic for every
> browser that uses the Caido proxy, including browsers you do not own. It also pauses matching
> traffic while it is armed. Clear the rule as soon as you are done.

`caido_intercept` lists, arms and clears persistent response rewrite rules. The rules live on the
Caido server, not in this process. `caido_status` reports `intercept.rules`, so a stale rule is
visible before it hurts.

The full reply is `{"ok":true,"data":<payload>}`.

## Actions

| Action | Effect |
| :--- | :--- |
| `list` | Read-only. Returns the global status, each direction with its filter, every rule, the ids and the queued count. |
| `add` | Enables the response direction for `url_filter` and creates one enabled rule per rewrite. |
| `clear` | Deletes one rule by `id`, or every `webmcp-intercept` rule when `id` is empty. |

`add` enables only the response direction. It never changes request interception or `stream_ws`.

## Inputs

| Input | Type | Required | Default | Limit |
| :--- | :--- | :--- | :--- | :--- |
| `action` | string | yes | — | `list`, `add` or `clear`. |
| `url_filter` | string | add | — | An HTTPQL request expression such as `req.host.eq:"example.com"`. Blank, `*`, `res.` and malformed values are refused. |
| `status` | integer | no | 0 | The response code to write, 200..599. 0 leaves the status alone. |
| `body` | string | no | absent | Replaces the whole response body. An empty string writes an empty body. |
| `headers` | object | no | `{}` | Each name is a response header. The value replaces that header. |
| `scope_id` | string | no | `""` | `""` uses the focus scope and `"*"` every scope. It narrows a new rule to the scope allowlist hosts. |
| `id` | string | no | `""` | One rule id from `list`. An empty id selects the kill switch. |
| `confirm` | boolean | clear | false | Must be `true` when `id` is empty. |

`add` needs at least one of `status`, `body` or `headers`.

`url_filter` is mandatory. A blank value, `*`, a `res.` expression or a malformed value is refused.
That guard stops an add from rewriting every response.

## A rule is project-global

A rule is not scoped to your page, browser or session. It rewrites every request that matches its
condition, from every browser that uses the proxy. It survives a restart.

`clear` takes no scope. `scope_id` only narrows a new rule at the moment of the write.

Caido allows one section per rule. An `add` with `status`, `body` and `headers` creates three rules.
`ids` holds all three.

`add` refuses while another filter is live. Read the message, then clear the live rule first.

## The no-id clear

`clear` with no `id` is the kill switch. It needs `confirm:true`.

- It deletes only rules whose name starts with `webmcp-intercept`.
- It leaves any other rule in place.
- It disables every direction and clears the filters.
- It also deletes a `webmcp-intercept` rule that another caller armed.

On 2026-10-30 a second caller armed a rule during a live run. The no-id clear deleted that rule too.
The prefix is the only ownership signal, and every caller of this tool writes the same prefix.

A clear with one `id` deletes that rule only. It does not disable the direction. The response filter
can stay enabled, so use the kill switch to disarm the pause.

## Returns

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `rules` | array | The live rules after the call. Each holds `id`, `name`, `enabled` and `sources`. |
| `count` | integer | The live rule count. |
| `ids` | array | On `add`, the new ids. On `clear`, the ids the call targeted. |
| `count_before` | integer | The rule count before the write. |
| `status` | string | `RUNNING` or `DISABLED`. |
| `queued` | integer | The intercepted messages that wait in the queue. |
| `filter` | string | The response filter now live. Empty after a full clear. |
| `request` | object | `enabled` and `filter` for the request direction. |
| `response` | object | `enabled` and `filter` for the response direction. |
| `stream_ws` | object | `enabled` for WebSocket interception. |

`existing` lists the rules armed before the write. `scope_id` and `note` appear only when they apply.

## Failures

| Message | Type | Fix |
| :--- | :--- | :--- |
| `caido_intercept: invalid arguments: add needs url_filter — an HTTPQL expression such as req.host.eq:"example.com"` | `invalid_args` | Send a request expression. |
| `caido_intercept: invalid arguments: url_filter … is a response expression` | `invalid_args` | Use a `req.` expression. |
| `caido_intercept: invalid arguments: add needs status, body, or headers` | `invalid_args` | Send at least one rewrite. |
| `caido_intercept: invalid arguments: clear needs confirm:true or an id` | `invalid_args` | Send `confirm:true` or an `id`. |
| `caido_intercept: intercept is already enabled with filter "…" — call caido_intercept action:clear before adding a different filter` | `invalid_args` | Clear the live rule first. |
| `caido_intercept: clear incomplete — N rule(s) remain: …` | `unknown` | Read `list` and clear the ids again. |
| `caido: no instance at http://127.0.0.1:8080/graphql — start it with make caido` | `unknown` | Ask the human to run `make caido`. |

## Related tools

- `caido_status` reports `intercept.rules`, a cheap stale-rule check.
- `caido_search` finds the traffic that a rule matches.
- `caido_replay` proves the request shape a filter selects.

## Maintainer anchors

- Tool, description and schema: `code://pkg/caido/tools/intercept.go#L15-L106`
- Execute: `code://pkg/caido/tools/intercept.go#L123-L196`
- Filter validation: `code://pkg/caido/client.go#L1783-L1824`
- Add: `code://pkg/caido/client.go#L1826-L1902`
- Clear and kill switch: `code://pkg/caido/client.go#L1910-L1983`
- Conflict guard: `code://pkg/caido/client.go#L2025-L2041`
- Rule sections: `code://pkg/caido/client.go#L2085-L2153`
- Read: `code://pkg/caido/client.go#L1702-L1781`
