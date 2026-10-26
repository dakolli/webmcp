# session_list — worked example

Live sequence, 2026-10-25. Read the tool description first. Use this file when a call
fails.

Reference: `.agents/skills/web-mcp/references/tools/session_list.md`.

Every result is one envelope. The payload lives in `data`. The rule with the failure
branch is in `SKILL.md` under Cross-cutting rules.

## 1. List before you create anything

```json
{"tool":"session_list","arguments":{}}
```

```json
{"ok":true,"data":[{"id":"page-1","url":"https://news.ycombinator.com/news"}]}
```

This run had no `browser-1`. The manager resolved an unowned default browser created by
another client and returned its pages. Several unowned candidates resolve in an
unspecified order. Use `page_list` when the answer must be exact.

## 2. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

```json
{"ok":true,"data":{"browser_id":"browser-4","owner":"unowned","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"reused":false}}
```

Record the `browser_id` inside `data`. The number varies.

## 3. Open one page in it

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-4","url":"https://react.dev/learn","wait_for":"stable","timeout_ms":30000}}
```

```json
{"ok":true,"data":{"session_id":"page-1","title":"Quick Start – React","url":"https://react.dev/learn","text_snippet":"React\nv19.3\nSearch\nCtrl\nK\n…","html_bytes":285239,"load_ms":2760}}
```

## 4. session_list is not guaranteed to show your page

Another unowned browser can win the resolution. The exact answer for your browser is:

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":[{"id":"page-1","url":"https://react.dev/learn"}]}
```

## 5. Failure: unknown id

`session_list` takes no arguments, so it has no argument failure. The failure you will
meet through its siblings is an unknown id. Live result through `page_list`:

```json
{"tool":"page_list","arguments":{"browser_id":"browser-999"}}
```

```json
{"ok":false,"error":{"type":"unknown","message":"page_list: browser \"browser-999\" not found (running: browser-3 (owner: unowned), browser-4 (owner: unowned)): unknown browser"}}
```

Recovery: call `browser_list` and copy a live id from `data`.

## 6. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":"browser browser-4 closed"}
```

`browser_list` no longer shows `browser-4`. Do not close `browser-1`.

## 7. Find stale sessions

1. Call `browser_list` for every live browser id.
2. Call `page_list` for each of those ids.
3. Mark every id in your transcript that appears in none of the results as stale.
4. Confirm a suspect id with `page_fetch`. Both stale messages are typed `page_closed`:

```
page_fetch: session "page-2" not found
```

5. Close only a tab that you created.
