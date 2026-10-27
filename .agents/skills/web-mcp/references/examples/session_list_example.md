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
{"ok":true,"data":[{"id":"page-1","url":"https://news.ycombinator.com/news","context":"default"}]}
```

This run had one live page. The tool reads every live browser and identity, so a shared
session can show more. Use `page_list` when the answer must be exact.

## 2. Create a browser you own

```json
{"tool":"browser_create","arguments":{"headless":true,"stealth":true,"persist":false}}
```

```json
{"ok":true,"data":{"browser_id":"browser-4","owner":"unowned","persist":false,"ttl":"30m0s","scope":"open","scope_strict":false,"limits":{"rps":5,"burst":10,"hosts":[]},"reused":false}}
```

Record the `browser_id` inside `data`. The number varies.

## 3. Open one page in it

```json
{"tool":"fetch_page","arguments":{"browser_id":"browser-4","url":"https://react.dev/learn","wait_for":"stable","timeout_ms":30000}}
```

```json
{"ok":true,"data":{"session_id":"page-1","title":"Quick Start – React","url":"https://react.dev/learn","text_snippet":"React\nv19.3\nSearch\nCtrl\nK\n…","html_bytes":285239,"load_ms":2760}}
```

## 4. session_list shows every live page

The call aggregates every browser and identity. The exact answer for one browser is:

```json
{"tool":"page_list","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":[{"id":"page-1","url":"https://react.dev/learn","context":"default"}]}
```

## 5. Failure: unknown identity

`session_list` looks the identity up and never creates it. Live result:

```json
{"tool":"session_list","arguments":{"identity":"ghost"}}
```

```json
{"ok":false,"error":{"type":"invalid_args","message":"session_list: browsers: unknown identity \"ghost\" (known: attacker, default, victim)","hint":"fix the arguments per the tool InputSchema (required fields, enum values)"}}
```

Recovery: call `session_list` with `{}` and copy a live name from a `context` value.

## 6. Clean up

```json
{"tool":"browser_close","arguments":{"browser_id":"browser-4"}}
```

```json
{"ok":true,"data":"browser browser-4 closed"}
```

`browser_list` no longer shows `browser-4`. Do not close `browser-1`.

## 7. Find stale sessions

1. Call `session_list` with `{}` and note every page.
2. Call `page_list` for each live browser id that matters.
3. Mark each id in your transcript that appears in neither result as stale.
4. Confirm a suspect id with `page_fetch`. Both stale messages are typed `page_closed`:

```
page_fetch: session "page-2" not found
```

5. Close only a tab that you created.
