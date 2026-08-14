// Package ipc implements the web-cli IPC transport: JSON-RPC 2.0 over a unix
// socket with newline-delimited framing (exactly one JSON object per line,
// LF-terminated). The Server is the long-lived half owned by `web-cli serve`;
// the client half (forwarding the other web-cli commands) is the parallel
// ticket WEB-29. The server talks only to pkg/browsers and pkg/cli/pageops —
// never rod directly, never pkg/mcp (AGENTS.md CLI boundary).
package ipc

import (
	"encoding/json"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/pageops"
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
	// MethodPageOpen opens a URL as a new page (minting a persistent page-N id).
	MethodPageOpen = "page.open"
	// MethodPageList lists the Browser's open pages.
	MethodPageList = "page.list"
	// MethodPageFetch extracts an aspect of a live page (html/dom/text/css/js/links/screenshot/meta).
	MethodPageFetch = "page.fetch"
	// MethodPageAct performs an action on a live page (click/input/hover/scroll/select/eval/navigate/back/forward/reload/wait).
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

// BrowserOpenParams carries the optional browser.open parameters. headless and
// stealth are accepted and validated for wire-contract parity but CANNOT change
// the running browser — Manager options are construction-time
// (browsers.NewManager(WithHeadless, WithStealth)); the effective config is
// fixed by `web-cli serve --headless/--stealth`. The client-side launch flow
// must pass the flags to serve itself, not rely on these params.
type BrowserOpenParams struct {
	// Headless requests headless Chromium (accepted for parity only).
	Headless *bool `json:"headless,omitempty"`
	// Stealth requests go-rod/stealth (accepted for parity only).
	Stealth *bool `json:"stealth,omitempty"`
}

// BrowserOpenResult is the result of browser.open.
type BrowserOpenResult struct {
	// BrowserID identifies the serve process's single Browser.
	BrowserID string `json:"browser_id"`
}

// BrowserCloseResult is the result of browser.close.
type BrowserCloseResult struct {
	// OK is true once the Browser and its pages are closed.
	OK bool `json:"ok"`
}

// BrowserListResult is the result of browser.list: the Browser's id and its
// open pages (reusing browsers.PageInfo, JSON-tagged id/url).
type BrowserListResult struct {
	// BrowserID identifies the live Browser.
	BrowserID string `json:"browser_id"`
	// Pages is the snapshot of the Browser's open pages.
	Pages []browsers.PageInfo `json:"pages"`
}

// PageOpenParams carries the page.open parameters. wait ∈ idle|load|stable,
// default idle.
type PageOpenParams struct {
	// URL is the absolute http(s) URL to open in a new page.
	URL string `json:"url"`
	// Wait is the readiness level before returning: idle (default), load, stable.
	Wait string `json:"wait,omitempty"`
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

// PageFetchParams carries the page.fetch parameters. what ∈ html|dom|text|css|
// js|links|screenshot|meta; selector scopes html/text; target fetches exactly
// one css/js resource's content instead of the {url,type,size} list; full_page
// captures the whole scrollable page in a screenshot; max_bytes caps the
// returned data (default pageops.DefaultPageMaxBytes; screenshots are never
// truncated).
type PageFetchParams struct {
	// SessionID identifies the live page to fetch from.
	SessionID string `json:"session_id"`
	// What is the aspect to extract: html, dom, text, css, js, links,
	// screenshot, meta.
	What string `json:"what"`
	// Selector optionally scopes html/text extraction to a CSS match.
	Selector string `json:"selector,omitempty"`
	// Target is a resource URL that, when set, makes what=css|js fetch exactly
	// that one resource's content instead of listing the page's resources.
	Target string `json:"target,omitempty"`
	// FullPage captures the entire scrollable page instead of the visible
	// viewport (screenshot only).
	FullPage bool `json:"full_page,omitempty"`
	// MaxBytes caps the returned data size (0 → DefaultPageMaxBytes).
	MaxBytes int `json:"max_bytes,omitempty"`
}

// PageFetchResult is the result of page.fetch.
type PageFetchResult struct {
	// Data holds the extracted content (truncated to max_bytes when needed).
	Data string `json:"data"`
}

// PageActParams carries the page.act parameters. action ∈ click|input|type|
// eval|navigate|back|forward|reload|hover|scroll|select|wait (type is accepted
// for parity but returns "not implemented"); selector/value/x/y are the
// per-action payloads; timeout_ms bounds the action (default 30000) and sizes
// the wait action's "ms" sleep; wait_mode selects what the wait action waits
// for: load|idle|stable|interactable|ms (default ms).
type PageActParams struct {
	// SessionID identifies the live page to act on.
	SessionID string `json:"session_id"`
	// Action is the action to perform.
	Action string `json:"action"`
	// Value is the payload: a JS function for eval, text for input, URL for
	// navigate, an option value for select.
	Value string `json:"value,omitempty"`
	// Selector targets an element for element actions.
	Selector string `json:"selector,omitempty"`
	// X is the horizontal offset for scroll.
	X float64 `json:"x,omitempty"`
	// Y is the vertical offset for scroll.
	Y float64 `json:"y,omitempty"`
	// TimeoutMS bounds the action in milliseconds (0 → server default 30000);
	// the wait action's "ms" mode sleeps for this long.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// WaitMode selects the readiness level the wait action waits for:
	// "load" | "idle" | "stable" | "interactable" | "ms" (default "ms").
	WaitMode string `json:"wait_mode,omitempty"`
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
