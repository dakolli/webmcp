package browsers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSinkHitsEnvelope(t *testing.T) {
	t.Run("uninstalled page is not an error", func(t *testing.T) {
		res, err := parseSinkSnapshot("null")
		if err != nil {
			t.Fatalf("parse null: %v", err)
		}
		if res.Installed || len(res.Hits) != 0 {
			t.Fatalf("uninstalled: got installed=%v hits=%d", res.Installed, len(res.Hits))
		}
		if res.Hits == nil {
			t.Fatal("uninstalled: hits must be [] on the wire, not nil")
		}
	})

	t.Run("ring loss truncation and newest window", func(t *testing.T) {
		hits := make([]SinkHit, 250)
		for i := range hits {
			hits[i] = SinkHit{
				Source:  "location.hash",
				Sink:    "innerHTML",
				Context: "html",
				Value:   fmt.Sprintf("hit-%d", i),
			}
		}
		raw, err := json.Marshal(sinkSnapshot{Hits: hits, Dropped: 7})
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		res, err := parseSinkSnapshot(string(raw))
		if err != nil {
			t.Fatalf("parse snapshot: %v", err)
		}
		if !res.Installed {
			t.Fatal("installed: got false, want true")
		}
		if len(res.Hits) != MaxSinkHits {
			t.Fatalf("emitted: got %d, want %d", len(res.Hits), MaxSinkHits)
		}
		if res.Truncated != 150 || res.Dropped != 7 {
			t.Fatalf("counters: got truncated=%d dropped=%d, want 150/7", res.Truncated, res.Dropped)
		}
		if res.Hits[0].Value != "hit-150" || res.Hits[len(res.Hits)-1].Value != "hit-249" {
			t.Fatalf("window: got %q..%q, want the newest %d", res.Hits[0].Value, res.Hits[len(res.Hits)-1].Value, MaxSinkHits)
		}
	})

	t.Run("script template fills the cap placeholders", func(t *testing.T) {
		if strings.Contains(sinkScript, "%d") || strings.Contains(sinkScript, "%!") {
			t.Fatal("sinkScript still carries a format verb")
		}
		if !strings.Contains(sinkScript, "const WM_CAP = 200;") {
			t.Fatalf("sinkScript: got %q, want the ring cap baked in", strings.SplitN(sinkScript, "\n", 4)[2])
		}
		if !strings.Contains(sinkScript, "const WM_VALUE_MAX = 200;") {
			t.Fatal("sinkScript: want the value cap baked in")
		}
		if !strings.HasPrefix(sinkInstallJS, "() => {") {
			t.Fatalf("sinkInstallJS: got %q, want a function expression for Page.Eval", sinkInstallJS[:12])
		}
	})

	t.Run("new-document script re-arms the tracer", func(t *testing.T) {
		if !strings.Contains(sinkScript, "if (window.__wmSinkInstalled) return;") {
			t.Fatal("sinkScript: want the in-page guard return")
		}
		if sinkNewDocumentScript == sinkScript {
			t.Fatal("sinkNewDocumentScript: the raw body's top-level return is not a valid init script")
		}
		if !strings.HasPrefix(sinkNewDocumentScript, "(() => {") || !strings.HasSuffix(sinkNewDocumentScript, "})()") {
			t.Fatal("sinkNewDocumentScript: want a self-invoking function")
		}
		if !strings.Contains(sinkNewDocumentScript, sinkScript) {
			t.Fatal("sinkNewDocumentScript: want the shared tracer body")
		}
		if !strings.Contains(sinkNewDocumentScript, "wmTagLocation") || !strings.Contains(sinkNewDocumentScript, "wmLiveLocation") {
			t.Fatal("sinkNewDocumentScript: want location tagging in every document")
		}
		for _, tag := range []string{"'location.hash'", "'location.search'", "'location.href'"} {
			if !strings.Contains(sinkNewDocumentScript, tag) {
				t.Fatalf("sinkNewDocumentScript: want source tag %s", tag)
			}
		}
	})

	t.Run("value clipping marks cuts", func(t *testing.T) {
		long := strings.Repeat("x", 250)
		stack := make([]string, 7)
		for i := range stack {
			stack[i] = long
		}
		raw, err := json.Marshal(sinkSnapshot{Hits: []SinkHit{{
			Sink:     "eval",
			Context:  "js",
			Value:    long,
			Stack:    stack,
			Selector: long,
		}}})
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		res, err := parseSinkSnapshot(string(raw))
		if err != nil {
			t.Fatalf("parse snapshot: %v", err)
		}
		got := res.Hits[0]
		if !got.ValueCut {
			t.Fatal("value_cut: got false, want true")
		}
		if got.Value != strings.Repeat("x", maxSinkValueRunes)+ellipsis {
			t.Fatalf("value: got %d runes, want %d plus an ellipsis", len([]rune(got.Value)), maxSinkValueRunes+1)
		}
		if len(got.Stack) != maxSinkStackFrames {
			t.Fatalf("stack: got %d frames, want %d", len(got.Stack), maxSinkStackFrames)
		}
		if len([]rune(got.Stack[0])) != maxSinkFieldRunes+1 {
			t.Fatalf("stack frame: got %d runes, want it clipped", len([]rune(got.Stack[0])))
		}
		if len([]rune(got.Selector)) != maxSinkFieldRunes+1 {
			t.Fatalf("selector: got %d runes, want it clipped", len([]rune(got.Selector)))
		}

		short, cut := clipSinkValue("wm-canary", false)
		if short != "wm-canary" || cut {
			t.Fatalf("short value: got %q cut=%v, want it unchanged", short, cut)
		}
	})
}

// TestSinkSourceHooks pins the source set the tracer tags. Each name is part of
// the sinks envelope contract, so a missing hook silently downgrades a hit to
// source:"unknown".
func TestSinkSourceHooks(t *testing.T) {
	cases := []struct {
		source string
		marker string
	}{
		{"location.hash", "'location.hash'"},
		{"location.search", "'location.search'"},
		{"location.href", "'location.href'"},
		{"document.URL", "'document.URL'"},
		{"document.referrer", "'document.referrer'"},
		{"document.cookie", "'document.cookie'"},
		{"window.name", "'window.name'"},
		{"localStorage.getItem", "'localStorage.getItem'"},
		{"sessionStorage.getItem", "'sessionStorage.getItem'"},
		{"message", "'message'"},
		{"history.state", "'history.state'"},
		{"URLSearchParams.get", "'URLSearchParams.get'"},
		{"URLSearchParams.getAll", "'URLSearchParams.getAll'"},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			if !strings.Contains(sinkScript, c.marker) {
				t.Fatalf("sinkScript is missing the %s source tag %s", c.source, c.marker)
			}
		})
	}

	if !strings.Contains(sinkScript, "WM_SOURCE_CAP = 24;") {
		t.Fatal("source ring cap changed; it must stay at 24")
	}
	if !strings.Contains(sinkScript, "wmClipCookie") {
		t.Fatal("document.cookie must be clipped before it is recorded")
	}
	if !strings.Contains(sinkScript, "'…'") {
		t.Fatal("document.cookie clipping must use an ellipsis")
	}
}

// TestSinkSourceAttribution proves each widened source is named end-to-end: the
// page reads the value and writes it with innerHTML, and the hit reports the
// tagged source. It skips when the host has no Chromium-family browser.
func TestSinkSourceAttribution(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><html><body></body></html>")
	}))
	t.Cleanup(server.Close)

	if err := p.Rod.Navigate(server.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := p.Rod.WaitLoad(); err != nil {
		t.Fatalf("wait for load: %v", err)
	}
	if err := p.InstallSinks(ctx); err != nil {
		t.Fatalf("InstallSinks: %v", err)
	}

	cases := []struct {
		name       string
		setup      string
		wantSource string
	}{
		{
			"window.name",
			`window.name = 'wmcanary-name-9371'; document.body.innerHTML = window.name;`,
			"window.name",
		},
		{
			"MessageEvent.data",
			`window.addEventListener('message', function (e) { document.body.innerHTML = e.data; });
			 window.dispatchEvent(new MessageEvent('message', { data: 'wmcanary-msg-4826' }));`,
			"message",
		},
		{
			"localStorage.getItem",
			`localStorage.setItem('wmk', 'wmcanary-local-5183'); document.body.innerHTML = localStorage.getItem('wmk');`,
			"localStorage.getItem",
		},
		{
			"sessionStorage.getItem",
			`sessionStorage.setItem('wmk', 'wmcanary-session-6204'); document.body.innerHTML = sessionStorage.getItem('wmk');`,
			"sessionStorage.getItem",
		},
		{
			"document.cookie",
			`document.cookie = 'wmsecret=SUPERSECRETVALUE123456; path=/'; document.body.innerHTML = document.cookie;`,
			"document.cookie",
		},
		{
			"history.state",
			`history.replaceState({ k: 'wmcanary-history-7345' }, '', location.href); document.body.innerHTML = JSON.stringify(history.state);`,
			"history.state",
		},
		{
			"URLSearchParams.get",
			`document.body.innerHTML = new URLSearchParams('q=wmcanary-params-8467').get('q');`,
			"URLSearchParams.get",
		},
		{
			"URLSearchParams.getAll",
			`document.body.innerHTML = new URLSearchParams('q=a&q=wmcanary-all-9578').getAll('q').join(',');`,
			"URLSearchParams.getAll",
		},
		{
			"unmatched value",
			`document.body.innerHTML = 'zzz-unmatched-wm-0000';`,
			"unknown",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := p.ClearSinks(ctx); err != nil {
				t.Fatalf("ClearSinks: %v", err)
			}
			if _, err := p.Rod.Eval("() => {" + c.setup + "}"); err != nil {
				t.Fatalf("run setup: %v", err)
			}
			res, err := p.SinkHits(ctx)
			if err != nil {
				t.Fatalf("SinkHits: %v", err)
			}
			var hit *SinkHit
			for i := range res.Hits {
				if res.Hits[i].Sink == "innerHTML" {
					hit = &res.Hits[i]
					break
				}
			}
			if hit == nil {
				t.Fatalf("hits = %+v, want an innerHTML hit", res.Hits)
			}
			if hit.Source != c.wantSource {
				t.Fatalf("source = %q, want %q (value %q)", hit.Source, c.wantSource, hit.Value)
			}
			if c.name == "document.cookie" {
				if strings.Contains(hit.Value, "SUPERSECRETVALUE") {
					t.Fatalf("cookie value leaked: %q", hit.Value)
				}
				if !strings.Contains(hit.Value, "…") {
					t.Fatalf("cookie value = %q, want it clipped with an ellipsis", hit.Value)
				}
			}
		})
	}
}
