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
	Open(ctx context.Context) (*browsers.Browser, error)
	OpenBrowser(ctx context.Context, id browsers.BrowserID) (*browsers.Browser, error)
	CreateBrowser(ctx context.Context, opts ...browsers.Option) (browsers.BrowserID, *browsers.Browser, error)
	Close(ctx context.Context) error
	CloseBrowser(ctx context.Context, id browsers.BrowserID) error
	Browser(id browsers.BrowserID) (*browsers.Browser, error)
	Browsers() map[browsers.BrowserID]*browsers.Browser
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
	case MethodBrowserFocus:
		return s.handleBrowserFocus(ctx, req.Params)
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

func (s *Server) handleBrowserOpen(ctx context.Context, params json.RawMessage) (any, *Error) {
	var p struct {
		Bin         string `json:"bin,omitempty"`
		Profile     string `json:"profile,omitempty"`
		Position    string `json:"position,omitempty"`
		Proxy       string `json:"proxy,omitempty"`
		ProxyBypass string `json:"proxy_bypass,omitempty"`
		Headless    *bool  `json:"headless,omitempty"`
		Stealth     *bool  `json:"stealth,omitempty"`
		Persist     *bool  `json:"persist,omitempty"`
		IgnoreCerts *bool  `json:"ignore_certs,omitempty"`
	}
	if len(params) > 0 && string(params) != "null" {
		_ = json.Unmarshal(params, &p)
	}

	var opts []browsers.Option
	if p.Bin != "" {
		opts = append(opts, browsers.WithBin(p.Bin))
	}
	if p.Profile != "" {
		opts = append(opts, browsers.WithProfileName(p.Profile))
	}
	if p.Persist != nil {
		opts = append(opts, browsers.WithPersist(*p.Persist))
	}
	if p.Position != "" {
		opts = append(opts, browsers.WithPosition(p.Position))
	}
	if p.Proxy != "" {
		opts = append(opts, browsers.WithProxy(p.Proxy))
	}
	if p.ProxyBypass != "" {
		opts = append(opts, browsers.WithProxyBypass(p.ProxyBypass))
	}
	if p.IgnoreCerts != nil {
		opts = append(opts, browsers.WithIgnoreCerts(*p.IgnoreCerts))
	}
	if p.Headless != nil {
		opts = append(opts, browsers.WithHeadless(*p.Headless))
	}
	if p.Stealth != nil {
		opts = append(opts, browsers.WithStealth(*p.Stealth))
	}

	if len(opts) > 0 {
		id, _, err := s.mgr.CreateBrowser(ctx, opts...)
		if err != nil {
			return nil, serverError(err)
		}
		return BrowserOpenResult{BrowserID: string(id)}, nil
	}

	b, err := s.mgr.Open(ctx)
	if err != nil {
		return nil, serverError(err)
	}
	return BrowserOpenResult{BrowserID: string(b.ID())}, nil
}

func (s *Server) handleBrowserClose(ctx context.Context, params json.RawMessage) (any, *Error) {
	var p struct {
		BrowserID string `json:"browser_id,omitempty"`
	}
	if len(params) > 0 && string(params) != "null" {
		_ = json.Unmarshal(params, &p)
	}
	if err := s.mgr.CloseBrowser(ctx, browsers.BrowserID(p.BrowserID)); err != nil {
		return nil, serverError(err)
	}
	return BrowserCloseResult{OK: true}, nil
}

func (s *Server) handleBrowserList(ctx context.Context, params json.RawMessage) (any, *Error) {
	active := s.mgr.Browsers()
	if len(active) == 0 {
		return nil, &Error{
			Code:    CodeServerError,
			Message: "browser.list: no browser open; call browser.open first",
		}
	}
	var list []BrowserEntry
	for id, b := range active {
		list = append(list, BrowserEntry{
			BrowserID:  string(id),
			Bin:        b.BinPath(),
			ProfileDir: b.ProfileDir(),
			Pages:      b.Pages(),
		})
	}
	res := BrowserListResult{Browsers: list}
	if len(list) > 0 {
		res.BrowserID = list[0].BrowserID
		res.Pages = list[0].Pages
	}
	return res, nil
}

// handleBrowserFocus implements browser.focus: it raises the serve process's
// Browser OS window to the foreground via pure CDP on the manager's live rod
// handle (Browser.getWindowForTarget + Browser.setWindowBounds — no external
// tools). It never lazily launches the browser (mirroring handleBrowserList):
// with no browser open it fails with an actionable -32000 error telling the
// caller to run browser.open first. state ∈ normal|maximized|fullscreen, empty
// → normal; an unknown state is a -32602 invalid-params error naming the
// offender.
func (s *Server) handleBrowserFocus(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[BrowserFocusParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	state := p.State
	if state == "" {
		state = string(browsers.WindowStateNormal) // default
	}
	switch browsers.WindowState(state) {
	case browsers.WindowStateNormal, browsers.WindowStateMaximized, browsers.WindowStateFullscreen:
	default:
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("browser.focus: unknown state=%q (must be one of normal, maximized, fullscreen)", p.State),
		}
	}

	b, err := s.mgr.Browser("") // no lazy launch
	if err != nil {
		return nil, &Error{
			Code:    CodeServerError,
			Message: "browser.focus: no browser open; call browser.open first",
		}
	}
	if err := b.Focus(ctx, browsers.WindowState(state)); err != nil {
		return nil, serverError(err)
	}
	return BrowserFocusResult{OK: true}, nil
}

// handlePageOpen implements page.open: it validates the URL and wait strategy
// (-32602 on failure), opens the URL as a new page in the target browser (the
// explicit browser_id when given, else the default browser), mints a fresh
// page-N id that persists for the life of the serve process, waits for the
// requested readiness level (idle by default) and returns the full
// fetchSummary shape. An unknown browser_id is a -32000 error naming the live
// ids; page.open never creates the named browser.
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
	// A viewport override must be positive when given (nil = plain launch,
	// rod's own defaults).
	if p.Viewport != nil && (p.Viewport.Width <= 0 || p.Viewport.Height <= 0) {
		return nil, &Error{Code: CodeInvalidParams, Message: "page.open: viewport width and height must be positive"}
	}
	// Pre-flight the device preset (same fail-fast doctrine as the MCP
	// fetch_page validate): an unknown name fails with the exact pinned
	// unknown-device error before any browser work.
	if p.Device != "" {
		if err := browsers.ValidateDeviceName(p.Device); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("page.open: %v", err)}
		}
	}

	start := time.Now() // load timing for the summary
	b, err := s.targetBrowser(ctx, p.BrowserID)
	if err != nil {
		return nil, serverError(err)
	}
	// NewPage applies the device preset pre-navigation (governs the load
	// itself); page.open has no user_agent param (MCP-only).
	page, err := b.NewPage(ctx, p.URL, p.Viewport, p.Device, "")
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

// handlePageFetch implements page.fetch: it reads any aspect of a live page
// through the shared pageops aspect registry — html, text, links, meta, dom,
// css/js (resource list or one target resource), screenshot, interactives, a11y,
// network/console (bounded log envelopes), body (one stored response body),
// article and marks. The registry owns both the accepted what values and the
// truncation policy, so this handler mirrors the CLI page fetch command without
// duplicating either. An unknown extractor is a -32602 invalid-params error.
func (s *Server) handlePageFetch(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageFetchParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.SessionID == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.fetch: missing required param "session_id"`}
	}
	if p.What == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.fetch: missing required param "what"`}
	}
	if err := pageops.ValidateAspect(p.What); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("page.fetch: %v", err)}
	}

	page, err := s.mgr.Page(ctx, browsers.PageID(p.SessionID)) // opens the Browser if needed; probes for liveness
	if err != nil {
		return nil, serverError(err)
	}

	maxBytes := p.MaxBytes
	if maxBytes <= 0 {
		maxBytes = pageops.DefaultPageMaxBytes
	}
	out, err := pageops.PageAspect(ctx, page, p.What, p.Selector, p.Target, p.FullPage, maxBytes)
	if err != nil {
		return nil, serverError(err)
	}
	return PageFetchResult{Data: out}, nil
}

// handlePageAct implements page.act: it performs the requested action on a
// live page — click, input, type, press_key, click_at, hover_at, drag_and_drop,
// upload_file, hover, scroll, select, eval, navigate, back, forward, reload,
// wait, set_viewport, emulate or clear_logs — mirroring the CLI page act
// command. An unknown action is a -32602 invalid-params error.
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
	case "click", "input", "type", "press_key", "click_at", "hover_at", "drag_and_drop", "upload_file", "eval", "navigate", "back", "forward", "reload", "hover", "scroll", "select", "wait", "set_viewport", "emulate", "clear_logs":
	default:
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("page.act: unknown action=%q (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate, clear_logs)", p.Action),
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
	// set_viewport carries its target size in the viewport param: reject a
	// missing or non-positive pair before any browser work (mirrors the MCP
	// page_act validation).
	if p.Action == "set_viewport" {
		if p.Viewport == nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "page.act: set_viewport requires a viewport"}
		}
		if p.Viewport.Width <= 0 || p.Viewport.Height <= 0 {
			return nil, &Error{Code: CodeInvalidParams, Message: "page.act: viewport width and height must be positive"}
		}
	}
	// emulate carries its target preset in the device param: reject a missing
	// or unknown device before any browser work (mirrors the MCP page_act
	// validation).
	if p.Action == "emulate" {
		if p.Device == "" {
			return nil, &Error{Code: CodeInvalidParams, Message: "page.act: emulate requires a device"}
		}
		if err := browsers.ValidateDeviceName(p.Device); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("page.act: %v", err)}
		}
	}

	page, err := s.mgr.Page(ctx, browsers.PageID(p.SessionID)) // opens the Browser if needed; probes for liveness
	if err != nil {
		return nil, serverError(err)
	}

	before, stateErr := pageops.PageStateOf(ctx, page)

	var out string
	switch p.Action {
	case "click":
		out, err = pageops.PageClickOpts(ctx, page, p.Handle, p.Selector, p.Button, p.ClickCount)
	case "input":
		out, err = pageops.PageInput(ctx, page, p.Handle, p.Selector, p.Value)
	case "type":
		out, err = pageops.PageType(ctx, page, p.Handle, p.Selector, p.Value)
	case "press_key":
		keyStr := p.Key
		if keyStr == "" {
			keyStr = p.Value
		}
		out, err = pageops.PagePressKey(ctx, page, p.Handle, p.Selector, keyStr)
	case "click_at":
		out, err = pageops.PageClickAt(ctx, page, p.X, p.Y, p.Button, p.ClickCount)
	case "hover_at":
		out, err = pageops.PageHoverAt(ctx, page, p.X, p.Y)
	case "drag_and_drop":
		out, err = pageops.PageDragAndDrop(ctx, page, p.X, p.Y, p.TargetX, p.TargetY, p.Selector, p.TargetSelector)
	case "upload_file":
		paths := p.Paths
		if len(paths) == 0 && p.Value != "" {
			paths = []string{p.Value}
		}
		out, err = pageops.PageUploadFile(ctx, page, p.Handle, p.Selector, paths)
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
		out, err = pageops.PageHover(ctx, page, p.Handle, p.Selector)
	case "scroll":
		out, err = pageops.PageScroll(ctx, page, p.X, p.Y)
	case "select":
		out, err = pageops.PageSelect(ctx, page, p.Handle, p.Selector, p.Value)
	case "wait":
		out, err = pageops.PageWait(ctx, page, p.WaitMode, p.Handle, p.Selector, time.Duration(p.TimeoutMS)*time.Millisecond)
	case "set_viewport":
		out, err = pageops.PageSetViewport(ctx, page, p.Viewport.Width, p.Viewport.Height)
	case "emulate":
		out, err = pageops.PageEmulate(ctx, page, p.Device)
	case "clear_logs":
		out, err = pageops.PageClearLogs(page)
	}
	if err != nil {
		return nil, serverError(err)
	}
	if stateErr == nil {
		after := pageops.PageStateAfterAct(ctx, page, p.Action, before)
		out += pageops.ActStateDeltaFor(p.Action, before, after)
	}
	return PageActResult{Data: out}, nil
}

// handlePageClose implements page.close: it resolves the page with the given id
// via Manager.Page (surfacing restart-aware errors for pre-restart sessions),
// then closes it through the browser named by browser_id, else the default
// browser. The session id becomes invalid afterwards.
func (s *Server) handlePageClose(ctx context.Context, params json.RawMessage) (any, *Error) {
	p, rpcErr := decodeParams[PageCloseParams](params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if p.SessionID == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: `page.close: missing required param "session_id"`}
	}

	b, err := s.targetBrowser(ctx, p.BrowserID)
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

// targetBrowser returns the browser a page method should operate on: the
// explicitly named live browser when id is set, else the lazily opened default.
func (s *Server) targetBrowser(ctx context.Context, id string) (*browsers.Browser, error) {
	if id == "" {
		return s.mgr.Open(ctx)
	}
	return s.mgr.Browser(browsers.BrowserID(id))
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
