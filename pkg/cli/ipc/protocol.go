// Package ipc implements the web-cli IPC transport: JSON-RPC 2.0 over a unix
// socket with newline-delimited framing (exactly one JSON object per line,
// LF-terminated). The Server is the long-lived half owned by `web-cli serve`;
// the client half (forwarding the other web-cli commands) is the parallel
// ticket WEB-29. The server talks only to pkg/browsers and pkg/cli/pageops —
// never rod directly, never pkg/mcp (AGENTS.md CLI boundary).
package ipc

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/pageops"
)

// browserID is the id reported for the serve process's single Browser,
// mirroring pkg/cli/cmd singleBrowserID and the MCP single-browser id
// (pkg/mcp/tools/browser.go). The Manager owns a single Browser; a
// browser_id → Browser map is the planned growth path (AGENTS.md).
const browserID = "browser-1"

// JSON-RPC method names, shared verbatim with the client side (WEB-29).
const (
	// MethodPing is a liveness probe; it doubles as the stale-socket check.
	MethodPing = "ping"
	// MethodBrowserOpen opens (idempotently) the serve process's Browser.
	MethodBrowserOpen = "browser.open"
	// MethodBrowserClose closes the Browser and all of its pages.
	MethodBrowserClose = "browser.close"
	// MethodBrowserList lists the live Browser and its open pages.
	MethodBrowserList = "browser.list"
	// MethodBrowserFocus raises the live Browser's OS window to the foreground
	// (pure CDP via the rod handle; no external tools).
	MethodBrowserFocus = "browser.focus"
	// MethodPageOpen opens a URL as a new page (minting a persistent page-N id).
	MethodPageOpen = "page.open"
	// MethodPageList lists the Browser's open pages.
	MethodPageList = "page.list"
	// MethodPageFetch extracts any aspect in the shared pageops registry from a live page.
	MethodPageFetch = "page.fetch"
	// MethodPageAct performs an action on a live page (click/input/hover/scroll/select/eval/navigate/back/forward/reload/wait/set_viewport/emulate/clear_logs/sinks_install/sinks_clear/replay/replay_fuzz/inject_canary/postmessage_install/postmessage_clear/post_message/wait_open/intercept_add/intercept_list/intercept_clear/poc_bundle).
	MethodPageAct = "page.act"
	// MethodPageClose closes an open page.
	MethodPageClose = "page.close"
	// MethodServerShutdown requests a graceful stop of the serve process: the
	// server answers {ok: true}, then stops accepting connections and removes
	// the socket file. The browser itself is torn down by the serve process's
	// own teardown path (mgr.Shutdown), not by this method.
	MethodServerShutdown = "server.shutdown"
)

// JSON-RPC error codes (https://www.jsonrpc.org/specification#error_object).
const (
	// CodeParseError: invalid JSON was received by the server (-32700).
	CodeParseError = -32700
	// CodeInvalidRequest: the JSON sent is not a valid Request object (-32600).
	CodeInvalidRequest = -32600
	// CodeMethodNotFound: the method does not exist / is not implemented (-32601).
	CodeMethodNotFound = -32601
	// CodeInvalidParams: invalid method parameter(s) (-32602).
	CodeInvalidParams = -32602
	// CodeServerError: reserved for implementation-defined server errors (-32000).
	CodeServerError = -32000
)

// pageActActions lists page.act's actions in schema order, taken from the
// pageops action table so the handler validation, the unknown-action error and
// every other surface speak one vocabulary.
var pageActActions = pageops.ActionNames()

// ValidPageActAction reports whether action is a supported page.act action.
func ValidPageActAction(action string) bool {
	for _, name := range pageActActions {
		if name == action {
			return true
		}
	}
	return false
}

// PageActActions returns a copy of page.act's supported action names.
func PageActActions() []string {
	return append([]string(nil), pageActActions...)
}

// PageActActionList renders the supported page.act actions for an error message.
func PageActActionList() string {
	return strings.Join(pageActActions, ", ")
}

// Request is a JSON-RPC 2.0 request object. Params is kept raw so each handler
// decodes exactly the params struct of its method; a request with a nil ID is
// a notification and gets no response.
type Request struct {
	// JSONRPC is always "2.0".
	JSONRPC string `json:"jsonrpc"`
	// ID correlates the response with the request; nil means notification.
	ID any `json:"id"`
	// Method names the JSON-RPC method to invoke.
	Method string `json:"method"`
	// Params holds the method's parameters, decoded per method.
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response object: exactly one of Result or Error is
// set. ID echoes the request's id (null when the request could not be parsed).
type Response struct {
	// JSONRPC is always "2.0".
	JSONRPC string `json:"jsonrpc"`
	// ID echoes the request's id (null when unknown, e.g. parse errors).
	ID any `json:"id"`
	// Result is the success payload; omitted on errors.
	Result any `json:"result,omitempty"`
	// Error is the error object; omitted on success.
	Error *Error `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object: a code and an LLM/human-actionable
// message (engine errors surface wrapped with %w).
type Error struct {
	// Code is one of the Code* constants above.
	Code int `json:"code"`
	// Message is a human/LLM-actionable description of the failure.
	Message string `json:"message"`
}

// PingResult is the result of the ping method: always {ok: true}. It doubles
// as the liveness probe for stale-socket detection (no manager involvement).
type PingResult struct {
	// OK is always true for a live server.
	OK bool `json:"ok"`
}

// BrowserOpenParams carries the optional browser.open parameters. Every field
// maps to a browsers.Option applied at create time; headless and stealth join
// the create key, so an explicit value overrides the serve process's manager
// default for the browser this call creates and never mutates a live one.
type BrowserOpenParams struct {
	// Bin is a custom browser executable path or command.
	Bin string `json:"bin,omitempty"`
	// Profile is a profile name resolved under the config dir, or an absolute
	// directory path.
	Profile string `json:"profile,omitempty"`
	// Position is the window position preset: left, right, max or normal.
	Position string `json:"position,omitempty"`
	// Headless requests headless Chromium. It joins the create key, so a
	// headless create and a headful create are separate browsers. Absent keeps
	// the serve process's manager default (headful unless serve was started
	// with --headless).
	Headless *bool `json:"headless,omitempty"`
	// Stealth requests go-rod/stealth (accepted for parity only).
	Stealth *bool `json:"stealth,omitempty"`
	// Owner is an explicit caller-chosen owner label. It joins the create key
	// and is shown by browser.list, so two owners with identical options get
	// separate browsers.
	Owner string `json:"owner,omitempty"`
	// Persist keeps the profile (and its logins) after close. Absent keeps the
	// manager default (ephemeral on the `serve mcp` path).
	Persist *bool `json:"persist,omitempty"`
	// Proxy is an upstream proxy for the new browser: host:port or
	// scheme://host:port. Credentials come only from proxy.json.
	Proxy string `json:"proxy,omitempty"`
	// ProxyBypass is the comma-separated list of hosts that skip the proxy.
	ProxyBypass string `json:"proxy_bypass,omitempty"`
	// IgnoreCerts makes Chromium accept invalid TLS certificates; it is
	// independent of Proxy.
	IgnoreCerts *bool `json:"ignore_certs,omitempty"`
	// Headers are request headers sent with every request from every page of
	// this browser, the first navigation included.
	Headers map[string]string `json:"headers,omitempty"`
	// Scope is the request allowlist: bare hosts (which cover subdomains),
	// leading "*." globs, "host:port" entries, or "*" for all. Empty means no
	// limit unless ScopeStrict is set. It covers navigation, replay, every
	// redirect hop and intercepted subresources.
	Scope []string `json:"scope,omitempty"`
	// ScopeStrict makes an empty allowlist deny every request.
	ScopeStrict *bool `json:"scope_strict,omitempty"`
	// RPS is the per-host sustained request rate. Absent keeps the manager
	// default (DefaultRateRPS).
	RPS *float64 `json:"rps,omitempty"`
	// Burst is the per-host token bucket depth. Absent keeps the manager
	// default (DefaultRateBurst).
	Burst *int `json:"burst,omitempty"`
}

// BrowserOpenResult is the result of browser.open.
type BrowserOpenResult struct {
	// BrowserID identifies the live browser.
	BrowserID string `json:"browser_id"`
	// Owner is the display label recorded at creation ("unowned" when none).
	Owner string `json:"owner"`
	// Persist reports whether the profile survives close.
	Persist bool `json:"persist"`
	// TTL is the idle timeout that closes the browser ("0s" when disabled).
	TTL string `json:"ttl"`
	// Scope is the request allowlist state ("open" when unrestricted).
	Scope string `json:"scope"`
	// ScopeStrict reports whether an empty allowlist denies every request.
	ScopeStrict bool `json:"scope_strict"`
	// Limits is the effective per-host request policy.
	Limits browsers.LimitReport `json:"limits"`
}

// BrowserCloseParams carries the browser.close parameters. purge deletes the
// closed browser's profile; prune closes every browser and removes orphan
// directories under profiles/.ephemeral.
type BrowserCloseParams struct {
	// BrowserID names the browser to close; optional when prune is set.
	BrowserID string `json:"browser_id,omitempty"`
	// Purge deletes the closed browser's profile directory (config-dir guarded).
	Purge bool `json:"purge,omitempty"`
	// Prune closes all browsers and removes orphan ephemeral profiles.
	Prune bool `json:"prune,omitempty"`
}

// BrowserCloseResult is the result of browser.close.
type BrowserCloseResult struct {
	// OK is true once the requested close/cleanup completed.
	OK bool `json:"ok"`
	// Message is the human-readable outcome, byte-identical to the MCP
	// browser_close reply for the same request.
	Message string `json:"message,omitempty"`
}

type BrowserEntry struct {
	BrowserID   string              `json:"browser_id"`
	Owner       string              `json:"owner"`
	Bin         string              `json:"bin,omitempty"`
	ProfileDir  string              `json:"profile_dir,omitempty"`
	Persist     bool                `json:"persist"`
	Ephemeral   bool                `json:"ephemeral"`
	CreatedAt   time.Time           `json:"created_at"`
	IdleFor     string              `json:"idle_for"`
	TTL         string              `json:"ttl"`
	Scope       string              `json:"scope"`
	ScopeStrict bool                `json:"scope_strict"`
	Pages       []browsers.PageInfo `json:"pages"`
}

// BrowserListResult is the result of browser.list: the snapshot of open Browsers
// and their open pages.
type BrowserListResult struct {
	Browsers  []BrowserEntry      `json:"browsers,omitempty"`
	BrowserID string              `json:"browser_id,omitempty"`
	Pages     []browsers.PageInfo `json:"pages,omitempty"`
}

// BrowserFocusParams carries the browser.focus parameters. State is the target
// OS window state; an empty value defaults to normal.
type BrowserFocusParams struct {
	// State is the target window state: normal (default), maximized or
	// fullscreen.
	State string `json:"state,omitempty"`
}

// BrowserFocusResult is the result of browser.focus.
type BrowserFocusResult struct {
	// OK is true once the window state was applied.
	OK bool `json:"ok"`
}

// PageOpenParams carries the page.open parameters. wait ∈ idle|load|stable,
// default idle. viewport optionally overrides the new page's viewport size —
// an explicit override only, applied pre-navigation; nil = plain launch with
// rod's own defaults (no default size is forced). device optionally applies a
// go-rod device preset BEFORE navigation (the primary, opt-in emulation
// mechanism — device UA + metrics govern the load itself).
type PageOpenParams struct {
	// URL is the absolute http(s) URL to open in a new page.
	URL string `json:"url"`
	// BrowserID targets an already-live browser; empty keeps the default
	// browser. page.open never creates the named browser.
	BrowserID string `json:"browser_id,omitempty"`
	// Wait is the readiness level before returning: idle (default), load, stable.
	Wait string `json:"wait,omitempty"`
	// Viewport optionally overrides the new page's viewport size; nil = plain
	// launch, rod's own defaults (explicit override only, never a default).
	Viewport *browsers.Viewport `json:"viewport,omitempty"`
	// Device optionally applies a go-rod device preset (lib/devices, e.g.
	// IPhoneX, IPadPro) to the new page BEFORE navigation; empty = plain
	// launch. When both Device and Viewport are given, Device wins.
	Device string `json:"device,omitempty"`
	// Identity opens the page in an incognito identity inside the target
	// browser. "default" (or empty) uses the browser's own cookie jar; a new
	// name creates an incognito clone in the same Chromium process.
	Identity string `json:"identity,omitempty"`
}

// PageOpenResult is the result of page.open, mirroring the CLI fetch command's
// fetchSummary shape exactly: session_id, title, url, text_snippet, html_bytes,
// load_ms. The session_id is minted by the serve process's live Browser and
// persists across client invocations.
type PageOpenResult = pageops.FetchSummary

// PageListResult is the result of page.list: the Browser's open pages.
type PageListResult struct {
	// Pages is the snapshot of the Browser's open pages.
	Pages []browsers.PageInfo `json:"pages"`
}

// PageFetchParams carries the page.fetch parameters. what is any aspect in the
// shared pageops registry (ValidateAspect is the allow-list); dom element nodes,
// matched links and DOM-backed a11y nodes carry a g<document>-b<backendNodeId>
// handle for page.act; selector scopes html/text/interactives (on interactives
// it selects a subtree, or a child frame when it matches an iframe); filter
// narrows network reads by request type and sinks reads by canary or known
// source; target fetches exactly one
// css/js resource's content, or with what=body selects one observed response by
// seq or URL substring; full_page captures the whole scrollable page in a
// screenshot; max_bytes caps the returned data (default pageops.DefaultPageMaxBytes),
// except for the aspects that self-cap; timeout_ms bounds the whole extraction
// (default 30000, clamped to 1..120000); include_values returns real cookie and
// storage values with what=storage instead of <redacted>. network/console return
// {entries,count,dropped,truncated} over the newest 100 buffered entries;
// capture is cumulative from page creation, so clear with page.act clear_logs
// before the step under observation. what=sinks returns the source-to-sink
// tracer ring {installed,hits,dropped,truncated}; arming it is the separate
// page.act sinks_install action. what=postmessage returns the postMessage
// recorder ring {installed,messages,dropped,truncated,frames}; arming it is the
// separate page.act postmessage_install action and target=clear empties it.
type PageFetchParams struct {
	// SessionID identifies the live page to fetch from.
	SessionID string `json:"session_id"`
	// BrowserID optionally resolves the session inside that browser only.
	BrowserID string `json:"browser_id,omitempty"`
	// Owner optionally restricts the lookup to browsers with that owner.
	Owner string `json:"owner,omitempty"`
	// What is the aspect to extract; see pageops.ValidateAspect for the values.
	What string `json:"what"`
	// Selector optionally scopes html/text/interactives extraction to a CSS
	// match; on interactives it scopes to that element's subtree, or to a child
	// frame when the match is an iframe.
	Selector string `json:"selector,omitempty"`
	// Filter narrows what=network by request type (a comma list where a bare
	// name keeps that type and a "-" prefix drops it) and what=sinks by canary
	// or known source; every other aspect ignores it. A filtered read adds
	// filtered_out to the envelope.
	Filter string `json:"filter,omitempty"`
	// Target is a css/js resource URL, or with what=body a seq number or URL
	// substring selecting one observed response body.
	Target string `json:"target,omitempty"`
	// Since keeps only what=mutations entries newer than this sequence; every
	// other aspect ignores it.
	Since int64 `json:"since,omitempty"`
	// FullPage captures the entire scrollable page instead of the visible
	// viewport (screenshot only).
	FullPage bool `json:"full_page,omitempty"`
	// MaxBytes caps the returned data size (0 → DefaultPageMaxBytes).
	MaxBytes int `json:"max_bytes,omitempty"`
	// TimeoutMS bounds the whole extraction in milliseconds (0 → 30000,
	// clamped to 1..120000). page.fetch gets a 180s transport budget so a
	// raised value survives the daemon.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// IncludeValues returns real cookie and storage values with what=storage
	// instead of the literal <redacted>; it exposes live session tokens.
	IncludeValues bool `json:"include_values,omitempty"`
}

// PageFetchResult is the result of page.fetch.
type PageFetchResult struct {
	// Data holds the extracted content (truncated to max_bytes when needed).
	Data string `json:"data"`
}

// PageActParams carries the page.act parameters. action ∈ click|input|type|
// press_key|click_at|hover_at|drag_and_drop|upload_file|eval|navigate|back|
// forward|reload|hover|scroll|select|wait|set_viewport|emulate|clear_logs|
// sinks_install|sinks_clear|replay|replay_fuzz|inject_canary|postmessage_install|postmessage_clear|
// post_message|intercept_add|intercept_list|intercept_clear|auth_save|auth_restore|auth_list|poc_bundle; selector/handle/value/key/button/click_count/x/y/
// target_x/target_y/target_selector/origin/paths/targets are the per-action
// payloads (handle wins over selector for element actions); viewport carries the
// target size for set_viewport and device the preset for emulate; timeout_ms
// bounds the action (default 30000) and sizes the wait action's "ms" sleep;
// wait_mode selects what the wait action waits for: load|idle|stable|interactable|ms
// (default ms). replay resends one captured network entry: target selects it by
// seq or URL substring, method/headers/query override the captured request
// (query edits URL parameters, an empty value removes one), value
// replaces the captured body when value_set is true (or value is non-empty), and
// max_bytes caps the returned body. replay_fuzz runs a bounded matrix over that
// captured entry: points/payloads/strategy define the variants, the size is
// computed and refused before any request (100-request bound), and the result
// is the engine's {runs,summary} envelope. clear_logs, sinks_install, sinks_clear, postmessage_install and
// postmessage_clear take no required payload: clear_logs empties both log
// buffers and returns the cleared counts; sinks_install arms the source-to-sink
// tracer and returns {"installed":true}; sinks_clear empties its ring and
// returns {"cleared":N}; inject_canary takes optional value (custom canary) and
// targets and returns the canary report; postmessage_install arms the
// postMessage recorder and returns {"installed":true}; postmessage_clear empties
// its rings and returns {"cleared":N}; post_message takes value (the payload),
// optional target_selector ("top" or an iframe CSS selector) and origin (target
// origin, default "*") and returns {origin,target}. intercept_add takes rules (a
// JSON array of {id,url_pattern,method,action,headers,status,body,delay_ms}) and
// returns the active rules plus a count; intercept_list returns them redacted;
// intercept_clear removes every rule and returns {rules_removed,active}.
type PageActParams struct {
	// SessionID identifies the live page to act on.
	SessionID string `json:"session_id"`
	// BrowserID optionally resolves the session inside that browser only.
	BrowserID string `json:"browser_id,omitempty"`
	// Owner optionally restricts the lookup to browsers with that owner.
	Owner string `json:"owner,omitempty"`
	// Action is the action to perform (including clear_logs, sinks_install,
	// sinks_clear and wait_open, which take no payload; clear_logs returns
	// {"network":N,"console":M,"websocket":K}, sinks_install {"installed":true}, sinks_clear
	// {"cleared":N} and wait_open {session_id,url,opener}).
	Action string `json:"action"`
	// Value is the payload: a JS function for eval, text for input/type, URL for
	// navigate, an option value for select, fallback key for press_key, single
	// path for upload_file, or the replacement request body for replay.
	Value string `json:"value,omitempty"`
	// ValueSet marks Value as explicitly supplied, so replay distincts an
	// absent body from an intentionally empty one. Empty Value with ValueSet
	// false keeps the captured replay body.
	ValueSet bool `json:"value_set,omitempty"`
	// Target selects the captured network entry replay resends or replay_fuzz
	// runs its matrix over: a seq number or a substring of its request URL.
	Target string `json:"target,omitempty"`
	// Method overrides the captured request method for replay.
	Method string `json:"method,omitempty"`
	// Headers overrides captured request headers for replay; each named header
	// replaces its captured value.
	Headers map[string]string `json:"headers,omitempty"`
	// Query edits the captured URL's query parameters for replay: a non-empty
	// value replaces the parameter and an empty value removes it. Every other
	// parameter keeps its captured value.
	Query map[string]string `json:"query,omitempty"`
	// Points are the replay_fuzz injection points: each param is injected at its
	// location (query, body, header or cookie) in the captured request.
	Points []browsers.FuzzPoint `json:"points,omitempty"`
	// Payloads are the replay_fuzz values injected at each point, in plan order.
	Payloads []string `json:"payloads,omitempty"`
	// Strategy selects how replay_fuzz pairs points with payloads: sniper (one
	// point at a time), pitchfork (nth point with nth payload) or clusterbomb
	// (every combination). Empty defaults to sniper.
	Strategy browsers.FuzzStrategy `json:"strategy,omitempty"`
	// MaxBytes caps the replay response body in bytes; 0 uses the engine's
	// 256 KB ceiling and larger values are clamped to it. With replay_fuzz it
	// caps each run body.
	MaxBytes int64 `json:"max_bytes,omitempty"`
	// Selector targets an element for element actions.
	Selector string `json:"selector,omitempty"`
	// Handle is an element handle from a previous page.fetch
	// (what=interactives|dom|links|a11y), e.g. "g3k9x2ab-b6". It carries the document
	// that minted it, so a handle from a previous document is rejected as stale.
	// It wins over Selector when both are set.
	Handle string `json:"handle,omitempty"`
	// Key is the key or key combination for press_key (e.g. Enter, Escape, Control+A).
	Key string `json:"key,omitempty"`
	// Button selects the mouse button for click/click_at: left (default), right, middle.
	Button string `json:"button,omitempty"`
	// ClickCount is the number of clicks for click/click_at (1 for single click, 2 for double click).
	ClickCount int `json:"click_count,omitempty"`
	// X is the horizontal offset for scroll/click_at/hover_at/drag_and_drop source.
	X float64 `json:"x,omitempty"`
	// Y is the vertical offset for scroll/click_at/hover_at/drag_and_drop source.
	Y float64 `json:"y,omitempty"`
	// TargetX is the target horizontal coordinate for drag_and_drop.
	TargetX float64 `json:"target_x,omitempty"`
	// TargetY is the target vertical coordinate for drag_and_drop.
	TargetY float64 `json:"target_y,omitempty"`
	// TargetSelector is the target element selector for drag_and_drop, or the
	// destination frame for post_message: "top" (or empty) sends to
	// window.top, a CSS selector naming an iframe sends to its contentWindow.
	TargetSelector string `json:"target_selector,omitempty"`
	// Origin is the target origin for post_message (default "*").
	Origin string `json:"origin,omitempty"`
	// Paths is the list of local file paths for upload_file.
	Paths []string `json:"paths,omitempty"`
	// Targets is the list of canary injection points for inject_canary:
	// query, hash, postmessage, name (empty means all four).
	Targets []string `json:"targets,omitempty"`
	// Raw disables humanized pointer and keyboard timing for this call, carrying
	// the CLI client's intent to the serve process (which cannot see the
	// client's environment). Absent keeps the serve process's default: its own
	// WEB_MCP_RAW_ACTIONS environment hatch or the browser's configured raw mode.
	Raw bool `json:"raw,omitempty"`
	// TimeoutMS bounds the action in milliseconds (0 → server default 30000),
	// exactly as it does over MCP; the wait action's "ms" mode sleeps for this
	// long. Behind the daemon the socket read carries a separate grace, so a
	// completed action is never reported as a transport timeout — the action
	// itself never gets extra time.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// WaitMode selects the readiness level the wait action waits for:
	// "load" | "idle" | "stable" | "interactable" | "ms" (default "ms").
	WaitMode string `json:"wait_mode,omitempty"`
	// Viewport is the target viewport size for the set_viewport action
	// (required when action is "set_viewport").
	Viewport *browsers.Viewport `json:"viewport,omitempty"`
	// Device is the go-rod device preset for the emulate action (required when
	// action is "emulate").
	Device string `json:"device,omitempty"`
	// Site scopes auth_save/auth_restore and optionally filters auth_list.
	Site string `json:"site,omitempty"`
	// Name is the snapshot name for auth_save/auth_restore.
	Name string `json:"name,omitempty"`
	// Force lets auth_restore proceed across a user-agent mismatch.
	Force bool `json:"force,omitempty"`
	// Rules are the request rules for intercept_add: each matches requests in
	// order, the first match wins, and a request matching none continues.
	Rules []browsers.InterceptRule `json:"rules,omitempty"`
}

// FuzzSpec projects the replay_fuzz wire fields onto the engine's FuzzSpec.
// checkScope authorizes each URL before a variant request leaves.
func (p PageActParams) FuzzSpec(checkScope func(rawURL string) error) browsers.FuzzSpec {
	return browsers.FuzzSpec{
		Target:     p.Target,
		Strategy:   p.Strategy,
		Points:     p.Points,
		Payloads:   p.Payloads,
		MaxBytes:   p.MaxBytes,
		CheckScope: checkScope,
	}
}

// PageActResult is the result of page.act.
type PageActResult struct {
	// Data holds the action's output (e.g. the JSON result of eval).
	Data string `json:"data"`
}

// PageCloseParams carries the page.close parameters.
type PageCloseParams struct {
	// SessionID identifies the page to close.
	SessionID string `json:"session_id"`
	// BrowserID optionally resolves the session inside that browser only.
	BrowserID string `json:"browser_id,omitempty"`
	// Owner optionally restricts the lookup to browsers with that owner.
	Owner string `json:"owner,omitempty"`
}

// PageCloseResult is the result of page.close.
type PageCloseResult struct {
	// OK is true once the page is closed and its id invalidated.
	OK bool `json:"ok"`
}

// ShutdownResult is the result of server.shutdown: {ok: true} once the graceful
// stop has been requested (the serve loop exits shortly after the response is
// flushed).
type ShutdownResult struct {
	// OK is always true for a successful shutdown request.
	OK bool `json:"ok"`
}
