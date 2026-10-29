package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// TestReplayQueryAndFetchFilterWireRoundTrip pins that replay's query edits and
// page.fetch's filter survive the JSON-RPC framing, including an explicit empty
// value (the removal signal) and an absent map.
func TestReplayQueryAndFetchFilterWireRoundTrip(t *testing.T) {
	actLine, err := json.Marshal(Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  MethodPageAct,
		Params: json.RawMessage(
			`{"session_id":"page-1","action":"replay","target":"/api/search","query":{"q":"other","drop":""}}`,
		),
	})
	if err != nil {
		t.Fatalf("marshal act request: %v", err)
	}
	actReq, rpcErr := decodeRequest(actLine)
	if rpcErr != nil {
		t.Fatalf("decodeRequest: %+v", rpcErr)
	}
	actParams, rpcErr := decodeParams[PageActParams](actReq.Params)
	if rpcErr != nil {
		t.Fatalf("decodeParams: %+v", rpcErr)
	}
	if got := actParams.Query["q"]; got != "other" {
		t.Errorf("query[q] = %q, want other", got)
	}
	if got, ok := actParams.Query["drop"]; !ok || got != "" {
		t.Errorf("query[drop] = %q (present=%v), want an empty removal edit", got, ok)
	}

	fetchLine, err := json.Marshal(Request{
		JSONRPC: "2.0",
		ID:      2,
		Method:  MethodPageFetch,
		Params: json.RawMessage(
			`{"session_id":"page-1","what":"network","filter":"-image,-font"}`,
		),
	})
	if err != nil {
		t.Fatalf("marshal fetch request: %v", err)
	}
	fetchReq, rpcErr := decodeRequest(fetchLine)
	if rpcErr != nil {
		t.Fatalf("decodeRequest: %+v", rpcErr)
	}
	fetchParams, rpcErr := decodeParams[PageFetchParams](fetchReq.Params)
	if rpcErr != nil {
		t.Fatalf("decodeParams: %+v", rpcErr)
	}
	if fetchParams.Filter != "-image,-font" {
		t.Errorf("filter = %q, want -image,-font", fetchParams.Filter)
	}
}

// TestPageActRawWireRoundTrip pins that the raw intent survives the JSON-RPC
// framing: true and false both decode back intact, and an absent field keeps
// the daemon's default (no raw).
func TestPageActRawWireRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		wire string
		want bool
	}{
		{name: "raw true", wire: `{"session_id":"page-1","action":"click","raw":true}`, want: true},
		{name: "raw false", wire: `{"session_id":"page-1","action":"click","raw":false}`, want: false},
		{name: "absent keeps the default", wire: `{"session_id":"page-1","action":"click"}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := json.Marshal(Request{
				JSONRPC: "2.0",
				ID:      1,
				Method:  MethodPageAct,
				Params:  json.RawMessage(tt.wire),
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req, rpcErr := decodeRequest(line)
			if rpcErr != nil {
				t.Fatalf("decodeRequest: %+v", rpcErr)
			}
			params, rpcErr := decodeParams[PageActParams](req.Params)
			if rpcErr != nil {
				t.Fatalf("decodeParams: %+v", rpcErr)
			}
			if params.Raw != tt.want {
				t.Errorf("raw = %v, want %v", params.Raw, tt.want)
			}
		})
	}
}

// TestPageActRawEncodesWhenSet pins the outbound side: the client's raw intent
// is on the wire only when set, so an unset value stays absent.
func TestPageActRawEncodesWhenSet(t *testing.T) {
	on, err := json.Marshal(PageActParams{SessionID: "page-1", Action: "click", Raw: true})
	if err != nil {
		t.Fatalf("marshal raw params: %v", err)
	}
	if !strings.Contains(string(on), `"raw":true`) {
		t.Errorf("encoded params = %s, want a raw:true field", on)
	}

	off, err := json.Marshal(PageActParams{SessionID: "page-1", Action: "click"})
	if err != nil {
		t.Fatalf("marshal plain params: %v", err)
	}
	if strings.Contains(string(off), `"raw"`) {
		t.Errorf("encoded params = %s, want no raw field when unset", off)
	}
}

// recordingManager wraps the stub manager and reports the deadline of the
// context page.act hands to the engine, so a test can pin the action budget.
type recordingManager struct {
	*stubbedManager
	pageDeadline chan time.Time
}

// Page implements manager.
func (m *recordingManager) Page(
	ctx context.Context,
	id browsers.PageID,
	browserID browsers.BrowserID,
	owner string,
) (*browsers.Page, error) {
	if deadline, ok := ctx.Deadline(); ok {
		select {
		case m.pageDeadline <- deadline:
		default:
		}
	}
	return m.stubbedManager.Page(ctx, id, browserID, owner)
}

// TestPageActActionDeadlineIsExact pins that the daemon hands the action the
// caller's timeout_ms with no grace: a 200ms click must not get 5.2s.
func TestPageActActionDeadlineIsExact(t *testing.T) {
	m := &recordingManager{
		stubbedManager: &stubbedManager{
			browser: browsers.NewBrowser("browser-1", context.Background(), false, false, "", ""),
		},
		pageDeadline: make(chan time.Time, 1),
	}
	s := newServer(m)

	start := time.Now()
	_, _ = s.handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  MethodPageAct,
		Params:  json.RawMessage(`{"session_id":"page-1","action":"click","timeout_ms":200}`),
	})

	select {
	case deadline := <-m.pageDeadline:
		got := deadline.Sub(start)
		if got < 150*time.Millisecond || got > 500*time.Millisecond {
			t.Errorf("action deadline = %s after the request, want ~200ms (the caller timeout, without a grace)", got)
		}
	case <-time.After(time.Second):
		t.Fatal("page.act never reached the engine with a context deadline")
	}
}

// TestPageActShortDeadlineReachesActionUnchanged pins that a tiny timeout_ms is
// not stretched: the humanized pause can only skip itself when it sees the real
// deadline.
func TestPageActShortDeadlineReachesActionUnchanged(t *testing.T) {
	if got := PageActTimeout(0); got != DefaultPageActTimeout {
		t.Errorf("PageActTimeout(0) = %s, want %s", got, DefaultPageActTimeout)
	}
	if got := PageActTimeout(-5); got != DefaultPageActTimeout {
		t.Errorf("PageActTimeout(-5) = %s, want %s", got, DefaultPageActTimeout)
	}
	if got := PageActTimeout(1); got != time.Millisecond {
		t.Errorf("PageActTimeout(1) = %s, want 1ms", got)
	}
	if got := PageActBudget("click", "", 1); got != time.Millisecond {
		t.Errorf("PageActBudget(click, 1) = %s, want 1ms", got)
	}
}

// TestPageActReadDeadlineOutlastsAction pins the two-budget split: the client's
// socket read outlasts the action deadline and the daemon action budget, so a
// fired action is never reported as an IPC i/o timeout.
func TestPageActReadDeadlineOutlastsAction(t *testing.T) {
	for _, ms := range []int{1, 200, 30000} {
		action := PageActTimeout(ms)
		if read := PageActReadTimeout(action); read <= action {
			t.Errorf("ms=%d: read budget %s does not outlast the %s action deadline", ms, read, action)
		}
		if read, budget := PageActReadTimeout(action), PageActBudget("click", "", ms); read <= budget {
			t.Errorf("ms=%d: read budget %s does not outlast the %s action budget", ms, read, budget)
		}
	}
	read := PageActReadTimeout(PageActTimeout(200))
	if budget := PageActBudget("wait", "ms", 200); read <= budget {
		t.Errorf("wait: read budget %s does not outlast the %s action budget", read, budget)
	}
}

// TestPageActWaitBudgetMirrorsMCP pins the one deliberate extension: the wait
// action's "ms" mode sleeps for timeout_ms, so its budget buys the sleep plus
// the same margin the MCP page_act budget uses.
func TestPageActWaitBudgetMirrorsMCP(t *testing.T) {
	if got, want := PageActBudget("wait", "ms", 1000), time.Second+waitSleepMargin; got != want {
		t.Errorf("PageActBudget(wait ms, 1000) = %s, want %s", got, want)
	}
	if got, want := PageActBudget("wait", "", 1000), time.Second+waitSleepMargin; got != want {
		t.Errorf("PageActBudget(wait default mode, 1000) = %s, want %s", got, want)
	}
	if got, want := PageActBudget("wait", "load", 1000), time.Second; got != want {
		t.Errorf("PageActBudget(wait load, 1000) = %s, want %s (no sleep to cushion)", got, want)
	}
}
