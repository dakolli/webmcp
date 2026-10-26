> Tool: `browser_create` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_create example

Use this file only when tool calling for `browser_create` fails. The tool description is primary. The full contract is in [browser_create.md](../tools/browser_create.md).

This sequence reuses the default browser, creates one isolated browser, confirms it, tests common failures, and closes it. Ids and paths differ on your host.

## 1. Reuse the live default browser

```json
{"tool":"browser_create","arguments":{}}
```

Expected output:

```json
{"browser_id":"browser-1"}
```

Zero arguments reuses `browser-1` only while it is alive. If it is dead, the call returns a new id. Do not assume `browser-1` after a restart.

## 2. Create an isolated browser

```json
{"tool":"browser_create","arguments":{"profile":"skill-create-93","headless":true,"stealth":true}}
```

Expected output:

```json
{"browser_id":"browser-5"}
```

The id can differ. Record it. Every option creates a new browser. Add `"persist":false` to delete the profile on close when your host exposes that field.

## 3. Confirm the new browser

```json
{"tool":"browser_list","arguments":{}}
```

Expected output, trimmed to your browser:

```json
[
  {
    "browser_id": "browser-5",
    "profile_dir": "/home/<user>/.config/browser-mcp/profiles/skill-create-93",
    "pages": []
  }
]
```

The `profile_dir` is absolute. `pages` is empty because `browser_create` opens no page.

## 4. Failure: reuse the default profile

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true}}
```

Expected error while `browser-1` is live:

```
MCP error -32603: browser_create: browsers: launch browser browser-2: browsers: launch chromium: profile "…/profiles/default" appears to be in use by another running browser — pass a distinct profile name: … SingletonLock: File exists (17)
```

Recovery: pass a unique `profile` name.

## 5. Failure: bad binary

```json
{"tool":"browser_create","arguments":{"bin":"/nonexistent/definitely-not-a-browser-93","profile":"skill-create-93-bin","headless":true}}
```

Expected error:

```
MCP error -32603: browser_create: browsers: launch browser browser-11: browsers: launch chromium: fork/exec /nonexistent/definitely-not-a-browser-93: no such file or directory
```

## 6. Failure: relative profile path

```json
{"tool":"browser_create","arguments":{"profile":"./leak","headless":true}}
```

Expected error:

```
MCP error -32603: browser_create: browsers: resolve profile for browser-12: config: profile "./leak" is a relative path; pass an absolute path or a bare name (resolved under browser-mcp/profiles)
```

## 7. Close your browser

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-5"}}
```

Expected output:

```
browser browser-5 closed
```

Close only the ids you created. Never send `{}` or an empty `browser_id` in a shared session.
