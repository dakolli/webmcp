package browsers

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RawActionsEnv names the environment hatch that disables humanized pointer and
// keyboard timing for the whole process. Set it to 1, true, yes or on to restore
// instant actions.
const RawActionsEnv = "WEB_MCP_RAW_ACTIONS"

// Humanized timing bounds. Every pointer or keyboard action gets a randomized
// pause in [HumanDelayMin, HumanDelayMax] and a stepped pointer move of
// [minPointerSteps, maxPointerSteps] steps whose intermediate positions carry
// at most PointerJitter CSS pixels of offset.
const (
	// HumanDelayMin is the shortest humanized action pause.
	HumanDelayMin = 100 * time.Millisecond
	// HumanDelayMax is the longest humanized action pause.
	HumanDelayMax = 500 * time.Millisecond
	// PointerJitter is the largest offset applied to an intermediate pointer
	// step, in CSS pixels. The final step is never jittered.
	PointerJitter   = 2.0
	minPointerSteps = 6
	maxPointerSteps = 12
)

// actionRoom is the budget an action must keep after a humanized pause for the
// stepped pointer move and the press/release round trips that follow it. A
// pause that would leave less is skipped, never clipped, so a short-deadline
// call still dispatches a real click instead of spending its budget waiting.
const actionRoom = 250 * time.Millisecond

// humanizer is one browser's randomized timing source. A single sequence is
// shared by every page of the browser, so two pages never draw the same delay
// or path in lockstep.
type humanizer struct {
	mu  sync.Mutex
	rnd *rand.Rand
}

// humanSeq makes every humanizer seed unique inside one process even when two
// browsers start in the same clock tick.
var humanSeq atomic.Uint64

// fallbackHumanizer serves a Page built without an owning Browser, such as a
// hand-built test page.
var fallbackHumanizer = newHumanizer()

// newHumanizer builds a humanizer seeded once, when its browser is constructed.
func newHumanizer() *humanizer {
	seq := humanSeq.Add(1)
	return &humanizer{rnd: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), seq))}
}

// delay returns a randomized human action pause.
func (h *humanizer) delay() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	span := int64(HumanDelayMax - HumanDelayMin)
	return HumanDelayMin + time.Duration(h.rnd.Int64N(span+1))
}

// path returns the stepped positions from (fromX, fromY) to (toX, toY). The
// last position is exactly the target; only intermediate steps carry bounded
// jitter, so the action always lands on the element.
func (h *humanizer) path(fromX, fromY, toX, toY float64) []Pointer {
	h.mu.Lock()
	steps := minPointerSteps + h.rnd.IntN(maxPointerSteps-minPointerSteps+1)
	offsets := make([]float64, steps*2)
	for i := range offsets {
		offsets[i] = (h.rnd.Float64()*2 - 1) * PointerJitter
	}
	h.mu.Unlock()

	path := make([]Pointer, 0, steps)
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := fromX + (toX-fromX)*t
		y := fromY + (toY-fromY)*t
		if i < steps {
			x += offsets[(i-1)*2]
			y += offsets[(i-1)*2+1]
		}
		path = append(path, Pointer{X: x, Y: y})
	}
	return path
}

// Pointer is one stepped mouse position in CSS pixels.
type Pointer struct {
	// X is the viewport horizontal position.
	X float64
	// Y is the viewport vertical position.
	Y float64
}

// humanizer returns the timing source this page draws from.
func (p *Page) humanizer() *humanizer {
	if p.browser != nil && p.browser.human != nil {
		return p.browser.human
	}
	return fallbackHumanizer
}

// HumanDelay returns a randomized pause in [HumanDelayMin, HumanDelayMax] drawn
// from this page's browser sequence.
func (p *Page) HumanDelay() time.Duration {
	return p.humanizer().delay()
}

// PointerPath returns the stepped positions that move the pointer from its last
// known position to (toX, toY). The final position is the exact target and the
// earlier steps carry bounded jitter. It records the target as the new pointer
// position. The caller must hold the page operation lock.
func (p *Page) PointerPath(toX, toY float64) []Pointer {
	path := p.humanizer().path(p.pointerX, p.pointerY, toX, toY)
	p.pointerX, p.pointerY = toX, toY
	return path
}

// HumanDelayWithin reports the pause a call can actually afford: d when d plus
// actionRoom still fits the remaining budget, or 0 when it does not. A pause is
// skipped rather than clipped, because a clipped pause is neither humanized nor
// worth the budget the action itself needs. A context without a deadline keeps d
// unchanged.
func HumanDelayWithin(ctx context.Context, d time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return d
	}
	if time.Until(deadline) < d+actionRoom {
		return 0
	}
	return d
}

// Sleep waits for d or until ctx is done, whichever comes first. It returns
// ctx.Err() when the call is cancelled or its deadline expires, so a humanized
// pause never outlives the call.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ActionTimeoutError reports that an action's call budget expired before its
// pointer or keyboard events could be dispatched — the humanized pause or the
// stepped pointer move used up the time left. It wraps the context cause so
// every surface classifies the failure as a timeout, and names the two escapes:
// raw actions or a larger timeout.
type ActionTimeoutError struct {
	// Op is the action that ran out of budget, e.g. "click".
	Op string
	// Page is the page the action targeted.
	Page PageID
	// Err is the context cause.
	Err error
}

// Hint is the one-line remediation a surface may attach to this timeout: the
// two ways out of a humanized action that cannot fit its budget.
func (e *ActionTimeoutError) Hint() string {
	return "retry with raw:true to skip the humanized pause, or raise timeout_ms"
}

// Error names the action, the page, the fix and the cause.
func (e *ActionTimeoutError) Error() string {
	return fmt.Sprintf(
		"%s: page %s ran out of its call budget before the pointer or keyboard event was dispatched — %s: %v",
		e.Op,
		e.Page,
		e.Hint(),
		e.Err,
	)
}

// Unwrap exposes the context cause so callers classify the failure as a timeout.
func (e *ActionTimeoutError) Unwrap() error { return e.Err }

// PauseAction sleeps one humanized pause before or after an action, then
// confirms the call still has budget for the action itself. A pause that does
// not fit is skipped, and an expired context returns a typed ActionTimeoutError
// so no CDP call starts on a spent deadline. The caller must already hold the
// page operation lock.
func (p *Page) PauseAction(ctx context.Context, op string) error {
	if err := actionDeadline(ctx, op, p.ID); err != nil {
		return err
	}
	if p.RawActions(ctx) {
		return nil
	}
	d := HumanDelayWithin(ctx, p.HumanDelay())
	if d <= 0 {
		return actionDeadline(ctx, op, p.ID)
	}
	if err := Sleep(ctx, d); err != nil {
		return &ActionTimeoutError{Op: op, Page: p.ID, Err: err}
	}
	return actionDeadline(ctx, op, p.ID)
}

// actionDeadline returns a typed timeout when ctx is already done, so an action
// never starts on an expired context.
func actionDeadline(ctx context.Context, op string, page PageID) error {
	if err := ctx.Err(); err != nil {
		return &ActionTimeoutError{Op: op, Page: page, Err: err}
	}
	return nil
}

// rawActionsCtxKey marks a single call as raw.
type rawActionsCtxKey struct{}

// WithRawCall returns a context whose pointer and keyboard actions skip
// humanized timing: instant pointer moves and no pre/post action pause.
func WithRawCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, rawActionsCtxKey{}, true)
}

// RawActions reports whether humanized timing is disabled for this call. Any
// one of the three hatches is enough: a WithRawCall context, the
// WEB_MCP_RAW_ACTIONS environment hatch, or a Browser built with raw actions.
func (p *Page) RawActions(ctx context.Context) bool {
	if raw, ok := ctx.Value(rawActionsCtxKey{}).(bool); ok && raw {
		return true
	}
	if rawActionsEnv() {
		return true
	}
	return p.browser != nil && p.browser.rawActions
}

// rawActionsEnv reports whether the environment hatch is set.
func rawActionsEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(RawActionsEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
