package browsers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func TestCanaryTargets(t *testing.T) {
	t.Run("empty means all four", func(t *testing.T) {
		got, err := parseCanaryTargets(nil)
		if err != nil {
			t.Fatalf("parseCanaryTargets(nil): %v", err)
		}
		want := []CanaryTarget{CanaryQuery, CanaryHash, CanaryPostMessage, CanaryName}
		if len(got) != len(want) {
			t.Fatalf("targets = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("targets = %v, want %v", got, want)
			}
		}
	})

	t.Run("comma list and repeats dedupe", func(t *testing.T) {
		got, err := parseCanaryTargets([]string{"hash, postMessage", "hash", "NAME"})
		if err != nil {
			t.Fatalf("parseCanaryTargets: %v", err)
		}
		want := []CanaryTarget{CanaryHash, CanaryPostMessage, CanaryName}
		if len(got) != len(want) {
			t.Fatalf("targets = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("targets = %v, want %v", got, want)
			}
		}
	})

	t.Run("unknown target is rejected", func(t *testing.T) {
		_, err := parseCanaryTargets([]string{"hash,dom"})
		if err == nil {
			t.Fatal("parseCanaryTargets accepted an unknown target")
		}
		if !strings.Contains(err.Error(), `invalid canary target "dom"`) {
			t.Fatalf("error = %q, want the invalid-target message", err)
		}
	})
}

func TestCanaryValueFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^wm-canary-[0-9a-f]{8}$`)
	for i := 0; i < 20; i++ {
		if got := newCanaryValue(); !pattern.MatchString(got) {
			t.Fatalf("newCanaryValue() = %q, want wm-canary-<8 hex>", got)
		}
	}
}

func TestCanaryFrameCap(t *testing.T) {
	frames := make([]canaryFrame, 25)
	for i := range frames {
		frames[i] = canaryFrame{page: &rod.Page{}}
	}
	got := canaryFrameWindow(frames)
	if len(got) != CanaryFrameCap {
		t.Fatalf("frame window = %d, want %d", len(got), CanaryFrameCap)
	}
}

func TestInjectCanaryGuard(t *testing.T) {
	p := &Page{Rod: &rod.Page{}}
	_, err := p.InjectCanary(context.Background(), []string{"hash"}, "")
	if !errors.Is(err, ErrCanaryNeedsTracer) {
		t.Fatalf("error = %v, want ErrCanaryNeedsTracer", err)
	}
	want := `inject_canary needs the sink tracer — run action "sinks_install" first`
	if err.Error() != want {
		t.Fatalf("guard error = %q, want %q", err.Error(), want)
	}
}

// TestInjectCanaryReachesHashSink proves the hash target reaches a sink with no
// reload: a marker set on window survives, and the hash reaches innerHTML.
func TestInjectCanaryReachesHashSink(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.InstallSinks(ctx); err != nil {
		t.Fatalf("InstallSinks: %v", err)
	}
	if _, err := p.Rod.Eval(`() => {
  window.__wmCanaryNoReload = true;
  window.addEventListener('hashchange', () => {
    (document.body || document.documentElement).innerHTML = location.hash;
  });
  return true;
}`); err != nil {
		t.Fatalf("install hash sink: %v", err)
	}

	res, err := p.InjectCanary(ctx, []string{"hash"}, "")
	if err != nil {
		t.Fatalf("InjectCanary: %v", err)
	}
	if !regexp.MustCompile(`^wm-canary-[0-9a-f]{8}$`).MatchString(res.Canary) {
		t.Fatalf("canary = %q, want the default wm-canary-<8 hex>", res.Canary)
	}
	if !res.Installed {
		t.Fatal("installed = false, want true")
	}
	if len(res.Injected) == 0 || res.Injected[0].Target != string(CanaryHash) {
		t.Fatalf("injected = %+v, want the hash target", res.Injected)
	}

	var found bool
	for _, hit := range res.Hits {
		if hit.Sink == "innerHTML" && hit.Context == "html" && strings.Contains(hit.Value, res.Canary) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v, want innerHTML/html carrying %q", res.Hits, res.Canary)
	}

	if marker, err := p.Rod.Eval("() => window.__wmCanaryNoReload === true"); err != nil || !marker.Value.Bool() {
		t.Fatalf("marker survived = %v (err %v), want true: hash must not reload", marker, err)
	}
}

// TestInjectCanaryReachesPostMessageSink proves the postmessage target reaches a
// listener and records the message source.
func TestInjectCanaryReachesPostMessageSink(t *testing.T) {
	p := newCanaryTestPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.InstallSinks(ctx); err != nil {
		t.Fatalf("InstallSinks: %v", err)
	}
	if _, err := p.Rod.Eval(`() => {
  window.addEventListener('message', (e) => {
    if (e.data && e.data.type === 'wm-probe') {
      (document.body || document.documentElement).innerHTML = e.data.data;
    }
  });
  return true;
}`); err != nil {
		t.Fatalf("install message listener: %v", err)
	}

	res, err := p.InjectCanary(ctx, []string{"postmessage"}, "")
	if err != nil {
		t.Fatalf("InjectCanary: %v", err)
	}
	if len(res.Injected) == 0 || res.Injected[0].Target != string(CanaryPostMessage) {
		t.Fatalf("injected = %+v, want the postmessage target", res.Injected)
	}

	var found bool
	for _, hit := range res.Hits {
		if strings.Contains(hit.Value, res.Canary) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v, want a sink carrying %q", res.Hits, res.Canary)
	}
}

// TestInjectCanaryReachesQuerySink proves the query target appends the canary,
// reloads, and is still captured after the tracer re-arms on the new document.
func TestInjectCanaryReachesQuerySink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><body><script>
(document.body || document.documentElement).innerHTML = location.search;
</script></body></html>`)
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
	if err := p.InstallSinks(ctx); err != nil {
		t.Fatalf("InstallSinks: %v", err)
	}

	res, err := p.InjectCanary(ctx, []string{"query"}, "")
	if err != nil {
		t.Fatalf("InjectCanary: %v", err)
	}
	if len(res.Injected) != 1 || res.Injected[0].Target != string(CanaryQuery) {
		t.Fatalf("injected = %+v, want one query injection", res.Injected)
	}
	if !strings.Contains(res.Injected[0].Where, "wmcanary="+res.Canary) {
		t.Fatalf("where = %q, want the wmcanary query parameter", res.Injected[0].Where)
	}

	var found bool
	for _, hit := range res.Hits {
		if hit.Sink == "innerHTML" && hit.Context == "html" && strings.Contains(hit.Value, res.Canary) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v, want innerHTML/html carrying %q", res.Hits, res.Canary)
	}
}

// newCanaryTestPage launches a headless browser for the canary integration test
// and skips when the host has none, so the unit suite stays portable.
func newCanaryTestPage(t *testing.T) *Page {
	t.Helper()
	bin := DefaultBinPath()
	if bin == "" {
		t.Skip("no Chromium-family browser on PATH")
	}
	l := launcher.New().Bin(bin).Headless(true).NoSandbox(true)
	controlURL, err := l.Launch()
	if err != nil {
		t.Skipf("launch browser: %v", err)
	}
	t.Cleanup(l.Cleanup)

	browser := rod.New().ControlURL(controlURL).NoDefaultDevice()
	if err := browser.Connect(); err != nil {
		t.Skipf("connect browser: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		t.Fatalf("open page: %v", err)
	}
	t.Cleanup(func() { _ = page.Close() })

	return &Page{ID: "page-1", Rod: page}
}
