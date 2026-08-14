package browsers

import (
	"context"
	"sync"
	"time"

	"github.com/go-rod/rod"
)

// PageID uniquely identifies a live page (a.k.a. session / browser tab) owned
// by a Browser. It is minted by Browser.NewPage and referenced from every
// page_fetch / page_act / session_close call, e.g. "page-1".
type PageID string

// Page wraps a live *rod.Page (a browser tab) and its own mutex, because rod
// Pages are NOT thread-safe: all access to the underlying page must be
// serialized with the per-page mutex. Tools Lock before touching the page and
// Unlock after the page call completes; the Browser does the same for its own
// lifecycle operations (ClosePage, Close).
type Page struct {
	// ID uniquely identifies this page within its Browser.
	ID PageID
	// URL is the URL the page was created with (set by NewPage).
	URL string
	// Rod is the live go-rod page handle for this tab. Only valid while the
	// page is open.
	Rod *rod.Page
	// lastAccess is the last time this page was used: set at creation (NewPage)
	// and refreshed by touch on every successful Browser.Page lookup. The
	// manager's background sweeper closes pages idle for longer than its TTL.
	lastAccess time.Time

	// mu serializes access to the underlying rod.Page (rod is not thread-safe).
	mu sync.Mutex
}

// touch records that the page was just used, resetting its idle clock so the
// manager's sweeper does not close it. It is safe to call while holding the
// Browser mutex: the browser lock is always taken before the page lock.
func (p *Page) touch() {
	p.mu.Lock()
	p.lastAccess = time.Now()
	p.mu.Unlock()
}

// Lock serializes access to the page's underlying rod page. Pair with Unlock.
func (p *Page) Lock() {
	p.mu.Lock()
}

// Unlock releases the per-page lock taken by Lock.
func (p *Page) Unlock() {
	p.mu.Unlock()
}

// Close closes the underlying rod tab, releasing the browser tab. The page
// must not be used afterwards. It is safe to call on an already-closed page
// (nil Rod handle) and is best-effort by design: teardown is attempted even if
// ctx has expired so resources are not leaked. The caller's bounded timeout
// (sweep, shutdown) is enforced at the rod boundary: a wedged CDP connection
// surfaces a ctx-bound error (e.g. context.DeadlineExceeded) instead of
// blocking the caller past its deadline.
func (p *Page) Close(ctx context.Context) error {
	p.Lock()
	defer p.Unlock()
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
	// URL is the URL the page was created with.
	URL string `json:"url"`
}
