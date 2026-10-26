> Tool: `browser_create` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# browser_create

Launch a Chromium browser or reuse the live default browser. Return the `browser_id` that every page tool needs.

## What it does

`browser_create` starts one Chromium process and registers it under a `browser_id`. The id looks like `browser-1` or `browser-7`.

A call with zero arguments reuses the live default browser. It returns `browser-1` when that browser is alive.

A call with at least one argument creates a new browser. It mints a new id. It does not replace `browser-1`.

The call returns after Chromium is up. It does not open a page. Use `fetch_page` to open a page.

## Inputs

All inputs are optional. The schema declares no required field. The source is `code://pkg/mcp/tools/browser.go#L53`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `bin` | string | no | empty | Browser executable path or command, for example `brave-browser`. Empty probes `brave-browser`, `brave`, `google-chrome`, `chrome`, then `chromium`. |
| `profile` | string | no | empty | Bare profile name or absolute directory. A bare name resolves under `~/.config/browser-mcp/profiles/<name>` when persistent, or under `profiles/.ephemeral/` when ephemeral. A relative path with a separator is rejected. |
| `persist` | boolean | no | `false` | Keep the profile after close. `false` gives secondary browsers an ephemeral profile that is deleted on close. `browser-1` with no explicit profile stays persistent. |
| `headless` | boolean | no | server setting | Run without a window. The manager default is headful. |
| `stealth` | boolean | no | server setting | Apply go-rod/stealth to every page in the new browser. The manager default is on. |
| `position` | string | no | empty | Headful window preset: `left`, `right`, `max`, or `normal`. |

Passing any input creates a new browser, even when the value equals the default. `{"headless":false}` and `{"stealth":true}` both create a new id.

## Returns

The tool returns one line of compact JSON. It has one field.

```json
{"browser_id":"browser-5"}
```

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `browser_id` | string | The live browser id. Use it with `fetch_page`, `browser_list`, `page_list`, and `browser_close`. |

Test run: `{"headless":true,"profile":"skill-create-93-eph","stealth":true}` returned `{"browser_id":"browser-5"}`.

Errors are MCP errors with the prefix `MCP error -32603: `. The failure section lists the tested messages.

## Behavior and limits

**Reuse needs zero arguments.** The tool reuses only the default browser. It checks that `browser-1` is alive. If `browser-1` is dead or absent, the call mints the next free id and does not use `browser-1`.

**You cannot reattach by id.** `browser_create` has no `browser_id` input. A second call with options always creates another Chromium process.

**Ids are not reused.** Each create or failed launch consumes the next number. Test run: a failed launch used `browser-2`, and a later success returned `browser-4`.

**Profiles separate state.** A persistent profile keeps cookies and logins across restarts. An ephemeral profile starts clean and is removed when the browser closes. An absolute path is always persistent.

**Persist default follows the server.** The schema default is `false`. The `browser serve mcp` daemon builds its manager with persistence on. Test run: a browser created without `persist` reported a path under `profiles/`, not `profiles/.ephemeral/`, so the engine treated it as persistent. Pass `persist:false` to request an ephemeral profile.

**The default profile is a trap.** A new browser with options and no `profile` can resolve the same directory as the server's default browser. The launch then fails with a `SingletonLock` error. Pass a distinct `profile` name for every new browser.

**`browser_list` shows the real path.** It returns `profile_dir` as an absolute path. Test run: `browser-5` reported `/home/<user>/.config/browser-mcp/profiles/skill-create-93-eph`.

**Position is headful only.** In headless mode the value is accepted and ignored. Test run: `left`, `right`, `max`, and `normal` all created a headless browser with no error and no window.

**Position mapping in headful mode.** `left` and the empty value place the window at `0,0` with size `1280x1400`. `right` places it at `1280,0` with size `1280x1400`. `max` starts the window maximized. `normal` falls through to the `left` case.

**Stealth is per page.** A `true` value makes every page in the new browser run the go-rod/stealth patches. It does not change existing pages.

**Cost.** Each browser is a full Chromium process. Keep the live count small. The 15-minute TTL closes idle pages, not browsers.

**No page is opened.** The new browser has an empty page list. `browser_list` shows `"pages": []`.

## Failure modes and recovery

The MCP server prefixes tool errors with `MCP error -32603: `. The strings below show the message after that prefix. Ids and paths differ on your host.

**Profile already in use.** Another live browser holds the same profile directory.

```
browser_create: browsers: launch browser browser-2: browsers: launch chromium: profile "…/profiles/default" appears to be in use by another running browser — pass a distinct profile name: [launcher] Failed to get the debug url: … SingletonLock: File exists (17)
```

Test run: a call with `{"headless":true,"stealth":true}` and no `profile` hit this error while `browser-1` held the default profile.

Recovery: add a unique `profile` name. Set `persist:false` with that name to use an ephemeral directory.

**Unknown or missing binary.**

```
browser_create: browsers: launch browser browser-11: browsers: launch chromium: fork/exec /nonexistent/definitely-not-a-browser-93: no such file or directory
```

Test run: the bad `bin` value returned this exact message.

Recovery: omit `bin` to use discovery, or pass a path that exists and is executable.

**Relative profile path.**

```
browser_create: browsers: resolve profile for browser-12: config: profile "./leak" is a relative path; pass an absolute path or a bare name (resolved under browser-mcp/profiles)
```

Test run: `{"profile":"./leak"}` returned this error before launch.

Recovery: use a bare name such as `audit`, or an absolute path.

**`persist` arrives as a string.** A host that caches an older tool schema may send the boolean as text.

```
browser_create: invalid arguments: json: cannot unmarshal string into Go struct field browserCreateArgs.persist of type bool
```

Test run: `{"persist":false}` returned this error while the host schema lacked the `persist` property.

Recovery: reconnect the MCP client so it reloads the tool schema. Until then, omit `persist` and pass a unique `profile` name.

**Bad argument shape.**

```
browser_create: invalid arguments: json: cannot unmarshal ...
```

Recovery: send one JSON object with the documented keys.

**Manager shut down.** The server is stopping.

```
browser_create: browsers: manager has been shut down
```

This path was not run live. The message comes from the source. Recovery: start a new `browser serve mcp` process.

## When not to use it

Do not call `browser_create` to open a page. Call `fetch_page`. It uses the default browser and creates the tab.

Do not create a browser for every task. Reuse the live default unless you need a separate profile or process.

Do not use it to inspect state. Use `browser_list`.

Do not omit `profile` on a second browser while the default browser is live. The launch fails on the profile lock.

Do not use it to close a browser. Use `browser_close`.

## Related tools

- `browser_list` — lists live browsers, their absolute `profile_dir`, and their pages.
- `browser_close` — closes one browser and its pages. Pass the id you got here.
- `fetch_page` — opens a page in a browser and returns a `session_id`.
- `page_list` — lists the pages of one browser.
- `session_list` — lists all live sessions without a `browser_id`.
- `session_close` — closes one page.

See [browser_create_example.md](../examples/browser_create_example.md) for copyable call sequences with expected output.
