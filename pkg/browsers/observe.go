package browsers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	// networkRingCap bounds the retained network entries per page.
	networkRingCap = 200
	// consoleRingCap bounds the retained console entries per page.
	consoleRingCap = 200
	// networkURLLimit bounds a stored request URL so one entry cannot dominate a response.
	networkURLLimit = 300
	// networkErrorLimit bounds a stored network failure text.
	networkErrorLimit = 200
	// consoleTextLimit bounds a stored console entry text.
	consoleTextLimit = 500
	// networkBodyCap bounds an on-demand response body at 256 KB.
	networkBodyCap = 256 << 10
	// ellipsis marks a string cut by cutString.
	ellipsis = "…"
)

// NetworkEntry is one observed request, joined from the CDP Network events.
type NetworkEntry struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Type   string `json:"type,omitempty"`
	Status int    `json:"status,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	MS     int64  `json:"ms,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`
	Seq    int64  `json:"seq"`
	// ReqHeaders holds the request headers, bounded per entry and redacted
	// unless the page was set to capture secrets.
	ReqHeaders map[string]string `json:"req_headers,omitempty"`
	// RespHeaders holds the response headers under the same rules.
	RespHeaders map[string]string `json:"resp_headers,omitempty"`
	// ReqHeadersTruncated reports a request header set cut by the entry bound.
	ReqHeadersTruncated bool `json:"req_headers_truncated,omitempty"`
	// RespHeadersTruncated reports a response header set cut by the entry bound.
	RespHeadersTruncated bool `json:"resp_headers_truncated,omitempty"`
	// Intercepted names the rule action that resolved this request in flight:
	// block, rewrite or fulfill. Empty means the request was not intercepted.
	Intercepted string `json:"intercepted,omitempty"`
	// InterceptRuleID names the rule that resolved the request in flight.
	InterceptRuleID string `json:"intercept_rule_id,omitempty"`

	requestID   proto.NetworkRequestID
	requestedAt proto.MonotonicTime
}

// NetworkBody is one on-demand response body, fetched from the browser by the
// entry's sequence number. Body carries UTF-8 text; Base64 carries binary
// content and is exclusive with Body.
type NetworkBody struct {
	Seq         int64  `json:"seq"`
	Method      string `json:"method,omitempty"`
	URL         string `json:"url"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated,omitempty"`
	Body        string `json:"body,omitempty"`
	Base64      string `json:"base64,omitempty"`
}

// ConsoleEntry is one observed console message, exception or log entry.
type ConsoleEntry struct {
	Level  string `json:"level"`
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
	Line   int    `json:"line,omitempty"`
	Seq    int64  `json:"seq"`
}

// ringBuffer is a bounded FIFO that keeps the newest cap items and counts every
// evicted one. It is not safe for concurrent use; the observer's mutex guards it.
type ringBuffer[T any] struct {
	cap     int
	items   []T
	dropped int64
}

// push appends item, evicting and counting the oldest item once the ring is full.
func (r *ringBuffer[T]) push(item T) {
	if len(r.items) < r.cap {
		r.items = append(r.items, item)
		return
	}
	copy(r.items, r.items[1:])
	r.items[r.cap-1] = item
	r.dropped++
}

// take returns a copy of the buffered items, oldest first.
func (r *ringBuffer[T]) take() []T {
	out := make([]T, len(r.items))
	copy(out, r.items)
	return out
}

// clear drops every buffered item and resets the dropped counter, returning how
// many items were cleared.
func (r *ringBuffer[T]) clear() int {
	n := r.len()
	r.items = nil
	r.dropped = 0
	return n
}

// len reports how many items are currently buffered.
func (r *ringBuffer[T]) len() int {
	return len(r.items)
}

// pageObserver captures CDP Network/Runtime/Log truth for one Page into three
// bounded rings. It has its own mutex; its handlers never touch Page.mu.
type pageObserver struct {
	mu     sync.Mutex
	net    ringBuffer[NetworkEntry]
	con    ringBuffer[ConsoleEntry]
	ws     ringBuffer[WebSocketEntry]
	netSeq int64
	conSeq int64
	wsSeq  int64
	// sockets names the live WebSocket connections their frames belong to.
	sockets map[proto.NetworkRequestID]*webSocketSocket
	// secrets keeps sensitive header values instead of redacting them.
	secrets bool
}

// newPageObserver returns an observer whose rings are bounded to the capture caps.
func newPageObserver() *pageObserver {
	return &pageObserver{
		net:     ringBuffer[NetworkEntry]{cap: networkRingCap},
		con:     ringBuffer[ConsoleEntry]{cap: consoleRingCap},
		ws:      ringBuffer[WebSocketEntry]{cap: websocketRingCap},
		sockets: make(map[proto.NetworkRequestID]*webSocketSocket),
	}
}

// indexOfRequest returns the newest ring position keyed by id, or -1. A
// redirect keeps the same RequestID across hops, so the newest entry is the
// hop the following response and finish events belong to.
func (o *pageObserver) indexOfRequest(id proto.NetworkRequestID) int {
	for i := len(o.net.items) - 1; i >= 0; i-- {
		if o.net.items[i].requestID == id {
			return i
		}
	}
	return -1
}

// applyResponse copies the status, MIME type, encoded byte count and redacted
// headers of r onto entry. The caller holds o.mu.
func (o *pageObserver) applyResponse(entry *NetworkEntry, r *proto.NetworkResponse) {
	if r == nil {
		return
	}
	entry.Status = r.Status
	entry.MIME = r.MIMEType
	entry.RespHeaders, entry.RespHeadersTruncated = redactHeaders(r.Headers, o.secrets)
	if r.EncodedDataLength > 0 {
		entry.Bytes = int64(r.EncodedDataLength)
	}
}

// recordRequest opens an entry for a request. A redirect re-fires the same
// RequestID with redirectResponse set: the previous hop is finalized from that
// response, which carries the 3xx status and its Set-Cookie, then the new hop is
// appended as its own entry. The request headers are captured bounded and
// redacted unless the page opted into secret capture.
func (o *pageObserver) recordRequest(e *proto.NetworkRequestWillBeSent) {
	if e.Request == nil {
		return
	}
	// Inline data: URLs carry no network truth and can be kilobytes of base64, so they are not recorded.
	if strings.HasPrefix(e.Request.URL, "data:") {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	headers, cut := redactHeaders(e.Request.Headers, o.secrets)
	i := o.indexOfRequest(e.RequestID)
	if e.RedirectResponse == nil {
		if i >= 0 {
			entry := &o.net.items[i]
			entry.Method = e.Request.Method
			entry.URL = cutString(e.Request.URL, networkURLLimit)
			entry.ReqHeaders = headers
			entry.ReqHeadersTruncated = cut
			if e.Type != "" {
				entry.Type = string(e.Type)
			}
			entry.requestedAt = e.Timestamp
			return
		}
	} else if i >= 0 {
		o.applyResponse(&o.net.items[i], e.RedirectResponse)
	}
	o.netSeq++
	o.net.push(NetworkEntry{
		Method:              e.Request.Method,
		URL:                 cutString(e.Request.URL, networkURLLimit),
		Type:                string(e.Type),
		Seq:                 o.netSeq,
		ReqHeaders:          headers,
		ReqHeadersTruncated: cut,
		requestID:           e.RequestID,
		requestedAt:         e.Timestamp,
	})
}

// recordResponse joins the response status, MIME type, resource type and
// redacted response headers onto the newest hop keyed by the request ID.
func (o *pageObserver) recordResponse(e *proto.NetworkResponseReceived) {
	o.mu.Lock()
	defer o.mu.Unlock()
	i := o.indexOfRequest(e.RequestID)
	if i < 0 {
		return
	}
	entry := &o.net.items[i]
	o.applyResponse(entry, e.Response)
	entry.Type = string(e.Type)
}

// recordFinished joins the wire byte count and elapsed milliseconds onto the
// entry keyed by the request ID.
func (o *pageObserver) recordFinished(e *proto.NetworkLoadingFinished) {
	o.mu.Lock()
	defer o.mu.Unlock()
	i := o.indexOfRequest(e.RequestID)
	if i < 0 {
		return
	}
	entry := &o.net.items[i]
	entry.Bytes = int64(e.EncodedDataLength)
	if entry.requestedAt > 0 {
		entry.MS = int64(math.Round(float64(e.Timestamp-entry.requestedAt) * 1000))
	}
}

// recordFailed marks the entry keyed by the request ID as failed, recording the
// network error text and treating a cancellation as real network truth.
func (o *pageObserver) recordFailed(e *proto.NetworkLoadingFailed) {
	o.mu.Lock()
	defer o.mu.Unlock()
	i := o.indexOfRequest(e.RequestID)
	if i < 0 {
		return
	}
	entry := &o.net.items[i]
	entry.Failed = true
	msg := e.ErrorText
	if e.Canceled {
		msg = "canceled: " + msg
	}
	entry.Error = cutString(msg, networkErrorLimit)
	if entry.Type == "" {
		entry.Type = string(e.Type)
	}
}

// recordIntercept tags the network entry for a paused request with the rule
// action that resolved it, appending an entry when the request was captured
// before the Network event arrived.
func (o *pageObserver) recordIntercept(e *proto.FetchRequestPaused, rule InterceptRule) {
	if e.Request == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	id := e.NetworkID
	idx := -1
	if id != "" {
		idx = o.indexOfRequest(id)
	}
	if idx < 0 {
		o.netSeq++
		entry := NetworkEntry{
			Method:          e.Request.Method,
			URL:             cutString(e.Request.URL, networkURLLimit),
			Type:            string(e.ResourceType),
			Seq:             o.netSeq,
			Intercepted:     rule.Action,
			InterceptRuleID: rule.ID,
			requestID:       id,
		}
		markIntercepted(&entry, rule)
		o.net.push(entry)
		return
	}
	entry := &o.net.items[idx]
	entry.Intercepted = rule.Action
	entry.InterceptRuleID = rule.ID
	markIntercepted(entry, rule)
}

// markIntercepted records the visible outcome of an intercepted action on an
// entry: a block is a failure, and a fulfill reports its synthetic status.
func markIntercepted(entry *NetworkEntry, rule InterceptRule) {
	switch rule.Action {
	case InterceptBlock:
		entry.Failed = true
		entry.Error = cutString("blocked by client (intercept rule "+rule.ID+")", networkErrorLimit)
	case InterceptFulfill:
		entry.Status = rule.Status
		if mime := capturedHeader(rule.Headers, "Content-Type"); mime != "" {
			entry.MIME = mime
		}
	}
}

// recordConsole appends one console.* call, stringifying its arguments through
// the page's error-returning JSON form.
func (o *pageObserver) recordConsole(e *proto.RuntimeConsoleAPICalled, page *rod.Page) {
	level := consoleLevel(e.Type)
	text := cutString(stringifyArgs(page, e.Args), consoleTextLimit)
	source, line := firstCallFrame(e.StackTrace)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.conSeq++
	o.con.push(ConsoleEntry{
		Level:  level,
		Text:   text,
		Source: source,
		Line:   line,
		Seq:    o.conSeq,
	})
}

// recordException appends one uncaught exception, preferring the exception's own
// description over the generic details text.
func (o *pageObserver) recordException(e *proto.RuntimeExceptionThrown) {
	d := e.ExceptionDetails
	if d == nil {
		return
	}
	text := d.Text
	if d.Exception != nil && d.Exception.Description != "" {
		text = d.Exception.Description
	}
	text = cutString(text, consoleTextLimit)
	source, line := firstCallFrame(d.StackTrace)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.conSeq++
	o.con.push(ConsoleEntry{
		Level:  "exception",
		Text:   text,
		Source: source,
		Line:   line,
		Seq:    o.conSeq,
	})
}

// recordLogEntry appends one browser log entry with its source URL and line.
func (o *pageObserver) recordLogEntry(e *proto.LogEntryAdded) {
	if e.Entry == nil {
		return
	}
	line := 0
	if e.Entry.LineNumber != nil {
		line = *e.Entry.LineNumber
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.conSeq++
	o.con.push(ConsoleEntry{
		Level:  logLevel(e.Entry.Level),
		Text:   cutString(e.Entry.Text, consoleTextLimit),
		Source: e.Entry.URL,
		Line:   line,
		Seq:    o.conSeq,
	})
}

// consoleLevel maps a console API call type to the wire level vocabulary.
func consoleLevel(t proto.RuntimeConsoleAPICalledType) string {
	switch t {
	case proto.RuntimeConsoleAPICalledTypeInfo:
		return "info"
	case proto.RuntimeConsoleAPICalledTypeWarning:
		return "warn"
	case proto.RuntimeConsoleAPICalledTypeError:
		return "error"
	default:
		return "log"
	}
}

// logLevel maps a browser log entry level to the wire level vocabulary.
func logLevel(l proto.LogLogEntryLevel) string {
	switch l {
	case proto.LogLogEntryLevelInfo:
		return "info"
	case proto.LogLogEntryLevelWarning:
		return "warn"
	case proto.LogLogEntryLevelError:
		return "error"
	default:
		return "log"
	}
}

// firstCallFrame returns the URL and line of the first stack frame, or zero
// values when no trace was captured.
func firstCallFrame(st *proto.RuntimeStackTrace) (string, int) {
	if st == nil || len(st.CallFrames) == 0 || st.CallFrames[0] == nil {
		return "", 0
	}
	f := st.CallFrames[0]
	return f.URL, f.LineNumber
}

// cutString returns s shortened to at most max bytes on a rune boundary, ending
// with an ellipsis when it had to be cut.
func cutString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	limit := max - len(ellipsis)
	if limit < 0 {
		limit = 0
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + ellipsis
}

// stringifyArgs renders remote console arguments as a JSON array, falling back
// to a fixed marker rather than panicking when the objects cannot be read.
func stringifyArgs(page *rod.Page, args []*proto.RuntimeRemoteObject) string {
	if len(args) == 0 {
		return ""
	}
	failed := fmt.Sprintf("<%d args, stringify failed>", len(args))
	vals := make([]any, 0, len(args))
	for _, a := range args {
		j, err := page.ObjectToJSON(a)
		if err != nil {
			return failed
		}
		vals = append(vals, j.Val())
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return failed
	}
	return string(b)
}

// NetworkEntries returns a copy of the observed network entries, oldest first,
// plus the number dropped by the ring bound.
func (p *Page) NetworkEntries() ([]NetworkEntry, int) {
	o := p.observer
	if o == nil {
		return []NetworkEntry{}, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.net.take(), int(o.net.dropped)
}

// CaptureSecrets controls whether headers captured from now on keep the values
// of Cookie, Set-Cookie, Authorization and X-CSRF-Token. The default replaces
// them with "<redacted>"; entries already captured are not rewritten.
func (p *Page) CaptureSecrets(on bool) {
	o := p.observer
	if o == nil {
		return
	}
	o.mu.Lock()
	o.secrets = on
	o.mu.Unlock()
}

// NetworkBody fetches the response body the browser still holds for the entry
// with the given Seq. The read is on demand, so memory stays bounded; a body
// the browser has already evicted, streamed or served from cache returns an
// actionable error rather than an empty string.
func (p *Page) NetworkBody(ctx context.Context, seq int64) (NetworkBody, error) {
	o := p.observer
	if o == nil {
		return NetworkBody{}, fmt.Errorf("network body: page has no network observer")
	}
	o.mu.Lock()
	var entry NetworkEntry
	found := false
	for i := range o.net.items {
		if o.net.items[i].Seq == seq {
			entry = o.net.items[i]
			found = true
			break
		}
	}
	o.mu.Unlock()
	if !found {
		return NetworkBody{}, fmt.Errorf(
			"network body: no network entry with seq %d (the ring keeps the newest %d; re-read what=network for current seq values)",
			seq,
			networkRingCap,
		)
	}
	if entry.Failed {
		return NetworkBody{}, fmt.Errorf(
			"network body: seq %d (%s %s) failed before a response arrived (%s)",
			seq,
			entry.Method,
			entry.URL,
			entry.Error,
		)
	}
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return NetworkBody{}, fmt.Errorf("network body: page session is closed")
	}
	res, err := proto.NetworkGetResponseBody{RequestID: entry.requestID}.Call(p.Rod.Context(ctx))
	if err != nil {
		return NetworkBody{}, fmt.Errorf(
			"network body: seq %d (%s %s) body is no longer available — the browser keeps response bodies only briefly (evicted, streamed, or served from cache); re-issue the request and read the body immediately: %w",
			seq,
			entry.Method,
			entry.URL,
			err,
		)
	}
	body, err := buildNetworkBody(entry, res)
	if err != nil {
		return NetworkBody{}, fmt.Errorf("network body: seq %d (%s %s): %w", seq, entry.Method, entry.URL, err)
	}
	if body.Body == "" && body.Base64 == "" {
		return NetworkBody{}, fmt.Errorf(
			"network body: seq %d (%s %s) has an empty body (status %d, mime %q) — the response carried no payload",
			seq,
			entry.Method,
			entry.URL,
			entry.Status,
			entry.MIME,
		)
	}
	return body, nil
}

// buildNetworkBody assembles a body result from a CDP body response, capping
// the payload at 256 KB and switching binary content to base64.
func buildNetworkBody(entry NetworkEntry, res *proto.NetworkGetResponseBodyResult) (NetworkBody, error) {
	raw := []byte(res.Body)
	if res.Base64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(res.Body)
		if err != nil {
			return NetworkBody{}, fmt.Errorf("decode base64 body: %w", err)
		}
		raw = decoded
	}
	out := NetworkBody{
		Seq:         entry.Seq,
		Method:      entry.Method,
		URL:         entry.URL,
		Status:      entry.Status,
		ContentType: entry.MIME,
		Bytes:       int64(len(raw)),
	}
	if len(raw) > networkBodyCap {
		end := networkBodyCap
		for end > 0 && !utf8.RuneStart(raw[end]) {
			end--
		}
		raw = raw[:end]
		out.Truncated = true
	}
	if res.Base64Encoded || !utf8.Valid(raw) {
		out.Base64 = base64.StdEncoding.EncodeToString(raw)
		return out, nil
	}
	out.Body = string(raw)
	return out, nil
}

// ConsoleEntries returns a copy of the observed console entries, oldest first,
// plus the number dropped by the ring bound.
func (p *Page) ConsoleEntries() ([]ConsoleEntry, int) {
	o := p.observer
	if o == nil {
		return []ConsoleEntry{}, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.con.take(), int(o.con.dropped)
}

// CaptureCounts reports how many entries each capture ring emptied.
type CaptureCounts struct {
	// Network is the number of network entries cleared.
	Network int
	// Console is the number of console entries cleared.
	Console int
	// WebSocket is the number of WebSocket frames cleared.
	WebSocket int
}

// ClearLogs empties the network, console and WebSocket capture rings and resets
// their dropped counters, returning the network and console counts. Call
// ClearCapture for the complete counts.
func (p *Page) ClearLogs() (int, int) {
	c := p.ClearCapture()
	return c.Network, c.Console
}

// ClearCapture empties every capture ring and resets their dropped counters.
func (p *Page) ClearCapture() CaptureCounts {
	o := p.observer
	if o == nil {
		return CaptureCounts{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return CaptureCounts{
		Network:   o.net.clear(),
		Console:   o.con.clear(),
		WebSocket: o.ws.clear(),
	}
}

// startObservation arms network, console and WebSocket capture on the page's
// own context. It runs in the pre-registration window, so no page lock is
// needed. Capture is strictly best-effort: a failed enable silences one stream
// but never fails page creation.
func (p *Page) startObservation() {
	if p.Rod == nil {
		return
	}
	o := newPageObserver()
	p.observer = o

	capturePage, cancel := p.Rod.WithCancel()
	p.obsCancel = cancel

	_ = proto.NetworkEnable{}.Call(capturePage)
	_ = proto.RuntimeEnable{}.Call(capturePage)
	_ = proto.LogEnable{}.Call(capturePage)

	stop := capturePage.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) { o.recordRequest(e) },
		func(e *proto.NetworkResponseReceived) { o.recordResponse(e) },
		func(e *proto.NetworkLoadingFinished) { o.recordFinished(e) },
		func(e *proto.NetworkLoadingFailed) { o.recordFailed(e) },
		func(e *proto.RuntimeConsoleAPICalled) { o.recordConsole(e, capturePage) },
		func(e *proto.RuntimeExceptionThrown) { o.recordException(e) },
		func(e *proto.LogEntryAdded) { o.recordLogEntry(e) },
		func(e *proto.NetworkWebSocketCreated) { o.recordWebSocketCreated(e) },
		func(e *proto.NetworkWebSocketHandshakeResponseReceived) { o.recordWebSocketHandshake(e) },
		func(e *proto.NetworkWebSocketFrameSent) { o.recordWebSocketFrameSent(e) },
		func(e *proto.NetworkWebSocketFrameReceived) { o.recordWebSocketFrameReceived(e) },
		func(e *proto.NetworkWebSocketClosed) { o.recordWebSocketClosed(e) },
	)
	go stop()
}

// stopObservation disarms capture idempotently by cancelling the capture context
// and clearing the stored cancel. It also disarms request interception, the
// other page-scoped CDP subscription, so every teardown path (including the one
// that closes the tab directly) leaves no live Fetch hook. It does not wait for
// the drain goroutine.
func (p *Page) stopObservation() {
	p.disarmInterception()
	if p.obsCancel == nil {
		return
	}
	p.obsCancel()
	p.obsCancel = nil
}
