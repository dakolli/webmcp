> Tool: `page_fetch` · Server: web-mcp · Surface: MCP · Tested: 2026-10-25

# page_fetch

Read one aspect of a live page that `fetch_page` opened.

## What it does

`page_fetch` reads one view of an open session. It does not navigate, click, or type. The
`session_id` from `fetch_page` picks the tab. `what` picks the aspect.

The tool returns one text content block. Most aspects return JSON as text. `html`, `text`,
`css`, `js`, and `screenshot` return raw strings with no JSON wrapper. The tool description
and input schema win. Use this file as a fallback.

## Inputs

The schema is `code://pkg/mcp/tools/page.go#L90`. The aspect table is
`code://pkg/mcp/tools/aspects.go#L67`.

| Name | Type | Required | Default | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `session_id` | string | yes | — | Tab to read, for example `page-1`. Comes from `fetch_page`. |
| `what` | string | yes | — | Aspect name. See the enum below. |
| `selector` | string | no | — | CSS selector. Only `html` and `text` use it. |
| `max_bytes` | integer | no | `50000` | Output cap. A value of `0` or less means `50000`, not unbounded. |
| `target` | string | no | — | Resource URL. Only `css` and `js` use it. |
| `full_page` | boolean | no | `false` | Capture the whole scrollable page. Only `screenshot` uses it. |

The live `what` enum has 14 values: `html`, `dom`, `text`, `css`, `js`, `links`,
`screenshot`, `meta`, `interactives`, `a11y`, `network`, `console`, `article`, `marks`.

**Drift.** The worktree `pkg/mcp/tools/aspects.go` also registers `body`. The running server
does not accept it. The live enum has 14 values. The `body` registration is uncommitted.
This reference documents the live server.

The tool has no `timeout_ms` input. Do not send one. Every call has a fixed 30 second bound.
A full-page screenshot gets its own 90 second budget.

## Returns

There is no `ok` field and no outer envelope. The content is the value itself.

### Aspect return shapes

| `what` | Returns | Uses `selector` | Capped by `max_bytes` |
| :--- | :--- | :--- | :--- |
| `html` | `outerHTML` string, full page or matched element. | yes | yes, text cut |
| `dom` | JSON array of document trees. | no | yes, JSON envelope |
| `text` | `document.body.innerText` string, or the matched element's `innerText`. | yes | yes, text cut |
| `css` | JSON array `[{url,type,size}]`, or one resource's source with `target`. | no | yes, text cut |
| `js` | JSON array `[{url,type,size}]`, or one resource's source with `target`. | no | yes, text cut |
| `links` | JSON array `[{text,href,handle?}]`. | no | yes, JSON envelope |
| `screenshot` | `data:image/png;base64,...` string. | no | **no** |
| `meta` | JSON object `{title,url,meta:[...]}`. | no | yes, JSON envelope |
| `interactives` | JSON `{interactives:[...],truncated}`. | no | **no** |
| `a11y` | JSON `{nodes:[...],count,truncated}`. | no | **no** |
| `network` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `console` | JSON `{entries,count,dropped,truncated}`. | no | **no** |
| `article` | JSON `{title,byline?,excerpt?,text,words,truncated,url}`. | no | **no** |
| `marks` | JSON `{marks,screenshot,viewport_width,viewport_height,truncated}`. | no | **no** |

`dom`, `meta`, and `links` keep their JSON parseable when cut. `css` and `js` do not. They
are cut as plain text. Read the truncation matrix below.

### Tree and list shapes

- **`dom`** returns one entry per document with `title`, `url`, `frame_id`, and `root`. A
  node has `tag`, `id`, `class`, `attrs`, `text`, `handle`, `visible`, `bounds`, `frame_url`,
  and `children`. A node without `handle` is structural. Live output showed a bare
  `{"tag":"html"}` child next to the real `html` element. Read nodes by `handle`.
- **`links`** is deduplicated by `href`, first wins. It drops `javascript:`, `mailto:`,
  `data:`, `tel:`, empty hrefs, exactly `#`, and anchors with no text. `text` falls back to
  `aria-label`, `title`, then `img[alt]`.
- **`meta`** holds `title`, `url`, and a `meta` array. Each tag has any of `name`,
  `property`, `http-equiv`, `content`, and `charset`. An empty meta tag still appears.
- **`css`** lists stylesheets. Inline `<style>` blocks appear as `inline-style[1]`,
  `inline-style[2]`, with `type: "inline-style"`. `size` can be `null`. The list ends with a
  hint line that names `what=css&target=<url>`. **`js`** lists scripts the same way with
  `type: "Script"`.
- **`interactives`** holds `handle`, `role`, `text`, `href`, `type`, `x`, `y`, `visible`.
  `visible: true` means the node has a layout box, not that it is in the viewport. A live
  example.com link at `y` 739 was visible in a 400x300 viewport.
- **`a11y`** holds `handle`, `depth`, `role`, `name`, `value`, `states`, plus `count` and
  `truncated`. Names and values are clipped at 200 runes. Root frame only.
- **`network`** entries hold `method`, `url`, `type`, `status`, `mime`, `bytes`, `ms`,
  `failed`, `error`, `seq`. **`console`** entries hold `level`, `text`, `source`, `line`,
  `seq`.
- **`marks`** items hold `index`, `role`, `text`, `x`, `y`, `width`, `height`. Coordinates
  are viewport CSS pixels and can be negative.

## Behavior and limits

### Handles and staleness

Every handle has the form `g<document>-b<backendNodeId>`, for example `gy1g0vstq-b162`.
Pass a handle to `page_act` with the `handle` input. The `g` token identifies the document
that produced it. Any navigation, reload, or redirect changes the token. The old handle
then fails as stale. Re-read the aspect after navigation.

### Caps

- `interactives`: 200 entries. `truncated` counts the rest. Live Wikipedia result had
  `truncated: 335`.
- `a11y`: 400 nodes. Live Wikipedia result had `count: 400` and `truncated: 299`.
- `network` and `console`: 200-entry ring per page. One call emits the newest 100.
  `dropped` counts true ring loss. `truncated` counts buffered entries above the emit cap.
- `article`: 40000 runes in `text`, reported by a boolean `truncated`. `marks`: 60 boxes,
  with `truncated` for the rest. Only nodes inside the viewport are boxed.

### Network and console capture

Capture starts before the first document loads. Both rings are cumulative from page
creation. `clear_logs` empties both rings and resets `dropped`. It returns the cleared
counts, for example `{"network":39,"console":0}`.

The `seq` counter does not reset. A live clear on react.dev then one click returned one
entry with `seq: 40`. Compare `seq` with the value seen before the clear to prove
attribution. The simpler recipe is `clear_logs`, then one action, then read `network` and
`console`. A closed page still answers from its buffers. That last point comes from the
source and was not run live.

### Screenshots and timeouts

`screenshot` returns PNG only, as `data:image/png;base64,...`. There is no format input.
`full_page: false` captures the visible viewport. `full_page: true` captures the whole
scrollable page on a 90 second budget that is independent of the 30 second bound. A very
tall page can still time out.

Large images are not cut by `max_bytes`. A 400x300 full-page capture produced over 87 KB
of base64, and some clients drop such a block. Prefer a small viewport for `marks`.

Every other aspect is bounded at 30 seconds. There is no way to raise the bound on this
tool. Use `page_act` with `wait` before the read if the page is still loading.

## Truncation matrix

| Group | When `max_bytes` is exceeded | Parseable | Signal |
| :--- | :--- | :--- | :--- |
| `html`, `text`, `css`, `js` | Cut on a rune boundary. A note is appended. | No. A `css`/`js` list is cut inside its JSON. | Text note |
| `dom`, `links` | Array root becomes an envelope object. | Yes | `truncated: true`, `omitted` |
| `meta` | Object keeps its fields, its first array field shrinks. | Yes | `truncated: true`, `omitted` |
| `screenshot`, `interactives`, `a11y`, `network`, `console`, `article`, `marks` | Not cut. | Yes | Shape's own `truncated` or `count` |

The text note is exact:

```
… (truncated at max_bytes=600 bytes; raise max_bytes — dom/links/meta do not use selector)
```

The array envelope is `{"items":[...],"max_bytes":N,"omitted":N,"truncated":true}`.
The object envelope adds `truncated` and `max_bytes` and keeps `title` and `url`.

**`dom` is nearly all-or-nothing.** The envelope cuts whole documents, not nodes. The
Wikipedia document exceeded 400000 bytes. At `max_bytes: 50000` the live result was
`{"items":[],"max_bytes":50000,"omitted":1,"truncated":true}`. Use `article`, `text`, or
`html` with a `selector` for large pages. Do not raise `max_bytes` to megabytes.

## Failure modes and recovery

The server prefixes tool errors with `MCP error -32603: `. The strings below omit that
prefix.

**Unknown session.** Live: `page_fetch: session "page-999" not found`.
Recovery: re-read `session_list` or `page_list`. A 15 minute idle sweep removes pages.

**Unknown `what`.** Live:
`page_fetch: unknown what="tablez" (must be one of html, dom, text, css, js, links,
screenshot, meta, interactives, a11y, network, console, article, marks)`.
Recovery: send one listed value. `body` is rejected by the live server.

**No selector match on `text`.** Live:
`page_fetch: no element matched selector "#does-not-exist-xyz"`.
Recovery: check the selector with `interactives` or `a11y` first.

**Bad selector on `html`.** Live:
`page_fetch: no element matched selector "##": eval js error: SyntaxError: Failed to
execute 'querySelector' on 'Document': '##' is not a valid selector.`
Recovery: send a valid CSS selector. Prefer `#id` or a tag over complex expressions.

**Unknown `target`.** Live:
`page_fetch: resource "https://en.wikipedia.org/does-not-exist.css" not found in the
page's resource tree: {-32000 No resource with given URL found }`.
Recovery: list `css` or `js` first, then copy a `url` value exactly.

**Session and its browser are gone.** Live:
`page_fetch: browsers: launch browser browser-1: browsers: launch chromium: [launcher]
Failed to get the debug url: Opening in existing browser session.`
The manager fell back to the default browser and its profile was locked. Recovery: open a
new session with `fetch_page`, then read it.

**Missing required input.** From the source: `page_fetch: missing required argument
"session_id"` or `..."what"`. Send both fields.

**Bad JSON shape.** From the source: `page_fetch: invalid arguments: json: cannot
unmarshal ...`. Send one JSON object with the documented keys.

## When not to use

Do not use `page_fetch` to open a page. Use `fetch_page`. Do not use it to click, type,
scroll, or navigate. Use `page_act`.

Do not use `dom` for prose on a large page. Use `article` or `text`. Do not use
`screenshot` for text. It costs bytes and returns no structure.

Do not use `interactives` to test whether an element is on screen. Use `marks` for
viewport boxes. Do not expect `selector` to scope `dom`, `meta`, or `links`. They ignore it.

## Related tools

- `fetch_page` — opens the session that `page_fetch` reads. It returns `session_id`.
- `page_act` — clicks, types, presses keys, evaluates JS, navigates, and calls `clear_logs`.
- `page_list` — lists the pages of one browser.
- `session_list` — lists every live session across browsers.
- `session_close` — closes one page.
- `browser_list` — lists browsers and their pages.

For copyable calls with expected output, see the example file at
`.agents/skills/web-mcp/references/examples/page_fetch_example.md`.
