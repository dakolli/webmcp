package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/pageops"
)

// manager is the minimal engine surface the JSON-RPC handlers dispatch
// against. *browsers.Manager satisfies it (proven by the compile-time
// assertion below); the package tests inject a stub implementation instead,
// so no handler path in a test can reach a real Chromium. Keeping the seam
// internal — and limited to the three methods the handlers actually use —
// means a future Manager refactor is caught at compile time without widening
// the public API (NewServer still accepts *browsers.Manager).
type manager interface {
	// Open lazily launches the single Browser on first use and returns it.
	Open(ctx context.Context) (*browsers.Browser, error)
	// Close closes the Browser and all of its pages (a no-op when none open).
	Close(ctx context.Context) error
	// Browser returns the live Browser, or an error when none is open yet.
	Browser() (*browsers.Browser, error)
	// Page resolves the live page with the given ID, opening the Browser if
	// needed; it surfaces restart-aware errors for pre-restart session ids.
	Page(ctx context.Context, id browsers.PageID) (*browsers.Page, error)
}

// compile-time proof that the concrete Manager satisfies the handler seam.
var _ manager = (*browsers.Manager)(nil)

// handle dispatches a decoded request to its method handler. Unknown methods
// get a -32601 error naming the method; handlers return (result, nil) on
// success and (nil, *Error) on JSON-RPC failure.
func (s *Server) handle(ctx context.Context, req Request) (any, *Error) {
	switch req.Method {
	case MethodPing:
		return PingResult{OK: true}, nil
	case MethodBrowserOpen:
		return s.handleBrowserOpen(ctx, req.Params)
	case MethodBrowserClose:
		return s.handleBrowserClose(ctx, req.Params)
	case MethodBrowserList:
		return s.handleBrowserList(ctx, req.Params)
	case MethodPageOpen:
		return s.handlePageOpen(ctx, req.Params)
	case MethodPageList:
		return s.handlePageList(ctx, req.Params)
	case MethodPageFetch:
		return s.handlePageFetch(ctx, req.Params)
	case MethodPageAct:
		return s.handlePageAct(ctx, req.Params)
	case MethodPageClose:
		return s.handlePageClose(ctx, req.Params)
	case MethodServerShutdown:
		return s.handleServerShutdown(ctx, req.Params)
	default:
		return nil, &Error{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		}
	}
}

// handleBrowserOpen implements browser.open: it lazily opens the manager's
// single Browser (idempotent — Manager.Open returns the already-running
// browser) and reports browser-1.
//
// Contract-parity note: the headless/stealth params are accepted and validated
// (a non-boolean value fails with -32602) but CANNOT change the running
// browser — Manager options are construction-time
// (browsers.NewManager(WithHeadless, WithStealth)); the effective config is
// fixed by `web-cli serve --headless/--stealth`. The client-side launch flow
// must pass the flags to serve itself, not rely on these params.
func (s *Server) handleBrowserOpen(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[BrowserOpenParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	// p.Headless/p.Stealth are accepted for wire-contract parity only.
	_ = p

	if _, err := s.mgr.Open(ctx); err != nil {
		return nil, serverError(err)
	}
	return BrowserOpenResult{BrowserID: browserID}, nil
}

// handleBrowserClose implements browser.close: it closes the manager's Browser
// and all of its pages (an idempotent no-op when nothing is open). After close,
// page.* methods fail with an actionable "no browser open" error from the
// engine.
func (s *Server) handleBrowserClose(ctx context.Context, params json.RawMessage) (any, *Error) {
	if err := s.mgr.Close(ctx); err != nil {
		return nil, serverError(err)
	}
	return BrowserCloseResult{OK: true}, nil
}

// handleBrowserList implements browser.list: it reports the live Browser's id
// and its open pages. It never lazily launches the browser (Manager.Browser is
// a non-blocking in-memory read, mirroring the CLI browser list command): with
// no browser open it fails with an actionable -32000 error telling the caller
// to run browser.open first.
func (s *Server) handleBrowserList(ctx context.Context, params json.RawMessage) (any, *Error) {
	b, err := s.mgr.Browser() // no lazy launch
	if err != nil {
		return nil, &Error{
			Code:    CodeServerError,
			Message: "browser.list: no browser open; call browser.open first",
		}
	}
	return BrowserListResult{BrowserID: browserID, Pages: b.Pages()}, nil
}

// handlePageOpen implements page.open: it validates the URL and wait strategy
// (-32602 on failure), opens the URL as a new page in the live browser (minting
// a fresh page-N id that persists for the life of the serve process), waits for
// the requested readiness level (idle by default) and returns the full
// fetchSummary shape.
func (s *Server) handlePageOpen(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageOpenParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.URL == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.open: missing required param "url"`}
	}
	if err := pageops.ValidateURL(p.URL); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("page.open: %v", err)}
	}
	wait := p.Wait
	if wait == "" {
		wait = "idle" // default readiness
	}
	if err := pageops.ValidateWait(wait); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("page.open: %v", err)}
	}

	start := time.Now() // load timing for the summary
	b, err := s.mgr.Open(ctx)
	if err != nil {
		return nil, serverError(err)
	}
	page, err := b.NewPage(ctx, p.URL)
	if err != nil {
		return nil, serverError(err)
	}
	if err := pageops.WaitForPage(ctx, page, wait); err != nil {
		return nil, serverError(err)
	}
	summary, err := pageops.PageSummary(ctx, page, start)
	if err != nil {
		return nil, serverError(err)
	}
	return PageOpenResult(summary), nil
}

// handlePageList implements page.list: it reports the Browser's open pages. The
// browser is lazily opened so the method works even when nothing was opened
// explicitly (mirroring the CLI page list command).
func (s *Server) handlePageList(ctx context.Context, params json.RawMessage) (any, *Error) {
	b, err := s.mgr.Open(ctx)
	if err != nil {
		return nil, serverError(err)
	}
	return PageListResult{Pages: b.Pages()}, nil
}

// handlePageFetch implements page.fetch: it extracts an aspect of a live page —
// html, text, links, meta (selector scoping html/text), dom (nested DOM JSON),
// css/js (resource list or one target resource), screenshot (base64 PNG
// data-URI; full_page captures the whole page) — mirroring the CLI page fetch
// command. Results are truncated to max_bytes (default
// pageops.DefaultPageMaxBytes) except screenshots, which bypass truncation
// (mirrors the CLI/MCP truncation exception). An unknown extractor is a -32602
// invalid-params error.
func (s *Server) handlePageFetch(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageFetchParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.SessionID == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.fetch: missing required param "session_id"`}
	}
	switch p.What {
	case "html", "text", "links", "meta", "dom", "css", "js", "screenshot":
	case "":
		return nil, &Error{Code: CodeInvalidParams, Message: `page.fetch: missing required param "what"`}
	default:
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("page.fetch: unknown what=%q (must be one of html, text, links, meta, dom, css, js, screenshot)", p.What),
		}
	}

	page, err := s.mgr.Page(ctx, browsers.PageID(p.SessionID)) // opens the Browser if needed; probes for liveness
	if err != nil {
		return nil, serverError(err)
	}

	var out string
	switch p.What {
	case "html":
		out, err = pageops.PageHTML(ctx, page, p.Selector)
	case "text":
		out, err = pageops.PageText(ctx, page, p.Selector)
	case "links":
		out, err = pageops.PageLinks(ctx, page)
	case "meta":
		out, err = pageops.PageMeta(ctx, page)
	case "dom":
		out, err = pageops.PageDOM(ctx, page)
	case "css":
		out, err = pageops.PageCSS(ctx, page, p.Target)
	case "js":
		out, err = pageops.PageJS(ctx, page, p.Target)
	case "screenshot":
		out, err = pageops.PageScreenshot(ctx, page, p.FullPage)
	}
	if err != nil {
		return nil, serverError(err)
	}

	// Screenshots bypass truncate: the data-URI is binary-as-text and cutting
	// it mid-stream would produce an undecodable image. Image size scales with
	// the viewport (or full-page height), not max_bytes (mirrors the CLI and
	// MCP truncation exception).
	if p.What == "screenshot" {
		return PageFetchResult{Data: out}, nil
	}

	maxBytes := p.MaxBytes
	if maxBytes <= 0 {
		maxBytes = pageops.DefaultPageMaxBytes
	}
	return PageFetchResult{Data: pageops.PageTruncate(out, maxBytes)}, nil
}

// handlePageAct implements page.act: it performs the requested action on a
// live page — click, input, hover, scroll, select, eval, navigate, back,
// forward, reload or wait — mirroring the CLI page act command. type is
// accepted for parity but stays deferred (the MCP page_act type action is
// still stubbed), returning a -32000 "not implemented" error. An unknown
// action is a -32602 invalid-params error.
func (s *Server) handlePageAct(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageActParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.SessionID == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.act: missing required param "session_id"`}
	}
	if p.Action == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.act: missing required param "action"`}
	}
	switch p.Action {
	case "click", "input", "type", "eval", "navigate", "back", "forward", "reload", "hover", "scroll", "select", "wait":
	default:
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("page.act: unknown action=%q (must be one of click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait)", p.Action),
		}
	}
	// type stays deferred (MCP parity — the MCP page_act type action is still
	// stubbed): reject it before touching the page with the same "not
	// implemented" surface as the CLI.
	if p.Action == "type" {
		return nil, &Error{
			Code:    CodeServerError,
			Message: "page.act type: not implemented",
		}
	}
	if p.Action == "eval" && p.Value == "" {
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: "page.act: eval requires a non-empty --value (a JS function, e.g. '() => document.title')",
		}
	}
	if p.Action == "wait" {
		if err := pageops.ValidateWait(p.WaitMode); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("page.act: %v", err)}
		}
	}

	page, err := s.mgr.Page(ctx, browsers.PageID(p.SessionID)) // opens the Browser if needed; probes for liveness
	if err != nil {
		return nil, serverError(err)
	}

	var out string
	switch p.Action {
	case "click":
		out, err = pageops.PageClick(ctx, page, p.Selector)
	case "input":
		out, err = pageops.PageInput(ctx, page, p.Selector, p.Value)
	case "eval":
		out, err = pageops.PageEval(ctx, page, p.Value)
	case "navigate":
		out, err = pageops.PageNavigate(ctx, page, p.Value)
	case "back":
		out, err = pageops.PageBack(ctx, page)
	case "forward":
		out, err = pageops.PageForward(ctx, page)
	case "reload":
		out, err = pageops.PageReload(ctx, page)
	case "hover":
		out, err = pageops.PageHover(ctx, page, p.Selector)
	case "scroll":
		out, err = pageops.PageScroll(ctx, page, p.X, p.Y)
	case "select":
		out, err = pageops.PageSelect(ctx, page, p.Selector, p.Value)
	case "wait":
		out, err = pageops.PageWait(ctx, page, p.WaitMode, p.Selector, time.Duration(p.TimeoutMS)*time.Millisecond)
	}
	if err != nil {
		return nil, serverError(err)
	}
	return PageActResult{Data: out}, nil
}

// handlePageClose implements page.close: it resolves the page with the given id
// via Manager.Page (surfacing restart-aware errors for pre-restart sessions),
// then closes it via Manager.Open -> Browser.ClosePage. The session id becomes
// invalid afterwards.
func (s *Server) handlePageClose(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageCloseParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.SessionID == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.close: missing required param "session_id"`}
	}

	b, err := s.mgr.Open(ctx)
	if err != nil {
		return nil, serverError(err)
	}
	// Resolve through the manager first so a pre-restart session id surfaces
	// the explicit "browser was restarted" error (mirrors the MCP
	// session_close tool).
	if _, err := s.mgr.Page(ctx, browsers.PageID(p.SessionID)); err != nil {
		return nil, serverError(err)
	}
	if err := b.ClosePage(ctx, browsers.PageID(p.SessionID)); err != nil {
		return nil, serverError(err)
	}
	return PageCloseResult{OK: true}, nil
}

// handleServerShutdown implements server.shutdown: it returns {ok: true} and
// asks the serve loop to stop gracefully. The manager is deliberately NOT
// closed here — the serve process's own teardown path (mgr.Shutdown in
// runServeForeground) shuts the browser down after Serve returns. The loop
// stop is triggered by dispatch only after the response line is flushed, so
// the client reliably sees the result before the connection closes.
func (s *Server) handleServerShutdown(_ context.Context, _ json.RawMessage) (any, *Error) {
	return ShutdownResult{OK: true}, nil
}

// decodeParams unmarshals a method's params into T. An absent params object is
// treated as the zero value of T (for methods whose params are all optional);
// an unmarshal failure becomes a -32602 invalid-params error naming the
// offending field (encoding/json reports it in the error text).
func decodeParams[T any](params json.RawMessage) (*T, *Error) {
	var p T
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &Error{
				Code:    CodeInvalidParams,
				Message: fmt.Sprintf("invalid params: %v", err),
			}
		}
	}
	return &p, nil
}

// serverError wraps an engine error into a JSON-RPC server error (-32000),
// keeping the wrapped %w message chain intact so it stays human/LLM-actionable.
func serverError(err error) *Error {
	return &Error{Code: CodeServerError, Message: err.Error()}
}
