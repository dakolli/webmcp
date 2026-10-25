---
name: web-mcp
description: Drive a live, stateful Chromium browser over MCP. Open pages (sessions) inside a persistent browser instance, inspect page state (meta, text, links, DOM snapshot, HTML, CSS/JS resources, screenshot), and execute interactions (click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, scroll, select, wait, set_viewport, emulate). Use whenever a real browser is needed for scraping JS-rendered content, testing web UIs, filling forms, exploring multi-page sites, or verifying live web behavior.
disable-model-invocation: true
---

# web-mcp: Browser Automation for LLM Agents

Drive Chromium via MCP tools using stateful sessions, structured DOM/text extractions, and robust interaction primitives.

---

## Core Architecture and Mental Model

- **Browser (`browser_id`, e.g., `browser-1`)**: The underlying Chromium/Brave process managed by the server. Created lazily or via `browser_create`, closed with `browser_close`. Auto-detects Brave (`brave-browser` / `brave`) as the default browser when available.
- **Persistent Profiles**: `browser-1` uses the persistent user profile at `~/.config/browser-mcp/profiles/<browser_name>` (e.g. `profiles/brave` or `profiles/default`), persisting cookies, storage, and logins across runs. `browser_create` with **no options** reuses a live `browser-1`; passing any option creates a new `browser-N`.
- **Sessions expire**: idle pages are closed after 15 minutes, and a crashed Chromium is relaunched under the same `browser_id`. A vanished `session_id` is normal — re-open the URL rather than retrying it.
- **Session (`session_id`, e.g., `page-1`, `page-2`)**: A distinct browser tab created by `fetch_page`. All read (`page_fetch`) and interaction (`page_act`) operations require a valid `session_id`.
- **Navigation Continuity**: A session maintains state across navigations, redirects, and history traversal (`back`/`forward`). Reading after a navigation reads the new page.
- **Resource Management**: Always close sessions with `session_close` and shut down browsers with `browser_close` when workflows conclude.

---

## Tool Reference

### 1. Lifecycle and Session Tools

| Tool | Parameters | Purpose and Returns |
|---|---|---|
| `browser_create` | `{ bin?: string, profile?: string, headless?: boolean, stealth?: boolean, position?: "left"\|"right"\|"max"\|"normal" }` | Launches a browser instance. Returns `{ browser_id }`. Omit all options to reuse `browser-1`. |
| `browser_list` | `{}` | Lists all active browsers and their pages. |
| `browser_close` | `{ browser_id: string }` | Terminates Chromium instance and all open tabs. |
| `fetch_page` | `{ url: string, wait_for: "load"\|"idle"\|"stable", timeout_ms?: number, viewport?: { width, height }, device?: string, user_agent?: string }` | Opens URL in a new tab. **Always pass `wait_for`** — an omitted value currently fails with `invalid wait option ""` (the schema's `idle` default is advisory only). **Avoid `wait_for: "idle"`** — the idle network-wait hangs on JS-heavy sites (bot WAFs, trackers, infinite polling) and stalls the call. Prefer `"load"` or `"stable"`. Pass `device` (e.g. `"IPhoneX"`) to apply emulation *before* navigation. Returns `{ session_id, title, url, text_snippet, html_bytes, load_ms }`. |
| `session_list` | `{}` | Lists live sessions as `[{ id, url }]` (today: the default browser's pages only). |
| `session_close` | `{ session_id: string }` | Closes tab and invalidates `session_id`. |
| `page_list` | `{ browser_id: string }` | Lists open pages. **`browser_id` is accepted but currently ignored** — it lists the default browser. |

### 2. Inspection Tool: `page_fetch`

Extracts data from a live session without mutating state.

```
page_fetch {
  session_id: string,
  what: "meta" | "text" | "links" | "dom" | "html" | "css" | "js" | "screenshot" | "interactives" | "a11y" | "network" | "console",
  selector?: string,     // Scopes "text", "html", or "dom" to a CSS selector
  max_bytes?: number,    // Response payload cap in bytes (default: 50000)
  target?: string,       // Specific URL when what="css"|"js" to fetch file content
  full_page?: boolean    // For what="screenshot", capture entire scrollable page
}
```

- `meta`: Returns `{ title, url, meta: [{ name/property, content }] }`. Fastest way to confirm title, URL, and OpenGraph tags.
- `text`: Visible page text. Use `selector` (e.g., `selector: "main"` or `selector: "article"`) to avoid nav/footer noise.
- `links`: Array of `[{ text, href, handle? }]`. The navigation map of the page; `handle` is the first anchor carrying that href (usable as `page_act`'s `handle`).
- `interactives`: **Start here when you intend to act.** `{ interactives: [{ handle, role, text, href, type, x, y, visible }], truncated }` — every visible clickable/typeable element, capped at 200 (the rest are counted in `truncated`). ~2–5 KB, never byte-truncated. Handles look like `g3k9x2ab-b1073741831` and can be passed straight to `page_act`'s `handle` argument (see below); otherwise use the `text`/`role`/`href`/`x`,`y` fields to choose a target and act with a selector or coordinates.
- `a11y`: **The semantic map.** `{ nodes: [{ handle?, depth, role, name?, value?, states? }], count, truncated }` — the accessibility tree in DFS order, flattened: computed `role` (button, textbox, link, heading, …), accessible `name` (from aria-label/label/content), `value` (inputs), `depth` (0 = root), and `states` holding only the flags whose value is true, among checked, expanded, selected, disabled, required, pressed, invalid, focused (`flag=mixed` for a mixed value). Capped at 400 (the rest counted in `truncated`), never byte-truncated. Text-fragment roles (StaticText, InlineTextBox, paragraph, code, …) and unnamed images are omitted, and `name`/`value` are clipped at 200 runes — use `text` for prose. DOM-backed nodes carry `handle` (`g<document>-b<backendNodeId>`) usable as `page_act`'s `handle`. Root frame only — iframes are separate trees. Use this to answer "what IS this control" rather than "where is it".
- `network`: **Request truth.** `{ entries: [{ method, url, type?, status?, mime?, bytes?, ms?, failed?, error?, seq }], count, dropped, truncated }` — observed requests/responses. `entries` is the newest 100 buffered entries in chronological order; `dropped` counts true loss to the 200-entry ring, `truncated` counts buffered older entries left out. Never byte-truncated. `data:` URLs are skipped; `url`, `error` and `text` are clipped at 300, 200 and 500 bytes. Capture is **cumulative from page creation**, so call `page_act` `clear_logs` right before the step whose traffic you want, then filter client-side.
- `console`: **Log truth.** Same envelope over `{ level, text, source?, line?, seq }` — `console.*` calls, uncaught exceptions, and browser log entries. Cumulative from page creation; same `clear_logs`-before-observe recipe.
- `dom`: Compact structured tree of DOM nodes with tags, attributes, visibility, and bounding boxes; covers every document and iframe, including shadow DOM. Node shape: `{ tag, id?, class?, attrs?, text?, handle?, visible?, bounds?, frame_url?, children? }`; every element node carries `handle` (`g<document>-b<backendNodeId>`). Always set `max_bytes` or scope with `selector`.
- `html`: Outer HTML markup. Use `selector` to avoid context explosion on large pages.
- `css` / `js`: Lists loaded resource URLs with sizes; `size` is nullable. Re-request with `target: "<url>"` to read raw stylesheet/script contents.
- `screenshot`: Returns base64 PNG data-URI; `full_page: true` captures the whole scrollable page (MCP gives that a 90s budget).
- **Truncation**: `dom`, `links`, `meta`, and the css/js list are cut at `max_bytes` and can end mid-JSON. `interactives`, `a11y`, `network`, `console`, and `screenshot` are never truncated (network/console signal incompleteness through `truncated` + `dropped`).


### 3. Interaction Tool: `page_act`

Executes user actions, navigations, keyboard/mouse events, file uploads, or JavaScript on a live session.

```
page_act {
  session_id: string,
  action: "click" | "input" | "type" | "press_key" | "click_at" | "hover_at" | "drag_and_drop" | "upload_file" | "eval" | "navigate" | "back" | "forward" | "reload" | "hover" | "scroll" | "select" | "wait" | "set_viewport" | "emulate" | "clear_logs",
  selector?: string,     // CSS selector targeting element (click, input, type, press_key, upload_file, hover, select, wait interactable)
  handle?: string,       // Element handle from page_fetch (g<document>-b<backendNodeId>); wins over selector when both are set
  value?: string,        // Payload for input/type, JS function for eval, URL for navigate, option value for select
  key?: string,          // Key or key combo for action="press_key" (e.g., "Enter", "Escape", "Tab", "Control+a", "Meta+c", "Shift+Tab")
  button?: "left" | "right" | "middle", // Mouse button for click, click_at, drag_and_drop (default: "left")
  click_count?: number,  // Click count for click and click_at (1=single, 2=double click; default: 1)
  target_x?: number, target_y?: number, // Target coordinates for action="drag_and_drop"
  target_selector?: string,             // Target element selector for action="drag_and_drop"
  paths?: string[],      // Array of local file paths for action="upload_file"
  wait_mode?: "load" | "idle" | "stable" | "interactable" | "ms", // For action="wait"
  timeout_ms?: number,   // Per-call timeout / sleep duration for wait_mode="ms"
  x?: number, y?: number,// Coordinates for action="scroll", "click_at", "hover_at", "drag_and_drop"
  viewport?: { width: number, height: number }, // For action="set_viewport"
  device?: string        // Device preset (e.g., "IPhoneX", "IPadPro", "clear") for action="emulate"
}
```

**Stable handles.** `page_fetch` mints a `handle` of the form `g<document>-b<backendNodeId>` (e.g. `g3k9x2ab-b1073741831`) on every element in `interactives`, on every element node in `dom`, on matched `links` entries, and on DOM-backed `a11y` nodes. Pass it as `handle` to `click`, `input`, `type`, `press_key`, `hover`, `select`, `wait` (`wait_mode: "interactable"`), or `upload_file`; a handle wins over `selector` when both are set. A handle lives only while its document does: navigation, reload, or a redirect makes every earlier handle fail with `handle "g…-bN" is stale — … (re-run page_fetch what=interactives to re-mint handles)`, and removing or re-rendering the element does the same. A malformed value fails with `invalid handle "x123" (expected g<document>-b<backendNodeId> minted by page_fetch what=interactives|dom|links)`.

```json
{ "tool": "page_act", "arguments": { "session_id": "page-1", "action": "click", "handle": "g3k9x2ab-b6" } }
```

**Observe one step.** Network/console capture is cumulative from page creation. To attribute traffic or logs to a single action: `page_act` `clear_logs` (returns `{"network":N,"console":M}`) → perform the action → `page_fetch` `what: "network"` / `"console"` → filter client-side.

---

## Standard Interaction Patterns

### 1. Discovery and Orientation (Token-Efficient)

Never fetch unscoped raw HTML on modern web pages (many pages exceed 2MB). Use this 3-step sequence:

1. **Open Page** (always pass `wait_for` — omitting it fails; never use `"idle"` — it hangs on JS-heavy/WAF pages):
   ```json
   { "tool": "fetch_page", "arguments": { "url": "https://example.com", "wait_for": "stable", "timeout_ms": 15000 } }
   ```
2. **Read Meta and Links (Parallel)**:
   ```json
   { "tool": "page_fetch", "arguments": { "session_id": "page-1", "what": "meta" } }
   { "tool": "page_fetch", "arguments": { "session_id": "page-1", "what": "links", "max_bytes": 20000 } }
   ```
3. **Read Targeted Text**:
   ```json
   { "tool": "page_fetch", "arguments": { "session_id": "page-1", "what": "text", "selector": "main" } }
   ```
4. **Before acting, read the action map** — cheaper and more reliable than guessing selectors:
   ```json
   { "tool": "page_fetch", "arguments": { "session_id": "page-1", "what": "interactives" } }
   ```
   Then act with the element's text as a selector (`a:has-text("Sign in")` is not supported — use a real CSS selector derived from the map) or with `x`/`y` via `click_at`.

### 2. Form Input, Key Presses, and File Uploads

- **Direct Value Setting**: Use `input` to clear existing content and set value.
  ```json
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "input", "selector": "input[name='q']", "value": "search query" } }
  ```
- **Keystroke & Shortcut Actions (`press_key`)**: Submit forms with `Enter`, dismiss modals with `Escape`, or execute keyboard shortcuts (`Control+a`, `Meta+c`, `Shift+Tab`).
  ```json
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "press_key", "selector": "input[name='q']", "key": "Enter" } }
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "press_key", "key": "Escape" } }
  ```
- **File Upload (`upload_file`)**: Attach local files to `<input type="file">` elements.
  ```json
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "upload_file", "selector": "input[type='file']", "paths": ["/path/to/doc.pdf"] } }
  ```

### 3. Mouse Movement, Coordinates & Context Menus

- **Coordinate Clicking (`click_at`) & Hovering (`hover_at`)**: Drive interactive canvas elements or map widgets by exact `{x, y}` coordinates.
  ```json
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "click_at", "x": 150, "y": 250, "button": "right" } }
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "hover_at", "x": 300, "y": 400 } }
  ```
- **Drag and Drop (`drag_and_drop`)**: Drag elements or mouse pointers from source to target coordinates or selectors.
  ```json
  { "tool": "page_act", "arguments": { "session_id": "page-1", "action": "drag_and_drop", "x": 100, "y": 100, "target_x": 300, "target_y": 300 } }
  ```

### 4. JavaScript Execution (`eval`)

`eval` is the primary mechanism for custom DOM queries, handling complex widgets, extracting structured JSON, and triggering synthetic events.

- **Arrow Function Syntax**: `value` MUST be a valid JavaScript arrow function or function expression: `() => { ... }`.
- **Serializability Rule**: `eval` functions MUST return JSON-serializable primitives (strings, numbers, booleans), plain objects, or arrays. Returning raw DOM Node instances directly (e.g., `() => document.querySelector('button')`) will evaluate to `null` or trigger CDP serialization errors because DOM nodes contain circular C++ bindings. Always extract specific primitive properties (e.g., `() => document.querySelector('button')?.innerText` or `() => ({ title: h.innerText, href: h.closest('a')?.href })`).
- Example: Extract structured table or list items:
  ```json
  {
    "tool": "page_act",
    "arguments": {
      "session_id": "page-1",
      "action": "eval",
      "value": "() => Array.from(document.querySelectorAll('article h2')).map(h => ({ title: h.innerText.trim(), link: h.closest('a')?.href }))"
    }
  }
  ```

### 5. Handling Overlays, Hidden SPA Elements, and Consent Modals

When clicks fail or pages show empty content due to cookie banners, overlays, or collapsed menus:

1. **Collapsing Navigations & Hidden Elements**: When clicking elements hidden inside collapsed sidebars/drawers (with zero bounding box dimensions), `page_act click` will burn the full 30s `WaitInteractable` loop and fail with `context deadline exceeded`. Before clicking hidden targets, inspect element visibility via `eval` (`() => { const el = document.querySelector('sel'); return el && el.offsetWidth > 0; }`) or click the parent menu/hamburger button to expand the container first.
2. **Inspect Consent Buttons**:
   ```json
   {
     "tool": "page_act",
     "arguments": {
       "session_id": "page-1",
       "action": "eval",
       "value": "() => Array.from(document.querySelectorAll('button, [role=\"button\"]')).filter(b => /accept|agree|consent|allow|continue/i.test(b.innerText)).map(b => b.innerText.trim())"
     }
   }
   ```
3. **Dismiss Overlays**:
   ```json
   {
     "tool": "page_act",
     "arguments": {
       "session_id": "page-1",
       "action": "eval",
       "value": "() => { document.querySelectorAll('[aria-modal=\"true\"], .overlay, iframe[src*=\"consent\"]').forEach(el => el.remove()); return 'cleared'; }"
     }
   }
   ```

### 6. Mobile and Responsive Testing

Test mobile rendering or bypass desktop-specific layouts:

```json
{ "tool": "page_act", "arguments": { "session_id": "page-1", "action": "emulate", "device": "IPhoneX" } }
{ "tool": "page_act", "arguments": { "session_id": "page-1", "action": "set_viewport", "viewport": { "width": 375, "height": 812 } } }
{ "tool": "page_act", "arguments": { "session_id": "page-1", "action": "emulate", "device": "clear" } }
```

---

## Error Handling and Troubleshooting

| Error / Symptom | Root Cause | Recovery Strategy |
|---|---|---|
| `context deadline exceeded` on `fetch_page` | Slow assets, heavy scripts, or network throttling. | Retry `fetch_page` with `timeout_ms: 60000` and `wait_for: "load"`. |
| `element "<selector>": context deadline exceeded` | Element not in DOM, hidden behind overlay, or dynamically rendered. | Verify presence with `page_fetch { what: "dom", selector: "..." }` or query via `eval`. |
| `SyntaxError: Unexpected token` in `eval` | `value` was not wrapped in an arrow function `() => ...` or had escaping issues. | Format `value` as `() => { return ...; }`. |
| `Argument should belong to the same JavaScript world` | DOM element re-rendered by SPA framework or resides inside a cross-origin iframe. | Re-query the selector or interact via `eval` in document context. |
| `invalid handle "x123" (expected g<document>-b<backendNodeId> …)` | The `handle` argument is malformed or minted by an older build. | Re-run `page_fetch what=interactives` (or `dom`/`links`) and pass the `handle` verbatim (`g` + document token + `-b` + digits). |
| `handle "g…-bN" is stale — minted for a previous document / element no longer in the document / detached …` | The page navigated, reloaded, redirected, or the element was removed after the handle was minted. | Re-run `page_fetch what=interactives` (or `dom`/`links`) on the current document and use a fresh handle. |
| Navigation redirect stall (e.g. tracking links) | Intermediate redirect URL timed out during wait. | Extract target URL using `links` or `eval`, then `navigate` directly to destination domain. |
| `session "X" no longer exists: the browser process died` | Chromium crash (memory exhaustion or CDP socket close). | Re-open session with `fetch_page`. The manager automatically respawns Chromium. |

---

## Complete Exploration Workflow Example

```
1. browser_create
   -> { browser_id: "browser-1" }

2. fetch_page { url: "https://news.google.com", wait_for: "load" }
   -> { session_id: "page-1", title: "Google News", ... }

3. page_fetch { session_id: "page-1", what: "meta" }
   -> Confirms title and canonical URL

4. page_fetch { session_id: "page-1", what: "links", max_bytes: 25000 }
   -> Extract article destination links

5. page_act { session_id: "page-1", action: "navigate", value: "https://example-news-source.com/article-1" }
   -> Navigates session to destination

6. page_act { session_id: "page-1", action: "wait", wait_mode: "load" }
   -> Waits for document load event

7. page_fetch { session_id: "page-1", what: "text", selector: "article" }
   -> Extracts clean article body

8. page_act { session_id: "page-1", action: "back" }
   -> Returns to Google News

9. session_close { session_id: "page-1" }
   -> Releases tab resources

10. browser_close { browser_id: "browser-1" }
    -> Terminates Chromium process
```
