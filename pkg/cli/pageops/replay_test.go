package pageops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// TestPageReplayOnWedgedPageTakesOneReadBudget pins WEB-242 through the real
// pageops entry point: a replay that needs browser-side input (the page's
// cookies) costs exactly one bounded read budget on a wedged page, while a
// replay that needs nothing from the page still completes, and a healthy replay
// stays sub-100 ms.
func TestPageReplayOnWedgedPageTakesOneReadBudget(t *testing.T) {
	bin := browsers.DefaultBinPath()
	if bin == "" {
		t.Skip("no Chromium-family browser on PATH")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Got-Cookie", r.Header.Get("Cookie"))
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel()
	b := browsers.NewBrowser("browser-1", baseCtx, true, false, bin, t.TempDir())
	launchCtx, launchCancel := context.WithTimeout(baseCtx, 30*time.Second)
	defer launchCancel()
	if err := b.Launch(launchCtx); err != nil {
		t.Skipf("launch browser: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = b.Close(closeCtx)
	}()

	openCtx, openCancel := context.WithTimeout(baseCtx, 30*time.Second)
	defer openCancel()
	p, err := b.NewPage(openCtx, srv.URL, nil, "", "")
	if err != nil {
		t.Fatalf("open page: %v", err)
	}

	seq := int64(-1)
	entries, _ := p.NetworkEntries()
	for _, e := range entries {
		if strings.HasPrefix(e.URL, srv.URL) {
			seq = e.Seq
			break
		}
	}
	if seq < 0 {
		t.Fatalf("no captured network entry for %s (entries=%d)", srv.URL, len(entries))
	}
	target := strconv.FormatInt(seq, 10)

	t.Run("healthy replay stays fast", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		if _, err := PageReplay(ctx, p, ReplayRequest{Target: target}); err != nil {
			t.Fatalf("healthy replay: %v", err)
		}
		if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
			t.Fatalf("healthy replay took %s, want under 100ms", elapsed.Round(time.Millisecond))
		}
	})

	t.Run("wedged replay needs one read budget", func(t *testing.T) {
		p.Lock()
		defer p.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		_, err := PageReplay(ctx, p, ReplayRequest{Target: target})
		elapsed := time.Since(started)
		if err == nil {
			t.Fatalf("wedged replay returned no error after %s", elapsed.Round(time.Millisecond))
		}
		if elapsed < 500*time.Millisecond || elapsed >= time.Second {
			t.Fatalf("wedged replay burned %s, want exactly one 500ms read budget (error = %v)", elapsed.Round(time.Millisecond), err)
		}
		lower := strings.ToLower(err.Error())
		for _, want := range []string{"unusable", "session_close"} {
			if !strings.Contains(lower, want) {
				t.Fatalf("wedged replay error = %q, want it to contain %q", err, want)
			}
		}
	})

	t.Run("wedged replay that needs no page read still runs", func(t *testing.T) {
		p.Lock()
		defer p.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		out, err := PageReplay(ctx, p, ReplayRequest{
			Target:  target,
			Headers: map[string]string{"Cookie": "sid=test"},
		})
		if err != nil {
			t.Fatalf("wedged lock-free replay: %v", err)
		}
		if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
			t.Fatalf("wedged lock-free replay took %s, want under 100ms", elapsed.Round(time.Millisecond))
		}
		if !strings.Contains(out, "sid=test") {
			t.Fatalf("wedged lock-free replay output = %s, want the Cookie override to reach the server", out)
		}
	})
}

func TestPageReplayDelegatesToEngine(t *testing.T) {
	p := &browsers.Page{}
	_, err := PageReplay(context.Background(), p, ReplayRequest{Target: "  "})
	if err == nil || !strings.Contains(err.Error(), "replay requires target") {
		t.Fatalf("PageReplay() error = %v, want the engine's target error", err)
	}
}

// TestReplayQueryURL pins the query contract: a non-empty value replaces the
// parameter, an empty value removes it, and every other parameter survives.
func TestReplayQueryURL(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		edits map[string]string
		want  string
	}{
		{
			name:  "add a parameter beside the captured one",
			raw:   "https://host.test/search?q=test",
			edits: map[string]string{"lang": "en"},
			want:  "https://host.test/search?lang=en&q=test",
		},
		{
			name:  "replace a parameter",
			raw:   "https://host.test/search?q=test",
			edits: map[string]string{"q": "other"},
			want:  "https://host.test/search?q=other",
		},
		{
			name:  "remove a parameter with an empty value",
			raw:   "https://host.test/search?q=test&lang=en",
			edits: map[string]string{"q": ""},
			want:  "https://host.test/search?lang=en",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := replayQueryURL(tt.raw, tt.edits)
			if err != nil {
				t.Fatalf("replayQueryURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("replayQueryURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
