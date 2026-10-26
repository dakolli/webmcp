> Tool: `browser_list` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_list example

This sequence creates an unscoped browser and a scoped one, reads the listing, and closes both. Read [browser_list.md](../tools/browser_list.md) first. In the output, `<profiles-dir>` is the absolute config profiles directory.

Every result rides the envelope: `{"ok":true,"data":<shown payload>}`. Errors ride `{"ok":false,"error":{"type":…,"message":…,"hint":…}}`. Step 2 shows the full success envelope once, then payloads are shown bare. Step 6 shows the live empty-envelope success.

## 1. Create one unscoped and one scoped browser

```json
{"tool":"browser_create","arguments":{"owner":"client-a","headless":true,"stealth":true,"persist":false}}
{"tool":"browser_create","arguments":{"owner":"client-b","scope":["wikipedia.org"],"headless":true,"stealth":true,"persist":false}}
```

Expected payloads:

```json
{"browser_id":"browser-2","owner":"client-a","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"reused":false}
{"browser_id":"browser-3","owner":"client-b","persist":false,"ttl":"30m0s","scope":"wikipedia.org","scope_strict":false,"reused":false}
```

The scoped browser can navigate only to wikipedia.org and its subdomains. A fetch to any other host fails with a refusal that names the allowed hosts.

## 2. Read the live state

```json
{"tool":"browser_list","arguments":{}}
```

Expected envelope. `created_at`, `idle_for`, and the ephemeral suffix differ on your host.

```json
{"ok":true,"data":[{"browser_id":"browser-2","owner":"client-a","profile_dir":"<profiles-dir>/.ephemeral/browser-2-150041042","persist":false,"ephemeral":true,"created_at":"2026-10-25T12:00:00.123456Z","idle_for":"0s","ttl":"30m0s","scope":"open","scope_strict":false,"pages":[]},{"browser_id":"browser-3","owner":"client-b","profile_dir":"<profiles-dir>/.ephemeral/browser-3-150041091","persist":false,"ephemeral":true,"created_at":"2026-10-25T12:00:05.654321Z","idle_for":"2s","ttl":"30m0s","scope":"wikipedia.org","scope_strict":false,"pages":[]}]}
```

`scope` is the navigation allowlist. `open` means no limit. A host list means traffic outside it fails. `strict` in place of `open` means every navigation is refused. Read `scope` before you send traffic to a new host. The array is sorted by `browser_id`.

## 3. Open a page in the unscoped browser

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-2","url":"https://example.com","wait_for":"load"}}
```

Expected payload:

```json
{"session_id":"page-1","title":"Example Domain","url":"https://example.com/"}
```

List again. The `pages` array of `browser-2` now holds that page. The page `url` is the one captured when the page registered, so it can lag behind a later navigation. Use `page_fetch` with `what: "meta"` for the current URL.

```json
{"tool":"browser_list","arguments":{}}
```

Expected entry for your browser:

```json
{
  "browser_id": "browser-2",
  "owner": "client-a",
  "profile_dir": "<profiles-dir>/.ephemeral/browser-2-150041042",
  "persist": false,
  "ephemeral": true,
  "created_at": "2026-10-25T12:00:00.123456Z",
  "idle_for": "8s",
  "ttl": "30m0s",
  "scope": "open",
  "scope_strict": false,
  "pages": [{"id": "page-1", "url": "https://example.com"}]
}
```

## 4. Decide what to close from one call

Read each entry once:

- `idle_for` at or past `ttl` — the sweeper closes this browser soon and deletes an ephemeral profile. Do not rely on its data surviving.
- `owner` not yours — do not close it. Ownership does not gate close.
- `pages` empty and idle — a safe close candidate.
- `pages` non-empty — live work may be inside. Check with the owner first.

## 5. Close both browsers

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-2"}}
{"tool":"browser_close","arguments":{"browser_id":"browser-3"}}
```

Expected payloads:

```json
"browser browser-2 closed"
"browser browser-3 closed"
```

## 6. Confirm the listing is empty

```json
{"tool":"browser_list","arguments":{}}
```

Expected envelope, tested live on 2026-10-25 on a server with no browsers:

```json
{"ok":true,"data":[]}
```

`serve mcp` starts no browser, so an empty `data` array is the normal state before your first `browser_create`. The CLI command `browser list` prints `no browsers running` for the same state.

## Failure branch

`browser_list` has no live failure path. The only error branch is a JSON marshal failure, never observed, which would ride the envelope as:

```json
{"ok":false,"error":{"type":"unknown","message":"browser_list: <marshal error>"}}
```
