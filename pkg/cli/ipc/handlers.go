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
// html, text, links, meta (selector scoping html/text; results truncated to
// max_bytes, default pageops.DefaultPageMaxBytes). dom/css/js/screenshot return
// the verbatim "not implemented" -32000 error the CLI produces.
func (s *Server) handlePageFetch(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageFetchParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.SessionID == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.fetch: missing required param "session_id"`}
	}
	// Fail fast on the deferred extractors (and unknown values) so the caller
	// is told the extractor itself is unimplemented, not that the page lookup
	// failed (verbatim parity with pkg/cli/cmd/page.go).
	switch p.What {
	case "html", "text", "links", "meta":
	case "dom", "css", "js", "screenshot":
		return nil, &Error{
			Code:    CodeServerError,
			Message: fmt.Sprintf("page fetch %s: not implemented (engine lacks it; see WEB-18/19/20)", p.What),
		}
	case "":
		return nil, &Error{Code: CodeInvalidParams, Message: `page.fetch: missing required param "what"`}
	default:
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("page.fetch: unknown what=%q (must be one of html, text, links, meta, dom, css, js, screenshot)", p.What),
		}
	}

	b, err := s.mgr.Open(ctx)
	if err != nil {
		return nil, serverError(err)
	}
	page, err := b.Page(browsers.PageID(p.SessionID))
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
	}
	if err != nil {
		return nil, serverError(err)
	}

	maxBytes := p.MaxBytes
	if maxBytes <= 0 {
		maxBytes = pageops.DefaultPageMaxBytes
	}
	return PageFetchResult{Data: pageops.PageTruncate(out, maxBytes)}, nil
}

// handlePageAct implements page.act: only eval is implemented (JS via
// PageEval); every other known action returns the verbatim "not implemented"
// -32000 error the CLI produces, deferred to a follow-up CLI ticket (engine
// interaction methods pending WEB-22/WEB-23). An unknown action is a -32602
// invalid-params error.
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
	// Fail fast on the deferred actions so the caller is told the action itself
	// is unimplemented, not that the page lookup failed (verbatim parity with
	// pkg/cli/cmd/page.go).
	if p.Action != "eval" {
		return nil, &Error{
			Code:    CodeServerError,
			Message: fmt.Sprintf("page act %s: not implemented — deferred to a follow-up CLI ticket (engine interaction methods pending WEB-22/WEB-23)", p.Action),
		}
	}
	if p.Value == "" {
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: "page.act: eval requires a non-empty value (a JS function, e.g. '() => document.title')",
		}
	}

	b, err := s.mgr.Open(ctx)
	if err != nil {
		return nil, serverError(err)
	}
	page, err := b.Page(browsers.PageID(p.SessionID))
	if err != nil {
		return nil, serverError(err)
	}

	out, err := pageops.PageEval(ctx, page, p.Value)
	if err != nil {
		return nil, serverError(err)
	}
	return PageActResult{Data: out}, nil
}

// handlePageClose implements page.close: it closes the page with the given id
// via Manager.Open -> Browser.ClosePage. The session id becomes invalid
// afterwards.
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
