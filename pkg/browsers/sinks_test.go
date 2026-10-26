package browsers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
