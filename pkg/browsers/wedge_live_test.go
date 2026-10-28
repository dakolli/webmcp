package browsers

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestNavigateTimeoutLeavesReadFastFailing pins WEB-229 against a real browser:
// a navigate to a non-routable address never commits a document, so the tab's
// renderer stops answering page-domain CDP calls. The read that page_fetch
// what=meta opens with — the guarded read lock, then the ctx-bound page-info
// read — must then fail in well under a second with the recovery prose instead
// of burning its whole call budget.
func TestNavigateTimeoutLeavesReadFastFailing(t *testing.T) {
	p := newCanaryTestPage(t)

	navCtx, navCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer navCancel()
	navStarted := time.Now()
	_, navErr := GuardPage(navCtx, p, "navigate", func(ctx context.Context) (string, error) {
		page := p.Rod.Context(ctx)
		if err := page.Navigate("http://10.255.255.1/"); err != nil {
			return "", err
		}
		return "", page.WaitLoad()
	})
	t.Logf("navigate returned after %s with %v", time.Since(navStarted).Round(time.Millisecond), navErr)

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	started := time.Now()
	err := readPageInfo(p, readCtx)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatalf("page-info read after the navigate timeout returned no error after %s", elapsed)
	}
	if elapsed >= time.Second {
		t.Fatalf("page-info read burned %s, want under 1s (error = %v)", elapsed.Round(time.Millisecond), err)
	}
	lower := strings.ToLower(err.Error())
	for _, want := range []string{"unusable", "session_close"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("page-info read error = %q, want it to contain %q", err, want)
		}
	}
}

// readPageInfo mirrors the read path page_fetch what=meta opens with: take the
// guarded read lock, then read the page's URL and title.
func readPageInfo(p *Page, ctx context.Context) error {
	release, err := p.BeginRead(ctx, "meta")
	if err != nil {
		return err
	}
	defer release()
	_, _, err = p.State(ctx)
	return err
}
