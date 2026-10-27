> Tool: `browser_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-27

# browser_list

List every live browser with its owner, profile, idle state, TTL, scope, request budget, and open pages.

## What it does

`browser_list` reads the server's live browser map and returns one JSON array. Each entry is one browser. Each entry carries the lifecycle and identity state an agent needs to decide whether to reuse, trust, or close that browser.

The tool is read only. It does not launch Chromium. It does not open, close, or focus a browser.

It takes no arguments. Unlike `session_list`, it never creates a browser to answer.

## Inputs

The tool takes no arguments. The input schema is an empty object with no properties.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| none | none | no | none | The tool reads server state and ignores any arguments. |

The schema is `code://pkg/mcp/tools/browser.go#L295-L300`. The handler is `code://pkg/mcp/tools/browser.go#L341-L364`. The handler never decodes arguments, so an extra argument does not cause a validation error.

## Returns

The payload is a compact JSON array of browser objects inside `data`: success is `{"ok":true,"data":[<browser>…]}`, and the empty listing is `{"ok":true,"data":[]}`. See [SKILL.md](../../SKILL.md) for the full envelope and the failure shape.

Fields appear in this order, the order `browserEntry` emits at `code://pkg/mcp/tools/browser.go#L303-L316`:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `browser_id` | string | The live browser id, for example `browser-2`. |
| `owner` | string | The display label recorded at creation. It is the explicit owner, the SSE client label, or `unowned`. Ownership is informational. |
| `bin` | string | The configured executable. Omitted when empty. |
| `profile_dir` | string | The absolute profile directory. Omitted when empty. |
| `persist` | boolean | `true` when the profile survives close. |
| `ephemeral` | boolean | `true` when the profile is deleted on close. |
| `created_at` | string | Creation time as an RFC3339 timestamp. |
| `idle_for` | string | Time since the browser was last used, rounded to seconds, for example `0s` or `12m4s`. |
| `ttl` | string | The browser idle TTL as a Go duration, for example `30m0s`. `0s` means browser sweeping is disabled. |
| `scope` | string | The navigation allowlist state. See the scope table below. |
| `scope_strict` | boolean | The strict flag as configured. It matters only when the allowlist is empty: then `true` denies every request. |
| `limits` | object | The effective per-host budget `{rps, burst, hosts}`. `rps` is the sustained rate, `burst` the token depth, and `hosts` lists `{host, throttled}` for every host whose bucket has seen traffic, sorted by host. `hosts` is `[]` before any request. |
| `pages` | array | The open pages of this browser. Always present, possibly `[]`. |

### Scope values

Rendered by `scopeState` at `code://pkg/browsers/policy.go#L157-L165`:

| Value | Meaning |
| :--- | :--- |
| `open` | No allowlist exists. Navigation is unrestricted. |
| `strict` | The allowlist is empty and `scope_strict` is true. Every request is refused. |
| joined patterns | The allowlist entries joined with `, `, for example `wikipedia.org, example.org`. A bare host covers its subdomains. `*` allows all. |

Read `scope` before you send traffic to a new host. `scope=open` means no limit. A value that names hosts means a request outside them is refused before it is sent. Scope covers navigation, replay, every redirect hop, and intercepted subresources. Scope and the `limits` budget are fixed at creation and join the reuse key, so a reuse with a different scope or rate gets a separate browser. Creation options live in [browser_create.md](browser_create.md).

In the examples, `<profiles-dir>` is the absolute ~/.config/browser-mcp/profiles directory.

The `data` payload for an unscoped browser and a browser scoped to one host:

```json
[
  {"browser_id":"browser-2","owner":"client-a","profile_dir":"<profiles-dir>/.ephemeral/browser-2-150041042","persist":false,"ephemeral":true,"created_at":"2026-10-25T12:00:00.123456Z","idle_for":"0s","ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"pages":[]},
  {"browser_id":"browser-3","owner":"client-b","profile_dir":"<profiles-dir>/audit","persist":true,"ephemeral":false,"created_at":"2026-10-25T12:00:05.654321Z","idle_for":"3s","ttl":"30m0s","scope":"wikipedia.org","scope_strict":false,"limits":{"rps":0.5,"burst":1,"hosts":[{"host":"en.wikipedia.org","throttled":2}]},"pages":[]}
]
```

`persist` and `ephemeral` are opposites. An ephemeral `profile_dir` always sits under `.ephemeral/`.

Each `pages` entry has these fields:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `id` | string | The session id, for example `page-1`. |
| `url` | string | The live URL of the tab, updated after navigation and redirects. |
| `title` | string | The live document title. Omitted when empty. |
| `context` | string | The incognito identity that owns the page. |
| `stale` | boolean | Present when the live URL and title could not be read. `url` then holds the last known value. |

## Behavior and limits

- The tool is a live snapshot. It takes the manager lock to copy the browser map at `code://pkg/browsers/manager.go#L1001-L1009`, then each browser lock to copy its pages.
- Entries are sorted by `browser_id` as text, so `browser-10` sorts before `browser-2`. Source: `code://pkg/mcp/tools/browser.go#L347-L351`.
- `pages` is sorted by `id` as text. A browser with more than nine pages lists `page-10` before `page-2`.
- One call decides what to close. `idle_for` at or past `ttl` means the sweeper closes that browser soon and deletes an ephemeral profile. `owner` not yours means do not close it. `pages` empty and idle means a safe close candidate. `pages` non-empty means live work may be in flight, so check with the owner first.
- `profile_dir` is absolute. A named persistent browser reports `<profiles-dir>/<name>`. An ephemeral browser reports a unique `<profiles-dir>/.ephemeral/<label>-<random>` path.
- `bin` is omitted when the server uses the default binary. Do not depend on its presence.
- The page `url` and `title` are live and refresh after navigation.
- A page id is unique inside one browser only. Two live browsers can both report `page-1`. Keep the `browser_id` with every page id.
- `idle_for` resets when a tool uses the browser. A browser idle longer than its `ttl` is closed by the sweeper and disappears from the next listing. An ephemeral profile is deleted on that sweep. A persistent profile is kept.
- `ttl` is 30 minutes by default. `browser_ttl` in ~/.config/browser-mcp/config.json overrides it. `"0"` disables browser sweeping and reports `0s`.
- The page idle sweep is separate. It removes pages after 15 minutes without touching the browser.
- `limits.throttled` counts requests to that host that had to wait for a token. A rising count with a low `rps` explains a slow call better than a page symptom.
- The tool does not truncate. There is no `max_bytes` argument and no pagination. It returns every browser and every page.

## Failure modes and recovery

`browser_list` has no argument validation. It ignores a malformed or extra argument because the handler does not decode arguments. Failures ride the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`, with `message` prefixed `browser_list: `.

**No browser is live.** The tool returns the envelope with an empty array. This is a normal result, not an error. Tested live on 2026-10-25 on a server with no browsers.

```json
{"ok":true,"data":[]}
```

`serve mcp` starts no browser, so an empty `data` usually means "no browser is running yet". Create one with `browser_create` and list again. The CLI command `browser list` renders the same state as the text `no browsers running`.

**The transport fails.** The only error branch inside the tool is a JSON marshal failure. It classifies as `unknown`, which carries no `hint`.

```json
{"ok":false,"error":{"type":"unknown","message":"browser_list: <marshal error>"}}
```

This branch was not observed. The payload holds strings, booleans, and slices only. Recovery: retry the call. If the server is not running, start it first. That is a connection error, not a tool error.

## Tested calls

Create the browsers, then read the state. Creation and the full cleanup sequence live in [browser_list_example.md](../examples/browser_list_example.md). The two-entry array from Returns is the expected shape. The exact `created_at`, `idle_for`, and ephemeral suffix differ on your host.

```json
{"tool":"browser_list","arguments":{}}
```

## When not to use it

Do not use `browser_list` to read meta tags. Use `page_fetch` with `what: "meta"`.

Do not use it to read page content. Use `page_fetch`.

Do not use it for the pages of one known browser. Use `page_list` with the `browser_id`.

Do not use it to close anything. Use `browser_close` for a browser and `session_close` for a session.

Do not use it as proof that a browser is yours. Read `owner` but remember that ownership does not gate close.

## Related tools

- `browser_create` — creates or reuses a browser and returns its `browser_id`. Its `scope`, `scope_strict`, `rps`, and `burst` options set the allowlist and the budget that this tool reports.
- `browser_close` — closes one browser. `purge:true` deletes one profile. `prune:true` closes all and cleans orphan ephemeral directories.
- `fetch_page` — opens a page in a chosen browser. The page then appears under `pages`. A fetch to a host outside `scope` fails.
- `page_list` — lists the pages of one browser. Use it when you already know the browser.
- `session_list` — lists live sessions. It can open a browser, so `browser_list` is safer for a pure read.
- `session_close` — closes one session. Resolve the browser first with `browser_list`.

See [browser_list_example.md](../examples/browser_list_example.md) for a copyable call sequence.

## Maintainer anchors

- Tool description: `code://pkg/mcp/tools/browser.go#L284-L292`
- Input schema: `code://pkg/mcp/tools/browser.go#L295-L300`
- Entry shape: `code://pkg/mcp/tools/browser.go#L302-L338`
- Execute path: `code://pkg/mcp/tools/browser.go#L341-L364`
- Lifecycle snapshot: `code://pkg/browsers/browser.go#L226-L249`
- Page snapshot: `code://pkg/browsers/browser.go#L828-L839`
- Scope constants and rendering: `code://pkg/browsers/policy.go#L14-L20`, `code://pkg/browsers/policy.go#L157-L165`
- Browser TTL: `code://pkg/browsers/manager.go#L37-L41`
- Idle sweep: `code://pkg/browsers/manager.go#L1271-L1290`
- Prune: `code://pkg/browsers/manager.go#L1055`
