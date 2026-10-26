> Tool: `browser_close` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_close example

This sequence creates two browsers, uses one, then shows a normal close, a `purge`, and a `prune`. The full contract is in [browser_close.md](../tools/browser_close.md). Run it on an isolated server, because `prune` closes every browser.

Every result rides the envelope: `{"ok":true,"data":<shown payload>}`. Errors ride `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Create payloads are shown bare, the close results in full envelope form.

> **WARNING: Never send `{}` or an empty `browser_id` on a shared server.** That closes every browser, including browsers owned by other agents, and returns `{"ok":true,"data":"all browsers closed"}`.

## 1. Create two browsers you own

```json
{"tool":"browser_create","arguments":{"owner":"client-a","profile":"audit","persist":true,"headless":true,"stealth":true}}
{"tool":"browser_create","arguments":{"owner":"client-b","headless":true,"stealth":true,"persist":false}}
```

Expected payloads:

```json
{"browser_id":"browser-2","owner":"client-a","persist":true,"ttl":"30m0s","reused":false}
{"browser_id":"browser-3","owner":"client-b","persist":false,"ttl":"30m0s","reused":false}
```

Record both ids. Use those exact ids in the next steps.

## 2. Use one browser

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-3","url":"https://example.com","wait_for":"load"}}
```

Expected payload:

```json
{"session_id":"page-1","title":"Example Domain","url":"https://example.com/"}
```

```json
{"tool":"browser_list","arguments":{}}
```

Expected entries, trimmed to your browsers:

```json
[
  {"browser_id":"browser-2","owner":"client-a","profile_dir":"<profiles-dir>/audit","persist":true,"ephemeral":false,"created_at":"2026-10-25T20:20:38Z","idle_for":"0s","ttl":"30m0s","pages":[]},
  {"browser_id":"browser-3","owner":"client-b","profile_dir":"<profiles-dir>/.ephemeral/browser-3-150041042","persist":false,"ephemeral":true,"created_at":"2026-10-25T20:20:41Z","idle_for":"4s","ttl":"30m0s","pages":[{"id":"page-1","url":"https://example.com"}]}
]
```

## 3. Close the ephemeral browser

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-3"}}
```

Expected envelope, tested live on 2026-10-25:

```json
{"ok":true,"data":"browser browser-3 closed"}
```

The close also closes every page in that browser. The `session_id` values from `browser-3` are now invalid. Its ephemeral profile is deleted.

## 4. Purge the persistent profile

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-2","purge":true}}
```

Expected envelope:

```json
{"ok":true,"data":"browser browser-2 closed and its profile deleted"}
```

`purge` needs a live `browser_id`. It closes first, then deletes a profile that resolves under the config profiles directory.

## 5. Prune orphan ephemeral profiles

```json
{"tool":"browser_close","arguments":{"prune":true}}
```

Expected envelope:

```json
{"ok":true,"data":"all browsers closed; pruned 0 orphan ephemeral profile(s)"}
```

`prune` takes no `browser_id`. It closes every browser on the server and removes leftover `.ephemeral/` directories. It never removes a named persistent profile.

## 6. A second close fails

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-3"}}
```

Expected envelope, tested live on 2026-10-25 with the same shape:

```json
{"ok":false,"error":{"type":"unknown","message":"browser_close: browser \"browser-3\" not found (running: none): unknown browser"}}
```

The type is `unknown` and there is no `hint`. The running list inside `message` shows `none` after the prune. On a shared server it lists the live ids with their owner labels. Do not retry the closed id.

Close every browser you created with its exact id. Do not close `browser-1` or a browser owned by another agent. Do not omit `browser_id`.
