# web-mcp Enhancement Plan and Roadmap

This document outlines architectural and feature enhancements to make `web-mcp` a comprehensive, production-grade browser automation server for LLM agents.

---

## 1. Element Referencing and Interactive Snapshot (Set-of-Marks)

Currently, an LLM must either inspect large DOM trees or guess CSS selectors to interact with page elements.

- [x] **Interactive Element Indexing (`page_fetch { what: "interactives" }`)** — shipped as **WEB-54** (commit `3ca0ccc`, 2026-08-16):
  - Deliberately diverges from this roadmap bullet on two points: the enum value is `interactives` (shortened from `interactive_elements` for consistency with the terse `what` values), and the "short integer reference IDs (`[@1]`…)" are replaced by the **stable `b<backendNodeId>` handle scheme (WEB-51)** — per-snapshot integers would re-number on every call and aren't actionable, whereas the CDP `backendNodeId` handle is stable across snapshots and feeds `page_act { handle }` directly once WEB-51's resolver lands.
  - Returns a compact, token-efficient dense action map (~2-5KB): every visible, non-inert clickable/typeable element with `handle` (`b<backendNodeId>`), `role` (ARIA role attr, else lowercased tag), `text` (layout/aria-label/title/placeholder fallback, ≤80 chars), `href` (links only, absolute), `type` (input type attr), center `x`/`y` (page-absolute CSS px), and `visible`. Capped at `MaxInteractives` (200) with a `truncated` count so the agent knows more exist.
  - Extraction core lives in `pkg/browsers` (`Page.Interactives`, one `CaptureDOMSnapshot` walk — no extra CDP round trips, and the layout tree gives a more accurate visibility filter than the JS `offsetParent` heuristic). Mirrored 1:1 across MCP `page_fetch` and `web-cli page fetch --what interactives` (identical wire shape by construction); never byte-truncated (its designed incompleteness signal is the `truncated` field).
  - **Known gap vs this bullet:** only the center of the bounding box is emitted, not the full box (no width/height) — sufficient for click-targeting, insufficient for geometry. The layout bounds `[left, top, width, height]` are already in hand, so extending `Interactive` with `width`/`height` is a small future ticket if needed.
- [ ] **Direct Reference Interactions in `page_act`**:
  - Support a `handle` parameter (the `b<backendNodeId>` scheme minted by WEB-54's interactives/dom snapshots) across `click`, `input`, `type`, `hover`, and `select` — **tracked as WEB-51** (todo, high): resolves handles via CDP `ElementFromNode`, with stale-handle error paths after re-render/navigation.
  - Eliminate selector brittleness and DOM parsing errors for the LLM.
- [ ] **Accessibility Tree Extraction (`what: "a11y"`)**:
  - Expose Chrome's native accessibility tree (AXTree) to provide semantic role, name, description, and state (focused, checked, disabled, expanded) — **tracked as WEB-73** (todo, med): compact AXTree with WEB-51 handles, engine in `pkg/browsers`.

---

## 2. Page & Popup Target Lifecycle Management (go-rod `Page` Targets)

> [!NOTE]
> In go-rod and CDP, all browser tabs, popups, and new windows are represented uniformly as `*rod.Page` targets. In `web-mcp`, every page target is a `session_id`. Agents interact with tabs simply by referencing their `session_id` (no visual "tab switching" required).

- [ ] **Automatic New Target / Popup Interception (`TargetCreated`)**:
  - Intercept CDP `Target.targetCreated` events (triggered by `target="_blank"` links or `window.open()`).
  - Automatically register newly spawned `*rod.Page` targets in `browsers.Manager` and assign a `session_id` (accessible via `session_list` or event notice).
- [ ] **Page Lifecycle Helpers in `page_act` / Tools**:
  - `new_page` action (opens a new blank session inside the active browser).
  - `duplicate_page` action (opens a new session initialized to the current session's URL).
- [ ] **Multi-Page Session Grouping**:
  - Support labeling or tagging `session_id`s by domain or task context to streamline multi-tab parallel crawls.

---

## 3. Keyboard, Mouse, and File Interaction Primitives

- [x] **Keyboard Combinations and Key Pressing**:
  - Added `press_key` action to send specific keys (e.g., `Enter`, `Escape`, `Tab`, `Backspace`, `ArrowDown`, `PageDown`) and key combos (e.g., `Control+A`, `Meta+C`, `Shift+Tab`).
  - Vital for submitting forms without explicit submit buttons and dismissing popups with `Escape`. Implemented in shared `pkg/cli/pageops` for 100% MCP and CLI parity.
- [x] **Coordinate-Based Mouse Actions**:
  - Added `click_at` and `hover_at` with explicit `{ x, y }` coordinates, optional mouse `button` (left, right, middle) and `click_count` (single, double click).
  - Added `drag_and_drop` with source and target coordinates/selectors. Implemented in shared `pkg/cli/pageops`.
- [x] **File Upload Support**:
  - Added `upload_file` action accepting element selector and local file path(s) to set input files on `<input type="file">`. Implemented in shared `pkg/cli/pageops`.
- [ ] **Dropdown and Date Picker Support**:
  - Add native support for multi-selects, custom ARIA comboboxes, and calendar/date input elements.

---

## 4. Console Logs, Network Inspection, and Resource Blocking

- [ ] **Console Log Capture (`page_fetch { what: "console" }`)**:
  - Buffer `console.log`, `console.warn`, `console.error`, and uncaught runtime exceptions per session.
  - Expose logs to help LLMs debug web applications and detect client-side errors.
- [ ] **Network Traffic Monitoring (`page_fetch { what: "network" }`)**:
  - Capture HTTP requests, status codes (200, 404, 500), response headers, and payload summaries.
  - Provide network failure detection (e.g., failed API calls, CORS blocks).
- [ ] **Request Blocking and Resource Filtering**:
  - Allow blocking images, media, fonts, tracker scripts, and ads (e.g., `fetch_page { block_media: true, block_trackers: true }`).
  - Accelerates scraping speeds and cuts bandwidth by up to 80%.

---

## 5. Storage, Cookies, and Authentication Persistence

- [ ] **Cookie Management**:
  - Add `cookies_get`, `cookies_set`, and `cookies_clear` actions.
  - Support importing and exporting session cookies for authenticated workflows.
- [ ] **Web Storage API Access**:
  - Add primitives for reading/writing `localStorage` and `sessionStorage`.
- [ ] **Persistent User Data Directory (Profiles)**:
  - Add configuration option in `browser_create` to attach a persistent disk directory (`user_data_dir`) so logins and browser state persist across agent restarts.

---

## 6. Smart Content Extraction (Readability and Markdown)

- [ ] **Article / Markdown Extraction (`page_fetch { what: "markdown" }`)**:
  - Integrate a readability algorithm (e.g., Mozilla Readability or defanged HTML-to-Markdown parser) to extract clean article markdown directly in 1-3 KB.
  - Eliminates noise (headers, sidebars, cookie banners, ads) for pure content summarization tasks.
- [ ] **Table Extraction (`what: "tables"`)**:
  - Extract HTML tables directly into clean CSV or Markdown table formats.
- [ ] **Form Map Extraction (`what: "forms"`)**:
  - Enumerate all inputs, labels, validation rules, and action endpoints of forms on the page.

---

## 7. Anti-Bot Stealth and Fingerprinting Defense

- [ ] **Integrated Stealth Plugin Integration**:
  - Integrate `go-rod/stealth` out of the box to mask `navigator.webdriver`, mock WebGL vendor strings, and randomize audio/canvas fingerprints.
- [ ] **HTTP/SOCKS5 Proxy Support**:
  - Allow passing `proxy_url` and `proxy_auth` in `browser_create` or `fetch_page`.
- [ ] **Automated CAPTCHA / WAF Detection**:
  - Automatically detect Cloudflare, DataDome, and reCAPTCHA challenge pages and return structured error codes to the agent.

---

## 8. Automated Interstitial and Consent Modal Dismissal

- [ ] **Built-in CMP / Cookie Consent Auto-Dismissal**:
  - Implement automated dismiss heuristics on page load for major consent frameworks (OneTrust, Cookiebot, Sourcepoint, Google Consent).
  - Configurable via `fetch_page { auto_dismiss_consent: true }`.

---

## 9. Visual Annotation and Multimodal Set-of-Marks

- [ ] **Annotated Screenshot Generation (`what: "screenshot_annotated"`)**:
  - Render numbered bounding box overlays on top of interactive elements directly in the captured screenshot image.
  - Allows multimodal vision models to visually select and confirm elements.

---

## 10. Robustness, Timeouts, and Error Handling

- [ ] **Explicit `timeout_ms` across All Tools**:
  - Add `timeout_ms` to `page_fetch` schema (currently hardcoded to default timeouts).
- [ ] **Automatic Retries on Stale Element References**:
  - When a DOM re-render occurs (e.g., React/Vue component swap), automatically retry element resolution up to a configurable deadline.
- [ ] **Session Idle Sweep and Memory Limits**:
  - Provide configurable session TTL and memory consumption safeguards to prevent zombie Chromium processes.
