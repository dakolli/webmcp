package browsers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/devices"
	"github.com/go-rod/rod/lib/proto"
)

// PageID uniquely identifies a live page (a.k.a. session / browser tab) owned
// by a Browser. It is minted by Browser.NewPage and referenced from every
// page_fetch / page_act / session_close call, e.g. "page-1".
type PageID string

// Page wraps a live *rod.Page (a browser tab) and its own operation lock,
// because rod Pages are NOT thread-safe: all access to the underlying page
// must be serialized. Lock/Unlock exist for callers with no deadline;
// LockContext is the default so a caller waiting on a busy page fails with an
// actionable error instead of blocking forever. Lifecycle state that listings
// read (last access) is atomic and never waits on the operation lock.
type Page struct {
	// ID uniquely identifies this page within its Browser.
	ID PageID
	// URL is the URL the page was created with (set by NewPage).
	URL string
	// Opener is the id of the page that opened this popup (a target="_blank"
	// link or window.open), empty for a top-level page. Immutable after
	// registration, so listings read it without the operation lock.
	Opener PageID
	// popupClaimed reports that wait_open already returned this popup, so a
	// second wait blocks for the next one instead of repeating itself. Guarded
	// by the owning browser's mutex.
	popupClaimed bool
	// Rod is the live go-rod page handle for this tab. Only valid while the
	// page is open. Guarded by the operation lock.
	Rod *rod.Page
	// browser is the engine Browser that owns this page, used to enforce the
	// browser's request policy (scope and per-host rate limit) on outbound
	// tool requests and intercepted subresources. Nil for a page built outside
	// the engine (tests); enforcement is skipped then.
	browser *Browser
	// info reads the tab's live URL and title. It is a field so tests can stub
	// the CDP round trip; nil reads through rod's Page.Info.
	info func(context.Context) (url, title string, err error)
	// lastAccessNano is the UnixNano time this page was last used: set at
	// creation (NewPage) and refreshed by touch on every successful
	// Browser.Page lookup. Atomic so listings and the sweeper never take the
	// operation lock (a stuck page must not block them).
	lastAccessNano atomic.Int64
	// wedgeErr holds the error of an operation whose watchdog forced the
	// operation lock open. While set, every new operation fails fast with it
	// instead of touching a page the stuck operation may still be using.
	// Atomic so a waiting caller can refuse without the operation lock.
	wedgeErr atomic.Pointer[PageWatchdogError]
	// closedErr marks a page the watchdog could not confirm usable after a
	// forced release. It fail-fasts every new operation with a page_closed
	// error; the page stays listed so the operator can close it.
	closedErr atomic.Pointer[PageBrokenError]
	// unresponsiveErr holds the error stored when the tab's renderer stopped
	// answering a liveness probe: the target is alive but its page agent does
	// not respond, so no read or action can run. Atomic so a waiting caller can
	// refuse without the operation lock.
	unresponsiveErr atomic.Pointer[PageUnresponsiveError]
	// pointerX and pointerY are the last humanized pointer position in CSS
	// pixels, so the next move is stepped from where the cursor already is.
	// Guarded by the operation lock.
	pointerX, pointerY float64
	// docMarkerInstalled reports whether the handle-scope marker script has
	// been installed on this page (see CaptureSnapshot). Guarded by the
	// operation lock.
	docMarkerInstalled bool
	// sinkScriptInstalled reports whether the source-to-sink tracer is armed:
	// the page-level flag outlives any single document, so a navigation
	// re-injects the script and only a page close disarms it (see InstallSinks).
	// Guarded by the operation lock.
	sinkScriptInstalled bool
	// postMessageScriptInstalled reports whether the postMessage recorder is
	// armed: the page-level flag outlives any single document, so a navigation
	// re-injects the script and only a page close disarms it (see
	// InstallPostMessages). Guarded by the operation lock.
	postMessageScriptInstalled bool
	// observer holds the page's network/console/WebSocket capture rings. It is
	// assigned once in the pre-registration window and never replaced, so read
	// methods may use it without taking the operation lock. Engine-internal:
	// surfaces never touch it.
	observer *pageObserver
	// obsCancel disarms the capture subscription. It is set in the same
	// pre-registration window and cleared by stopObservation. Guarded by the
	// operation lock.
	obsCancel context.CancelFunc
	// dialogs holds the page's JavaScript-dialog and HTTP-auth state: the
	// bounded entry ring, the answer policy and the CDP subscriptions that
	// answer a dialog before it blocks the renderer. It has its own mutex and is
	// deliberately outside the operation lock, so a dialog can be answered
	// while an operation holds the page. Assigned once in the pre-registration
	// window.
	dialogs *pageDialogs
	// violations holds the page's bound CSP violation ring, fed by a
	// document-start listener through a CDP binding. It has its own mutex and
	// sits outside the operation lock, because the reporter runs from the page's
	// own event handler. Assigned once in the pre-registration window.
	violations *pageViolations
	// intercept holds the page's request interception state, nil until the first
	// rule is added and after it is cleared. It is guarded by interceptMu, never
	// by the operation lock, because its handlers make CDP calls from their own
	// goroutines.
	intercept *interceptor
	// interceptMu guards intercept and serializes interception arm/disarm. Lock
	// order is Page.mu -> interceptMu; no path takes them the other way.
	interceptMu sync.Mutex

	// opMu is the operation lock: a one-slot channel semaphore, lazily
	// initialized so a zero-value Page stays usable. A channel (rather than
	// sync.Mutex) lets a caller wait with a context deadline.
	opMu chan struct{}
	// opOnce initializes opMu exactly once.
	opOnce sync.Once
}

// Operation-watchdog budgets. An operation gets watchdogGrace past its call
// budget to unwind on cancellation before the watchdog stops trusting it, and
// watchdogRecover to return at all before the page is marked closed.
const (
	defaultOpBudget = 30 * time.Second
	watchdogGrace   = time.Second
	watchdogRecover = 5 * time.Second
	watchdogProbe   = 2 * time.Second
)

// readLockBudget bounds how long a read or an action waits for the page
// operation lock. A healthy page yields the lock in microseconds, so the budget
// only elapses on a lock a wedged operation still holds; it stays comfortably
// below the one-second recovery promise. It sizes the wait only: an action that
// holds the lock keeps its full call budget.
const readLockBudget = 500 * time.Millisecond

// pageCloseHint is the one remediation every page-unusable error names. Both
// surfaces run their own close verb, so the sentence names both: an MCP caller
// has session_close, a CLI caller has browser page close.
const pageCloseHint = "close it with session_close (MCP) or browser page close (CLI)"

// pageCloseNextStep is the one-line remediation a surface attaches to a wedged
// page: close the session and open a fresh page.
const pageCloseNextStep = "the page is unusable — call session_close (MCP) or browser page close (CLI), then fetch_page to open a fresh page"

// rendererProbeBudget bounds one renderer liveness probe. A healthy renderer
// answers a frame-tree read in milliseconds, so the budget only elapses on a
// renderer stuck behind a navigation that never commits a document; it stays
// below the one-second recovery promise with room for a loaded host.
const rendererProbeBudget = 500 * time.Millisecond

// PageWatchdogError reports that an operation outlived its call budget and the
// page watchdog force-released the operation lock. It unwraps to
// context.DeadlineExceeded so every surface classifies it as a timeout, and its
// message names the watchdog, the unusable page and the only recovery.
type PageWatchdogError struct {
	// Op is the operation the watchdog stopped waiting for, e.g. "click".
	Op string
	// Page is the page that operation targeted.
	Page PageID
	// Waited is how long the operation ran before the watchdog tripped.
	Waited time.Duration
}

// Error names the operation, the page, the watchdog and the recovery.
func (e *PageWatchdogError) Error() string {
	return fmt.Sprintf(
		"%s: page %s did not return within %s — the page is unusable and the page watchdog force-released its lock; %s",
		e.Op,
		e.Page,
		e.Waited.Round(time.Millisecond),
		pageCloseHint,
	)
}

// Hint is the one-line remediation a surface attaches to a wedged page: close
// the session and open a fresh page.
func (e *PageWatchdogError) Hint() string {
	return pageCloseNextStep
}

// Unwrap exposes context.DeadlineExceeded so surfaces classify the trip as a
// timeout.
func (e *PageWatchdogError) Unwrap() error { return context.DeadlineExceeded }

// PageLockedError reports that a read or action waited its whole bounded lock
// budget and the page operation lock was still held, so the page cannot serve
// the call without blocking. It is the bounded-wait sibling of
// PageWatchdogError: its message calls the page unusable, names both close
// verbs as the recovery, and deliberately does not unwrap to
// context.DeadlineExceeded, so a surface never relabels a wedged page as a slow
// one.
type PageLockedError struct {
	// Op is the read or action operation that could not take the lock, e.g.
	// "meta" or "click".
	Op string
	// Page is the locked page.
	Page PageID
	// Waited is how long the read waited before giving up.
	Waited time.Duration
}

// Error names the operation, the page, the wait and the recovery.
func (e *PageLockedError) Error() string {
	return fmt.Sprintf(
		"%s: page %s is still busy after %s — a previous operation holds the page and did not return, so the page is unusable; %s",
		e.Op,
		e.Page,
		e.Waited.Round(time.Millisecond),
		pageCloseHint,
	)
}

// Hint is the one-line remediation for a page whose lock never frees.
func (e *PageLockedError) Hint() string {
	return pageCloseNextStep
}

// PageUnresponsiveError reports that the tab's renderer did not answer a
// minimal CDP call within the liveness budget, so the page can neither serve a
// read nor run an action. It is the sibling of PageLockedError: the message
// calls the page unusable, names both close verbs as the recovery, and
// deliberately does not unwrap to context.DeadlineExceeded, so a surface never
// relabels a stuck renderer as a slow read.
type PageUnresponsiveError struct {
	// Op is the operation that found the renderer unresponsive.
	Op string
	// Page is the page whose renderer stopped answering.
	Page PageID
}

// Error names the operation, the page, the stuck renderer and the recovery.
func (e *PageUnresponsiveError) Error() string {
	return fmt.Sprintf(
		"%s: page %s is unusable — the tab's renderer stopped answering CDP calls; %s",
		e.Op,
		e.Page,
		pageCloseHint,
	)
}

// Hint is the one-line remediation for a page whose renderer stopped answering.
func (e *PageUnresponsiveError) Hint() string {
	return pageCloseNextStep
}

// PageBrokenError reports that the watchdog forced a wedged page open and the
// page did not answer the recovery probe, so the session was marked closed. It
// is the typed page_closed error for a page that can no longer answer.
type PageBrokenError struct {
	// Op is the operation the watchdog was recovering from.
	Op string
	// Page is the page that was marked closed.
	Page PageID
	// Err is why the page could not be confirmed usable.
	Err error
}

// Error states that the session is closed and names the cause.
func (e *PageBrokenError) Error() string {
	return fmt.Sprintf(
		"%s: page %s did not answer the recovery probe after the page watchdog forced its lock open — page session is closed: %v; %s",
		e.Op,
		e.Page,
		e.Err,
		pageCloseHint,
	)
}

// Hint is the one-line remediation a surface attaches to a closed page: close
// the session and open a fresh page.
func (e *PageBrokenError) Hint() string {
	return pageCloseNextStep
}

// Unwrap exposes the probe failure.
func (e *PageBrokenError) Unwrap() error { return e.Err }

// GuardPage runs fn under the page operation lock while a watchdog watches the
// call budget. The caller returns as soon as fn finishes; if fn outlives the
// budget by watchdogGrace, the watchdog cancels fn, force-releases the lock and
// returns a *PageWatchdogError. A recovery goroutine then waits for fn to
// return, probes the page and either clears the wedge (the page is usable) or
// marks the session closed with *PageBrokenError. The lock is acquired under
// the same bounded budget as a read, so an action that starts behind a wedged
// operation returns *PageLockedError in under a second; the budget sizes only
// that wait, so an action that acquires the lock keeps its whole call budget.
// The caller must not hold the lock; fn must not lock either — the lock stays
// held for its whole run.
func GuardPage[T any](
	ctx context.Context,
	p *Page,
	op string,
	fn func(context.Context) (T, error),
) (T, error) {
	var zero T
	started := time.Now()
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultOpBudget)
		defer cancel()
	}
	if err := p.lockContextBudget(ctx, op, readLockBudget, true); err != nil {
		return zero, err
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(p.Unlock) }

	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("%s: page %s panicked: %v", op, p.ID, r)}
			}
		}()
		v, err := fn(opCtx)
		done <- result{value: v, err: err}
	}()

	select {
	case r := <-done:
		release()
		return r.value, r.err
	case <-ctx.Done():
	}

	cancel()
	select {
	case r := <-done:
		release()
		return r.value, r.err
	case <-time.After(watchdogGrace):
	}

	return zero, p.tripWatchdog(op, started, finished, release)
}

// BeginOperation takes the page operation lock for one call and arms a
// watchdog on ctx. When ctx is cancelled and the caller has still not released
// the lock after watchdogGrace, the watchdog force-releases it, marks the page
// wedged and probes it in the background, so a CDP call that ignores its
// context cannot keep the page locked forever. The returned release func is
// idempotent and must be called once the operation is done; on healthy calls it
// releases the lock immediately and nothing else changes.
func (p *Page) BeginOperation(ctx context.Context, op string) (func(), error) {
	return p.beginOperation(ctx, op, 0)
}

// BeginRead takes the page operation lock for one read with the bounded read
// budget and arms the same watchdog as BeginOperation. A read therefore never
// waits behind a wedged operation: when the lock does not free within
// readLockBudget it returns *PageLockedError at once. A healthy page yields the
// lock immediately, so ordinary contention between fast reads is unaffected.
func (p *Page) BeginRead(ctx context.Context, op string) (func(), error) {
	return p.beginOperation(ctx, op, readLockBudget)
}

// beginOperation takes the page operation lock — waiting no longer than
// lockBudget when it is positive — then arms the ctx watchdog that force-releases
// a call the page never finishes.
func (p *Page) beginOperation(
	ctx context.Context,
	op string,
	lockBudget time.Duration,
) (func(), error) {
	if err := p.lockContextBudget(ctx, op, lockBudget, true); err != nil {
		return nil, err
	}
	started := time.Now()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(p.Unlock) }
	finished := make(chan struct{})
	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			close(finished)
			unlock()
		})
	}
	stop := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(watchdogGrace)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
		}
		select {
		case <-finished:
			return
		default:
		}
		p.tripWatchdog(op, started, finished, unlock)
	})
	return func() {
		finish()
		stop()
	}, nil
}

// tripWatchdog force-releases a page operation that outlived its budget, marks
// the page wedged so new operations fail fast, and starts the recovery: once
// the stuck operation returns (finished is closed) the page is probed and
// either cleared as usable or marked closed when it never answers.
func (p *Page) tripWatchdog(
	op string,
	started time.Time,
	finished <-chan struct{},
	release func(),
) *PageWatchdogError {
	trip := &PageWatchdogError{Op: op, Page: p.ID, Waited: time.Since(started)}
	p.wedgeErr.Store(trip)
	release()
	go func() {
		select {
		case <-finished:
		case <-time.After(watchdogRecover):
			p.closedErr.Store(&PageBrokenError{
				Op:   op,
				Page: p.ID,
				Err:  errors.New("the stuck operation never returned"),
			})
			return
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), watchdogProbe)
		defer cancel()
		if err := p.probe(probeCtx); err != nil {
			p.closedErr.Store(&PageBrokenError{Op: op, Page: p.ID, Err: err})
			return
		}
		p.wedgeErr.CompareAndSwap(trip, nil)
	}()
	return trip
}

// probe confirms the page still answers one minimal read. It takes the
// operation lock itself; the caller must not hold it. A page already in use by
// a newer operation is alive, so a lock that stays busy only up to ctx is not
// treated as a failure.
func (p *Page) probe(ctx context.Context) error {
	select {
	case <-p.lockCh():
	case <-ctx.Done():
		return nil
	}
	defer p.Unlock()
	if _, _, err := p.readInfo(ctx); err != nil {
		return err
	}
	return nil
}

// unusableFor returns the page's typed unusable error relabelled with op, so a
// caller that finds the page wedged or closed names the operation it attempted
// instead of the earlier one that tripped the watchdog. Every other field of
// the stored error is preserved and the stored error itself is left untouched.
func (p *Page) unusableFor(op string) error {
	switch e := p.Unusable().(type) {
	case nil:
		return nil
	case *PageBrokenError:
		return &PageBrokenError{Op: op, Page: e.Page, Err: e.Err}
	case *PageWatchdogError:
		return &PageWatchdogError{Op: op, Page: e.Page, Waited: e.Waited}
	case *PageUnresponsiveError:
		return &PageUnresponsiveError{Op: op, Page: e.Page}
	default:
		return e
	}
}

// Unusable returns the typed error a page carries while the watchdog has marked
// it wedged or closed, or while a renderer probe found the tab unresponsive, or
// nil when the page may be operated. It never waits on the operation lock, so a
// read can fail fast before touching the tab.
func (p *Page) Unusable() error {
	if e := p.closedErr.Load(); e != nil {
		return e
	}
	if e := p.unresponsiveErr.Load(); e != nil {
		return e
	}
	if e := p.wedgeErr.Load(); e != nil {
		return e
	}
	return nil
}

// ensureRenderer verifies, while the operation lock is held, that the tab's
// renderer still answers one minimal page-domain call. A renderer that does not
// answer within the probe budget is marked unresponsive and the caller fails
// immediately, so a navigation that never commits a document can no longer cost
// every later call its whole budget. A caller whose own context expires during
// the probe keeps the plain timeout error instead of marking the page. The
// caller must hold the lock; a nil tab is left to the reader's own
// closed-session error.
func (p *Page) ensureRenderer(ctx context.Context, op string) error {
	if p.Rod == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, rendererProbeBudget)
	defer cancel()
	if err := p.rendererAnswers(probeCtx); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf(
				"%s: page %s did not answer within %s: %w",
				op,
				p.ID,
				rendererProbeBudget,
				err,
			)
		}
		return p.markUnresponsive(op)
	}
	return nil
}

// rendererAnswers issues one page-domain CDP call the renderer must answer. The
// caller must hold the operation lock. It uses the frame tree rather than an
// eval because a tab left without a document has no execution context to
// evaluate in, and that missing context is what the probe is looking past.
func (p *Page) rendererAnswers(ctx context.Context) error {
	if p.Rod == nil {
		return nil
	}
	_, err := proto.PageGetFrameTree{}.Call(p.Rod.Context(ctx))
	return err
}

// markUnresponsive records that the page's renderer stopped answering and starts
// one recovery loop that clears the mark once the renderer answers again — a
// cancelled navigation whose load completes late — or closes the session when
// it never does.
func (p *Page) markUnresponsive(op string) error {
	dead := &PageUnresponsiveError{Op: op, Page: p.ID}
	if !p.unresponsiveErr.CompareAndSwap(nil, dead) {
		return p.Unusable()
	}
	go p.recoverRenderer(dead)
	return dead
}

// recoverRenderer polls the renderer until it answers again or the recovery
// window expires, then clears the mark or closes the session.
func (p *Page) recoverRenderer(dead *PageUnresponsiveError) {
	deadline := time.Now().Add(watchdogRecover)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(context.Background(), rendererProbeBudget)
		err := p.probeRenderer(probeCtx)
		cancel()
		if err == nil {
			p.unresponsiveErr.CompareAndSwap(dead, nil)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.closedErr.Store(&PageBrokenError{
		Op:   dead.Op,
		Page: p.ID,
		Err:  errors.New("the renderer never answered again"),
	})
}

// probeRenderer takes the operation lock through the unguarded path — a page
// already marked unresponsive must still be probed — and asks the renderer one
// minimal question.
func (p *Page) probeRenderer(ctx context.Context) error {
	if err := p.lockContext(ctx, "probe renderer", false); err != nil {
		return err
	}
	defer p.Unlock()
	return p.rendererAnswers(ctx)
}

// touch records that the page was just used, resetting its idle clock so the
// manager's sweeper does not close it. It is lock-free: listings and the
// sweeper must never wait on a page operation.
func (p *Page) touch() {
	p.lastAccessNano.Store(time.Now().UnixNano())
}

// idleFor returns how long the page has gone unused without taking the
// operation lock.
func (p *Page) idleFor() time.Duration {
	return time.Since(time.Unix(0, p.lastAccessNano.Load()))
}

// lockCh returns the operation-lock channel, initializing it on first use.
func (p *Page) lockCh() chan struct{} {
	p.opOnce.Do(func() {
		ch := make(chan struct{}, 1)
		ch <- struct{}{}
		p.opMu = ch
	})
	return p.opMu
}

// Lock serializes access to the page's underlying rod page. Pair with Unlock.
// It waits forever; prefer LockContext so a busy page cannot hang the caller.
func (p *Page) Lock() {
	<-p.lockCh()
}

// Unlock releases the per-page operation lock taken by Lock or LockContext.
func (p *Page) Unlock() {
	p.lockCh() <- struct{}{}
}

// TryLock acquires the operation lock without waiting, reporting whether it
// succeeded. A caller that cannot acquire it must treat the page as busy, not
// dead: an in-flight operation means the tab is alive. A page its watchdog
// force-released or marked closed reports not-ok without touching the lock.
func (p *Page) TryLock() (unlock func(), ok bool) {
	if p.Unusable() != nil {
		return nil, false
	}
	select {
	case <-p.lockCh():
		if p.Unusable() != nil {
			p.Unlock()
			return nil, false
		}
		return p.Unlock, true
	default:
		return nil, false
	}
}

// LockContext acquires the page operation lock, waiting only until ctx is
// done. It returns one actionable sentence naming op and the time spent
// waiting when another operation still holds the page, and the page's typed
// watchdog or closed error when the page is no longer usable.
func (p *Page) LockContext(ctx context.Context, op string) error {
	return p.lockContextBudget(ctx, op, 0, true)
}

// lockContext is LockContext with the unusable-page guard switchable, so
// teardown and recovery probes can still take a page their watchdog marked
// closed.
func (p *Page) lockContext(ctx context.Context, op string, guard bool) error {
	return p.lockContextBudget(ctx, op, 0, guard)
}

// lockRead takes the page operation lock for a read with the bounded read
// budget, so the engine read seams cannot block behind a wedged operation. The
// caller must Unlock after a nil error.
func (p *Page) lockRead(ctx context.Context, op string) error {
	return p.lockContextBudget(ctx, op, readLockBudget, true)
}

// lockContextBudget takes the page operation lock, waiting until ctx is done or
// — when budget is positive — at most budget. A budget expiry on a still-locked
// page returns *PageLockedError, so a read reports the page unusable instead of
// burning the call budget behind it. When guard is set, the acquired lock is
// then held against a renderer probe, so a tab whose page agent stopped
// answering fails the caller here instead of every CDP call afterwards.
func (p *Page) lockContextBudget(
	ctx context.Context,
	op string,
	budget time.Duration,
	guard bool,
) error {
	if guard {
		if err := p.unusableFor(op); err != nil {
			return err
		}
	}
	start := time.Now()
	waitCtx := ctx
	if budget > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	select {
	case <-p.lockCh():
	case <-waitCtx.Done():
		if err := p.unusableFor(op); err != nil {
			return err
		}
		if budget > 0 && ctx.Err() == nil {
			return &PageLockedError{Op: op, Page: p.ID, Waited: time.Since(start)}
		}
		return fmt.Errorf(
			"%s: page %s is still busy after %s — a previous operation on this page did not return; retry, or %s",
			op,
			p.ID,
			time.Since(start).Round(time.Millisecond),
			pageCloseHint,
		)
	}
	if guard {
		if err := p.unusableFor(op); err != nil {
			p.Unlock()
			return err
		}
		if err := p.ensureRenderer(ctx, op); err != nil {
			p.Unlock()
			return err
		}
	}
	return nil
}

// Close closes the underlying rod tab, releasing the browser tab. The page
// must not be used afterwards. It is safe to call on an already-closed page
// (nil Rod handle) and is best-effort by design: teardown is attempted even if
// ctx has expired so resources are not leaked. The caller's bounded timeout
// (sweep, shutdown) is enforced at the rod boundary: a wedged CDP connection
// surfaces a ctx-bound error (e.g. context.DeadlineExceeded) instead of
// blocking the caller past its deadline.
func (p *Page) Close(ctx context.Context) error {
	p.stopDialogs()
	p.stopInterception(ctx)
	if err := p.lockContextBudget(ctx, "close page", 0, false); err != nil {
		return err
	}
	defer p.Unlock()
	p.stopObservation()
	p.stopViolations()
	if p.Rod == nil {
		return nil
	}
	// rod's Close has no ctx-aware form, but Context(ctx) returns a clone whose
	// chained operations honor ctx. Error-returning form; no Must*.
	return p.Rod.Context(ctx).Close()
}

// PageInfo is a read-only snapshot of a page's metadata, safe to copy and
// marshal. It deliberately excludes the live *rod.Page handle.
type PageInfo struct {
	// ID uniquely identifies the page.
	ID PageID `json:"id"`
	// URL is the tab's live URL when it could be read, else the URL the page
	// was created with (see Stale).
	URL string `json:"url"`
	// Opener is the id of the page that opened this popup, absent for a
	// top-level page.
	Opener PageID `json:"opener,omitempty"`
	// Context is the incognito identity the page lives in ("default" for the
	// parent browser); identities share one Chromium process but not cookies.
	Context string `json:"context"`
	// Title is the live document title, empty when it could not be read.
	Title string `json:"title,omitempty"`
	// Stale marks an entry whose URL and title could not be read from the live
	// tab (busy page, dead target, or read timeout); URL then holds the last
	// known value. Absent means the entry was read live.
	Stale bool `json:"stale,omitempty"`
}

// listingInfoBudget bounds all live URL/title reads of one listing. A listing
// must never wait on the page operation lock, and a dead CDP connection must
// fall back to the stored URL instead of stalling the whole listing.
const listingInfoBudget = time.Second

// snapshot returns this page's listing row tagged with its owning identity. It
// refreshes URL and title from the live tab, and never waits on the page
// operation lock: a busy or unreadable page is reported from the stored URL
// with Stale set. The renderer is probed before the read, so a tab whose
// renderer stopped answering is reported stale and marked unresponsive without
// sending it a live Eval that would burn the whole listing budget. URL and
// title come from ONE atomic read, so a row can never pair a new URL with the
// previous document's title. The caller bounds the read with ctx.
func (p *Page) snapshot(ctx context.Context, identity string) PageInfo {
	info := PageInfo{
		ID:      p.ID,
		URL:     p.URL,
		Opener:  p.Opener,
		Context: identity,
	}
	unlock, ok := p.TryLock()
	if !ok {
		info.Stale = true
		return info
	}
	defer unlock()

	if err := p.ensureRenderer(ctx, "listing"); err != nil {
		info.Stale = true
		return info
	}

	url, title, err := p.readInfo(ctx)
	if err != nil {
		info.Stale = true
		return info
	}
	if url == "" {
		// An empty live URL (about:blank) is never paired with a live title:
		// that would combine the stored URL with another document's title.
		info.Stale = title != ""
		return info
	}
	info.URL = url
	info.Title = title
	return info
}

// State reads the tab's live URL and title under ctx. Unlike rod's Page.Info,
// which routes through the browser's own unbounded context, this read is bound
// to the caller's deadline, so a wedged target cannot hold the page lock past
// the call budget. The caller must already hold the operation lock.
func (p *Page) State(ctx context.Context) (string, string, error) {
	return p.readInfo(ctx)
}

// TryState reads the tab's live URL and title for an act's pre- or post-action
// state delta. It never waits for the page operation lock, and both the
// renderer probe and the read run on the bounded read budget, so a wedged page
// cannot spend the caller's action budget before the action starts. A page that
// another operation holds returns a busy error at once, and a page its watchdog
// marked unusable reports that typed error.
func (p *Page) TryState(ctx context.Context, op string) (string, string, error) {
	if err := p.unusableFor(op); err != nil {
		return "", "", err
	}
	unlock, ok := p.TryLock()
	if !ok {
		if err := p.unusableFor(op); err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("%s: page %s is busy — another operation holds it", op, p.ID)
	}
	defer unlock()
	if err := p.ensureRenderer(ctx, op); err != nil {
		return "", "", err
	}
	readCtx, cancel := context.WithTimeout(ctx, readLockBudget)
	defer cancel()
	return p.readInfo(readCtx)
}

// readInfo reads the tab's live URL and title as one atomic pair: a single
// in-page evaluation reads location.href and document.title from the same
// document, so a navigation that has committed but not finished cannot pair the
// new URL with the previous document's title. The stubbable seam returns a pair
// for tests.
func (p *Page) readInfo(ctx context.Context) (string, string, error) {
	if p.info != nil {
		return p.info(ctx)
	}
	if p.Rod == nil {
		return "", "", fmt.Errorf("browsers: page %q has no live tab", p.ID)
	}
	live, err := p.Rod.Context(ctx).Eval(`() => ({url: location.href, title: document.title})`)
	if err != nil {
		return "", "", err
	}
	return live.Value.Get("url").Str(), live.Value.Get("title").Str(), nil
}

// Viewport is a page viewport size in CSS pixels. It is the engine's MCP-free
// carrier for a per-page MANUAL viewport override (an explicit user option,
// never a default): the MCP/CLI/IPC layers translate their JSON into this
// struct and the engine applies it via Page.SetViewport
// (Emulation.setDeviceMetricsOverride) before navigating. The override sets
// both the emulated viewport and the matching screen metrics (screen.width
// tracks window.innerWidth). A nil *Viewport means "no override" — the page
// launches PLAIN with rod's own defaults (device presets via Page.Emulate are
// the primary, opt-in emulation mechanism; see Emulate).
type Viewport struct {
	// Width is the viewport width in CSS pixels.
	Width int `json:"width"`
	// Height is the viewport height in CSS pixels.
	Height int `json:"height"`
}

// devicePreset is one entry of the go-rod device-preset lookup table: the
// preset's Go identifier (e.g. "IPhoneX"), its human Title (e.g. "iPhone X"),
// and the preset value itself. The table is a hand-typed mirror VERIFIED
// against the vendored rod v0.116.2 lib/devices/list.go (lines 5-690, the
// single `var (` block) — the exported vars there are the source of truth,
// never invent presets (rod v0.116.2 has no IPhone12Pro). A go-rod upgrade
// must diff-review this table against the new list.go.
type devicePreset struct {
	// name is the preset's exported Go identifier in lib/devices/list.go.
	name string
	// title is the preset's Device.Title (e.g. "iPhone X").
	title string
	// dev is the preset value to hand to Page.Emulate.
	dev devices.Device
}

// devicePresets is the full vendor preset list — a hand-typed mirror of every
// exported Device variable in the vendored rod v0.116.2 lib/devices/list.go
// (lines 5-690, the single `var (` block), with its Title, in declaration
// order. The vendor file is the source of truth (never hand-typed, per
// AGENTS.md); a go-rod upgrade trip-wires a diff-review of this table. Both
// spellings resolve case-insensitively, so "iphonex" and "iPhone X" hit the
// same preset.
var devicePresets = []devicePreset{
	{"IPhone4", "iPhone 4", devices.IPhone4},
	{"IPhone5orSE", "iPhone 5/SE", devices.IPhone5orSE},
	{"IPhone6or7or8", "iPhone 6/7/8", devices.IPhone6or7or8},
	{"IPhone6or7or8Plus", "iPhone 6/7/8 Plus", devices.IPhone6or7or8Plus},
	{"IPhoneX", "iPhone X", devices.IPhoneX},
	{"BlackBerryZ30", "BlackBerry Z30", devices.BlackBerryZ30},
	{"Nexus4", "Nexus 4", devices.Nexus4},
	{"Nexus5", "Nexus 5", devices.Nexus5},
	{"Nexus5X", "Nexus 5X", devices.Nexus5X},
	{"Nexus6", "Nexus 6", devices.Nexus6},
	{"Nexus6P", "Nexus 6P", devices.Nexus6P},
	{"Pixel2", "Pixel 2", devices.Pixel2},
	{"Pixel2XL", "Pixel 2 XL", devices.Pixel2XL},
	{"LGOptimusL70", "LG Optimus L70", devices.LGOptimusL70},
	{"NokiaN9", "Nokia N9", devices.NokiaN9},
	{"NokiaLumia520", "Nokia Lumia 520", devices.NokiaLumia520},
	{"MicrosoftLumia550", "Microsoft Lumia 550", devices.MicrosoftLumia550},
	{"MicrosoftLumia950", "Microsoft Lumia 950", devices.MicrosoftLumia950},
	{"GalaxySIII", "Galaxy S III", devices.GalaxySIII},
	{"GalaxyS5", "Galaxy S5", devices.GalaxyS5},
	{"JioPhone2", "JioPhone 2", devices.JioPhone2},
	{"KindleFireHDX", "Kindle Fire HDX", devices.KindleFireHDX},
	{"IPadMini", "iPad Mini", devices.IPadMini},
	{"IPad", "iPad", devices.IPad},
	{"IPadPro", "iPad Pro", devices.IPadPro},
	{"BlackberryPlayBook", "BlackBerry PlayBook", devices.BlackberryPlayBook},
	{"Nexus10", "Nexus 10", devices.Nexus10},
	{"Nexus7", "Nexus 7", devices.Nexus7},
	{"GalaxyNote3", "Galaxy Note 3", devices.GalaxyNote3},
	{"GalaxyNoteII", "Galaxy Note II", devices.GalaxyNoteII},
	{"LaptopWithTouch", "Laptop with touch", devices.LaptopWithTouch.Landscape()},
	{"LaptopWithHiDPIScreen", "Laptop with HiDPI screen", devices.LaptopWithHiDPIScreen.Landscape()},
	{"LaptopWithMDPIScreen", "Laptop with MDPI screen", devices.LaptopWithMDPIScreen.Landscape()},
	{"MotoG4", "Moto G4", devices.MotoG4},
	{"SurfaceDuo", "Surface Duo", devices.SurfaceDuo},
	{"GalaxyFold", "Galaxy Fold", devices.GalaxyFold},
}

// deviceByName resolves a device preset name to its devices.Device value. The
// lookup is case-insensitive on BOTH the exported Go identifier ("iphonex")
// and the preset's Device.Title ("iPhone X"); the special name "clear" (any
// case) resolves to devices.Clear, which resets all overrides. The linear scan
// is fine for the 35-entry vendor table and keeps the table self-documenting.
func deviceByName(name string) (devices.Device, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "clear" {
		return devices.Clear, true
	}
	for _, p := range devicePresets {
		if n == strings.ToLower(p.name) || n == strings.ToLower(p.title) {
			return p.dev, true
		}
	}
	return devices.Device{}, false
}

// ValidateDeviceName reports whether name resolves to a go-rod device preset
// (its Go identifier or Title, case-insensitive) or "clear". It is the
// engine's pure, Chromium-free pre-flight check: the MCP/CLI/IPC layers call
// it before any browser work so an unknown device fails fast with the exact
// pinned error, mirroring the WindowState validation in Browser.Focus. The
// error is exact and pinned: unknown device %q (see go-rod lib/devices
// presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen).
func ValidateDeviceName(name string) error {
	if _, ok := deviceByName(name); !ok {
		return fmt.Errorf("unknown device %q (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)", name)
	}
	return nil
}

// Emulate applies a go-rod device preset (lib/devices, v0.116.2) to the live
// page: the primary, opt-in screen-size emulation mechanism (user decision
// 2026-08-14 — pages launch PLAIN by default; manual width/height via
// SetViewport is only an explicit option). deviceName resolves
// case-insensitively against the preset's Go identifier AND its Device.Title
// (e.g. "iphonex" or "iPhone X"); the special name "clear" applies
// devices.Clear, which resets the metrics/touch/UA overrides. The apply is the
// error-returning, ctx-bound rod form Page.Emulate (no Must*) under the page
// lock, with the nil-Rod guard; a name that matches no preset returns the
// exact pinned unknown-device error.
func (p *Page) Emulate(ctx context.Context, deviceName string) error {
	if err := p.lockContextBudget(ctx, "emulate", readLockBudget, true); err != nil {
		return err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}
	dev, ok := deviceByName(deviceName)
	if !ok {
		return fmt.Errorf("unknown device %q (see go-rod lib/devices presets, e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen)", deviceName)
	}
	if err := p.Rod.Context(ctx).Emulate(dev); err != nil {
		return fmt.Errorf("emulate device %q: %w", deviceName, err)
	}
	return nil
}

// SetViewport applies a MANUAL width × height viewport override to the live
// page via Emulation.setDeviceMetricsOverride with a device scale factor of 1
// (desktop, no mobile emulation) and matching screen metrics (screen.width
// tracks window.innerWidth). It is the engine's single seam for the manual
// override — an explicit user option, never a default: pages launch PLAIN and
// the primary emulation mechanism is the opt-in device preset via Page.Emulate
// (user decision 2026-08-14). The apply is the error-returning, ctx-bound rod
// form Page.SetViewport (no Must*) under the page lock, with the nil-Rod
// guard. vp must be non-nil; the MCP/CLI layers enforce their own pinned
// "requires a viewport" errors before calling.
func (p *Page) SetViewport(ctx context.Context, vp *Viewport) error {
	if err := p.lockContextBudget(ctx, "set viewport", readLockBudget, true); err != nil {
		return err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return fmt.Errorf("page session is closed")
	}
	sw, sh := vp.Width, vp.Height // proto ScreenWidth/ScreenHeight are *int
	if err := p.Rod.Context(ctx).SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             vp.Width,
		Height:            vp.Height,
		DeviceScaleFactor: 1,
		Mobile:            false,
		ScreenWidth:       &sw,
		ScreenHeight:      &sh,
	}); err != nil {
		return fmt.Errorf("browsers: set viewport: %w", err)
	}
	return nil
}
