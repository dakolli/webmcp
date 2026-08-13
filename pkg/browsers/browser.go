package browsers

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// Browser owns a single live Chromium instance (a *rod.Browser) and the map of
// its live Pages (browser tabs, a.k.a. MCP sessions).
//
// Thread safety: the pages map is guarded by the Browser mutex; each Page
// carries its own mutex because rod Pages are not thread-safe (see session.go).
type Browser struct {
	// mu guards the pages map and the rod browser pointer.
	mu sync.Mutex
	// rod is the live go-rod browser handle; nil until Launch succeeds.
	rod *rod.Browser
	// pages maps page IDs to their live pages.
	pages map[PageID]*Page
	// nextID is the counter used to mint fresh PageIDs in NewPage.
	nextID int
	// headless selects headless Chromium at launch time (default: headful).
	headless bool
	// stealth applies go-rod/stealth to every new page (default: on).
	stealth bool
}

// NewBrowser constructs a Browser ready to be launched. headless selects
// headless Chromium (headful is the default); stealth enables go-rod/stealth
// on every new page. No browser process is started until Launch.
func NewBrowser(headless, stealth bool) *Browser {
	return &Browser{
		pages:    make(map[PageID]*Page),
		headless: headless,
		stealth:  stealth,
	}
}

// Launch connects the Browser to a Chromium instance via the go-rod launcher.
// Chromium runs headful by default (`.Headless(false)`) unless the Browser was
// constructed with headless=true; NoSandbox is always set, as required in
// containers. Launch is idempotent: it is a no-op once the browser is up.
func (b *Browser) Launch(ctx context.Context) error {
	b.mu.Lock()
	if b.rod != nil {
		b.mu.Unlock()
		return nil // idempotent: already launched
	}
	b.mu.Unlock()

	// Bail out early when the caller's context is already done; the launcher
	// and connect steps below are local but can still block (e.g. first run
	// downloading Chromium), so honor ctx between steps.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browsers: launch browser: %w", err)
	}

	l := launcher.New().NoSandbox(true).Headless(b.headless) // headful by default
	u, err := l.Launch()                                     // error form; no Must*
	if err != nil {
		return fmt.Errorf("browsers: launch chromium: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browsers: launch browser: %w", err)
	}

	rb := rod.New().ControlURL(u)
	if err := rb.Connect(); err != nil {
		return fmt.Errorf("browsers: connect: %w", err)
	}

	b.mu.Lock()
	b.rod = rb
	b.mu.Unlock()
	return nil
}

// NewPage opens a new tab in the Browser, navigates it to url (when non-empty)
// and registers it in the pages map under a fresh PageID. Stealth is applied
// to every new page by default and skipped only when the Browser was
// constructed with stealth=false.
func (b *Browser) NewPage(ctx context.Context, url string) (*Page, error) {
	b.mu.Lock()
	if b.rod == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("browsers: browser not launched")
	}
	b.mu.Unlock()

	var p *rod.Page
	var err error
	if b.stealth {
		p, err = stealth.Page(b.rod) // stealth on by default (error form)
	} else {
		p, err = b.rod.Page(proto.TargetCreateTarget{})
	}
	if err != nil {
		return nil, fmt.Errorf("browsers: new page: %w", err)
	}

	if url != "" {
		// Bound to ctx so a hung navigation cannot outlive the tool call.
		pc := p.Context(ctx)
		if err := pc.Navigate(url); err != nil {
			_ = p.Close() // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: navigate: %w", err)
		}
		if err := pc.WaitLoad(); err != nil {
			_ = p.Close() // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: wait load: %w", err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	page := &Page{ID: PageID(fmt.Sprintf("page-%d", b.nextID)), URL: url, Rod: p}
	b.pages[page.ID] = page
	return page, nil
}

// Page returns the live page with the given ID, or an error when no page with
// that ID is open (e.g. it was closed or swept).
func (b *Browser) Page(id PageID) (*Page, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pages[id]
	if !ok {
		return nil, fmt.Errorf("browsers: page %q not found", id)
	}
	return p, nil
}

// Pages returns a read-only snapshot of the Browser's live pages (id + url),
// safe to marshal and iterate; it never exposes the live *rod.Page handles.
func (b *Browser) Pages() []PageInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	infos := make([]PageInfo, 0, len(b.pages))
	for _, p := range b.pages {
		infos = append(infos, PageInfo{ID: p.ID, URL: p.URL})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

// ClosePage closes the page with the given ID, removing it from the pages map
// and releasing its browser tab.
func (b *Browser) ClosePage(ctx context.Context, id PageID) error {
	b.mu.Lock()
	p, ok := b.pages[id]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("browsers: page %q not found", id)
	}

	if err := p.Close(ctx); err != nil {
		return fmt.Errorf("browsers: close page %q: %w", id, err)
	}

	b.mu.Lock()
	delete(b.pages, id)
	b.mu.Unlock()
	return nil
}

// Close closes the Browser and all of its live pages, releasing the underlying
// Chromium process. After Close the Browser must not be reused.
func (b *Browser) Close(ctx context.Context) error {
	b.mu.Lock()
	for _, p := range b.pages {
		_ = p.Close(ctx) // best-effort per-page close
	}
	b.pages = make(map[PageID]*Page)
	rb := b.rod
	b.rod = nil
	b.mu.Unlock()

	if rb == nil {
		return nil
	}
	return rb.Close() // error form; no Must*
}
