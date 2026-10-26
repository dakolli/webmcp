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

// TestPostMessageUninstalled pins the read side before arming: an untouched
// page reports installed=false with an empty (never nil) message list and an
// unprobed root frame.
func TestPostMessageUninstalled(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := p.PostMessages(ctx)
	if err != nil {
		t.Fatalf("PostMessages: %v", err)
	}
	if res.Installed {
		t.Fatal("installed = true, want false before install")
	}
	if len(res.Messages) != 0 {
		t.Fatalf("messages = %+v, want none", res.Messages)
	}
	if res.Messages == nil {
		t.Fatal("messages must be [] on the wire, not nil")
	}
	if len(res.Frames) != 1 || res.Frames[0].Probed {
		t.Fatalf("frames = %+v, want one unprobed root frame", res.Frames)
	}
}

// TestPostMessageToTopAndFrame proves delivery to window.top and to an iframe,
// with the origin recorded, the payload clipped and the frame reported by the
// read side.
func TestPostMessageToTopAndFrame(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<iframe id="f" srcdoc="<!doctype html><html><body>child</body></html>"></iframe>
</body></html>`)
	}))
	t.Cleanup(server.Close)

	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.Rod.Navigate(server.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := p.Rod.WaitLoad(); err != nil {
		t.Fatalf("wait for load: %v", err)
	}
	waitFrameReady(t, p, "iframe#f")

	if err := p.InstallPostMessages(ctx); err != nil {
		t.Fatalf("InstallPostMessages: %v", err)
	}

	sent, err := p.SendPostMessage(ctx, "hello-top", "top", "")
	if err != nil {
		t.Fatalf("send to top: %v", err)
	}
	if sent.Origin != "*" || sent.Target != "top" {
		t.Fatalf("send to top = %+v, want origin * target top", sent)
	}
	if _, err := p.SendPostMessage(ctx, "hello-frame", "iframe#f", ""); err != nil {
		t.Fatalf("send to frame: %v", err)
	}
	if _, err := p.SendPostMessage(ctx, `{"t":"wm"}`, "top", ""); err != nil {
		t.Fatalf("send object: %v", err)
	}

	long := strings.Repeat("x", 600)
	if _, err := p.SendPostMessage(ctx, long, "top", "*"); err != nil {
		t.Fatalf("send long payload: %v", err)
	}

	res, err := p.PostMessages(ctx)
	if err != nil {
		t.Fatalf("PostMessages: %v", err)
	}
	if !res.Installed {
		t.Fatal("installed = false, want true after install")
	}

	byData := map[string]PostMessage{}
	for _, m := range res.Messages {
		byData[m.Data] = m
	}
	top, ok := byData["hello-top"]
	if !ok {
		t.Fatalf("messages = %+v, want the top payload", res.Messages)
	}
	if top.Origin != server.URL {
		t.Errorf("top origin = %q, want %q", top.Origin, server.URL)
	}
	if top.DataType != "string" || top.SourceKind != "window" {
		t.Errorf("top message = %+v, want data_type string and source_kind window", top)
	}

	frame, ok := byData["hello-frame"]
	if !ok {
		t.Fatalf("messages = %+v, want the frame payload", res.Messages)
	}
	if frame.Origin != server.URL {
		t.Errorf("frame origin = %q, want the sender origin %q", frame.Origin, server.URL)
	}

	obj, ok := byData[`{"t":"wm"}`]
	if !ok {
		t.Fatalf("messages = %+v, want the object payload", res.Messages)
	}
	if obj.DataType != "json" {
		t.Errorf("object data_type = %q, want json", obj.DataType)
	}

	if got := len([]rune(byData[strings.Repeat("x", maxPostMessageDataRunes)].Data)); got != maxPostMessageDataRunes {
		t.Errorf("clipped data = %d runes, want %d", got, maxPostMessageDataRunes)
	}

	var probedFrame bool
	for _, f := range res.Frames {
		if f.Probed && f.URL != server.URL {
			probedFrame = true
		}
	}
	if !probedFrame {
		t.Fatalf("frames = %+v, want a probed iframe", res.Frames)
	}
}

// TestPostMessageFrameSelectorRouting proves a frame selector delivers only to
// that frame's contentWindow: the payload lands in the frame ring and never in
// the top ring, keywords still resolve, and a non-frame selector returns the
// documented error instead of falling back to top.
func TestPostMessageFrameSelectorRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<iframe id="f" srcdoc="<!doctype html><html><body>child</body></html>"></iframe>
<div id="d">not a frame</div>
</body></html>`)
	}))
	t.Cleanup(server.Close)

	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.Rod.Navigate(server.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := p.Rod.WaitLoad(); err != nil {
		t.Fatalf("wait for load: %v", err)
	}
	waitFrameReady(t, p, "iframe#f")
	if err := p.InstallPostMessages(ctx); err != nil {
		t.Fatalf("InstallPostMessages: %v", err)
	}

	top := p.Rod.Context(ctx)
	frameEl, err := top.Element("iframe#f")
	if err != nil {
		t.Fatalf("find frame element: %v", err)
	}
	frame, err := frameEl.Frame()
	if err != nil {
		t.Fatalf("promote frame: %v", err)
	}

	for _, target := range []string{"iframe", "iframe#f"} {
		t.Run("frame selector "+target, func(t *testing.T) {
			payload := "frame-" + target
			sent, err := p.SendPostMessage(ctx, payload, target, "")
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if sent.Origin != "*" || sent.Target != target {
				t.Fatalf("sent = %+v, want origin * target %q", sent, target)
			}
			if topMessages, _, err := readPostMessageRing(top); err != nil {
				t.Fatalf("read top ring: %v", err)
			} else if postMessageHasData(topMessages, payload) {
				t.Fatalf("top ring holds %q; a frame selector must not deliver to top", payload)
			}
			if frameMessages, _, err := readPostMessageRing(frame.Context(ctx)); err != nil {
				t.Fatalf("read frame ring: %v", err)
			} else if !postMessageHasData(frameMessages, payload) {
				t.Fatalf("frame ring = %+v, want %q", frameMessages, payload)
			}
		})
	}

	for _, keyword := range []string{"top", "self", "parent"} {
		t.Run("keyword "+keyword, func(t *testing.T) {
			payload := "keyword-" + keyword
			sent, err := p.SendPostMessage(ctx, payload, keyword, "")
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if sent.Target != keyword {
				t.Fatalf("target = %q, want %q", sent.Target, keyword)
			}
			if topMessages, _, err := readPostMessageRing(top); err != nil {
				t.Fatalf("read top ring: %v", err)
			} else if !postMessageHasData(topMessages, payload) {
				t.Fatalf("top ring = %+v, want %q", topMessages, payload)
			}
			if frameMessages, _, err := readPostMessageRing(frame.Context(ctx)); err != nil {
				t.Fatalf("read frame ring: %v", err)
			} else if postMessageHasData(frameMessages, payload) {
				t.Fatalf("frame ring holds %q; keyword %q must deliver to the top window", payload, keyword)
			}
		})
	}

	t.Run("non-frame selector returns the documented error", func(t *testing.T) {
		_, err := p.SendPostMessage(ctx, "nope", "div", "")
		want := `post_message target "div" is not a frame — pass "top", "self", "parent", or a CSS selector for an <iframe> or <frame>`
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	})
}

// postMessageHasData reports whether any recorded message carries data.
func postMessageHasData(messages []PostMessage, data string) bool {
	for _, m := range messages {
		if m.Data == data {
			return true
		}
	}
	return false
}

// TestPostMessageRingCap pins the 100-entry ring: the newest 100 survive, the
// evicted 5 are counted as dropped, and the envelope reports it.
func TestPostMessageRingCap(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.InstallPostMessages(ctx); err != nil {
		t.Fatalf("InstallPostMessages: %v", err)
	}
	if _, err := p.Rod.Eval(`() => {
  for (let i = 0; i < 105; i++) window.postMessage('m' + i, '*');
  return true;
}`); err != nil {
		t.Fatalf("post messages: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	res, err := p.PostMessages(ctx)
	if err != nil {
		t.Fatalf("PostMessages: %v", err)
	}
	if len(res.Messages) != PostMessageRingCap {
		t.Fatalf("messages = %d, want %d", len(res.Messages), PostMessageRingCap)
	}
	if res.Dropped != 5 {
		t.Fatalf("dropped = %d, want 5", res.Dropped)
	}
	if res.Messages[0].Data != "m5" || res.Messages[len(res.Messages)-1].Data != "m104" {
		t.Fatalf("window = %q..%q, want m5..m104", res.Messages[0].Data, res.Messages[len(res.Messages)-1].Data)
	}
}

// TestPostMessageClear pins that clearing empties every ring and resets the
// dropped counter.
func TestPostMessageClear(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.InstallPostMessages(ctx); err != nil {
		t.Fatalf("InstallPostMessages: %v", err)
	}
	if _, err := p.SendPostMessage(ctx, "one", "top", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	cleared, err := p.ClearPostMessages(ctx)
	if err != nil {
		t.Fatalf("ClearPostMessages: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}

	res, err := p.PostMessages(ctx)
	if err != nil {
		t.Fatalf("PostMessages: %v", err)
	}
	if !res.Installed || len(res.Messages) != 0 || res.Dropped != 0 {
		t.Fatalf("after clear = %+v, want installed with an empty ring", res)
	}
}

// TestPostMessageSnapshot pins the decoder and the Go-side defensive clipping.
func TestPostMessageSnapshot(t *testing.T) {
	t.Run("null is an empty uninstalled window", func(t *testing.T) {
		messages, dropped, err := parsePostMessageSnapshot("null")
		if err != nil {
			t.Fatalf("parse null: %v", err)
		}
		if len(messages) != 0 || dropped != 0 {
			t.Fatalf("null snapshot = %d messages, %d dropped", len(messages), dropped)
		}
	})

	t.Run("newest window and clipping", func(t *testing.T) {
		long := strings.Repeat("x", 600)
		messages := make([]PostMessage, 250)
		for i := range messages {
			messages[i] = PostMessage{
				Origin:     "https://example.com",
				SourceKind: "window",
				Data:       fmt.Sprintf("hit-%d", i),
				DataType:   "string",
				TS:         int64(i),
			}
		}
		messages[0].Data = long
		raw, err := json.Marshal(postMessageSnapshot{Messages: messages, Dropped: 7})
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		parsed, dropped, err := parsePostMessageSnapshot(string(raw))
		if err != nil {
			t.Fatalf("parse snapshot: %v", err)
		}
		res := trimPostMessages(parsed, dropped, nil)
		if len(res.Messages) != MaxPostMessages {
			t.Fatalf("emitted = %d, want %d", len(res.Messages), MaxPostMessages)
		}
		if res.Truncated != 150 || res.Dropped != 7 {
			t.Fatalf("counters = truncated %d dropped %d, want 150/7", res.Truncated, res.Dropped)
		}
		if res.Messages[0].Data != "hit-150" || res.Messages[len(res.Messages)-1].Data != "hit-249" {
			t.Fatalf("window = %q..%q, want hit-150..hit-249", res.Messages[0].Data, res.Messages[len(res.Messages)-1].Data)
		}

		clipped := trimPostMessages([]PostMessage{{Data: long, TS: 1}}, 0, nil)
		if got := len([]rune(clipped.Messages[0].Data)); got != maxPostMessageDataRunes+1 {
			t.Fatalf("clipped data = %d runes, want %d plus an ellipsis", got, maxPostMessageDataRunes)
		}
	})

	t.Run("recorder body has its caps baked in", func(t *testing.T) {
		if strings.Contains(postMessageScript, "%d") || strings.Contains(postMessageScript, "%!") {
			t.Fatal("postMessageScript still carries a format verb")
		}
		if !strings.Contains(postMessageScript, "window.addEventListener('message', wmPmRecord, true)") {
			t.Fatal("postMessageScript: want a capture-phase window listener")
		}
		if strings.Contains(postMessageScript, "addEventListener =") {
			t.Fatal("postMessageScript must not wrap addEventListener")
		}
		if !strings.Contains(postMessageScript, "JSON.stringify(data)") {
			t.Fatal("postMessageScript: want object payloads serialized")
		}
		if !strings.HasPrefix(postMessageInstallJS, "() => {") {
			t.Fatalf("postMessageInstallJS: got %q, want a function expression", postMessageInstallJS[:12])
		}
		if !strings.HasPrefix(postMessageNewDocumentScript, "(() => {") {
			t.Fatal("postMessageNewDocumentScript: want a self-invoking function")
		}
	})
}

// waitFrameReady blocks until the iframe's document finished loading, so the
// install walk sees a live frame.
func waitFrameReady(t *testing.T, p *Page, selector string) {
	t.Helper()
	js := `() => new Promise((resolve) => {
  const f = document.querySelector(` + jsLiteral(selector) + `);
  if (!f) return resolve(false);
  if (f.contentDocument && f.contentDocument.readyState === 'complete') return resolve(true);
  f.addEventListener('load', () => resolve(true), { once: true });
})`
	if _, err := p.Rod.Timeout(10 * time.Second).Eval(js); err != nil {
		t.Fatalf("wait for frame %q: %v", selector, err)
	}
}
