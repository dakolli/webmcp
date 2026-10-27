package pageops

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRawActReadsStateOnce pins the raw fast path: a raw click never enters the
// settle poll, so it reads the post-action state exactly once even when a later
// read would have reported a navigation.
func TestRawActReadsStateOnce(t *testing.T) {
	before := PageState{URL: "https://example.com", Title: "Home"}
	reads := 0
	read := func() (PageState, error) {
		reads++
		if reads > 1 {
			return PageState{URL: "https://example.com/next", Title: "Next"}, nil
		}
		return before, nil
	}

	started := time.Now()
	after := settleAfterAction(context.Background(), read, "click", before, true)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("raw settle took %v, want an immediate single read", elapsed)
	}
	if reads != 1 {
		t.Fatalf("state reads = %d, want 1 for a raw call", reads)
	}
	if after != before {
		t.Fatalf("raw state = %+v, want the single read %+v", after, before)
	}
	if delta := ActStateDeltaFor("click", before, after); delta != "" {
		t.Fatalf("raw delta = %q, want nothing claimed from a settle that did not run", delta)
	}
}

// TestDefaultActStillSettles pins the humanized path: a click that navigates on
// a later read is polled until the change is seen, so the delta stays reliable.
func TestDefaultActStillSettles(t *testing.T) {
	before := PageState{URL: "https://example.com", Title: "Home"}
	next := PageState{URL: "https://example.com/next", Title: "Next"}
	reads := 0
	read := func() (PageState, error) {
		reads++
		if reads == 1 {
			return before, nil
		}
		return next, nil
	}

	after := settleAfterAction(context.Background(), read, "click", before, false)
	if reads < 2 {
		t.Fatalf("state reads = %d, want a poll after the unchanged first read", reads)
	}
	if after != next {
		t.Fatalf("settled state = %+v, want %+v", after, next)
	}
	want := "\n→ url=https://example.com/next title=\"Next\""
	if delta := ActStateDeltaFor("click", before, after); delta != want {
		t.Fatalf("delta = %q, want %q", delta, want)
	}
}

// TestSettleReadFailureFallsBackToBefore pins that a failed read never invents
// a state change, raw or not.
func TestSettleReadFailureFallsBackToBefore(t *testing.T) {
	before := PageState{URL: "https://example.com", Title: "Home"}
	read := func() (PageState, error) { return PageState{}, errors.New("target closed") }
	if after := settleAfterAction(context.Background(), read, "click", before, true); after != before {
		t.Fatalf("state = %+v, want the before state %+v", after, before)
	}
}

// TestNonNavigatingActReadsOnce pins that an action which cannot navigate never
// polls, even when humanized.
func TestNonNavigatingActReadsOnce(t *testing.T) {
	before := PageState{URL: "https://example.com", Title: "Home"}
	reads := 0
	read := func() (PageState, error) {
		reads++
		return before, nil
	}
	settleAfterAction(context.Background(), read, "input", before, false)
	if reads != 1 {
		t.Fatalf("state reads = %d, want 1 for a non-navigating action", reads)
	}
}
