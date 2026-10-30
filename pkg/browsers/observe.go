package browsers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
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
	// Protocol is the HTTP protocol Chrome used for the response, as CDP
	// reported it ("http/1.1", "h2", "h3"). Empty when Chrome did not report
	// one.
	Protocol string `json:"protocol,omitempty"`
	// RemoteIP is the peer IP of the response, as Chrome reported it. Empty when
	// Chrome did not report one.
	RemoteIP string `json:"remote_ip,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	MS       int64  `json:"ms,omitempty"`
	Failed   bool   `json:"failed,omitempty"`
	Error    string `json:"error,omitempty"`
	Seq      int64  `json:"seq"`
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
	// FrameID is the CDP frame that issued the request, empty when CDP did not
	// report one (a worker request carries no frame).
	FrameID string `json:"frame_id,omitempty"`
	// LoaderID is the CDP loader of the frame's document at request time, empty
	// when CDP did not report one. Every request the document starts shares the
	// document's loader.
	LoaderID string `json:"loader_id,omitempty"`
	// TopDocument marks the document request that produced the page's current
	// top-level document. Exactly one entry carries it, and it is decided from
	// the browser's frame and loader identity, so a consumer can find the
	// current document boundary without matching URLs. Absent when that request
	// has left the ring.
	TopDocument bool `json:"top_document,omitempty"`

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

// documentSecurityHeaders lists, lowercased, the response headers the audit
// judges that the page stores uncapped. The set is closed, so the record stays
// bounded no matter how many headers a response carries.
var documentSecurityHeaders = []string{
	"strict-transport-security",
	"x-content-type-options",
	"x-frame-options",
	"referrer-policy",
	"permissions-policy",
	"cross-origin-opener-policy",
	"cross-origin-resource-policy",
	"access-control-allow-origin",
	"access-control-allow-credentials",
	"access-control-allow-methods",
	"access-control-allow-headers",
	"access-control-expose-headers",
}

// documentHeaderCapture is the uncapped header state of the page's current
// top-level document response: the two Content-Security-Policy variants plus the
// closed set of security headers the audit reads. Empty strings mean the
// response did not carry that header. It is replaced as one record on every
// top-level document response, so a page holds one bounded state and never
// accumulates.
type documentHeaderCapture struct {
	// Enforced is the Content-Security-Policy value, before the 512-byte clip
	// the network ring applies to every header.
	Enforced string
	// ReportOnly is the Content-Security-Policy-Report-Only value, under the
	// same uncapped rule.
	ReportOnly string
	// Security holds documentSecurityHeaders that the response carried, keyed
	// by their lowercased name and with repeated values newline-joined.
	Security map[string]string
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

// pageObserver captures CDP Network/Runtime/Log truth and the page-reported DOM
// mutation timeline for one Page into bounded rings. It has its own mutex; its
// handlers never touch Page.mu.
type pageObserver struct {
	mu     sync.Mutex
	net    ringBuffer[NetworkEntry]
	con    ringBuffer[ConsoleEntry]
	ws     ringBuffer[WebSocketEntry]
	mut    ringBuffer[MutationEntry]
	netSeq int64
	conSeq int64
	wsSeq  int64
	mutSeq int64
	// mutRemove and mutStop unarm the in-page mutation observer and its binding.
	mutRemove func() error
	mutStop   func() error
	// sockets names the live WebSocket connections their frames belong to.
	sockets map[proto.NetworkRequestID]*webSocketSocket
	// secrets keeps sensitive header values instead of redacting them.
	secrets bool
	// topFrameID is the page's main frame. It is stable for the life of the tab.
	topFrameID proto.PageFrameID
	// topLoaderID is the loader of the document the top frame is showing now. It
	// changes on navigation and on a BFCache restore, so it names the current
	// document rather than the last one a URL heuristic would pick.
	topLoaderID proto.NetworkLoaderID
	// docHeaders holds the uncapped header state of the current top-level
	// document response. It is not a ring and is never cleared by ClearCapture,
	// so an audit keeps the document's policy and security headers after
	// clear_logs.
	docHeaders documentHeaderCapture
}

// newPageObserver returns an observer whose rings are bounded to the capture caps.
func newPageObserver() *pageObserver {
	return &pageObserver{
		net:     ringBuffer[NetworkEntry]{cap: networkRingCap},
		con:     ringBuffer[ConsoleEntry]{cap: consoleRingCap},
		ws:      ringBuffer[WebSocketEntry]{cap: websocketRingCap},
		mut:     ringBuffer[MutationEntry]{cap: MutationRingCap},
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
	if r.Protocol != "" {
		entry.Protocol = r.Protocol
	}
	if r.RemoteIPAddress != "" {
		entry.RemoteIP = r.RemoteIPAddress
	}
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
	if e.Type == proto.NetworkResourceTypeDocument && o.isTopFrame(e.FrameID) && e.LoaderID != "" {
		o.topLoaderID = e.LoaderID
	}
	i := o.indexOfRequest(e.RequestID)
	if e.RedirectResponse == nil {
		if i >= 0 {
			entry := &o.net.items[i]
			entry.Method = e.Request.Method
			entry.URL = cutString(e.Request.URL, networkURLLimit)
			entry.ReqHeaders = headers
			entry.ReqHeadersTruncated = cut
			entry.FrameID = string(e.FrameID)
			entry.LoaderID = string(e.LoaderID)
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
		FrameID:             string(e.FrameID),
		LoaderID:            string(e.LoaderID),
		requestID:           e.RequestID,
		requestedAt:         e.Timestamp,
	})
}

// isTopFrame reports whether frame is the page's main frame. An empty frame id
// is unknown, never the top frame. The caller holds o.mu.
func (o *pageObserver) isTopFrame(frame proto.PageFrameID) bool {
	return o.topFrameID != "" && frame == o.topFrameID
}

// recordFrameNavigated tracks the document the top frame is showing. A frame
// navigation is the only signal for a BFCache restore, which fires no request,
// so the loader is taken from the frame itself; child frames are ignored so an
// iframe that loads the page's own URL cannot move the top-document boundary.
func (o *pageObserver) recordFrameNavigated(e *proto.PageFrameNavigated) {
	if e.Frame == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if e.Frame.ParentID != "" {
		return
	}
	if o.topFrameID == "" {
		o.topFrameID = e.Frame.ID
	}
	if e.Frame.ID != o.topFrameID || e.Frame.LoaderID == "" {
		return
	}
	o.topLoaderID = e.Frame.LoaderID
}

// markTopDocument flags the newest Document request that belongs to the loader
// the top frame is showing now. Frame and loader identity come from CDP, so an
// earlier navigation, a same-origin route change and a same-origin iframe all
// resolve to the same entry a URL match could only guess. The caller holds o.mu.
func (o *pageObserver) markTopDocument(entries []NetworkEntry) {
	if o.topFrameID == "" || o.topLoaderID == "" {
		return
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := &entries[i]
		if entry.Type != string(proto.NetworkResourceTypeDocument) {
			continue
		}
		if entry.FrameID != string(o.topFrameID) || entry.LoaderID != string(o.topLoaderID) {
			continue
		}
		entry.TopDocument = true
		return
	}
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
	o.captureDocumentHeaders(e, entry)
}

// captureDocumentHeaders stores the uncapped CSP text and security headers of
// the page's current top-level document response. It reads the raw CDP header
// values, so these names bypass the network ring's 512-byte clip that every
// other header keeps, and the record survives ClearCapture. A meta-delivered
// policy is not a network header and is read from the live DOM by the audit
// instead. The caller holds o.mu.
func (o *pageObserver) captureDocumentHeaders(e *proto.NetworkResponseReceived, entry *NetworkEntry) {
	if e.Type != proto.NetworkResourceTypeDocument || e.Response == nil {
		return
	}
	if o.topFrameID == "" || proto.PageFrameID(entry.FrameID) != o.topFrameID {
		return
	}
	security := make(map[string]string, len(documentSecurityHeaders))
	for _, name := range documentSecurityHeaders {
		if v := rawHeaderText(e.Response.Headers, name); v != "" {
			security[name] = v
		}
	}
	o.docHeaders = documentHeaderCapture{
		Enforced:   joinedHeaderValues(e.Response.Headers, "Content-Security-Policy"),
		ReportOnly: joinedHeaderValues(e.Response.Headers, "Content-Security-Policy-Report-Only"),
		Security:   security,
	}
}

// joinedHeaderValues returns every value the named header carried, joined with
// ", " the way a browser combines repeated CSP fields.
func joinedHeaderValues(h proto.NetworkHeaders, name string) string {
	raw := rawHeaderText(h, name)
	if raw == "" {
		return ""
	}
	return strings.Join(strings.Split(raw, "\n"), ", ")
}

// rawHeaderText returns every value the named header carried, newline-joined so
// a repeated header keeps its distinct values. Chrome joins repeated header
// fields with a newline inside one map value, so both the newline separated
// copies and any case-variant map keys are merged in sorted key order.
func rawHeaderText(h proto.NetworkHeaders, name string) string {
	var keys []string
	for k := range h {
		if strings.EqualFold(k, name) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, line := range strings.Split(h[k].Str(), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				parts = append(parts, line)
			}
		}
	}
	return strings.Join(parts, "\n")
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
			FrameID:         string(e.FrameID),
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
// plus the number dropped by the ring bound. The entry for the page's current
// top-level document is marked on the copy, so the boundary reflects the live
// document at read time and never mutates the ring.
func (p *Page) NetworkEntries() ([]NetworkEntry, int) {
	o := p.observer
	if o == nil {
		return []NetworkEntry{}, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.net.take()
	o.markTopDocument(out)
	return out, int(o.net.dropped)
}

// DocumentCSP returns the uncapped Content-Security-Policy text of the page's
// current top-level document response, in both the enforced and the report-only
// variants. Empty strings mean the response carried none. The text is captured
// raw at the network layer before the ring's 512-byte header clip, and it
// survives ClearCapture, so a security audit keeps the policy after clear_logs.
func (p *Page) DocumentCSP() (enforced, reportOnly string) {
	o := p.observer
	if o == nil {
		return "", ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.docHeaders.Enforced, o.docHeaders.ReportOnly
}

// DocumentSecurityHeaders returns a copy of the uncapped security response
// headers of the page's current top-level document response, keyed by the
// lowercased name. A missing name means the response did not carry it. The
// values are captured raw at the network layer before the ring's 512-byte
// header clip, and the record survives ClearCapture, so a security audit keeps
// its header verdicts after clear_logs.
func (p *Page) DocumentSecurityHeaders() map[string]string {
	o := p.observer
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.docHeaders.Security) == 0 {
		return nil
	}
	headers := make(map[string]string, len(o.docHeaders.Security))
	for name, value := range o.docHeaders.Security {
		headers[name] = value
	}
	return headers
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

// SecretsCaptured reports whether the page keeps sensitive values instead of
// replacing them with the redaction marker.
func (p *Page) SecretsCaptured() bool {
	return p.captureSecrets()
}

// observedAt returns the buffered entry with the given Seq and whether it is
// still in the ring.
func (o *pageObserver) observedAt(seq int64) (NetworkEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range o.net.items {
		if o.net.items[i].Seq == seq {
			return o.net.items[i], true
		}
	}
	return NetworkEntry{}, false
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
	entry, found := o.observedAt(seq)
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
	if err := p.lockRead(ctx, "body"); err != nil {
		return NetworkBody{}, err
	}
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

// NetworkRequestBody fetches the request post body the browser still holds for
// the entry with the given Seq, reading the same CDP record the replay seam
// uses. Text content lands in Body and binary content in Base64, with Bytes as
// the decoded size. A request the browser no longer retains returns an
// actionable error instead of an empty body.
func (p *Page) NetworkRequestBody(ctx context.Context, seq int64) (NetworkBody, error) {
	o := p.observer
	if o == nil {
		return NetworkBody{}, fmt.Errorf("network request body: page has no network observer")
	}
	entry, found := o.observedAt(seq)
	if !found {
		return NetworkBody{}, fmt.Errorf(
			"network request body: no network entry with seq %d (the ring keeps the newest %d; re-read what=network for current seq values)",
			seq,
			networkRingCap,
		)
	}
	if !replayCarriedBody(entry) {
		return NetworkBody{}, fmt.Errorf(
			"network request body: seq %d (%s %s) carried no body (no Content-Type and no non-zero Content-Length)",
			seq,
			entry.Method,
			entry.URL,
		)
	}
	post, retained, err := p.requestPostData(ctx, entry)
	if err != nil {
		return NetworkBody{}, fmt.Errorf("network request body: seq %d (%s %s): %w", seq, entry.Method, entry.URL, err)
	}
	if !retained {
		return NetworkBody{}, fmt.Errorf(
			"network request body: seq %d (%s %s) is no longer retained — the browser keeps a request body only while the request's network record is live",
			seq,
			entry.Method,
			entry.URL,
		)
	}
	if post == "" {
		return NetworkBody{}, fmt.Errorf(
			"network request body: seq %d (%s %s) carried no body",
			seq,
			entry.Method,
			entry.URL,
		)
	}
	out := NetworkBody{
		Seq:         entry.Seq,
		Method:      entry.Method,
		URL:         entry.URL,
		Status:      entry.Status,
		ContentType: capturedHeader(entry.ReqHeaders, "Content-Type"),
		Bytes:       int64(len(post)),
	}
	if utf8.ValidString(post) {
		out.Body = post
		return out, nil
	}
	out.Base64 = base64.StdEncoding.EncodeToString([]byte(post))
	return out, nil
}

// the payload at 256 KB and switching binary content to base64.
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
// own context, the per-page DOM mutation timeline, plus the frame navigation
// events that identify the current top-level document. It runs in the
// pre-registration window, so no page lock is needed. Capture is strictly
// best-effort: a failed enable silences one stream but never fails page creation.
func (p *Page) startObservation() {
	if p.Rod == nil {
		return
	}
	o := newPageObserver()
	o.topFrameID = p.Rod.FrameID
	p.observer = o

	capturePage, cancel := p.Rod.WithCancel()
	p.obsCancel = cancel

	p.startMutationObservation()
	_ = proto.NetworkEnable{}.Call(capturePage)
	_ = proto.RuntimeEnable{}.Call(capturePage)
	_ = proto.LogEnable{}.Call(capturePage)
	_ = proto.PageEnable{}.Call(capturePage)

	stop := capturePage.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) { o.recordRequest(e) },
		func(e *proto.NetworkResponseReceived) { o.recordResponse(e) },
		func(e *proto.NetworkLoadingFinished) { o.recordFinished(e) },
		func(e *proto.NetworkLoadingFailed) { o.recordFailed(e) },
		func(e *proto.PageFrameNavigated) { o.recordFrameNavigated(e) },
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
	if p.observer != nil {
		p.observer.stopMutations()
	}
	if p.obsCancel == nil {
		return
	}
	p.obsCancel()
	p.obsCancel = nil
}
