package browsers

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// humanPage builds a live page with its own humanized timing source.
func humanPage() *Page {
	return &Page{ID: "page-1", browser: &Browser{human: newHumanizer()}}
}

func TestHumanDelayWithinFitsOrSkips(t *testing.T) {
	tests := []struct {
		name   string
		budget time.Duration
		delay  time.Duration
		want   time.Duration
	}{
		{"short deadline skips the shortest pause", 200 * time.Millisecond, HumanDelayMin, 0},
		{"short deadline skips the longest pause", 200 * time.Millisecond, HumanDelayMax, 0},
		{"pause that leaves no action room is skipped", 400 * time.Millisecond, HumanDelayMax, 0},
		{"pause that fits is kept", 2 * time.Second, HumanDelayMax, HumanDelayMax},
		{"normal timeout keeps the pause unchanged", 30 * time.Second, HumanDelayMin, HumanDelayMin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.budget)
			defer cancel()
			if got := HumanDelayWithin(ctx, tt.delay); got != tt.want {
				t.Fatalf("HumanDelayWithin(%v budget, %v) = %v, want %v", tt.budget, tt.delay, got, tt.want)
			}
		})
	}
}

func TestHumanDelayWithinSpentBudget(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if d := HumanDelayWithin(ctx, HumanDelayMax); d != 0 {
		t.Fatalf("delay = %v, want 0 on a spent budget", d)
	}
}

func TestHumanDelayWithinNoDeadline(t *testing.T) {
	if d := HumanDelayWithin(context.Background(), 250*time.Millisecond); d != 250*time.Millisecond {
		t.Fatalf("delay = %v, want the unclipped 250ms", d)
	}
}

// TestShortDeadlineSkipsPauseSoActionProceeds pins the WEB-215 regression: a
// 200ms call must not spend its budget in the humanized pause. The gate returns
// immediately, so the click that follows it still has budget to run.
func TestShortDeadlineSkipsPauseSoActionProceeds(t *testing.T) {
	t.Setenv(RawActionsEnv, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	p := humanPage()
	started := time.Now()
	if err := p.PauseAction(ctx, "click"); err != nil {
		t.Fatalf("PauseAction(200ms) = %v, want the pause skipped", err)
	}
	if elapsed := time.Since(started); elapsed > 75*time.Millisecond {
		t.Fatalf("PauseAction took %v, want an immediate return so the click can run", elapsed)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("no budget left for the click attempt: %v", err)
	}
}

// TestTwoShortDeadlineCallsReleaseLock pins the second WEB-215 regression: two
// consecutive short-deadline calls both return, and the page lock is free
// afterwards.
func TestTwoShortDeadlineCallsReleaseLock(t *testing.T) {
	t.Setenv(RawActionsEnv, "")
	p := humanPage()
	for i := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if err := p.LockContext(ctx, "click"); err != nil {
			cancel()
			t.Fatalf("call %d: LockContext: %v", i, err)
		}
		started := time.Now()
		err := p.PauseAction(ctx, "click")
		p.Unlock()
		cancel()
		if err != nil {
			t.Fatalf("call %d: PauseAction: %v", i, err)
		}
		if elapsed := time.Since(started); elapsed > 75*time.Millisecond {
			t.Fatalf("call %d took %v, want the pause skipped", i, elapsed)
		}
	}

	unlock, ok := p.TryLock()
	if !ok {
		t.Fatal("page lock still held after two short-deadline calls")
	}
	unlock()
}

// TestCancelledPauseReleasesLockAndPageUsable pins the third WEB-215
// regression: a pause cancelled mid-sleep returns the typed timeout and leaves
// the page lock free for the next call.
func TestCancelledPauseReleasesLockAndPageUsable(t *testing.T) {
	t.Setenv(RawActionsEnv, "")
	p := humanPage()
	ctx, cancel := context.WithCancel(context.Background())
	cancelAfter := time.AfterFunc(20*time.Millisecond, cancel)
	defer cancelAfter.Stop()

	if err := p.LockContext(ctx, "click"); err != nil {
		t.Fatalf("LockContext: %v", err)
	}
	err := p.PauseAction(ctx, "click")
	p.Unlock()

	var timed *ActionTimeoutError
	if !errors.As(err, &timed) {
		t.Fatalf("PauseAction after cancel = %v, want *ActionTimeoutError", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}

	unlock, ok := p.TryLock()
	if !ok {
		t.Fatal("page lock still held after a cancelled pause")
	}
	unlock()
}

// TestActionTimeoutErrorIsTimeout pins the type every surface branches on: the
// error unwraps to the context cause and names both escapes in its message.
func TestActionTimeoutErrorIsTimeout(t *testing.T) {
	err := error(&ActionTimeoutError{Op: "click", Page: "page-4", Err: context.DeadlineExceeded})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	var timed *ActionTimeoutError
	if !errors.As(err, &timed) || timed.Op != "click" || timed.Page != "page-4" {
		t.Fatalf("error type = %T, want the click action on page-4", err)
	}
	hint := timed.Hint()
	if !strings.Contains(hint, "raw:true") || !strings.Contains(hint, "timeout_ms") {
		t.Fatalf("Hint() = %q, want it to name raw:true and timeout_ms", hint)
	}
	if !strings.Contains(err.Error(), hint) {
		t.Fatalf("Error() = %q, want it to carry Hint()", err)
	}
}

func TestRawActionsHatches(t *testing.T) {
	t.Run("default humanizes", func(t *testing.T) {
		t.Setenv(RawActionsEnv, "")
		if humanPage().RawActions(context.Background()) {
			t.Fatal("RawActions = true, want false by default")
		}
	})

	t.Run("per-call context", func(t *testing.T) {
		t.Setenv(RawActionsEnv, "")
		if !humanPage().RawActions(WithRawCall(context.Background())) {
			t.Fatal("RawActions = false, want true from WithRawCall")
		}
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv(RawActionsEnv, "1")
		if !humanPage().RawActions(context.Background()) {
			t.Fatal("RawActions = false, want true from " + RawActionsEnv)
		}
	})

	t.Run("manager option", func(t *testing.T) {
		t.Setenv(RawActionsEnv, "")
		m := NewManager(WithRawActions(true))
		p := &Page{browser: &Browser{human: newHumanizer(), rawActions: m.rawActions}}
		if !p.RawActions(context.Background()) {
			t.Fatal("RawActions = false, want true from WithRawActions")
		}
	})
}

func TestHumanDelayInRange(t *testing.T) {
	b := &Browser{human: newHumanizer()}
	pages := []*Page{{browser: b}, {browser: b}}
	seen := make(map[time.Duration]bool)

	for range 100 {
		for _, p := range pages {
			d := p.HumanDelay()
			if d < HumanDelayMin || d > HumanDelayMax {
				t.Fatalf("delay = %v, want in [%v, %v]", d, HumanDelayMin, HumanDelayMax)
			}
			seen[d] = true
		}
	}
	if len(seen) < 2 {
		t.Fatal("shared browser drew one delay for every call, want per-call variation")
	}
}

func TestPointerPathJitterStaysInRange(t *testing.T) {
	const (
		fromX, fromY = 10.0, 20.0
		toX, toY     = 210.0, 120.0
	)
	p := humanPage()
	p.pointerX, p.pointerY = fromX, fromY
	path := p.PointerPath(toX, toY)

	if len(path) < minPointerSteps || len(path) > maxPointerSteps {
		t.Fatalf("steps = %d, want in [%d, %d]", len(path), minPointerSteps, maxPointerSteps)
	}
	last := path[len(path)-1]
	if last.X != toX || last.Y != toY {
		t.Fatalf("last step = (%v, %v), want the exact target (%v, %v)", last.X, last.Y, toX, toY)
	}

	steps := float64(len(path))
	for i, pt := range path[:len(path)-1] {
		t0 := float64(i+1) / steps
		wantX := fromX + (toX-fromX)*t0
		wantY := fromY + (toY-fromY)*t0
		if math.Abs(pt.X-wantX) > PointerJitter+1e-9 {
			t.Fatalf("step %d x = %v, want %v ± %v", i, pt.X, wantX, PointerJitter)
		}
		if math.Abs(pt.Y-wantY) > PointerJitter+1e-9 {
			t.Fatalf("step %d y = %v, want %v ± %v", i, pt.Y, wantY, PointerJitter)
		}
	}
}
