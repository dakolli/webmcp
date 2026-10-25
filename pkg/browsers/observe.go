package browsers

import (
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

	redirectDedupeKey proto.NetworkRequestID
	requestedAt       proto.MonotonicTime
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

// pageObserver captures CDP Network/Runtime/Log truth for one Page into two
// bounded rings. It has its own mutex; its handlers never touch Page.mu.
type pageObserver struct {
	mu     sync.Mutex
	net    ringBuffer[NetworkEntry]
	con    ringBuffer[ConsoleEntry]
	netSeq int64
	conSeq int64
}

// newPageObserver returns an observer whose rings are bounded to the capture caps.
func newPageObserver() *pageObserver {
	return &pageObserver{
		net: ringBuffer[NetworkEntry]{cap: networkRingCap},
		con: ringBuffer[ConsoleEntry]{cap: consoleRingCap},
	}
}

// indexOfRequest returns the ring position currently keyed by id, or -1.
func (o *pageObserver) indexOfRequest(id proto.NetworkRequestID) int {
	for i := range o.net.items {
		if o.net.items[i].redirectDedupeKey == id {
			return i
		}
	}
	return -1
}

// recordRequest opens an entry for a request, or rewrites the existing entry in
// place when the same RequestID re-fires on a redirect hop.
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
	if i := o.indexOfRequest(e.RequestID); i >= 0 {
		entry := &o.net.items[i]
		entry.Method = e.Request.Method
		entry.URL = cutString(e.Request.URL, networkURLLimit)
		if e.Type != "" {
			entry.Type = string(e.Type)
		}
		entry.requestedAt = e.Timestamp
		return
	}
	o.netSeq++
	o.net.push(NetworkEntry{
		Method:            e.Request.Method,
		URL:               cutString(e.Request.URL, networkURLLimit),
		Type:              string(e.Type),
		Seq:               o.netSeq,
		redirectDedupeKey: e.RequestID,
		requestedAt:       e.Timestamp,
	})
}

// recordResponse joins the response status, MIME type and resource type onto the
// entry keyed by the request ID.
func (o *pageObserver) recordResponse(e *proto.NetworkResponseReceived) {
	o.mu.Lock()
	defer o.mu.Unlock()
	i := o.indexOfRequest(e.RequestID)
	if i < 0 {
		return
	}
	entry := &o.net.items[i]
	if e.Response != nil {
		entry.Status = e.Response.Status
		entry.MIME = e.Response.MIMEType
	}
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

// ClearLogs empties both capture rings and resets their dropped counters,
// returning the number of network and console entries cleared.
func (p *Page) ClearLogs() (int, int) {
	o := p.observer
	if o == nil {
		return 0, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.net.clear(), o.con.clear()
}

// startObservation arms network and console capture on the page's own context.
// It runs in the pre-registration window, so no page lock is needed. Capture is
// strictly best-effort: a failed enable silences one stream but never fails page
// creation.
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
	)
	go stop()
}

// stopObservation disarms capture idempotently by cancelling the capture context
// and clearing the stored cancel. It does not wait for the drain goroutine.
func (p *Page) stopObservation() {
	if p.obsCancel == nil {
		return
	}
	p.obsCancel()
	p.obsCancel = nil
}
