package browsers

import (
	"context"
	"sync"

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

	// mu serializes access to the underlying rod.Page (rod is not thread-safe).
	mu sync.Mutex
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
// ctx has expired so resources are not leaked.
func (p *Page) Close(ctx context.Context) error {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return nil
	}
	// rod has no ctx-aware Close; the error-returning form is used and the
	// context is honored at the caller boundary (Browser.Close, ClosePage).
	return p.Rod.Close() // error form; no Must*
}

// PageInfo is a read-only snapshot of a page's metadata, safe to copy and
// marshal. It deliberately excludes the live *rod.Page handle.
type PageInfo struct {
	// ID uniquely identifies the page.
	ID PageID `json:"id"`
	// URL is the URL the page was created with.
	URL string `json:"url"`
}
