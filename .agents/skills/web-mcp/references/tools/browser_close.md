> Tool: `browser_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_close

Close one live browser, or every live browser. Optionally delete one profile, or clean orphan ephemeral profiles.

## What it does

`browser_close` tears down a browser and closes every page in it. It stops the Chromium process behind it.

Pass `browser_id` to close one browser. Add `purge:true` to delete its profile directory. Pass `prune:true` with no `browser_id` to close all browsers and remove leftover directories under `profiles/.ephemeral/`.

This tool does not close one tab. Use `session_close` for one tab.

## Inputs

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `browser_id` | string | no | empty | The id from `browser_create`. Omit to close all browsers. Required by `purge`. Forbidden with `prune`. |
| `purge` | boolean | no | `false` | After closing `browser_id`, delete its profile directory. The path must resolve under `~/.config/browser-mcp/profiles`. Requires `browser_id`. |
| `prune` | boolean | no | `false` | Close all browsers, then delete leftover directories under `profiles/.ephemeral/` that no live browser owns. Named persistent profiles are never removed. Pass with no `browser_id`. |

The schema is `code://pkg/mcp/tools/browser.go#L403-L421`. The schema declares no required fields.

`purge` and `prune` are mutually exclusive. `prune` takes no `browser_id`.

> **WARNING: An empty `browser_id` closes every browser on the server.**
>
> That includes browsers owned by other agents and all their pages. Never send `{}`, `{"browser_id":""}`, or a misspelled key in a shared session. A misspelled key becomes an empty id and the call succeeds, returning `{"ok":true,"data":"all browsers closed"}`.

## Returns

The payload is a prose sentence. It rides inside `data` as a JSON string, so the reply is always envelope JSON. See [SKILL.md](../../SKILL.md) for the envelope.

| Condition | Full reply |
| :--- | :--- |
| `browser_id` given and closed | `{"ok":true,"data":"browser <browser_id> closed"}` |
| `browser_id` given with `purge:true` | `{"ok":true,"data":"browser <browser_id> closed and its profile deleted"}` |
| `browser_id` omitted and every close succeeds | `{"ok":true,"data":"all browsers closed"}` |
| `prune:true` with no `browser_id` | `{"ok":true,"data":"all browsers closed; pruned N orphan ephemeral profile(s)"}` |
| Any failure | `{"ok":false,"error":{"type":…,"message":…,"hint":…}}` |

## Behavior and limits

A close closes every page in that browser. Every `session_id` from that browser becomes invalid. A later `page_fetch` or `page_act` call with an old id returns an envelope whose `message` contains `session "page-1" not found`.

The tool removes the browser from the manager map before it stops the process. A failed process stop still removes the id. The tool then returns the stop error.

The process teardown has one shared budget of about 10 seconds. The budget does not use the caller context. A canceled request does not abort a teardown that already started. Source: `code://pkg/browsers/manager.go#L1174-L1205`.

An ephemeral profile is deleted on a normal close. A persistent profile stays. A secondary browser is ephemeral unless you set `persist:true`.

**`purge` deletes one profile.** It first closes the live browser, then removes its profile directory. It refuses a path that resolves outside `~/.config/browser-mcp/profiles` and returns before the close. This is the only path that deletes a named persistent profile. Source: `code://pkg/browsers/manager.go#L1026-L1050`.

**`purge` needs a live browser id.** The manager looks the id up in the live map. An already-closed id returns a not-found error and no directory is removed. To clean orphan ephemeral directories after a crash, use `prune`.

**`prune` closes all and cleans only `.ephemeral/`.** It skips any directory a live browser still owns. It never touches a named profile outside `.ephemeral/`. Source: `code://pkg/browsers/manager.go#L1052-L1096`.

**Ownership is informational.** The tool closes by id and never checks `owner`. You can close another owner's browser. Do not.

A typo key is dangerous. The decoder ignores unknown keys. `{"browser": "browser-5"}` becomes an empty id and closes every browser. Send the exact key `browser_id`.

## Failure modes and recovery

Every failure rides the envelope: `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. The tool prefixes `message` with `browser_close: `. The types this tool can return are `invalid_args` and `unknown`.

**Unknown or already closed id.** The running list includes each owner label. A second close of `browser-3` fails the same way. Tested live on 2026-10-25.

```json
{"ok":false,"error":{"type":"unknown","message":"browser_close: browser \"browser-3\" not found (running: browser-2 (owner: client-a)): unknown browser"}}
```

The type is `unknown` and there is no `hint`. Recovery: call `browser_list`. Use a current id. Or call `browser_create` for a new browser. Do not retry the closed id.

**`purge` without `browser_id`.** Type `invalid_args`.

```json
{"ok":false,"error":{"type":"invalid_args","message":"browser_close: purge requires a browser_id — pass the browser whose profile to delete, or use prune to clean orphan ephemeral profiles","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: pass the live id. To clean orphan ephemeral directories, use `prune` alone.

**`prune` with `browser_id`.** Type `invalid_args`.

```json
{"ok":false,"error":{"type":"invalid_args","message":"browser_close: prune takes no browser_id — it closes all browsers and cleans orphan ephemeral profiles; use purge with browser_id to delete one profile","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: send `{"prune":true}` alone, or use `purge` on one id.

**`purge` and `prune` together.** Type `invalid_args`.

```json
{"ok":false,"error":{"type":"invalid_args","message":"browser_close: purge and prune are mutually exclusive — pass purge with a browser_id, or prune alone","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

**Profile outside the profiles directory.** `purge` refuses an absolute profile outside the guarded directory. The refusal text matches no known marker class, so the type is `unknown`. Source-derived, not run live.

```json
{"ok":false,"error":{"type":"unknown","message":"browser_close: browsers: refuse to delete profile \"/tmp/audit\": it resolves outside the profiles directory \"<profiles-dir>\" — only profiles under browser-mcp/profiles can be purged"}}
```

Recovery: only purge a profile that lives under the config profiles directory. The browser stays running after this refusal.

**Bad argument type.** Type `invalid_args`.

```json
{"ok":false,"error":{"type":"invalid_args","message":"browser_close: invalid arguments: json: cannot unmarshal number into Go struct field browserCloseArgs.browser_id of type string","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: send a JSON object with a string value.

## Tested calls

Create a persistent browser with `profile: "audit"` and an ephemeral browser under a second owner. These return `browser-2` and `browser-3`.

```json
{"tool":"browser_create","arguments":{"owner":"client-a","profile":"audit","persist":true,"headless":true,"stealth":true}}
{"tool":"browser_create","arguments":{"owner":"client-b","headless":true,"stealth":true,"persist":false}}
```

Close the ephemeral browser with its exact id.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-3"}}
```

Result: `{"ok":true,"data":"browser browser-3 closed"}`.

Purge the persistent profile.

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-2","purge":true}}
```

Result: `{"ok":true,"data":"browser browser-2 closed and its profile deleted"}`.

Prune leftover ephemeral directories. This closes every browser, so run it only when you own the server.

```json
{"tool":"browser_close","arguments":{"prune":true}}
```

Result: `{"ok":true,"data":"all browsers closed; pruned 0 orphan ephemeral profile(s)"}`.

## When not to use it

Do not use `browser_close` to close one tab. Use `session_close` with a `session_id`.

Do not close a browser you did not create. Other agents may share the server.

Do not omit `browser_id` in a shared session. That closes every browser.

Do not use `purge` to clean a crashed browser's leftovers. It needs a live id. Use `prune` for orphan `.ephemeral/` directories.

Do not use a close as a health check. Use `browser_list`.

## Related tools

- `browser_create` — creates or reuses a browser and returns its `browser_id`.
- `browser_list` — lists live browsers with `owner`, `persist`, `ephemeral`, and `ttl`. Use it to confirm a close.
- `session_close` — closes one session in one browser.
- `page_list` — lists the pages of one browser before you close it.

## Maintainer anchors

- Tool description: `code://pkg/mcp/tools/browser.go#L388-L399`
- Input schema: `code://pkg/mcp/tools/browser.go#L403-L421`
- Execute path: `code://pkg/mcp/tools/browser.go#L432-L466`
- Manager close: `code://pkg/browsers/manager.go#L1174-L1205`
- Purge profile: `code://pkg/browsers/manager.go#L1026-L1050`
- Prune profiles: `code://pkg/browsers/manager.go#L1052-L1096`
- Profile guard: `code://pkg/browsers/manager.go#L1106-L1140`
- Browser teardown: `code://pkg/browsers/browser.go#L902-L968`
