package browsers

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPageLockContextTimesOut pins the contract that a caller waiting on a busy
// page gets one actionable sentence naming the operation, the page and the
// elapsed wait instead of blocking forever.
func TestPageLockContextTimesOut(t *testing.T) {
	p := &Page{ID: "page-1"}
	p.Lock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := p.LockContext(ctx, "click")
	if err == nil {
		p.Unlock()
		t.Fatal("LockContext acquired a page that is already locked")
	}
	for _, want := range []string{"click", "page-1", "busy"} {
		if !strings.Contains(err.Error(), want) {
			p.Unlock()
			t.Fatalf("LockContext error = %q, want it to contain %q", err, want)
		}
	}

	p.Unlock()
	if err := p.LockContext(context.Background(), "click"); err != nil {
		t.Fatalf("lock not released after the waiting caller timed out: %v", err)
	}
	p.Unlock()
}

// TestPageLockReleasedOnPanic pins that a deferred Unlock releases the page
// even when the operation panics, so one bad call cannot wedge the page.
func TestPageLockReleasedOnPanic(t *testing.T) {
	p := &Page{ID: "page-1"}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the operation to panic")
			}
		}()
		p.Lock()
		defer p.Unlock()
		panic("operation failed")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := p.LockContext(ctx, "click"); err != nil {
		t.Fatalf("lock not released after panic: %v", err)
	}
	p.Unlock()
}

// TestPageTouchAndIdleForNeverWaitOnOperationLock pins that listing and sweep
// bookkeeping is lock-free: a stuck operation must not block a page's idle
// clock reads.
func TestPageTouchAndIdleForNeverWaitOnOperationLock(t *testing.T) {
	p := &Page{ID: "page-1"}
	p.Lock()
	defer p.Unlock()

	done := make(chan struct{})
	go func() {
		p.touch()
		_ = p.idleFor()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("touch/idleFor blocked on the page operation lock")
	}
}

// TestBrowserPageReturnsWhilePageBusy pins that resolving a page never waits on
// an in-flight operation: a busy page is alive, so the lookup skips the probe
// and returns.
func TestBrowserPageReturnsWhilePageBusy(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	p := &Page{ID: "page-1"}
	b.pages[p.ID] = p
	p.Lock()
	defer p.Unlock()

	type result struct {
		page *Page
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := b.Page(context.Background(), p.ID)
		done <- result{page: page, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Browser.Page() error = %v", got.err)
		}
		if got.page != p {
			t.Fatalf("Browser.Page() = %v, want the busy page", got.page)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Browser.Page blocked on a busy page")
	}
}

// TestBrowserPagesListsWhilePageBusy pins that page_list/session_list answer
// while a page operation is in flight.
func TestBrowserPagesListsWhilePageBusy(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	p := &Page{ID: "page-1", URL: "https://example.com"}
	b.pages[p.ID] = p
	p.Lock()
	defer p.Unlock()

	done := make(chan []PageInfo, 1)
	go func() { done <- b.Pages() }()

	select {
	case pages := <-done:
		if len(pages) != 1 || pages[0].ID != p.ID {
			t.Fatalf("Pages() = %v, want the busy page", pages)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Pages() blocked on a busy page")
	}
}

// TestBrowserCloseDoesNotWaitOnBusyPage pins that teardown of a wedged browser
// completes: a page held by an in-flight operation is skipped and the browser
// close tears its renderer down instead of waiting on the page lock.
func TestBrowserCloseDoesNotWaitOnBusyPage(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	p := &Page{ID: "page-1"}
	b.pages[p.ID] = p
	p.Lock()

	done := make(chan error, 1)
	go func() { done <- b.Close(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Browser.Close() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		p.Unlock()
		t.Fatal("Browser.Close blocked on a busy page")
	}
	if len(b.pages) != 0 {
		b.mu.Lock()
		remaining := len(b.pages)
		b.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("Browser.Close left %d pages registered", remaining)
		}
	}
	p.Unlock()
}

// TestPagesReportsLiveURLAndTitle pins that a listing follows navigation: it
// reports the tab's current URL and title, not the URL captured at creation.
func TestPagesReportsLiveURLAndTitle(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	p := &Page{
		ID:  "page-1",
		URL: "https://app.test/old",
		info: func(context.Context) (string, string, error) {
			return "https://app.test/new", "Orders", nil
		},
	}
	b.pages[p.ID] = p

	pages := b.Pages()
	if len(pages) != 1 {
		t.Fatalf("Pages() = %d entries, want 1", len(pages))
	}
	got := pages[0]
	if got.URL != "https://app.test/new" || got.Title != "Orders" {
		t.Fatalf("Pages()[0] = %+v, want the live url and title", got)
	}
	if got.Stale {
		t.Fatal("Pages()[0].Stale = true, want a live read")
	}
}

// TestPagesBusyPageFallsBackStale pins the no-block contract: a page held by an
// in-flight operation is reported from its stored URL with Stale set, and the
// listing returns immediately without attempting the read.
func TestPagesBusyPageFallsBackStale(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	read := false
	p := &Page{
		ID:  "page-1",
		URL: "https://app.test/last-view",
		info: func(context.Context) (string, string, error) {
			read = true
			return "https://app.test/live", "Live", nil
		},
	}
	b.pages[p.ID] = p
	p.Lock()
	defer p.Unlock()

	done := make(chan []PageInfo, 1)
	go func() { done <- b.Pages() }()

	select {
	case pages := <-done:
		if len(pages) != 1 {
			t.Fatalf("Pages() = %d entries, want 1", len(pages))
		}
		if !pages[0].Stale {
			t.Fatalf("Pages()[0] = %+v, want Stale on a busy page", pages[0])
		}
		if pages[0].URL != "https://app.test/last-view" {
			t.Fatalf("Pages()[0].URL = %q, want the stored URL", pages[0].URL)
		}
		if read {
			t.Fatal("Pages() read a busy page")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Pages() blocked on a busy page")
	}
}

// TestPagesUnreadablePageFallsBackStale pins that a dead target reports the
// stored URL with Stale set instead of failing the whole listing.
func TestPagesUnreadablePageFallsBackStale(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	p := &Page{
		ID:  "page-1",
		URL: "https://app.test/last-view",
		info: func(context.Context) (string, string, error) {
			return "", "", errors.New("target closed")
		},
	}
	b.pages[p.ID] = p

	pages := b.Pages()
	if len(pages) != 1 || !pages[0].Stale {
		t.Fatalf("Pages() = %+v, want one Stale entry", pages)
	}
	if pages[0].URL != "https://app.test/last-view" {
		t.Fatalf("Pages()[0].URL = %q, want the stored URL", pages[0].URL)
	}
}

// TestPagesBoundsLiveRead pins the read budget: a read that never answers is
// abandoned at listingInfoTimeout and reported stale, so one wedged tab cannot
// stall the listing.
func TestPagesBoundsLiveRead(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	p := &Page{
		ID:  "page-1",
		URL: "https://app.test/last-view",
		info: func(ctx context.Context) (string, string, error) {
			<-ctx.Done()
			return "", "", ctx.Err()
		},
	}
	b.pages[p.ID] = p

	start := time.Now()
	pages := b.Pages()
	if elapsed := time.Since(start); elapsed > 2*listingInfoBudget {
		t.Fatalf("Pages() took %s, want it bounded near %s", elapsed, listingInfoBudget)
	}
	if len(pages) != 1 || !pages[0].Stale {
		t.Fatalf("Pages() = %+v, want one Stale entry", pages)
	}
}

// TestPageInfoPayloadShape pins the listing payload keys the tool descriptions
// promise: id, url and context always; title and the stale marker when known.
func TestPageInfoPayloadShape(t *testing.T) {
	data, err := json.Marshal(PageInfo{
		ID:      "page-1",
		URL:     "https://app.test/new",
		Context: "default",
		Title:   "Orders",
		Stale:   true,
	})
	if err != nil {
		t.Fatalf("marshal PageInfo: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal PageInfo: %v", err)
	}
	for _, key := range []string{"id", "url", "context", "title", "stale"} {
		if _, ok := got[key]; !ok {
			t.Errorf("PageInfo payload missing key %q: %s", key, data)
		}
	}
}

// healthyInfo is a page metadata seam that always answers, so a watchdog
// recovery probe passes without a live browser.
func healthyInfo(context.Context) (string, string, error) {
	return "https://app.test/", "App", nil
}

// settle polls cond for up to three seconds.
func settle(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestGuardPageShortDeadlineCallsLeaveNoLock pins WEB-220: fifty operations
// that spend their whole budget each return, leave the operation lock free,
// and leave no goroutine behind.
func TestGuardPageShortDeadlineCallsLeaveNoLock(t *testing.T) {
	p := &Page{ID: "page-1", info: healthyInfo}
	baseline := runtime.NumGoroutine()

	for i := range 50 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		value, err := GuardPage(ctx, p, "click", func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		cancel()
		if value != "" {
			t.Fatalf("call %d: value = %q, want empty", i, value)
		}
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: error = %v, want the operation budget error", i, err)
		}
	}

	unlock, ok := p.TryLock()
	if !ok {
		t.Fatal("page lock still held after 50 short-deadline calls")
	}
	unlock()

	settle(t, "guard goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= baseline+2
	})
}

// TestGuardPageWatchdogTripsThenRecovers pins WEB-220: an operation that
// ignores its cancelled context trips the watchdog, the call returns the typed
// watchdog error, the page refuses new work while wedged, and once the stuck
// operation returns the page is probed and usable again.
func TestGuardPageWatchdogTripsThenRecovers(t *testing.T) {
	p := &Page{ID: "page-1", info: healthyInfo}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	release := make(chan struct{})
	started := time.Now()
	value, err := GuardPage(ctx, p, "click", func(context.Context) (string, error) {
		<-release
		return "ok", nil
	})
	if value != "" {
		t.Fatalf("value = %q, want empty on a watchdog trip", value)
	}
	var trip *PageWatchdogError
	if !errors.As(err, &trip) {
		t.Fatalf("error = %v, want *PageWatchdogError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to unwrap context.DeadlineExceeded", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "watchdog") {
		t.Fatalf("error = %q, want it to name the watchdog", err)
	}
	if elapsed := time.Since(started); elapsed < watchdogGrace {
		t.Fatalf("watchdog tripped after %s, want at least the grace %s", elapsed, watchdogGrace)
	}

	if lockErr := p.LockContext(context.Background(), "probe"); !errors.As(lockErr, &trip) {
		t.Fatalf("LockContext while wedged = %v, want *PageWatchdogError", lockErr)
	}

	close(release)
	settle(t, "the page to recover", func() bool {
		return p.wedgeErr.Load() == nil && p.closedErr.Load() == nil
	})

	after := context.Background()
	next, err := GuardPage(after, p, "click", func(context.Context) (string, error) {
		return "ok", nil
	})
	if err != nil || next != "ok" {
		t.Fatalf("next GuardPage = (%q, %v), want (\"ok\", nil)", next, err)
	}
}

// TestGuardPageReleasesLockOnEveryReturnPath pins that a value return, an
// error return, a panic and a budget cancellation each release the operation
// lock.
func TestGuardPageReleasesLockOnEveryReturnPath(t *testing.T) {
	tests := []struct {
		name    string
		fn      func(context.Context) (string, error)
		wantErr string
	}{
		{
			name: "value",
			fn:   func(context.Context) (string, error) { return "ok", nil },
		},
		{
			name:    "error",
			fn:      func(context.Context) (string, error) { return "", errors.New("boom") },
			wantErr: "boom",
		},
		{
			name:    "panic",
			fn:      func(context.Context) (string, error) { panic("kaboom") },
			wantErr: "panicked",
		},
		{
			name: "budget cancellation",
			fn: func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
			wantErr: "deadline exceeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Page{ID: "page-1", info: healthyInfo}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := GuardPage(ctx, p, "click", tt.fn)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("GuardPage() error = %v, want nil", err)
				}
			} else if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantErr) {
				t.Fatalf("GuardPage() error = %v, want it to contain %q", err, tt.wantErr)
			}
			settle(t, "the operation lock to be free", func() bool {
				unlock, ok := p.TryLock()
				if ok {
					unlock()
				}
				return ok
			})
		})
	}
}

// TestPageBrokenErrorClosesTheSession pins the typed closure path: a page the
// watchdog could not recover fails every new operation with a page_closed
// error that says the session is closed.
func TestPageBrokenErrorClosesTheSession(t *testing.T) {
	p := &Page{ID: "page-1", info: healthyInfo}
	p.closedErr.Store(&PageBrokenError{
		Op:   "click",
		Page: p.ID,
		Err:  errors.New("no answer"),
	})

	err := p.LockContext(context.Background(), "click")
	var broken *PageBrokenError
	if !errors.As(err, &broken) {
		t.Fatalf("LockContext() = %v, want *PageBrokenError", err)
	}
	if !strings.Contains(err.Error(), "page session is closed") {
		t.Fatalf("error = %q, want it to say the session is closed", err)
	}
	if _, ok := p.TryLock(); ok {
		t.Fatal("TryLock acquired a session marked closed")
	}
}
