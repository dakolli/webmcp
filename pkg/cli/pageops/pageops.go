// Package pageops holds the cobra-free page helpers shared by the web-cli
// commands (pkg/cli/cmd) and the IPC server (pkg/cli/ipc): fetching page
// summaries, extracting page aspects (html, dom, text, css, js, links,
// screenshot, meta, interactives), performing page actions (click, input,
// hover, scroll, select, eval, navigate, back, forward, reload, wait) and
// truncating output.
// The helpers own the per-page Lock/Unlock discipline required because rod
// Pages are not thread-safe (pkg/browsers session.go) — callers must not lock
// themselves. Only this package drives rod on the CLI side; cmd/ipc never
// import rod-family packages (AGENTS.md CLI boundary).
package pageops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// DefaultPageMaxBytes caps how much of any page fetch extraction is returned
// in one call (the CLI --max-bytes default and the IPC page.fetch default).
// Huge pages are dehydrated to a first slice; the caller can re-run with a
// narrower selector or a higher max_bytes. Screenshots and interactives are
// the documented exceptions: the data-URI (screenshot) and the
// machine-parseable JSON envelope with its own "truncated" field
// (interactives) bypass PageTruncate entirely.
const DefaultPageMaxBytes = 50_000

// defaultActTimeout bounds the page_act-style helpers' "ms" wait-mode sleep
// when the caller passes no explicit timeout (mirroring defaultActTimeout in
// pkg/mcp/tools/act.go). Callers bound the ctx themselves (defaultCmdTimeout /
// the IPC per-call timeout); this fallback only sizes the sleep.
const defaultActTimeout = 30 * time.Second

// defaultPageOpTimeout is the deadline every page operation gets when its
// caller passes a context with none. No operation may run unbounded: a wedged
// CDP call would hold the page lock and block the daemon.
const defaultPageOpTimeout = 30 * time.Second

// inputAckTimeout bounds one mouse or keyboard acknowledgement. Input events
// are acknowledged in milliseconds; a longer wait means the page navigated (or
// is wedged), and the click already landed, so the caller must not wait for the
// whole call budget.
const inputAckTimeout = 2 * time.Second

// dispatchActions lists the action names the CLI (pkg/cli/cmd) and IPC
// (pkg/cli/ipc) dispatch paths handle, including auth_list and encode, which
// both surfaces resolve before the main switch because they need no live page.
// The action table is the source of truth; actions_test.go asserts this mirror
// stays in lockstep so an action cannot exist on only one side.
var dispatchActions = []string{
	"click", "input", "type", "press_key", "click_at", "hover_at",
	"drag_and_drop", "upload_file", "eval", "navigate",
	"back", "forward", "reload", "hover", "set_style", "set_attr", "set_html", "scroll", "select",
	"fill_form", "wait", "set_viewport", "emulate", "clear_logs",
	"sinks_install", "sinks_clear", "replay", "inject_canary",
	"postmessage_install", "postmessage_clear", "post_message",
	"wait_open", "intercept_add", "intercept_list", "intercept_clear",
	"auth_save", "auth_restore", "auth_list", "replay_fuzz", "poc_bundle",
	"dialog_accept", "dialog_dismiss", "http_auth", "encode",
}

// PageHTTPAuth arms per-page HTTP basic-auth credentials from a user:password
// value split at the first colon, or clears them when value is empty.
func PageHTTPAuth(ctx context.Context, p *browsers.Page, value string) (string, error) {
	if value == "" {
		return PageHTTPAuthClear(ctx, p)
	}
	user, pass, _ := strings.Cut(value, ":")
	return PageHTTPAuthSet(ctx, p, user, pass)
}

// resolveSelector resolves one CSS selector without rod's unbounded retry: a
// selector that matches nothing returns the typed *rod.ElementNotFoundError
// immediately instead of burning the whole call budget and surfacing as a
// timeout. A selector that matches resolves as before, and a malformed selector
// returns its eval error unchanged. Callers must already hold the page lock;
// this never locks.
func resolveSelector(page *rod.Page, selector string) (*rod.Element, error) {
	return page.Sleeper(rod.NotFoundSleeper).Element(selector)
}

// resolveError maps a selector-resolution failure to the shared not-found
// sentence when nothing matched, so the result envelope classifies it as
// element_not_found. Any other failure (malformed selector, eval error) passes
// through unchanged and keeps its own type.
func resolveError(selector string, err error) error {
	var missing *rod.ElementNotFoundError
	if errors.As(err, &missing) {
		return fmt.Errorf("no element matched selector %q: %w", selector, err)
	}
	return err
}

// beginPage gives ctx a deadline when it has none, then takes the page
// operation lock through the engine's guarded read hold. The returned release
// func cancels the derived context and releases the lock; callers defer it
// immediately. The read hold waits at most the engine's bounded read budget for
// the lock, so a page a wedged operation still holds returns the unusable error
// at once instead of burning the call budget; a healthy page acquires it
// immediately. A page already marked unusable by the watchdog returns its typed
// error without touching the lock, and a CDP call that outlives a cancelled
// context is force-released by the page watchdog instead of keeping the page
// locked.
func beginPage(
	ctx context.Context,
	p *browsers.Page,
	op string,
) (context.Context, func(), error) {
	opCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		var stop context.CancelFunc
		opCtx, stop = context.WithTimeout(ctx, defaultPageOpTimeout)
		cancel = stop
	}
	release, err := p.BeginRead(opCtx, op)
	if err != nil {
		cancel()
		return opCtx, nil, asPageUnusable(err)
	}
	return opCtx, func() {
		release()
		cancel()
	}, nil
}

// pageUnusableError is the read-path failure for a page its watchdog marked
// unusable. It keeps the engine error's prose and Hint but deliberately does not
// unwrap to context.DeadlineExceeded: a wedged page is not a slow page, so a
// read must not advise a higher timeout_ms. Surfaces still resolve the recovery
// through Hint.
type pageUnusableError struct {
	cause error
}

// Error reports the engine's page-unusable sentence.
func (e *pageUnusableError) Error() string { return e.cause.Error() }

// Hint names the only recovery for a wedged page.
func (e *pageUnusableError) Hint() string {
	var hinted interface{ Hint() string }
	if errors.As(e.cause, &hinted) {
		return hinted.Hint()
	}
	return ""
}

// asPageUnusable labels a watchdog, locked or closed page error as a read
// failure. Any other error, including a nil error, passes through unchanged.
func asPageUnusable(err error) error {
	var wedge *browsers.PageWatchdogError
	var locked *browsers.PageLockedError
	var broken *browsers.PageBrokenError
	var unresponsive *browsers.PageUnresponsiveError
	if errors.As(err, &wedge) || errors.As(err, &locked) ||
		errors.As(err, &broken) || errors.As(err, &unresponsive) {
		return &pageUnusableError{cause: err}
	}
	return err
}

// guardRead fails a read before any CDP call when the page watchdog marked the
// page unusable. It never waits on the operation lock, so every page_fetch
// aspect rejects a wedged page in microseconds instead of burning its budget.
func guardRead(p *browsers.Page) error {
	return asPageUnusable(p.Unusable())
}

// opDeadlineError turns a cancelled or expired operation into one actionable
// sentence naming the operation and the elapsed time. Other errors pass
// through unchanged.
func opDeadlineError(
	op string,
	p *browsers.Page,
	started time.Time,
	err error,
) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf(
		"%s: page %s did not finish within %s — re-read the page with page_fetch what=meta before retrying: %w",
		op,
		p.ID,
		time.Since(started).Round(time.Millisecond),
		err,
	)
}

// ValidateURL checks that s is an absolute http(s) URL the browser can
// navigate to: it must parse, be absolute and carry a host.
func ValidateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", s, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("invalid url %q: must be an absolute http(s) URL", s)
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("invalid url %q: scheme %q not supported (must be http or https)", s, u.Scheme)
	}
}

// ValidateWait checks that the wait strategy is one of the shared readiness
// vocabulary: idle, load, stable (used by fetch and page.open) plus the
// page_act-only interactable and ms modes (used by the wait action's --wait).
func ValidateWait(waitFor string) error {
	switch waitFor {
	case "idle", "load", "stable", "interactable", "ms":
		return nil
	default:
		return fmt.Errorf("invalid --wait %q (must be one of idle, load, stable, interactable, ms)", waitFor)
	}
}

// ParseViewport parses a "WxH" viewport string (e.g. "1440x900") into a
// browsers.Viewport. An empty or absent flag yields nil — a plain launch with
// rod's own defaults, NO override (the explicit-override-only doctrine: pages
// launch plain by default; device presets via Page.Emulate are the primary
// opt-in mechanism). Any malformed or non-positive value fails with the exact
// pinned error so the CLI and IPC layers share one parse surface (mirrors the
// ValidateURL/ValidateWait pattern).
func ParseViewport(s string) (*browsers.Viewport, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid viewport %q (must be WxH, e.g. 1440x900)", s)
	}
	return &browsers.Viewport{Width: w, Height: h}, nil
}

// WaitForPage blocks until the page satisfies the requested readiness level.
// It maps "load" to WaitLoad, "idle" to WaitRequestIdle, and "stable" to
// WaitDOMStable. All rod page calls are serialized with the per-page lock. The
// page_act-only "interactable" and "ms" modes are handled by PageWait, not
// here (they need a selector and a timeout respectively).
func WaitForPage(ctx context.Context, p *browsers.Page, waitFor string) error {
	ctx, done, err := beginPage(ctx, p, "wait "+waitFor)
	if err != nil {
		return err
	}
	defer done()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}

	pc := p.Rod.Context(ctx) // bound to ctx so cancellation aborts a stuck wait
	switch waitFor {
	case "idle":
		// WaitRequestIdle RETURNS a blocking func() — call it, do not return it.
		wait := pc.WaitRequestIdle(500*time.Millisecond, nil, nil, nil)
		wait()
		return nil
	case "load":
		return pc.WaitLoad()
	case "stable":
		return pc.WaitDOMStable(500*time.Millisecond, 0.05)
	default:
		return fmt.Errorf("invalid wait option %q", waitFor)
	}
}

// FetchSummary is the output shape of opening a page, identical between the
// CLI fetch command's human-readable/--json forms and the IPC page.open
// result. It carries the session_id (usable with the page command / page.*
// IPC methods), page metadata and compact size/timing hints — never the full
// HTML (first-50KB rule).
type FetchSummary struct {
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	TextSnippet string `json:"text_snippet"`
	HTMLBytes   int    `json:"html_bytes"`
	LoadMs      int64  `json:"load_ms"`
}

// PageSummary renders a compact summary of a live page: the page ID, its title
// and URL (via the ctx-bound State seam), plus a short text snippet and
// byte-size hints.
// loadStarted anchors the load_ms timing (captured around open → navigate →
// wait).
func PageSummary(ctx context.Context, p *browsers.Page, loadStarted time.Time) (FetchSummary, error) {
	ctx, done, err := beginPage(ctx, p, "page summary")
	if err != nil {
		return FetchSummary{}, err
	}
	defer done()
	if p.Rod == nil {
		return FetchSummary{}, fmt.Errorf("page session is closed")
	}

	url, title, err := p.State(ctx)
	if err != nil {
		return FetchSummary{}, fmt.Errorf("page info: %w", err)
	}

	html, err := p.Rod.Context(ctx).HTML() // byte-size hint only; not printed in full
	if err != nil {
		return FetchSummary{}, fmt.Errorf("read html: %w", err)
	}

	text, err := p.Rod.Context(ctx).Eval(`() => document.body.innerText.slice(0, 500)`)
	if err != nil {
		return FetchSummary{}, fmt.Errorf("read text: %w", err)
	}

	return FetchSummary{
		SessionID:   string(p.ID),
		Title:       title,
		URL:         url,
		TextSnippet: text.Value.Str(),
		HTMLBytes:   len(html),
		LoadMs:      time.Since(loadStarted).Milliseconds(),
	}, nil
}

// PageHTML returns the page's outerHTML, or of the element matched by selector
// when one is given. Backed by p.Rod.HTML() / element HTML() — mirrors the MCP
// extractHTML.
func PageHTML(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	ctx, done, err := beginPage(ctx, p, "html")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	if selector == "" {
		html, err := p.Rod.Context(ctx).HTML()
		if err != nil {
			return "", fmt.Errorf("read page html: %w", err)
		}
		return html, nil
	}

	el, err := resolveSelector(p.Rod.Context(ctx), selector)
	if err != nil {
		return "", resolveError(selector, err)
	}
	html, err := el.HTML()
	if err != nil {
		return "", fmt.Errorf("read html of element %q: %w", selector, err)
	}
	return html, nil
}

// PageText returns the visible text of the page (or of the element matched by
// selector). Backed by Eval over document.body.innerText / element.innerText —
// mirrors the MCP extractText.
func PageText(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	ctx, done, err := beginPage(ctx, p, "text")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	js := `() => document.body ? document.body.innerText : ''`
	if selector != "" {
		// JSON-quote the selector so it is safe to embed in the JS source
		// (quotes, backslashes and control characters are escaped).
		q, err := json.Marshal(selector)
		if err != nil {
			return "", fmt.Errorf("encode selector %q: %w", selector, err)
		}
		js = `() => { const el = document.querySelector(` + string(q) + `); return el ? el.innerText : null }`
	}

	res, err := p.Rod.Context(ctx).Eval(js)
	if err != nil {
		return "", fmt.Errorf("evaluate innerText: %w", err)
	}
	// The selector-scoped Eval resolves to null when nothing matches; the
	// whole-document Eval always yields a string. Nil() must be checked before
	// Str(), because gson Str() renders a null as "<nil>".
	if res.Value.Nil() {
		return "", resolveError(selector, &rod.ElementNotFoundError{})
	}
	return res.Value.Str(), nil
}

// PageLinks returns the page's anchor links as a JSON array of {text, href,
// handle}, deduplicated by href (first occurrence wins). Non-navigable hrefs
// (javascript:/mailto:/data:/tel:, empty, exactly "#") and text-less anchors
// are dropped; remaining hrefs are entity-decoded — mirrors the MCP
// extractLinks shape and behavior. Handles come from the same document
// snapshot as the DOM extractor, so they carry that document's token and are
// rejected after a navigation.
func PageLinks(ctx context.Context, p *browsers.Page) (string, error) {
	links, err := pageAnchorLinks(ctx, p)
	if err != nil {
		return "", err
	}

	snap, token, err := p.CaptureSnapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("capture DOM snapshot for link handles: %w", err)
	}
	handles := linkHandles(snap, token)
	for i := range links {
		links[i].Handle = handles[links[i].Href]
	}

	// Encode without HTML-safe escaping so "&" renders literally on the links
	// surface (json.Marshal would emit \u0026); every other extractor keeps
	// the default escaping.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(links); err != nil {
		return "", fmt.Errorf("encode links: %w", err)
	}
	// json.Encoder appends a trailing newline that json.Marshal did not —
	// trim it so the output shape is unchanged.
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// pageAnchorLinks reads the page's anchors under the page lock and returns
// them filtered, entity-decoded and deduplicated. Text comes from textContent
// (rendering-independent: captures anchors in CSS-hidden dropdown panels that
// innerText reports as ""), normalized to single spaces, with an
// accessible-name fallback chain (aria-label → title → img[alt]) for anchors
// with no text node. Anchors with no text and no fallback are dropped right
// here (no "text":"" on the wire, ever).
func pageAnchorLinks(ctx context.Context, p *browsers.Page) ([]PageLink, error) {
	ctx, done, err := beginPage(ctx, p, "links")
	if err != nil {
		return nil, err
	}
	defer done()
	if p.Rod == nil {
		return nil, fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(
		`() => [...document.querySelectorAll('a[href]')].map(a => {
  const text = (a.textContent || '').replace(/\s+/g, ' ').trim()
    || (a.getAttribute('aria-label') || '').trim()
    || (a.title || '').trim()
    || ((a.querySelector('img') || {}).alt || '').trim();
  return { text, href: a.href };
}).filter(l => l.text !== '')`)
	if err != nil {
		return nil, fmt.Errorf("evaluate links: %w", err)
	}

	var links []PageLink
	if err := res.Value.Unmarshal(&links); err != nil {
		return nil, fmt.Errorf("decode links: %w", err)
	}

	// Filter, decode and dedupe in one pass — dedupe AFTER filtering so junk
	// schemes never occupy the seen map. Drop non-navigable hrefs and any
	// text-less leftover (the Eval already skips those; this guard keeps the
	// no-empty-text invariant at the output boundary), unescape entity
	// references a page may have set literally from JS ("&amp;" — harmless on
	// already-decoded hrefs), then keep the first occurrence per href.
	seen := make(map[string]bool, len(links))
	dedup := make([]PageLink, 0, len(links))
	for _, l := range links {
		l.Href = html.UnescapeString(l.Href)
		if l.Text == "" || !navigableHref(l.Href) || seen[l.Href] {
			continue
		}
		seen[l.Href] = true
		dedup = append(dedup, l)
	}
	return dedup, nil
}

// navigableHref reports whether an href is one the browser can navigate to:
// non-empty, not exactly "#", and not a non-navigable pseudo-scheme
// (javascript:, mailto:, data:, tel: — compared case-insensitively). Relative
// URLs (already resolved absolute by a.href) and same-page fragment links like
// "#skip" pass through.
func navigableHref(href string) bool {
	if href == "" || href == "#" {
		return false
	}
	switch strings.ToLower(strings.SplitN(href, ":", 2)[0]) {
	case "javascript", "mailto", "data", "tel":
		return false
	}
	return true
}

// linkHandles maps each absolute anchor href in the snapshot to the handle of
// the first `a[href]` element that carries it, in document order. docToken is
// the snapshot document's token, stamped onto every minted handle.
func linkHandles(snap *proto.DOMSnapshotCaptureSnapshotResult, docToken string) map[string]string {
	handles := make(map[string]string)
	var walk func(base string, n *domNode)
	walk = func(base string, n *domNode) {
		if n == nil {
			return
		}
		if n.Tag == "a" && n.Handle != "" {
			if href := n.Attrs["href"]; href != "" {
				if key := absoluteHref(base, href); handles[key] == "" {
					handles[key] = n.Handle
				}
			}
		}
		for _, child := range n.Children {
			walk(base, child)
		}
	}
	for _, doc := range buildDOMDocuments(snap, docToken) {
		walk(doc.URL, doc.Root)
	}
	return handles
}

// absoluteHref resolves an anchor's raw href attribute against its document
// URL and entity-decodes it, producing the same absolute string `a.href`
// yields so snapshot handles can be matched to extracted links.
func absoluteHref(base, href string) string {
	href = html.UnescapeString(href)
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	if base == "" {
		return ref.String()
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref.String()
	}
	return b.ResolveReference(ref).String()
}

// PageMeta returns the page's meta info (title, url, meta tags) as a JSON
// object — mirrors the MCP extractMeta shape. Backed by p.State plus Eval
// collecting every <meta> tag.
func PageMeta(ctx context.Context, p *browsers.Page) (string, error) {
	ctx, done, err := beginPage(ctx, p, "meta")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	url, title, err := p.State(ctx)
	if err != nil {
		return "", fmt.Errorf("read page info: %w", err)
	}

	// Each meta tag is flattened to its meaningful attributes; absent ones
	// (getAttribute returned null) are dropped Go-side via omitempty.
	res, err := p.Rod.Context(ctx).Eval(
		`() => [...document.querySelectorAll('meta')].map(m => ({name: m.getAttribute('name'), ` +
			`property: m.getAttribute('property'), 'http-equiv': m.getAttribute('http-equiv'), ` +
			`content: m.getAttribute('content'), charset: m.getAttribute('charset')}))`)
	if err != nil {
		return "", fmt.Errorf("evaluate meta tags: %w", err)
	}

	var tags []PageMetaTag
	if err := res.Value.Unmarshal(&tags); err != nil {
		return "", fmt.Errorf("decode meta tags: %w", err)
	}

	out := PageMetaResult{Title: title, URL: url, Meta: tags}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode page meta: %w", err)
	}
	return string(b), nil
}

// PageInteractives returns the page's interactive elements as a dense JSON
// action map: every visible, non-inert clickable/typeable element with a
// stable handle, role, text, href, input type, page-absolute center/box geometry
// and the split root/frame budgets — the snapshot used to act immediately
// (page act { handle: ... }) without selector guesswork. selector, when set,
// scopes the read to that element's subtree (an iframe selector returns that
// frame's entries). Mirrors the MCP page_fetch what=interactives shape exactly.
//
// Deliberately does NOT lock: the engine method Page.Interactives takes the
// page lock itself and owns the nil-Rod guard (mirroring PageSetViewport/
// PageEmulate's engine-seam pattern) — an exception to the pageops
// self-locking convention, so callers must not pre-lock either.
func PageInteractives(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	res, err := p.Interactives(ctx, selector)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode interactives: %w", err)
	}
	return string(b), nil
}

// PageA11y returns the page's flattened accessibility tree as JSON:
// {"nodes":[{handle?,depth,role,name?,value?,states?}],"count":N,"truncated":M}
// — the computed semantics (roles, accessible names, values, state flags) an
// agent uses to know what a control IS, not just where it is. Handles are
// g<document>-b<backendNodeId> and usable as page act's handle arg.
//
// Deliberately does NOT lock: the engine method Page.A11yNodes takes the page
// lock itself and owns the nil-Rod guard — the same engine-seam exception as
// PageInteractives, so callers must not pre-lock either.
func PageA11y(ctx context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	res, err := p.A11yNodes(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode a11y tree: %w", err)
	}
	return string(b), nil
}

// MaxLogEmit caps how many of the newest buffered network/console entries one
// PageNetwork or PageConsole response emits; the envelope's truncated field
// reports the older buffered entries left out.
const MaxLogEmit = 100

// logsEnvelope is the JSON shape shared by the network, console, WebSocket,
// dialog and mutation readers: the emitted entries, their count, the engine
// ring's true overflow loss, and the buffered older entries omitted by the
// MaxLogEmit cap.
type logsEnvelope[T any] struct {
	// Entries are the emitted entries, oldest first.
	Entries []T `json:"entries"`
	// Count is len(Entries).
	Count int `json:"count"`
	// Dropped counts entries lost to the engine ring bound.
	Dropped int `json:"dropped"`
	// Truncated counts buffered entries omitted by the emit cap.
	Truncated int `json:"truncated"`
	// FilteredOut counts the entries a filter removed, and is absent when no
	// filter ran so unfiltered output keeps its exact shape.
	FilteredOut *int `json:"filtered_out,omitempty"`
	// LastSeq is the newest sequence on the page, and is present only for the
	// mutations envelope so a caller can bracket an action with two reads.
	LastSeq *int64 `json:"last_seq,omitempty"`
	// Policy is the answer policy now in effect, and is present only on the
	// dialogs envelope.
	Policy *string `json:"policy,omitempty"`
}

// PageNetwork returns the newest buffered network entries, capped at MaxLogEmit
// and ordered oldest first, as {"entries","count","dropped","truncated"}.
// Deliberately does NOT lock: the engine read touches only the observer's own
// mutex, so a page the watchdog has not marked unusable still answers from its
// in-memory buffer. A wedged page is rejected before the ring is read.
func PageNetwork(_ context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	entries, dropped := p.NetworkEntries()
	return encodeLogs(entries, dropped, "network")
}

// PageNetworkFiltered returns PageNetwork's envelope narrowed to the request
// types named by filter: a comma list where a bare name keeps that type and a
// "-" prefix drops it. An empty filter is PageNetwork unchanged; a filtered read
// reports how many entries the filter dropped in filtered_out.
func PageNetworkFiltered(ctx context.Context, p *browsers.Page, filter string) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	f, err := parseNetworkFilter(filter)
	if err != nil {
		return "", err
	}
	if f == nil {
		return PageNetwork(ctx, p)
	}
	entries, dropped := p.NetworkEntries()
	kept, filteredOut := f.apply(entries)
	return encodeFilteredLogs(kept, dropped, filteredOut, "network")
}

// sinksEnvelope is the what=sinks wire shape: the engine result plus
// filtered_out, which is present only when a filter ran.
type sinksEnvelope struct {
	browsers.SinksResult
	// FilteredOut counts the hits a filter removed.
	FilteredOut *int `json:"filtered_out,omitempty"`
}

// PageSinksFiltered returns PageSinks' envelope narrowed by filter: canary keeps
// only hits whose value holds the injected wm-canary-… marker and known drops
// hits whose source is "unknown". An empty filter is PageSinks unchanged.
func PageSinksFiltered(ctx context.Context, p *browsers.Page, filter string) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	f, err := parseSinksFilter(filter)
	if err != nil {
		return "", err
	}
	if f == "" {
		return PageSinks(ctx, p)
	}
	res, err := p.SinkHits(ctx)
	if err != nil {
		return "", err
	}
	kept, filteredOut := f.apply(res.Hits)
	res.Hits = kept
	b, err := json.Marshal(sinksEnvelope{SinksResult: res, FilteredOut: &filteredOut})
	if err != nil {
		return "", fmt.Errorf("encode sink hits: %w", err)
	}
	return string(b), nil
}

// PageConsole returns the newest buffered console entries, capped at MaxLogEmit
// and ordered oldest first, as {"entries","count","dropped","truncated"}.
// Deliberately does NOT lock: the engine read touches only the observer's own
// mutex, so a page the watchdog has not marked unusable still answers from its
// in-memory buffer. A wedged page is rejected before the ring is read.
func PageConsole(_ context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	entries, dropped := p.ConsoleEntries()
	return encodeLogs(entries, dropped, "console")
}

// PageBody returns the on-demand response body of one observed network entry as
// a self-capped JSON object ({seq,url,content_type,bytes,truncated,body|base64}).
// target is either a sequence number or a substring of the request URL, matched
// against the newest entry. The body itself is fetched from the browser on
// demand, so no bodies are buffered.
func PageBody(ctx context.Context, p *browsers.Page, target string) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("body requires target=<seq> or target=<url substring>")
	}
	entries, _ := p.NetworkEntries()
	var hit *browsers.NetworkEntry
	if seq, err := strconv.ParseInt(target, 10, 64); err == nil {
		for i := range entries {
			if entries[i].Seq == seq {
				hit = &entries[i]
				break
			}
		}
	} else {
		for i := len(entries) - 1; i >= 0; i-- {
			if strings.Contains(entries[i].URL, target) {
				hit = &entries[i]
				break
			}
		}
	}
	if hit == nil {
		return "", fmt.Errorf(
			"body: no network entry matches target %q (read what=network first; target is a seq number or a substring of the request URL, newest match wins)",
			target,
		)
	}
	body, err := p.NetworkBody(ctx, hit.Seq)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode network body: %w", err)
	}
	return string(b), nil
}

// AspectQuery selects one page aspect and the optional inputs it reads. Each
// reader uses only the fields its aspect documents.
type AspectQuery struct {
	// What is the aspect name.
	What string
	// Selector optionally scopes html/text/interactives extraction to a CSS
	// match; on interactives it scopes to that element's subtree, or to a child
	// frame when the match is an iframe.
	Selector string
	// Target selects one css/js resource, or with body one observed response.
	Target string
	// FullPage captures the whole scrollable page (screenshot only).
	FullPage bool
	// IncludeValues exposes cookie and storage values (storage only).
	IncludeValues bool
	// Filter narrows network reads by request type and sinks reads by canary or
	// known source; other aspects ignore it.
	Filter string
	// Since keeps only mutations entries newer than that sequence.
	Since int64
	// MaxBytes caps the output unless the aspect self-caps.
	MaxBytes int
}

// aspectEntry is one page aspect: its what name, its reader and whether the
// reader already caps its own output (bypassing max_bytes truncation).
type aspectEntry struct {
	name       string
	selfCapped bool
	read       func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error)
}

// aspectEntries is the single registry of page aspects, in the order error
// messages list them. The CLI and the daemon resolve every what through it, so a
// new aspect is one entry here instead of another hardcoded allow-list.
var aspectEntries = []aspectEntry{
	{name: "html", read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageHTML(ctx, p, q.Selector)
	}},
	{name: "text", read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageText(ctx, p, q.Selector)
	}},
	{name: "links", read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageLinks(ctx, p)
	}},
	{name: "meta", read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageMeta(ctx, p)
	}},
	{name: "dom", read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageDOM(ctx, p)
	}},
	{name: "css", read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageCSS(ctx, p, q.Target)
	}},
	{name: "js", read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageJS(ctx, p, q.Target)
	}},
	{name: "screenshot", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageScreenshot(ctx, p, q.FullPage)
	}},
	{name: "interactives", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageInteractives(ctx, p, q.Selector)
	}},
	{name: "a11y", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageA11y(ctx, p)
	}},
	{name: "network", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageNetworkFiltered(ctx, p, q.Filter)
	}},
	{name: "console", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageConsole(ctx, p)
	}},
	{name: "body", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageBody(ctx, p, q.Target)
	}},
	{name: "article", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageArticle(ctx, p)
	}},
	{name: "tables", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageTables(ctx, p)
	}},
	{name: "forms", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageForms(ctx, p)
	}},
	{name: "marks", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageMarks(ctx, p)
	}},
	{name: "security", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageSecurity(ctx, p)
	}},
	{name: "storage", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageStorage(ctx, p, q.IncludeValues)
	}},
	{name: "sinks", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageSinksFiltered(ctx, p, q.Filter)
	}},
	{name: "postmessage", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PagePostMessage(ctx, p, q.Target)
	}},
	{name: "har", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageHAR(ctx, p, q.MaxBytes)
	}},
	{name: "websocket", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageWebSocket(ctx, p)
	}},
	{name: "dialogs", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
		return PageDialogs(ctx, p)
	}},
	{name: "mutations", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageMutations(ctx, p, q.Since, q.Selector)
	}},
	{name: "reflections", selfCapped: true, read: func(ctx context.Context, p *browsers.Page, q AspectQuery) (string, error) {
		return PageReflections(ctx, p, q.Target, q.Selector)
	}},
}

// unknownAspectError is the one unknown-what error every surface emits; it names
// every supported value from the shared registry.
func unknownAspectError(what string) error {
	names := make([]string, len(aspectEntries))
	for i, a := range aspectEntries {
		names[i] = a.name
	}
	return fmt.Errorf("unknown what=%q (must be one of %s)", what, strings.Join(names, ", "))
}

// ValidateAspect reports whether what names a supported page aspect. Surfaces
// call it before touching a page so an unknown value fails fast with the shared
// error text.
func ValidateAspect(what string) error {
	for _, a := range aspectEntries {
		if a.name == what {
			return nil
		}
	}
	return unknownAspectError(what)
}

// PageAspect reads the aspect named q.What through the shared registry,
// truncating the output to q.MaxBytes on a rune boundary unless the reader
// self-caps. An unknown what returns the shared error naming every supported
// value.
func PageAspect(
	ctx context.Context,
	p *browsers.Page,
	q AspectQuery,
) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultPageOpTimeout)
		defer cancel()
	}
	for _, a := range aspectEntries {
		if a.name != q.What {
			continue
		}
		out, err := a.read(ctx, p, q)
		if err != nil || a.selfCapped {
			return out, err
		}
		return PageTruncate(out, q.MaxBytes), nil
	}
	return "", unknownAspectError(q.What)
}

// encodeLogs keeps the newest MaxLogEmit entries in chronological order and
// marshals the shared log envelope; what names the log kind in an encode error.
func encodeLogs[T any](entries []T, dropped int, what string) (string, error) {
	return encodeLogEnvelope(entries, dropped, nil, nil, what)
}

// encodeFilteredLogs is encodeLogs for a filtered read: filteredOut always
// appears, even when it is zero, so a caller can tell a filter ran.
func encodeFilteredLogs[T any](entries []T, dropped, filteredOut int, what string) (string, error) {
	return encodeLogEnvelope(entries, dropped, &filteredOut, nil, what)
}

// encodeLogEnvelope trims to the emit window and marshals one log envelope.
// filteredOut is nil for a read that ran no filter; lastSeq is nil for every
// log but mutations.
func encodeLogEnvelope[T any](
	entries []T,
	dropped int,
	filteredOut *int,
	lastSeq *int64,
	what string,
) (string, error) {
	entries, truncated := trimLogs(entries)
	b, err := json.Marshal(logsEnvelope[T]{
		Entries:     entries,
		Count:       len(entries),
		Dropped:     dropped,
		Truncated:   truncated,
		FilteredOut: filteredOut,
		LastSeq:     lastSeq,
	})
	if err != nil {
		return "", fmt.Errorf("encode %s log: %w", what, err)
	}
	return string(b), nil
}

// trimLogs keeps the newest MaxLogEmit entries and reports how many older
// buffered entries the emit cap left out.
func trimLogs[T any](entries []T) ([]T, int) {
	if len(entries) <= MaxLogEmit {
		return entries, 0
	}
	return entries[len(entries)-MaxLogEmit:], len(entries) - MaxLogEmit
}

// PageState is the page address bar as reported by CDP: the current URL and
// title.
type PageState struct {
	URL   string
	Title string
}

// PageStateOf reads the page's current URL and title through the ctx-bound
// State seam, so a wedged target cannot hold the page lock past the budget.
func PageStateOf(ctx context.Context, p *browsers.Page) (PageState, error) {
	ctx, done, err := beginPage(ctx, p, "state")
	if err != nil {
		return PageState{}, err
	}
	defer done()
	if p.Rod == nil {
		return PageState{}, fmt.Errorf("page session is closed")
	}

	url, title, err := p.State(ctx)
	if err != nil {
		return PageState{}, fmt.Errorf("read page state: %w", err)
	}
	return PageState{URL: url, Title: title}, nil
}

// PageStateIfFree reads the page state through the engine's bounded, guarded
// read, so a page another operation holds, or one whose renderer stopped
// answering, costs at most one read budget instead of the caller's whole action
// budget. The act path uses it as the pre- and post-action baseline, so a wedged
// page fails the action at the guard instead of relabelling the spent budget as
// a 500ms probe failure.
func PageStateIfFree(ctx context.Context, p *browsers.Page) (PageState, error) {
	url, title, err := p.TryState(ctx, "state")
	if err != nil {
		return PageState{}, asPageUnusable(err)
	}
	return PageState{URL: url, Title: title}, nil
}

// actSettleWindow bounds how long PageStateAfterAct waits for a click-driven
// navigation to commit before it reports the unchanged state.
const actSettleWindow = 900 * time.Millisecond

// actMayNavigate reports whether an action can start a navigation without a
// follow-up wait call.
func actMayNavigate(action string) bool {
	switch action {
	case "click", "click_at", "press_key", "upload_file", "eval", "select", "fill_form":
		return true
	}
	return false
}

// PageStateAfterAct reads the page state after an action. For actions that can
// navigate it polls up to actSettleWindow so a click that starts a navigation
// reports the new URL. A raw call reads once and returns, skipping the settle.
func PageStateAfterAct(ctx context.Context, p *browsers.Page, action string, before PageState) PageState {
	return settleAfterAction(ctx, func() (PageState, error) {
		return PageStateIfFree(ctx, p)
	}, action, before, p.RawActions(ctx))
}

// settleAfterAction reads the post-action state once and, unless the call is
// raw or the action cannot navigate, polls until the address or title changes
// or actSettleWindow expires. A failed or empty read falls back to before.
func settleAfterAction(
	ctx context.Context,
	read func() (PageState, error),
	action string,
	before PageState,
	raw bool,
) PageState {
	after, err := read()
	if err != nil || after.URL == "" {
		after = before
	}
	if raw || !actMayNavigate(action) {
		return after
	}
	if after.URL != before.URL || after.Title != before.Title {
		return after
	}
	deadline := time.Now().Add(actSettleWindow)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return after
		case <-time.After(50 * time.Millisecond):
		}
		next, err := read()
		if err != nil || next.URL == "" {
			return after
		}
		after = next
		if after.URL != before.URL || after.Title != before.Title {
			return after
		}
	}
	return after
}

// ActStateDeltaFor formats the state change for one action. Actions whose own
// result already carries the destination or a JSON envelope — navigate, eval,
// poc_bundle and fill_form — produce no suffix.
func ActStateDeltaFor(action string, before, after PageState) string {
	switch action {
	case "navigate", "poc_bundle", "eval", "fill_form":
		return ""
	}
	return ActStateDelta(before, after)
}

// ActStateDelta formats the change between two states as a one-line suffix.
// It returns "" when neither URL nor title changed.
func ActStateDelta(before, after PageState) string {
	var parts []string
	if before.URL != after.URL {
		parts = append(parts, "url="+after.URL)
	}
	if before.Title != after.Title {
		parts = append(parts, fmt.Sprintf("title=%q", after.Title))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n→ " + strings.Join(parts, " ")
}

// PageScreenshot returns a PNG screenshot of the page as a base64 data-URI
// ("data:image/png;base64,…") that consumers can render directly. When fullPage
// is true the entire scrollable page is captured; otherwise only the visible
// viewport. Backed by p.Rod.Screenshot(fullPage, nil) — a nil request yields
// CDP defaults (PNG). The output deliberately bypasses PageTruncate at the call
// sites: image size scales with the viewport, not max_bytes, and cutting a
// data-URI mid-stream would corrupt it. Mirrors the MCP extractScreenshot.
func PageScreenshot(ctx context.Context, p *browsers.Page, fullPage bool) (string, error) {
	ctx, done, err := beginPage(ctx, p, "screenshot")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	b, err := p.Rod.Context(ctx).Screenshot(fullPage, nil)
	if err != nil {
		return "", fmt.Errorf("capture screenshot (full_page=%t): %w", fullPage, err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b), nil
}

// PageCSS returns the page's stylesheet resources: a JSON list of
// {url, type, size} entries, or — when target is non-empty — the full content
// of exactly that one stylesheet. Mirrors the MCP extractCSS.
func PageCSS(ctx context.Context, p *browsers.Page, target string) (string, error) {
	return PageResources(ctx, p, target, "css", proto.NetworkResourceTypeStylesheet)
}

// PageJS returns the page's script resources: a JSON list of {url, type, size}
// entries, or — when target is non-empty — the full content of exactly that one
// script. Mirrors the MCP extractJS.
func PageJS(ctx context.Context, p *browsers.Page, target string) (string, error) {
	return PageResources(ctx, p, target, "js", proto.NetworkResourceTypeScript)
}

// PageResources is the shared backend of PageCSS and PageJS. It enumerates the
// page's resources of the given kind by walking the CDP resource tree
// recursively (iframes/subframes included) and returns either:
//
//   - with target set: the content of exactly that resource, fetched via
//     p.Rod.GetResource(url) and prefixed with a small // url= / // bytes=
//     header, or an LLM-actionable error when the resource is unknown; or
//   - without target: the JSON list of matching resources, capped downstream
//     by PageTruncate (re-request with --what css|js --target <url> for full
//     content).
//
// what is the tool's own what value for the kind ("css" or "js"); it is used
// in the hint so the suggested re-request uses a valid enum value (the CDP
// resource type string, e.g. "Stylesheet", is not part of the what enum).
//
// The per-page mutex is held for the whole extraction because rod pages are
// not thread-safe. Mirrors the MCP extractResources.
func PageResources(ctx context.Context, p *browsers.Page, target, what string, kind proto.NetworkResourceType) (string, error) {
	ctx, done, err := beginPage(ctx, p, what)
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	// Bind the ctx-bounded clone once and reuse it for the tree call and the
	// GetResource fetch, so the whole extraction honors the caller's context.
	page := p.Rod.Context(ctx)

	data, err := page.Call(ctx, string(page.SessionID), proto.PageGetResourceTree{}.ProtoReq(), proto.PageGetResourceTree{})
	if err != nil {
		return "", fmt.Errorf("enumerate resource tree: %w", err)
	}
	var tree proto.PageGetResourceTreeResult
	if err := json.Unmarshal(data, &tree); err != nil {
		return "", fmt.Errorf("decode resource tree: %w", err)
	}

	var resources []*proto.PageFrameResource
	collectResources(tree.FrameTree, kind, &resources)

	if target != "" {
		return fetchResource(page, resources, target)
	}

	list := make([]resourceEntry, 0, len(resources))
	for _, r := range resources {
		list = append(list, resourceEntry{URL: r.URL, Type: string(r.Type), Size: r.ContentSize})
	}
	if kind == proto.NetworkResourceTypeStylesheet {
		inline, err := collectInlineStyles(page)
		if err != nil {
			return "", fmt.Errorf("read inline styles: %w", err)
		}
		list = append(list, inline...)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("encode resource list: %w", err)
	}
	// The list is capped by PageTruncate at the call sites; the hint tells the
	// caller how to fetch a single resource's full content (AGENTS.md
	// dehydration convention: "first 50KB; use what=css&target=URL to fetch
	// more").
	return string(b) + "\n// hint: to fetch one resource's full content, re-request with what=" +
		what + "&target=<url>", nil
}

// collectInlineStyles returns one entry per inline <style> block so pages that
// inject their CSS at runtime (SPAs, styled-components) still report it. The
// caller holds the page lock.
func collectInlineStyles(page *rod.Page) ([]resourceEntry, error) {
	res, err := page.Eval(`() => [...document.querySelectorAll('style')].map(s => s.textContent || '')`)
	if err != nil {
		return nil, err
	}
	var bodies []string
	if err := res.Value.Unmarshal(&bodies); err != nil {
		return nil, fmt.Errorf("decode inline styles: %w", err)
	}
	return inlineStyleEntries(bodies), nil
}

// inlineStyleEntries shapes inline style bodies into resource entries numbered
// from 1 with their byte length as size.
func inlineStyleEntries(bodies []string) []resourceEntry {
	entries := make([]resourceEntry, 0, len(bodies))
	for i, body := range bodies {
		size := float64(len(body))
		entries = append(entries, resourceEntry{
			URL:  fmt.Sprintf("inline-style[%d]", i+1),
			Type: "inline-style",
			Size: &size,
		})
	}
	return entries
}

// collectResources walks the resource tree depth-first, appending every
// resource of the given kind that actually loaded (Failed and Canceled both
// false) to out. ChildFrames are visited recursively so iframe/subframe
// resources are included.
func collectResources(tree *proto.PageFrameResourceTree, kind proto.NetworkResourceType, out *[]*proto.PageFrameResource) {
	if tree == nil {
		return
	}
	for _, r := range tree.Resources {
		if r.Type == kind && !r.Failed && !r.Canceled {
			*out = append(*out, r)
		}
	}
	for _, child := range tree.ChildFrames {
		collectResources(child, kind, out)
	}
}

// fetchResource returns the content of the resource whose URL equals target,
// preferring a match from the enumerated (already-loaded) resources but
// falling back to a direct GetResource attempt so an unknown-but-fetchable URL
// still resolves. The content is prefixed with a small // url= / // bytes=
// header and capped downstream by PageTruncate.
func fetchResource(page *rod.Page, resources []*proto.PageFrameResource, target string) (string, error) {
	for _, r := range resources {
		if r.URL == target {
			b, err := page.GetResource(r.URL)
			if err != nil {
				return "", fmt.Errorf("fetch resource %q: %w", target, err)
			}
			return formatResource(target, len(b), b), nil
		}
	}
	b, err := page.GetResource(target)
	if err != nil {
		return "", fmt.Errorf("resource %q not found in the page's resource tree: %w", target, err)
	}
	return formatResource(target, len(b), b), nil
}

// formatResource prefixes fetched resource bytes with a header identifying the
// URL and byte size, so the caller knows which resource the content belongs to.
func formatResource(url string, size int, b []byte) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "// url=%s\n// bytes=%d\n", url, size)
	buf.Write(b)
	return buf.String()
}

// resourceEntry is one css/js resource in the enumeration list returned by
// PageResources.
type resourceEntry struct {
	// URL is the absolute resource URL.
	URL string `json:"url"`
	// Type is the resource type as reported by the browser ("Stylesheet" or
	// "Script").
	Type string `json:"type"`
	// Size is the content size in bytes reported by the browser, or null when
	// the browser did not report one. Resources are never fetched just to size
	// them.
	Size *float64 `json:"size"`
}

// PageDOM returns the page's DOM as a compact, LLM-friendly JSON tree.
// CaptureDOMSnapshot yields a flattened Documents array (index 0 = root
// document, later indexes = iframe/imported documents) plus a shared Strings
// table; this helper re-nests each document's parallel-array node tables by
// ParentIndex and resolves every string index through the table, so the reader
// sees a real tree (iframes, template contents and flattened shadow DOM
// included) instead of raw CDP arrays. Every element node carries a
// g<document>-b<backendNodeId> handle usable as page_act's handle argument;
// the document token scopes it to the document that produced this snapshot.
// Backed by Page.CaptureSnapshot; mirrors the MCP extractDOMSnapshot shape
// exactly.
func PageDOM(ctx context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	snap, token, err := p.CaptureSnapshot(ctx)
	if err != nil {
		return "", err
	}

	docs := buildDOMDocuments(snap, token)
	b, err := json.Marshal(docs)
	if err != nil {
		return "", fmt.Errorf("encode DOM snapshot: %w", err)
	}
	return string(b), nil
}

// domDocument is the JSON shape of one document in the DOM snapshot output:
// its context (title, url, frame id) plus the re-nested element tree.
type domDocument struct {
	// Title is the document's title.
	Title string `json:"title"`
	// URL is the document's URL.
	URL string `json:"url"`
	// FrameID is the CDP frame id this document belongs to.
	FrameID string `json:"frame_id,omitempty"`
	// Root is the nested tree root (#document).
	Root *domNode `json:"root"`
}

// domNode is one node in the re-nested DOM tree.
type domNode struct {
	// Tag is the lowercased element name ("div"), or a pseudo-name for
	// non-elements ("#document", "#text", "#comment").
	Tag string `json:"tag"`
	// ID is the element's id attribute, hoisted out of Attrs.
	ID string `json:"id,omitempty"`
	// Class is the element's class attribute, whitespace-normalized, hoisted
	// out of Attrs.
	Class string `json:"class,omitempty"`
	// Attrs holds the element's remaining attributes.
	Attrs map[string]string `json:"attrs,omitempty"`
	// Text is the node's text: the nodeValue for text nodes, the textarea/
	// input value or the layout text for elements.
	Text string `json:"text,omitempty"`
	// Handle is the element's action handle in the
	// g<document>-b<backendNodeId> form minted by every snapshot extractor,
	// usable as page_act's handle argument. The document token scopes it to
	// the document that produced the snapshot. Set on element nodes only;
	// absent when CDP reports no backend node id.
	Handle string `json:"handle,omitempty"`
	// Visible is true when the node has a layout box (absolute bounds), i.e.
	// it is rendered rather than display:none or detached. Absent when false.
	Visible bool `json:"visible,omitempty"`
	// Bounds is the node's absolute position bounding box as reported by CDP
	// (an array of floats), when the node has one.
	Bounds []float64 `json:"bounds,omitempty"`
	// FrameURL is set on frame-owner elements (iframe/frame/object) that CDP
	// reports as containing another document; the child document itself is
	// also emitted as its own entry in the Documents array.
	FrameURL string `json:"frame_url,omitempty"`
	// Children are the node's element/text/comment children in document order.
	Children []*domNode `json:"children,omitempty"`
}

// buildDOMDocuments converts every document in the snapshot into a nested tree
// and attaches iframe child documents to their frame-owner nodes. Each
// document is emitted with its title/url/frame id context so no document is
// lost, and frame owners carry the child document's URL inline.
func buildDOMDocuments(snap *proto.DOMSnapshotCaptureSnapshotResult, docToken string) []domDocument {
	docs := make([]domDocument, len(snap.Documents))
	nodesByDoc := make([]map[int]*domNode, len(snap.Documents))
	for i, d := range snap.Documents {
		root, byIndex := buildDOMTree(snap.Strings, d, docToken)
		nodesByDoc[i] = byIndex
		docs[i] = domDocument{
			Title:   str(snap.Strings, d.Title),
			URL:     str(snap.Strings, d.DocumentURL),
			FrameID: str(snap.Strings, d.FrameID),
			Root:    root,
		}
	}

	// Second pass: a node whose ContentDocumentIndex rare data points at
	// another document is a frame owner — annotate it with the child
	// document's URL so the reader can jump from the iframe to its content.
	for i, d := range snap.Documents {
		byIndex := nodesByDoc[i]
		if byIndex == nil || d.Nodes == nil || d.Nodes.ContentDocumentIndex == nil {
			continue
		}
		for k, nodeIdx := range d.Nodes.ContentDocumentIndex.Index {
			childIdx := d.Nodes.ContentDocumentIndex.Value[k]
			if childIdx < 0 || childIdx >= len(docs) || childIdx == i {
				continue
			}
			if node := byIndex[nodeIdx]; node != nil {
				node.FrameURL = docs[childIdx].URL
			}
		}
	}
	return docs
}

// buildDOMTree re-nests one document's flat parallel-array node table into a
// tree rooted at the node with parent -1, and returns the root plus a flat
// node-index → *domNode map (used to attach iframe documents afterwards).
// Whitespace-only text nodes and the inline bodies of <script>/<style>
// elements are elided to keep the output compact (script/style sources are
// available via --what js/css instead).
func buildDOMTree(
	strs []string,
	doc *proto.DOMSnapshotDocumentSnapshot,
	docToken string,
) (*domNode, map[int]*domNode) {
	n := doc.Nodes
	if n == nil || len(n.ParentIndex) == 0 {
		return nil, nil
	}

	layout := layoutIndex(doc.Layout)
	byIndex := make(map[int]*domNode, len(n.ParentIndex))

	// Pass 1: populate every node from its parallel-array rows.
	for i := range n.ParentIndex {
		nd := &domNode{Tag: nodeTag(strs, n, i)}
		if i < len(n.NodeType) {
			switch n.NodeType[i] {
			case 1: // element: hoist id/class, keep other attributes
				nd.ID, nd.Class, nd.Attrs = nodeAttrs(strs, n.Attributes, i)
				nd.Text = rareString(strs, n.TextValue, i)
				if nd.Text == "" {
					nd.Text = rareString(strs, n.InputValue, i)
				}
				if i < len(n.BackendNodeID) && n.BackendNodeID[i] > 0 {
					nd.Handle = browsers.MintHandle(docToken, n.BackendNodeID[i])
				}
			case 3: // text: the nodeValue is the text itself
				nd.Text = str(strs, n.NodeValue[i])
			}
		}
		if li, ok := layout[i]; ok {
			if li < len(doc.Layout.Bounds) && len(doc.Layout.Bounds[li]) > 0 {
				nd.Visible = true
				nd.Bounds = doc.Layout.Bounds[li]
			}
			if nd.Text == "" && li < len(doc.Layout.Text) {
				nd.Text = str(strs, doc.Layout.Text[li])
			}
		}
		byIndex[i] = nd
	}

	// Pass 2: attach children by ParentIndex, in document order.
	var root *domNode
	for i := range n.ParentIndex {
		nd := byIndex[i]
		parent := n.ParentIndex[i]
		if parent < 0 || parent >= len(n.ParentIndex) || parent == i {
			root = nd
			continue
		}
		// Elide whitespace-only text nodes (pretty-printed markup noise) and
		// the bodies of script/style elements (sources via --what js/css).
		if i < len(n.NodeType) && n.NodeType[i] == 3 && strings.TrimSpace(nd.Text) == "" {
			continue
		}
		pn := byIndex[parent]
		if pn == nil {
			root = nd
			continue
		}
		if pn.Tag == "script" || pn.Tag == "style" {
			continue
		}
		pn.Children = append(pn.Children, nd)
	}
	return root, byIndex
}

// layoutIndex maps node-tree index → layout-tree entry for O(1) lookups.
func layoutIndex(layout *proto.DOMSnapshotLayoutTreeSnapshot) map[int]int {
	if layout == nil {
		return nil
	}
	m := make(map[int]int, len(layout.NodeIndex))
	for i, node := range layout.NodeIndex {
		m[node] = i
	}
	return m
}

// nodeTag resolves a node's tag name: the nodeName lowercased for elements
// ("DIV" → "div"), or the raw pseudo-name for non-elements ("#document",
// "#text", "#comment").
func nodeTag(strs []string, n *proto.DOMSnapshotNodeTreeSnapshot, i int) string {
	if i >= len(n.NodeName) {
		return ""
	}
	name := str(strs, n.NodeName[i])
	if name == "" || strings.HasPrefix(name, "#") {
		return name
	}
	return strings.ToLower(name)
}

// nodeAttrs parses the flattened name,value attribute pairs of element node i,
// hoisting id and class out and returning the remaining attributes. Unnamed or
// unvalued trailing pairs are ignored.
func nodeAttrs(strs []string, attrs []proto.DOMSnapshotArrayOfStrings, i int) (id, class string, rest map[string]string) {
	if i >= len(attrs) {
		return "", "", nil
	}
	pairs := attrs[i]
	for k := 0; k+1 < len(pairs); k += 2 {
		name := str(strs, pairs[k])
		value := str(strs, pairs[k+1])
		switch name {
		case "id":
			id = value
		case "class":
			class = strings.Join(strings.Fields(value), " ")
		case "":
			// unnamed attribute pair — skip
		default:
			if rest == nil {
				rest = make(map[string]string, 4)
			}
			rest[name] = value
		}
	}
	return id, class, rest
}

// str resolves a string-table index, returning "" for negative or
// out-of-range indexes (a node may simply have no value).
func str(strs []string, idx proto.DOMSnapshotStringIndex) string {
	if idx < 0 || int(idx) >= len(strs) {
		return ""
	}
	return strs[idx]
}

// rareString returns the string-table value of the rare-data entry for node
// index i, or "" when the table has no entry for it. Rare data is small, so a
// linear scan is fine.
func rareString(strs []string, rd *proto.DOMSnapshotRareStringData, i int) string {
	if rd == nil {
		return ""
	}
	for k, node := range rd.Index {
		if node == i {
			return str(strs, rd.Value[k])
		}
	}
	return ""
}

// PageEval runs a JavaScript function in the page — the primary escape hatch
// for any page manipulation — and returns the JSON result. Backed by
// p.Rod.Context(ctx).Eval(js); js must be a JS function definition, e.g.
// "() => document.title". Mirrors the Eval pattern proven in extractText.
func PageEval(ctx context.Context, p *browsers.Page, js string) (string, error) {
	ctx, done, err := beginPage(ctx, p, "eval")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(js) // Eval only accepts a JS function definition
	if err != nil {
		return "", fmt.Errorf("evaluate js: %w", err)
	}
	return res.Value.JSON("", ""), nil // gson marshals to compact JSON text
}

// parseMouseButton resolves a mouse button string ("left", "right", "middle") to proto.InputMouseButton.
func parseMouseButton(button string) (proto.InputMouseButton, error) {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "", "left":
		return proto.InputMouseButtonLeft, nil
	case "right":
		return proto.InputMouseButtonRight, nil
	case "middle":
		return proto.InputMouseButtonMiddle, nil
	default:
		return "", fmt.Errorf("unknown mouse button %q (must be left, right, or middle)", button)
	}
}

// actTarget resolves an action's element target: a non-empty handle wins over
// the selector. A handle carries the token of the document that minted it; the
// element's document marker is read in the same eval as the liveness check, so
// the scope check costs no extra round trip. A handle from any other document
// is rejected before it can act on a reused backend node id, while a document
// without a marker is stamped with the handle's token and treated as current
// (frames and pre-marker documents must not raise a false stale error).
// Callers must already hold the page lock; this never locks.
func actTarget(page *rod.Page, handle, selector string) (*rod.Element, error) {
	if handle == "" {
		if selector == "" {
			return nil, errors.New("either selector or handle is required")
		}
		return resolveSelector(page, selector)
	}
	token, id, err := parseHandle(handle)
	if err != nil {
		return nil, err
	}
	el, err := page.ElementFromNode(&proto.DOMNode{BackendNodeID: id})
	if err != nil {
		return nil, staleResolveError(handle, err)
	}
	res, err := el.Eval(
		`(doc) => { if (!window.`+browsers.DocMarker+`) window.`+browsers.DocMarker+
			` = doc; return {doc: window.`+browsers.DocMarker+`, live: this.isConnected} }`,
		token,
	)
	if err != nil {
		return nil, fmt.Errorf("handle %q: check liveness: %w", handle, err)
	}
	var state struct {
		Doc  string `json:"doc"`
		Live bool   `json:"live"`
	}
	if err := res.Value.Unmarshal(&state); err != nil {
		return nil, fmt.Errorf("handle %q: decode liveness: %w", handle, err)
	}
	if state.Doc != token {
		return nil, staleDocumentError(handle)
	}
	if !state.Live {
		return nil, staleDetachedError(handle)
	}
	return el, nil
}

// staleDocumentError reports a handle minted by a document that has since been
// replaced (navigation, reload, redirect). Chromium may reuse the backend node
// id in the newer document, so the handle must never act there.
func staleDocumentError(handle string) error {
	return fmt.Errorf("handle %q is stale — minted for a previous document (re-run page_fetch what=interactives to re-mint handles)", handle)
}

// staleResolveError reports a handle whose backend node no longer resolves.
func staleResolveError(handle string, err error) error {
	return fmt.Errorf("handle %q is stale — element no longer in the document (re-run page_fetch what=interactives to re-mint handles): %w", handle, err)
}

// staleDetachedError reports a handle that resolves to a detached element.
func staleDetachedError(handle string) error {
	return fmt.Errorf("handle %q is stale — element detached from the document (re-run page_fetch what=interactives to re-mint handles)", handle)
}

// parseHandle splits a g<document>-b<backendNodeId> handle minted by
// page_fetch into its document token and backend node id.
func parseHandle(handle string) (string, proto.DOMBackendNodeID, error) {
	invalid := func() (string, proto.DOMBackendNodeID, error) {
		return "", 0, fmt.Errorf("invalid handle %q (expected g<document>-b<backendNodeId> minted by page_fetch what=interactives|dom|links)", handle)
	}
	rest, ok := strings.CutPrefix(handle, "g")
	if !ok {
		return invalid()
	}
	token, idText, ok := strings.Cut(rest, "-b")
	if !ok || token == "" {
		return invalid()
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return invalid()
	}
	return token, proto.DOMBackendNodeID(id), nil
}

// actionError adds action and selector context to an actTarget failure; handle
// errors already carry their complete message.
func actionError(action, handle, selector string, err error) error {
	if handle != "" || selector == "" {
		return err
	}
	return fmt.Errorf("%s: element %q: %w", action, selector, err)
}

// targetFailure adds the resolved target to an element-operation failure.
func targetFailure(action, handle, selector string, err error) error {
	if handle != "" {
		return fmt.Errorf("%s: handle %q: %w", action, handle, err)
	}
	return fmt.Errorf("%s: element %q: %w", action, selector, err)
}

// PageClickOpts clicks the element targeted by handle or selector with
// optional button and click count. It reads the page state first so a click
// that navigates during the input acknowledgement is still reported as landed.
func PageClickOpts(
	ctx context.Context,
	p *browsers.Page,
	handle, selector string,
	button string,
	clickCount int,
) (string, error) {
	before, _ := PageStateIfFree(ctx, p)
	return PageClickFrom(ctx, p, handle, selector, button, clickCount, before)
}

// PageClickFrom is PageClickOpts with the pre-click page state supplied by the
// caller (page_act already read it). The mouse events run on the ctx-bound page
// clone with their own short acknowledgement budget, so a submit click that
// starts a navigation returns as soon as the navigation commits instead of
// waiting out the whole call deadline. The humanized pauses fit the remaining
// budget or are skipped, and a budget that expires before the mouse events
// produce the typed ActionTimeoutError (classified as timeout) instead of a
// click that never fires.
func PageClickFrom(
	ctx context.Context,
	p *browsers.Page,
	handle, selector string,
	button string,
	clickCount int,
	before PageState,
) (string, error) {
	return browsers.GuardPage(ctx, p, "click", func(ctx context.Context) (string, error) {
		started := time.Now()
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		btn, err := parseMouseButton(button)
		if err != nil {
			return "", fmt.Errorf("click: %w", err)
		}
		if clickCount <= 0 {
			clickCount = 1
		}

		page := p.Rod.Context(ctx)
		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", opDeadlineError("click", p, started, actionError("click", handle, selector, err))
		}
		pt, err := el.WaitInteractable()
		if err != nil {
			return "", opDeadlineError("click", p, started, targetFailure("click", handle, selector, err))
		}
		if err := el.WaitEnabled(); err != nil {
			return "", opDeadlineError("click", p, started, targetFailure("click", handle, selector, err))
		}

		ackCtx, ackCancel := context.WithTimeout(ctx, inputAckTimeout)
		defer ackCancel()
		if err := p.PauseAction(ctx, "click"); err != nil {
			return "", err
		}
		if err := dispatchClick(ackCtx, p, p.Rod.Context(ackCtx), *pt, btn, clickCount); err != nil {
			if stateMoved(ctx, p, before) {
				return clickConfirmation(handle, selector, button, clickCount), nil
			}
			return "", actionDeadlineError("click", p, err)
		}
		if err := p.PauseAction(ctx, "click"); err != nil {
			return "", err
		}
		return clickConfirmation(handle, selector, button, clickCount), nil
	})
}

// stateMoved reports whether the page's address or title differs from before.
// It is used to recognize a click whose navigation started before the input
// acknowledgement returned; an empty baseline never counts as a change. The
// read is ctx-bound, so a wedged target cannot hold the page lock.
func stateMoved(ctx context.Context, p *browsers.Page, before PageState) bool {
	if before.URL == "" && before.Title == "" {
		return false
	}
	url, title, err := p.State(ctx)
	if err != nil {
		return false
	}
	return url != before.URL || title != before.Title
}

// clickConfirmation renders the one-line click result shared by every surface.
func clickConfirmation(handle, selector, button string, clickCount int) string {
	ref := fmt.Sprintf("handle=%q", handle)
	if handle == "" {
		ref = fmt.Sprintf("selector=%q", selector)
	}
	if (button == "" || button == "left") && clickCount == 1 {
		return fmt.Sprintf("clicked %s", ref)
	}
	btnName := button
	if btnName == "" {
		btnName = "left"
	}
	return fmt.Sprintf("clicked %s button=%s count=%d", ref, btnName, clickCount)
}

// dispatchClick steps the pointer to pt, then sends a press and release through
// the ctx-bound page clone. It replaces rod's Element.Click and rod's Mouse
// helper, whose methods keep the placeholder page context and would outlive the
// call deadline.
func dispatchClick(
	ctx context.Context,
	p *browsers.Page,
	page *rod.Page,
	pt proto.Point,
	btn proto.InputMouseButton,
	clickCount int,
) error {
	if err := movePointer(ctx, p, page, pt.X, pt.Y, ""); err != nil {
		return err
	}
	if err := mouseButton(ctx, page, proto.InputDispatchMouseEventTypeMousePressed, pt.X, pt.Y, btn, clickCount); err != nil {
		return err
	}
	if err := mouseButton(ctx, page, proto.InputDispatchMouseEventTypeMouseReleased, pt.X, pt.Y, btn, clickCount); err != nil {
		return err
	}
	return nil
}

// actionDeadlineError turns an action failure caused by a cancelled or expired
// call budget into the typed timeout that names the raw-action and timeout_ms
// escapes. Any other failure keeps its own context, prefixed with op. The caller
// holds the page operation lock; this never locks.
func actionDeadlineError(op string, p *browsers.Page, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &browsers.ActionTimeoutError{Op: op, Page: p.ID, Err: err}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// movePointer steps the pointer from its last position to (x, y) with bounded
// jitter, so a click or hover is not an instant teleport. A raw call collapses
// the path to the exact target. The caller must hold the page lock.
func movePointer(
	ctx context.Context,
	p *browsers.Page,
	page *rod.Page,
	x, y float64,
	held proto.InputMouseButton,
) error {
	path := p.PointerPath(x, y)
	if p.RawActions(ctx) {
		path = path[len(path)-1:]
	}
	for _, step := range path {
		if err := moveMouse(ctx, page, step.X, step.Y, held); err != nil {
			return err
		}
	}
	return nil
}

// moveMouse dispatches one mouse-moved event at (x, y). held is the button
// already held down during a drag; an empty string means no button.
func moveMouse(
	ctx context.Context,
	page *rod.Page,
	x, y float64,
	held proto.InputMouseButton,
) error {
	buttons := 0
	button := proto.InputMouseButton("none")
	if held != "" {
		button = held
		buttons = input.MouseKeys[held]
	}
	ev := proto.InputDispatchMouseEvent{
		Type:    proto.InputDispatchMouseEventTypeMouseMoved,
		X:       x,
		Y:       y,
		Button:  button,
		Buttons: &buttons,
	}
	if _, err := page.Call(ctx, string(page.SessionID), ev.ProtoReq(), ev); err != nil {
		return fmt.Errorf("move mouse to (%.1f, %.1f): %w", x, y, err)
	}
	return nil
}

// mouseButton dispatches a pressed or released event for btn at (x, y).
func mouseButton(
	ctx context.Context,
	page *rod.Page,
	typ proto.InputDispatchMouseEventType,
	x, y float64,
	btn proto.InputMouseButton,
	clickCount int,
) error {
	buttons := 0
	if typ == proto.InputDispatchMouseEventTypeMousePressed {
		buttons = input.MouseKeys[btn]
	}
	ev := proto.InputDispatchMouseEvent{
		Type:       typ,
		X:          x,
		Y:          y,
		Button:     btn,
		Buttons:    &buttons,
		ClickCount: clickCount,
	}
	if _, err := page.Call(ctx, string(page.SessionID), ev.ProtoReq(), ev); err != nil {
		return fmt.Errorf("%s at (%.1f, %.1f): %w", typ, x, y, err)
	}
	return nil
}

// PageClick clicks the element targeted by handle or selector with the left
// mouse button once.
func PageClick(
	ctx context.Context,
	p *browsers.Page,
	handle, selector string,
) (string, error) {
	return PageClickOpts(ctx, p, handle, selector, "left", 1)
}

// PagePressKey presses a key or key combination (e.g. "Enter", "Escape",
// "Control+a", "Meta+c"). A non-empty handle or selector focuses that element
// first.
func PagePressKey(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, keyStr string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "press_key", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		mods, mainKey, err := parseKeyCombo(keyStr)
		if err != nil {
			return "", fmt.Errorf("press_key: %w", err)
		}

		page := p.Rod.Context(ctx)

		if handle != "" || selector != "" {
			el, err := actTarget(page, handle, selector)
			if err != nil {
				return "", actionError("press_key", handle, selector, err)
			}
			if err := el.Focus(); err != nil {
				if handle != "" {
					return "", fmt.Errorf("press_key: focus handle %q: %w", handle, err)
				}
				return "", fmt.Errorf("press_key: focus element %q: %w", selector, err)
			}
		}

		if err := p.PauseAction(ctx, "press_key"); err != nil {
			return "", err
		}
		if err := pressKeyCombo(ctx, page, mods, mainKey); err != nil {
			return "", fmt.Errorf("press_key %q: %w", keyStr, err)
		}
		if err := p.PauseAction(ctx, "press_key"); err != nil {
			return "", err
		}

		if handle != "" {
			return fmt.Sprintf("pressed key %q in handle=%q", keyStr, handle), nil
		}
		if selector != "" {
			return fmt.Sprintf("pressed key %q in selector=%q", keyStr, selector), nil
		}
		return fmt.Sprintf("pressed key %q", keyStr), nil
	})
}

// pressKeyCombo presses the modifiers, presses and releases the main key, then
// releases the modifiers in reverse order. Every event goes through the
// ctx-bound page clone (rod's KeyActions keeps the placeholder page context and
// would outlive the call deadline).
func pressKeyCombo(ctx context.Context, page *rod.Page, mods []input.Key, key input.Key) error {
	pressed := make(map[input.Key]struct{}, len(mods)+1)
	modifiers := func() int {
		bits := 0
		for k := range pressed {
			bits |= k.Modifier()
		}
		return bits
	}
	dispatch := func(k input.Key, typ proto.InputDispatchKeyEventType) error {
		ev := k.Encode(typ, modifiers())
		_, err := page.Call(ctx, string(page.SessionID), ev.ProtoReq(), ev)
		return err
	}
	for _, mod := range mods {
		pressed[mod] = struct{}{}
		if err := dispatch(mod, proto.InputDispatchKeyEventTypeKeyDown); err != nil {
			return fmt.Errorf("press modifier %s: %w", mod.Info().Code, err)
		}
	}
	pressed[key] = struct{}{}
	if err := dispatch(key, proto.InputDispatchKeyEventTypeKeyDown); err != nil {
		return fmt.Errorf("press %s: %w", key.Info().Code, err)
	}
	delete(pressed, key)
	if err := dispatch(key, proto.InputDispatchKeyEventTypeKeyUp); err != nil {
		return fmt.Errorf("release %s: %w", key.Info().Code, err)
	}
	for i := len(mods) - 1; i >= 0; i-- {
		delete(pressed, mods[i])
		if err := dispatch(mods[i], proto.InputDispatchKeyEventTypeKeyUp); err != nil {
			return fmt.Errorf("release modifier %s: %w", mods[i].Info().Code, err)
		}
	}
	return nil
}

// PageClickAt moves the mouse cursor to absolute coordinates (x, y) and clicks.
// button can be "left", "right", or "middle"; clickCount defaults to 1.
func PageClickAt(ctx context.Context, p *browsers.Page, x, y float64, button string, clickCount int) (string, error) {
	return browsers.GuardPage(ctx, p, "click_at", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		btn, err := parseMouseButton(button)
		if err != nil {
			return "", fmt.Errorf("click_at: %w", err)
		}
		if clickCount <= 0 {
			clickCount = 1
		}

		page := p.Rod.Context(ctx)
		if err := p.PauseAction(ctx, "click_at"); err != nil {
			return "", err
		}
		if err := dispatchClick(ctx, p, page, proto.Point{X: x, Y: y}, btn, clickCount); err != nil {
			return "", actionDeadlineError("click_at", p, err)
		}
		if err := p.PauseAction(ctx, "click_at"); err != nil {
			return "", err
		}

		btnName := button
		if btnName == "" {
			btnName = "left"
		}
		return fmt.Sprintf("clicked at (%.1f, %.1f) button=%s count=%d", x, y, btnName, clickCount), nil
	})
}

// PageHoverAt moves the mouse cursor to absolute coordinates (x, y).
func PageHoverAt(ctx context.Context, p *browsers.Page, x, y float64) (string, error) {
	return browsers.GuardPage(ctx, p, "hover_at", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		if err := p.PauseAction(ctx, "hover_at"); err != nil {
			return "", err
		}
		if err := movePointer(ctx, p, p.Rod.Context(ctx), x, y, ""); err != nil {
			return "", fmt.Errorf("hover_at: %w", err)
		}
		if err := p.PauseAction(ctx, "hover_at"); err != nil {
			return "", err
		}
		return fmt.Sprintf("hovered at (%.1f, %.1f)", x, y), nil
	})
}

// maxStyleTextRunes caps the cssText echoed in a set_style result.
const maxStyleTextRunes = 2000

// setStyleResult is the page_act set_style payload: the resolved target, whether
// the inline style was cleared, the property names the CSSOM kept, the
// declarations it dropped, and the resulting cssText.
type setStyleResult struct {
	Target   string   `json:"target"`
	Cleared  bool     `json:"cleared"`
	Applied  []string `json:"applied"`
	Rejected []string `json:"rejected"`
	Style    string   `json:"style"`
}

// setStyleScript applies a declaration list to the element bound as this. A
// detached probe validates each declaration first, so an invalid value is
// reported in rejected even when the element already held that property. A
// trailing !important becomes the setProperty priority rather than part of the
// value.
const setStyleScript = `(decls) => {
	const el = this;
	const probe = document.createElement('div').style;
	const applied = [];
	const rejected = [];
	for (const decl of decls) {
		const i = decl.indexOf(':');
		if (i <= 0) { rejected.push(decl); continue; }
		const prop = decl.slice(0, i).trim();
		let val = decl.slice(i + 1).trim();
		let priority = '';
		const important = /!\s*important\s*$/i.exec(val);
		if (important) {
			val = val.slice(0, important.index).trim();
			priority = 'important';
		}
		if (prop === '' || val === '') { rejected.push(decl); continue; }
		probe.setProperty(prop, val, priority);
		if (probe.getPropertyValue(prop) === '') { rejected.push(decl); continue; }
		el.style.setProperty(prop, val, priority);
		applied.push(prop);
	}
	return {applied, rejected, style: el.style.cssText};
}`

// splitStyleDeclarations splits a CSS declaration list on semicolons at bracket
// depth zero and outside quotes, so url('a;b'), quoted strings and rgb(1,2,3)
// survive. Empty segments are dropped.
func splitStyleDeclarations(decls string) []string {
	var out []string
	var quote rune
	depth := 0
	start := 0
	runes := []rune(decls)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == '\\' {
				i++
				continue
			}
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 {
				if seg := strings.TrimSpace(string(runes[start:i])); seg != "" {
					out = append(out, seg)
				}
				start = i + 1
			}
		}
	}
	if seg := strings.TrimSpace(string(runes[start:])); seg != "" {
		out = append(out, seg)
	}
	return out
}

// styleTarget renders the resolved set_style target for the result.
func styleTarget(handle, selector string) string {
	if handle != "" {
		return fmt.Sprintf("handle=%q", handle)
	}
	return fmt.Sprintf("selector=%q", selector)
}

// clipStyleText caps cssText at maxStyleTextRunes on a rune boundary, appending
// an ellipsis when it cuts.
func clipStyleText(s string) string {
	r := []rune(s)
	if len(r) <= maxStyleTextRunes {
		return s
	}
	return string(r[:maxStyleTextRunes]) + "…"
}

// PageSetStyle sets inline CSS declarations on the element targeted by handle
// or selector. An empty declarations value removes the inline style attribute.
// The result reports the target, the property names the CSSOM kept, the
// declarations it dropped, and the resulting cssText.
func PageSetStyle(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, declarations string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "set_style", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		page := p.Rod.Context(ctx)
		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", actionError("set_style", handle, selector, err)
		}

		if err := p.PauseAction(ctx, "set_style"); err != nil {
			return "", err
		}

		result := setStyleResult{
			Target:   styleTarget(handle, selector),
			Cleared:  declarations == "",
			Applied:  []string{},
			Rejected: []string{},
		}
		if declarations == "" {
			if _, err := el.Eval(`() => { this.removeAttribute('style'); return true; }`); err != nil {
				return "", targetFailure("set_style", handle, selector, err)
			}
		} else {
			decls := splitStyleDeclarations(declarations)
			if decls == nil {
				decls = []string{}
			}
			res, err := el.Eval(setStyleScript, decls)
			if err != nil {
				return "", targetFailure("set_style", handle, selector, err)
			}
			var got struct {
				Applied  []string `json:"applied"`
				Rejected []string `json:"rejected"`
				Style    string   `json:"style"`
			}
			if err := res.Value.Unmarshal(&got); err != nil {
				return "", fmt.Errorf("set_style: decode result: %w", err)
			}
			if got.Applied != nil {
				result.Applied = got.Applied
			}
			if got.Rejected != nil {
				result.Rejected = got.Rejected
			}
			result.Style = clipStyleText(got.Style)
		}

		if err := p.PauseAction(ctx, "set_style"); err != nil {
			return "", err
		}

		b, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("set_style: encode result: %w", err)
		}
		return string(b), nil
	})
}

// PageDragAndDrop performs a mouse drag-and-drop operation from source coordinates/selector to target coordinates/selector.
func PageDragAndDrop(ctx context.Context, p *browsers.Page, fromX, fromY, toX, toY float64, selector, targetSelector string) (string, error) {
	return browsers.GuardPage(ctx, p, "drag_and_drop", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		page := p.Rod.Context(ctx)

		if selector != "" {
			el, err := resolveSelector(page, selector)
			if err != nil {
				return "", fmt.Errorf("drag_and_drop: source element %q: %w", selector, err)
			}
			shape, err := el.Shape()
			if err != nil {
				return "", fmt.Errorf("drag_and_drop: source element %q shape: %w", selector, err)
			}
			box := shape.Box()
			fromX, fromY = box.X+box.Width/2, box.Y+box.Height/2
		}

		if targetSelector != "" {
			el, err := resolveSelector(page, targetSelector)
			if err != nil {
				return "", fmt.Errorf("drag_and_drop: target element %q: %w", targetSelector, err)
			}
			shape, err := el.Shape()
			if err != nil {
				return "", fmt.Errorf("drag_and_drop: target element %q shape: %w", targetSelector, err)
			}
			box := shape.Box()
			toX, toY = box.X+box.Width/2, box.Y+box.Height/2
		}

		if err := moveMouse(ctx, page, fromX, fromY, ""); err != nil {
			return "", fmt.Errorf("drag_and_drop: source: %w", err)
		}
		if err := mouseButton(ctx, page, proto.InputDispatchMouseEventTypeMousePressed, fromX, fromY, proto.InputMouseButtonLeft, 1); err != nil {
			return "", fmt.Errorf("drag_and_drop: mouse down: %w", err)
		}
		const steps = 10
		for i := 1; i <= steps; i++ {
			x := fromX + (toX-fromX)*float64(i)/steps
			y := fromY + (toY-fromY)*float64(i)/steps
			if err := moveMouse(ctx, page, x, y, proto.InputMouseButtonLeft); err != nil {
				return "", fmt.Errorf("drag_and_drop: target: %w", err)
			}
		}
		if err := mouseButton(ctx, page, proto.InputDispatchMouseEventTypeMouseReleased, toX, toY, proto.InputMouseButtonLeft, 1); err != nil {
			return "", fmt.Errorf("drag_and_drop: mouse up: %w", err)
		}

		return fmt.Sprintf("dragged from (%.1f, %.1f) to (%.1f, %.1f)", fromX, fromY, toX, toY), nil
	})
}

// PageUploadFile attaches local file paths to the file input targeted by
// handle or selector.
func PageUploadFile(
	ctx context.Context,
	p *browsers.Page,
	handle, selector string,
	paths []string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "upload_file", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		if len(paths) == 0 {
			return "", fmt.Errorf("upload_file: no file paths provided")
		}

		el, err := actTarget(p.Rod.Context(ctx), handle, selector)
		if err != nil {
			return "", actionError("upload_file", handle, selector, err)
		}
		if err := el.SetFiles(paths); err != nil {
			return "", targetFailure("upload_file", handle, selector, err)
		}
		if handle != "" {
			return fmt.Sprintf("uploaded files %v to handle=%q", paths, handle), nil
		}
		return fmt.Sprintf("uploaded files %v to selector=%q", paths, selector), nil
	})
}

// hidesTypedValue reports whether el is a control whose content Chrome masks or
// never renders, so an action confirmation must not describe its value.
func hidesTypedValue(el *rod.Element) bool {
	attr, err := el.Attribute("type")
	if err != nil || attr == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*attr)) {
	case "password", "hidden":
		return true
	}
	return false
}

// inputConfirmation renders an input/type confirmation without the typed value:
// it reports the target and the value's rune length, and adds
// value_redacted=true for a password or hidden field.
func inputConfirmation(
	verb, handle, selector, value string,
	secret bool,
) string {
	target := fmt.Sprintf("selector=%q", selector)
	if handle != "" {
		target = fmt.Sprintf("handle=%q", handle)
	}
	line := fmt.Sprintf("%s %s length=%d", verb, target, utf8.RuneCountInString(value))
	if secret {
		line += " value_redacted=true"
	}
	return line
}

// PageInput sets the value of the element targeted by handle or selector: it
// selects all existing text (el.SelectAllText) then types value via rod's
// Input, so the field ends up exactly value. The confirmation reports the
// target and the value's rune length only, never the value itself.
func PageInput(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, value string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "input", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		page := p.Rod.Context(ctx)
		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", actionError("input", handle, selector, err)
		}
		if err := p.PauseAction(ctx, "input"); err != nil {
			return "", err
		}
		if shape, err := el.Shape(); err == nil {
			box := shape.Box()
			if box.Width > 0 && box.Height > 0 {
				if err := movePointer(ctx, p, page, box.X+box.Width/2, box.Y+box.Height/2, ""); err != nil {
					return "", targetFailure("input", handle, selector, err)
				}
			}
		}
		secret := hidesTypedValue(el)
		if err := el.SelectAllText(); err != nil {
			return "", targetFailure("input", handle, selector, err)
		}
		if err := el.Input(value); err != nil {
			return "", targetFailure("input", handle, selector, err)
		}
		if err := p.PauseAction(ctx, "input"); err != nil {
			return "", err
		}
		return inputConfirmation("set input", handle, selector, value, secret), nil
	})
}

// PageType types keystrokes into the element targeted by handle or selector
// without clearing it — unlike PageInput (the input action selects all text
// first), type is Focus + InsertText: it focuses the element (scrolling it into
// view by user gesture) and pastes value at the current caret position. The
// confirmation reports the target and the value's rune length only, never the
// value itself.
func PageType(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, value string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "type", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		page := p.Rod.Context(ctx)

		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", actionError("type", handle, selector, err)
		}
		secret := hidesTypedValue(el)
		if err := el.Focus(); err != nil {
			return "", targetFailure("type", handle, selector, err)
		}
		if err := page.InsertText(value); err != nil {
			return "", fmt.Errorf("type: %w", err)
		}
		return inputConfirmation("typed", handle, selector, value, secret), nil
	})
}

// PageHover moves the mouse over the element targeted by handle or selector,
// triggering hover-dependent UI.
func PageHover(
	ctx context.Context,
	p *browsers.Page,
	handle, selector string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "hover", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		page := p.Rod.Context(ctx)
		el, err := actTarget(page, handle, selector)
		if err != nil {
			return "", actionError("hover", handle, selector, err)
		}
		pt, err := el.WaitInteractable()
		if err != nil {
			return "", targetFailure("hover", handle, selector, err)
		}
		if err := p.PauseAction(ctx, "hover"); err != nil {
			return "", err
		}
		if err := movePointer(ctx, p, page, pt.X, pt.Y, ""); err != nil {
			return "", targetFailure("hover", handle, selector, err)
		}
		if err := p.PauseAction(ctx, "hover"); err != nil {
			return "", err
		}
		if handle != "" {
			return fmt.Sprintf("hovered handle=%q", handle), nil
		}
		return fmt.Sprintf("hovered selector=%q", selector), nil
	})
}

// PageScroll scrolls the window by x/y pixels (window.scrollBy via Eval — the
// primary path) and returns the resulting scroll position [scrollX, scrollY].
// Missing x/y are treated as 0. Mirrors the MCP page_act scroll confirmation.
func PageScroll(ctx context.Context, p *browsers.Page, x, y float64) (string, error) {
	return browsers.GuardPage(ctx, p, "scroll", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		res, err := p.Rod.Context(ctx).Eval(
			"(x, y) => { window.scrollBy(x, y); return [window.scrollX, window.scrollY]; }",
			x, y,
		)
		if err != nil {
			return "", fmt.Errorf("scroll: %w", err)
		}
		return fmt.Sprintf("scrolled to %s", res.Value.JSON("", "  ")), nil
	})
}

// PageSelect selects an <option> with the given value in the element targeted
// by handle or selector. rod v0.116.2 has no SelectorTypeValue, so the option
// is matched with a CSS attribute selector [value="..."].
func PageSelect(
	ctx context.Context,
	p *browsers.Page,
	handle, selector, value string,
) (string, error) {
	return browsers.GuardPage(ctx, p, "select", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}

		el, err := actTarget(p.Rod.Context(ctx), handle, selector)
		if err != nil {
			return "", actionError("select", handle, selector, err)
		}
		opt := `[value="` + value + `"]`
		if err := el.Select([]string{opt}, true, rod.SelectorTypeCSSSector); err != nil {
			return "", targetFailure("select", handle, selector, err)
		}
		if handle != "" {
			return fmt.Sprintf("selected option value=%q in handle=%q", value, handle), nil
		}
		return fmt.Sprintf("selected option value=%q in selector=%q", value, selector), nil
	})
}

// PageNavigate navigates the page to url and waits for the new page to load
// (rod's Navigate returns after the response header, so WaitLoad is
// mandatory). Returns the final URL and title — the same confirmation shape as
// the MCP page_act navigate. The wait runs under the page watchdog, so a target
// that never finishes loading cannot hold the page lock past the call budget.
func PageNavigate(ctx context.Context, p *browsers.Page, url string) (string, error) {
	return browsers.GuardPage(ctx, p, "navigate", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		page := p.Rod.Context(ctx)
		if err := page.Navigate(url); err != nil {
			if browsers.IsInvalidAuthCredential(err) {
				return "", browsers.InvalidAuthCredentialError(url, p.ID)
			}
			return "", fmt.Errorf("navigate: %w", err)
		}
		if err := page.WaitLoad(); err != nil {
			return "", fmt.Errorf("wait for load: %w", err)
		}
		// State reports the final URL (post-redirect) and title; fall back to
		// the requested URL if the read fails — the navigation already landed.
		msg := fmt.Sprintf("navigated to %q", url)
		if finalURL, title, err := p.State(ctx); err == nil {
			msg = fmt.Sprintf("navigated to %q (title %q)", finalURL, title)
		}
		return msg, nil
	})
}

// PageBack navigates one step back in the page's history and waits for the
// previous page to load (history.back() triggers a fresh navigation). Returns
// the resulting URL — the same confirmation shape as the MCP page_act back. The
// wait runs under the page watchdog.
func PageBack(ctx context.Context, p *browsers.Page) (string, error) {
	return browsers.GuardPage(ctx, p, "back", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		page := p.Rod.Context(ctx)
		if err := page.NavigateBack(); err != nil {
			return "", fmt.Errorf("navigate back: %w", err)
		}
		// WaitLoad returns immediately if no navigation happened (no history);
		// when it does, the URL below reflects the previous page.
		if err := page.WaitLoad(); err != nil {
			return "", fmt.Errorf("wait for load: %w", err)
		}
		backURL, _, err := p.State(ctx)
		if err != nil {
			return "", fmt.Errorf("read page info: %w", err)
		}
		return fmt.Sprintf("navigated back to %q", backURL), nil
	})
}

// PageForward navigates one step forward in the page's history and waits for
// the page to load. Returns the resulting URL — the same confirmation shape as
// the MCP page_act forward. The wait runs under the page watchdog.
func PageForward(ctx context.Context, p *browsers.Page) (string, error) {
	return browsers.GuardPage(ctx, p, "forward", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		page := p.Rod.Context(ctx)
		if err := page.NavigateForward(); err != nil {
			return "", fmt.Errorf("navigate forward: %w", err)
		}
		if err := page.WaitLoad(); err != nil {
			return "", fmt.Errorf("wait for load: %w", err)
		}
		forwardURL, _, err := p.State(ctx)
		if err != nil {
			return "", fmt.Errorf("read page info: %w", err)
		}
		return fmt.Sprintf("navigated forward to %q", forwardURL), nil
	})
}

// PageReload reloads the page and waits for it to load again. Returns the same
// confirmation string as the MCP page_act reload. The wait runs under the page
// watchdog.
func PageReload(ctx context.Context, p *browsers.Page) (string, error) {
	return browsers.GuardPage(ctx, p, "reload", func(ctx context.Context) (string, error) {
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		page := p.Rod.Context(ctx)
		if err := page.Reload(); err != nil {
			return "", fmt.Errorf("reload: %w", err)
		}
		if err := page.WaitLoad(); err != nil {
			return "", fmt.Errorf("wait for load: %w", err)
		}
		return "reloaded page", nil
	})
}

// PageSetViewport changes the live page's viewport size to w × h CSS pixels
// via the engine seam Page.SetViewport (Emulation.setDeviceMetricsOverride
// with a device scale factor of 1, desktop, no mobile emulation, and matching
// screen metrics — screen.width tracks window.innerWidth). The engine method
// owns the page lock and the nil-Rod guard. Callers validate that w and h are
// positive (the CLI --width/--height flags and the IPC page.act handler do);
// a non-positive value would disable the override per the proto semantics.
// Returns the same confirmation string as the MCP page_act set_viewport.
func PageSetViewport(ctx context.Context, p *browsers.Page, w, h int) (string, error) {
	if err := p.SetViewport(ctx, &browsers.Viewport{Width: w, Height: h}); err != nil {
		return "", err
	}
	return fmt.Sprintf("set viewport to %dx%d", w, h), nil
}

// PageEmulate applies a go-rod device preset (lib/devices, v0.116.2) to the
// live page via the engine seam Page.Emulate — the primary, opt-in emulation
// mechanism (device UA + metrics + touch), so the CLI can switch a live page
// to a mobile/desktop preset without re-opening it. The engine method owns
// the page lock and the nil-Rod guard and resolves the name
// case-insensitively, failing with the pinned unknown-device error. Callers
// pre-validate the name via browsers.ValidateDeviceName (the CLI --device
// flag and the IPC page.act handler do, mirroring the MCP page_act). Returns
// the same confirmation string as the MCP page_act emulate.
func PageEmulate(ctx context.Context, p *browsers.Page, device string) (string, error) {
	if err := p.Emulate(ctx, device); err != nil {
		return "", err
	}
	return fmt.Sprintf("emulated device: %s", device), nil
}

// PageWait blocks until the page satisfies the requested readiness level, or
// sleeps for a fixed duration. It supports the readiness modes shared with
// fetch ("load", "idle", "stable"), an element-level "interactable" mode
// (requires a handle or selector), and the default "ms" mode — a ctx-aware
// sleep honoring timeout that lets in-flight requests and animations settle
// before the next action. The "ms" sleep touches no rod state, so it does not
// take the page lock; every rod-backed mode is fully locked (load/idle/stable
// delegate to WaitForPage, which owns the lock).
func PageWait(
	ctx context.Context,
	p *browsers.Page,
	mode, handle, selector string,
	timeout time.Duration,
) (string, error) {
	// Bound the wait when the caller left ctx without a deadline, then check
	// the page under its operation lock. The lock is released before the mode
	// switch — the "ms" sleep touches no rod state, and the rod-backed modes
	// re-lock (and re-check) themselves below.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultPageOpTimeout)
		defer cancel()
	}
	release, err := p.BeginRead(ctx, "wait")
	if err != nil {
		return "", err
	}
	closed := p.Rod == nil
	release()
	if closed {
		return "", fmt.Errorf("page session is closed")
	}

	switch mode {
	case "", "ms":
		// Backward-compatible default: sleep (ctx-aware) for timeout, so a
		// cancelled context aborts the sleep instead of blocking the caller.
		d := timeout
		if d <= 0 {
			d = defaultActTimeout
		}
		select {
		case <-time.After(d):
			return fmt.Sprintf("waited %s", d), nil
		case <-ctx.Done():
			return "", fmt.Errorf("wait: %w", ctx.Err())
		}

	case "load", "idle", "stable":
		// WaitForPage takes the page lock itself (shared with fetch).
		if err := WaitForPage(ctx, p, mode); err != nil {
			return "", fmt.Errorf("wait: %w", err)
		}
		return fmt.Sprintf("waited for %q", mode), nil

	case "interactable":
		if handle == "" && selector == "" {
			return "", fmt.Errorf("wait: interactable mode requires a selector or handle")
		}
		release, err := p.BeginRead(ctx, "wait")
		if err != nil {
			return "", err
		}
		defer release()
		// Re-check under the lock: a concurrent eviction may have nilled p.Rod
		// since the entry check above released the lock.
		if p.Rod == nil {
			return "", fmt.Errorf("page session is closed")
		}
		el, err := actTarget(p.Rod.Context(ctx), handle, selector)
		if err != nil {
			return "", actionError("wait", handle, selector, err)
		}
		if _, err := el.WaitInteractable(); err != nil {
			return "", targetFailure("wait", handle, selector, err)
		}
		if handle != "" {
			return fmt.Sprintf("element %q is interactable", handle), nil
		}
		return fmt.Sprintf("element %q is interactable", selector), nil

	default:
		return "", fmt.Errorf("wait: unknown wait_mode %q (must be one of load, idle, stable, interactable, ms)", mode)
	}
}

// PageLink is one anchor link collected by PageLinks: its text (the anchor's
// own text or its accessible name) and its absolute href.
type PageLink struct {
	// Text is the anchor's whitespace-normalized text, or its accessible name
	// when it has no text (fallback chain: aria-label → title → img[alt]).
	// Never empty — text-less anchors are filtered out.
	Text string `json:"text"`
	// Href is the anchor's absolute, entity-decoded URL (resolved against the
	// document base).
	Href string `json:"href"`
	// Handle is the handle of the first anchor carrying this href, in the
	// g<document>-b<backendNodeId> form; omitted when the DOM snapshot has no
	// match.
	Handle string `json:"handle,omitempty"`
}

// PageMetaResult is the JSON shape returned by PageMeta: the page title and URL
// plus the collected <meta> tags.
type PageMetaResult struct {
	// Title is the page's document title.
	Title string `json:"title"`
	// URL is the page's current URL.
	URL string `json:"url"`
	// Meta is the ordered list of <meta> tags.
	Meta []PageMetaTag `json:"meta"`
}

// PageMetaTag is one <meta> tag flattened to its meaningful attributes.
// Attributes absent on the tag are dropped via omitempty.
type PageMetaTag struct {
	// Name is the tag's name attribute (e.g. "viewport", "description").
	Name string `json:"name,omitempty"`
	// Property is the tag's property attribute (e.g. OpenGraph "og:title").
	Property string `json:"property,omitempty"`
	// HTTPEquiv is the tag's http-equiv attribute (e.g. "refresh").
	HTTPEquiv string `json:"http-equiv,omitempty"`
	// Content is the tag's content attribute.
	Content string `json:"content,omitempty"`
	// Charset is the tag's charset attribute.
	Charset string `json:"charset,omitempty"`
}

// PageTruncate caps s at maxBytes bytes, cutting on a UTF-8 rune boundary so
// the printed text never contains a half-decoded character. A non-positive
// maxBytes returns s unchanged. A truncation note is appended so the caller
// knows the output was dehydrated. Mirrors the MCP page_fetch truncate.
func PageTruncate(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	// Guard maxBytes > 0 so the walk-back is total even for malformed UTF-8
	// input (a run of orphan continuation bytes could otherwise decrement
	// past zero and index b[-1], panicking).
	for maxBytes > 0 && len(b) > maxBytes && !utf8.RuneStart(b[maxBytes]) {
		maxBytes--
	}
	return string(b[:maxBytes]) + "\n… (output truncated — raise --max-bytes or narrow the selector)"
}
