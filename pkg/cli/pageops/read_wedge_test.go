package pageops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// TestReadReturnsWhenPageLockIsHeld pins WEB-229's real read-path failure: a
// read started while another operation still holds the page lock used to wait
// out its own call deadline and then be relabelled a timeout. The test holds the
// lock — the state a wedged navigate leaves behind — and never trips the
// watchdog, so only the bounded read lock itself can make a read return in time.
// Readers that answer from the in-memory rings without the page lock (network,
// console) or need a captured entry first (body) are out of scope here; har is
// likewise a ring export that only touches the page per entry.
func TestReadReturnsWhenPageLockIsHeld(t *testing.T) {
	p := &browsers.Page{ID: "page-1"}
	p.Lock()
	t.Cleanup(p.Unlock)

	reads := []struct {
		name string
		read func(context.Context) (string, error)
	}{
		{"html", func(ctx context.Context) (string, error) { return PageHTML(ctx, p, "") }},
		{"dom", func(ctx context.Context) (string, error) { return PageDOM(ctx, p) }},
		{"text", func(ctx context.Context) (string, error) { return PageText(ctx, p, "") }},
		{"links", func(ctx context.Context) (string, error) { return PageLinks(ctx, p) }},
		{"meta", func(ctx context.Context) (string, error) { return PageMeta(ctx, p) }},
		{"css", func(ctx context.Context) (string, error) { return PageCSS(ctx, p, "") }},
		{"js", func(ctx context.Context) (string, error) { return PageJS(ctx, p, "") }},
		{"screenshot", func(ctx context.Context) (string, error) { return PageScreenshot(ctx, p, false) }},
		{"interactives", func(ctx context.Context) (string, error) { return PageInteractives(ctx, p, "") }},
		{"a11y", func(ctx context.Context) (string, error) { return PageA11y(ctx, p) }},
		{"tables", func(ctx context.Context) (string, error) { return PageTables(ctx, p) }},
		{"forms", func(ctx context.Context) (string, error) { return PageForms(ctx, p) }},
		{"storage", func(ctx context.Context) (string, error) { return PageStorage(ctx, p, false) }},
		{"security", func(ctx context.Context) (string, error) { return PageSecurity(ctx, p) }},
		{"article", func(ctx context.Context) (string, error) { return PageArticle(ctx, p) }},
		{"marks", func(ctx context.Context) (string, error) { return PageMarks(ctx, p) }},
		{"sinks", func(ctx context.Context) (string, error) { return PageSinks(ctx, p) }},
		{"postmessage", func(ctx context.Context) (string, error) { return PagePostMessage(ctx, p, "") }},
		{"state", func(ctx context.Context) (string, error) {
			_, err := PageStateOf(ctx, p)
			return "", err
		}},
	}

	for _, tt := range reads {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			started := time.Now()
			_, err := tt.read(ctx)
			elapsed := time.Since(started)

			if err == nil {
				t.Fatal("read on a locked page returned no error")
			}
			if elapsed >= time.Second {
				t.Fatalf("read waited %s behind a held page lock, want under 1s", elapsed)
			}
			lower := strings.ToLower(err.Error())
			for _, want := range []string{"unusable", "session_close"} {
				if !strings.Contains(lower, want) {
					t.Fatalf("read error = %q, want it to contain %q", err, want)
				}
			}
			if strings.Contains(lower, "raise timeout_ms") {
				t.Fatalf("read error = %q, must not advise a higher timeout_ms for a locked page", err)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("read error = %v, must not unwrap to DeadlineExceeded (page_fetch would relabel it as a timeout)", err)
			}
			var hinted interface{ Hint() string }
			if !errors.As(err, &hinted) || !strings.Contains(hinted.Hint(), "session_close") {
				t.Fatalf("read hint = %v, want it to name session_close", err)
			}
		})
	}
}
