package pageops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// busyWaitPattern captures the wait a "still busy" error reports, so the test
// can compare the prose against the wall time.
var busyWaitPattern = regexp.MustCompile(`busy after ([0-9.]+(?:µs|ms|s))`)

// TestEvalOnWedgedPageFailsFast pins WEB-247 for the eval act: on a page whose
// operation lock a wedged operation still holds, eval must reject in well under
// the one-second recovery promise with the real wait and both close verbs
// instead of burning the caller's whole action budget.
func TestEvalOnWedgedPageFailsFast(t *testing.T) {
	assertActFailsFastOnWedgedPage(t, "eval", func(ctx context.Context, p *browsers.Page) error {
		_, err := PageEval(ctx, p, "() => document.title")
		return err
	})
}

// TestClickOnWedgedPageFailsFast pins WEB-247 for the click act: click acquires
// the page lock through the same bounded budget as a read, so a wedged page
// rejects it in under the one-second recovery promise rather than after the
// full action budget.
func TestClickOnWedgedPageFailsFast(t *testing.T) {
	assertActFailsFastOnWedgedPage(t, "click", func(ctx context.Context, p *browsers.Page) error {
		_, err := PageClickFrom(ctx, p, "", "#submit", "left", 1, PageState{})
		return err
	})
}

// assertActFailsFastOnWedgedPage holds the page lock — the state a wedged
// operation leaves behind — and runs one act against it. The action budget
// mirrors the MCP default, so the old unbounded wait would take the full 30s.
func assertActFailsFastOnWedgedPage(
	t *testing.T,
	op string,
	act func(context.Context, *browsers.Page) error,
) {
	t.Helper()
	p := &browsers.Page{ID: "page-1"}
	p.Lock()
	t.Cleanup(p.Unlock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	started := time.Now()
	err := act(ctx, p)
	elapsed := time.Since(started)
	t.Logf("%s on a wedged page returned after %s with %v", op, elapsed.Round(time.Millisecond), err)

	if err == nil {
		t.Fatalf("%s on a wedged page returned no error", op)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("%s burned %s, want under 1.5s", op, elapsed.Round(time.Millisecond))
	}

	msg := strings.ToLower(err.Error())
	for _, want := range []string{op, "page-1", "busy after", "unusable", "session_close", "browser page close"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("%s error = %q, want it to contain %q", op, err, want)
		}
	}

	matches := busyWaitPattern.FindStringSubmatch(err.Error())
	if matches == nil {
		t.Fatalf("%s error = %q, want it to report the wait after \"busy after\"", op, err)
	}
	reported, parseErr := time.ParseDuration(matches[1])
	if parseErr != nil {
		t.Fatalf("%s error reports wait %q, cannot parse: %v", op, matches[1], parseErr)
	}
	if diff := reported - elapsed; diff > 300*time.Millisecond || diff < -300*time.Millisecond {
		t.Fatalf("%s error reports a %s wait, wall time was %s", op, reported, elapsed.Round(time.Millisecond))
	}

	var hinted interface{ Hint() string }
	if !errors.As(err, &hinted) {
		t.Fatalf("%s error = %v, want a Hint", op, err)
	}
	for _, want := range []string{"session_close", "browser page close"} {
		if !strings.Contains(hinted.Hint(), want) {
			t.Fatalf("%s hint = %q, want it to name %q", op, hinted.Hint(), want)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s error = %v, must not unwrap to DeadlineExceeded", op, err)
	}
}

// wedgedLivePage launches a headless browser, opens one page and drives a
// navigate into a non-routable address, so the navigation never commits and the
// tab's renderer stops answering page-domain calls. The navigate's own budget
// expires first, so GuardPage releases the operation lock without tripping the
// watchdog: the page is left lock-free and renderer-wedged, the state the live
// MCP act hit.
func wedgedLivePage(t *testing.T) *browsers.Page {
	t.Helper()
	bin := browsers.DefaultBinPath()
	if bin == "" {
		t.Skip("no Chromium-family browser on PATH")
	}

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)
	b := browsers.NewBrowser("browser-1", baseCtx, true, false, bin, t.TempDir())
	launchCtx, launchCancel := context.WithTimeout(baseCtx, 30*time.Second)
	defer launchCancel()
	if err := b.Launch(launchCtx); err != nil {
		t.Skipf("launch browser: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = b.Close(closeCtx)
	})

	openCtx, openCancel := context.WithTimeout(baseCtx, 30*time.Second)
	defer openCancel()
	p, err := b.NewPage(openCtx, "", nil, "", "")
	if err != nil {
		t.Fatalf("open page: %v", err)
	}

	navCtx, navCancel := context.WithTimeout(baseCtx, 2*time.Second)
	defer navCancel()
	_, navErr := browsers.GuardPage(navCtx, p, "navigate", func(ctx context.Context) (string, error) {
		page := p.Rod.Context(ctx)
		if err := page.Navigate("http://10.255.255.1/"); err != nil {
			return "", err
		}
		return "", page.WaitLoad()
	})
	t.Logf("navigate left the renderer wedged: %v", navErr)
	return p
}

// TestActOnWedgedPageNeverSpendsTheActionBudget pins WEB-247's second pass: the
// live 30s eval was not the guard. The act path reads the page state as its
// pre-action baseline, and that read used the whole action budget, so the action
// itself then ran on an expired context and reported a 500ms probe failure (or a
// 0s busy wait) after 30s. The baseline must fail in one bounded read instead,
// leaving the action to reject the wedged page without another attempt.
func TestActOnWedgedPageNeverSpendsTheActionBudget(t *testing.T) {
	cases := []struct {
		name string
		act  func(context.Context, *browsers.Page) error
	}{
		{"eval", func(ctx context.Context, p *browsers.Page) error {
			_, err := PageEval(ctx, p, "() => document.title")
			return err
		}},
		{"click", func(ctx context.Context, p *browsers.Page) error {
			_, err := PageClickFrom(ctx, p, "", "#submit", "left", 1, PageState{})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := wedgedLivePage(t)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			started := time.Now()
			_, stateErr := PageStateIfFree(ctx, p)
			err := tc.act(ctx, p)
			elapsed := time.Since(started)
			t.Logf("%s plus its baseline returned after %s with %v", tc.name, elapsed.Round(time.Millisecond), err)

			if err == nil {
				t.Fatalf("%s on a wedged page returned no error", tc.name)
			}
			if elapsed >= 1500*time.Millisecond {
				t.Fatalf("%s burned %s, want under 1.5s", tc.name, elapsed.Round(time.Millisecond))
			}

			msg := strings.ToLower(err.Error())
			for _, want := range []string{tc.name, "unusable", "session_close", "browser page close"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("%s error = %q, want it to contain %q", tc.name, err, want)
				}
			}
			if strings.Contains(msg, "higher timeout_ms") {
				t.Fatalf("%s error = %q, must not advise a higher timeout_ms for a wedged page", tc.name, err)
			}

			var hinted interface{ Hint() string }
			if !errors.As(err, &hinted) {
				t.Fatalf("%s error = %v, want a Hint", tc.name, err)
			}
			for _, want := range []string{"session_close", "browser page close"} {
				if !strings.Contains(hinted.Hint(), want) {
					t.Fatalf("%s hint = %q, want it to name %q", tc.name, hinted.Hint(), want)
				}
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s error = %v, must not unwrap to DeadlineExceeded", tc.name, err)
			}

			if stateErr == nil {
				t.Fatalf("%s baseline read a wedged page without an error", tc.name)
			}
			if baseline := strings.ToLower(stateErr.Error()); !strings.Contains(baseline, "unusable") {
				t.Fatalf("%s baseline error = %q, want the page-unusable sentence", tc.name, stateErr)
			}
		})
	}
}
