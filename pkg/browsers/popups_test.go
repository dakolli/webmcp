package browsers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// TestPopupRegistrationReportsOpener pins that a created target is registered
// as a page with its opener, so both page_list and session_list expose it.
func TestPopupRegistrationReportsOpener(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	opener := &Page{ID: "page-1", URL: "https://app.test/"}
	b.pages[opener.ID] = opener
	b.popups.targets = map[proto.TargetTargetID]*Page{"target-opener": opener}

	popup := &Page{ID: "page-2", URL: "https://app.test/window"}
	registered, err := b.registerPage(&proto.TargetTargetInfo{
		TargetID: "target-popup",
		OpenerID: "target-opener",
		Type:     proto.TargetTargetInfoTypePage,
		URL:      popup.URL,
	}, popup)
	if err != nil {
		t.Fatalf("registerPage: %v", err)
	}
	if !registered {
		t.Fatal("registerPage refused a popup below the cap")
	}

	pages := b.Pages()
	if len(pages) != 2 {
		t.Fatalf("Pages() = %+v, want the opener and its popup", pages)
	}
	found := false
	for _, p := range pages {
		if p.ID != popup.ID {
			continue
		}
		found = true
		if p.Opener != opener.ID {
			t.Errorf("popup opener = %q, want %q", p.Opener, opener.ID)
		}
		if p.URL != popup.URL {
			t.Errorf("popup url = %q, want %q", p.URL, popup.URL)
		}
	}
	if !found {
		t.Fatalf("Pages() = %+v, want the registered popup", pages)
	}

	data, err := json.Marshal(pages)
	if err != nil {
		t.Fatalf("marshal pages: %v", err)
	}
	if !strings.Contains(string(data), `"opener":"page-1"`) {
		t.Fatalf("listing payload %s does not carry the opener", data)
	}
}

// TestPageCapRefusesNewPages pins the pinned cap error once a browser holds the
// maximum number of live pages.
func TestPageCapRefusesNewPages(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	for i := 1; i <= maxPagesPerBrowser; i++ {
		id := PageID(fmt.Sprintf("page-%d", i))
		b.pages[id] = &Page{ID: id}
	}

	if err := b.checkPageCap(); err == nil {
		t.Fatal("checkPageCap() = nil, want the page limit error")
	} else {
		for _, want := range []string{
			"page limit reached",
			"browser browser-1 already has 20 live pages",
			"max 20",
			"session_close",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("cap error = %q, want it to contain %q", err, want)
			}
		}
	}

	registered, err := b.registerPage(&proto.TargetTargetInfo{TargetID: "target-popup"}, &Page{ID: "page-21"})
	if registered {
		t.Fatal("registerPage accepted a popup past the cap")
	}
	if err == nil || !strings.Contains(err.Error(), "page limit reached") {
		t.Fatalf("registerPage error = %v, want the page limit error", err)
	}
	if _, ok := b.pages["page-21"]; ok {
		t.Fatal("registerPage left a page past the cap registered")
	}
}

// TestWaitOpenReturnsPopup pins that wait_open blocks until a popup registers
// and reports its session id, URL and opener.
func TestWaitOpenReturnsPopup(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	opener := &Page{ID: "page-1", URL: "https://app.test/"}
	b.pages[opener.ID] = opener
	b.popups.targets = map[proto.TargetTargetID]*Page{"target-opener": opener}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	registered := make(chan struct{})
	go func() {
		defer close(registered)
		time.Sleep(20 * time.Millisecond)
		_, _ = b.registerPage(&proto.TargetTargetInfo{
			TargetID: "target-popup",
			OpenerID: "target-opener",
		}, &Page{ID: "page-2", URL: "https://app.test/window"})
	}()

	popup, err := b.WaitOpen(ctx, opener.ID)
	if err != nil {
		t.Fatalf("WaitOpen: %v", err)
	}
	<-registered
	if popup.SessionID != "page-2" {
		t.Errorf("WaitOpen session = %q, want page-2", popup.SessionID)
	}
	if popup.Opener != opener.ID {
		t.Errorf("WaitOpen opener = %q, want %q", popup.Opener, opener.ID)
	}
	if popup.URL != "https://app.test/window" {
		t.Errorf("WaitOpen url = %q, want the popup URL", popup.URL)
	}

	short, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()
	if _, err := b.WaitOpen(short, opener.ID); err == nil {
		t.Fatal("a second WaitOpen repeated the already claimed popup")
	}
}

// TestClosePageClosesPopupChildren pins that closing an opener also closes the
// popups it opened, so no orphan target survives it.
func TestClosePageClosesPopupChildren(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	b := NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	opener := &Page{ID: "page-1"}
	popup := &Page{ID: "page-2", Opener: opener.ID}
	grandchild := &Page{ID: "page-3", Opener: popup.ID}
	b.pages[opener.ID] = opener
	b.pages[popup.ID] = popup
	b.pages[grandchild.ID] = grandchild
	b.popups.targets = map[proto.TargetTargetID]*Page{
		"target-1": opener,
		"target-2": popup,
		"target-3": grandchild,
	}
	m.browsers[b.id] = b

	if _, err := m.ClosePage(context.Background(), opener.ID, "", ""); err != nil {
		t.Fatalf("ClosePage(%q): %v", opener.ID, err)
	}
	for _, id := range []PageID{opener.ID, popup.ID, grandchild.ID} {
		if _, ok := b.pages[id]; ok {
			t.Errorf("ClosePage left popup %q registered", id)
		}
	}
}

// TestPopupTargetCreatedAndWaitOpen exercises the real target event: clicking a
// target="_blank" link registers a popup and wait_open returns it. It skips
// when no Chromium-family browser is installed.
func TestPopupTargetCreatedAndWaitOpen(t *testing.T) {
	bin := DefaultBinPath()
	if bin == "" {
		t.Skip("no Chromium-family browser on PATH")
	}
	l := launcher.New().Bin(bin).Headless(true).NoSandbox(true)
	controlURL, err := l.Launch()
	if err != nil {
		t.Skipf("launch browser: %v", err)
	}
	t.Cleanup(l.Cleanup)

	rb := rod.New().ControlURL(controlURL).NoDefaultDevice()
	if err := rb.Connect(); err != nil {
		t.Skipf("connect browser: %v", err)
	}

	b := NewBrowser("browser-1", context.Background(), true, false, bin, "")
	b.rod = rb
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	b.startTargetTracking()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/window" {
			_, _ = fmt.Fprint(w, "<h1>popup</h1>")
			return
		}
		_, _ = fmt.Fprint(w, `<a id="open" href="/window" target="_blank">open</a>`)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page, err := b.NewPage(ctx, server.URL, nil, "", "")
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	link, err := page.Rod.Context(ctx).Element("#open")
	if err != nil {
		t.Fatalf("find target=_blank link: %v", err)
	}
	if err := link.Click(proto.InputMouseButtonLeft, 1); err != nil {
		t.Fatalf("click target=_blank link: %v", err)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	popup, err := b.WaitOpen(waitCtx, page.ID)
	if err != nil {
		t.Fatalf("WaitOpen: %v", err)
	}
	if popup.SessionID == page.ID {
		t.Fatalf("WaitOpen returned the opener %q instead of the popup", page.ID)
	}
	if popup.Opener != page.ID {
		t.Errorf("popup opener = %q, want %q", popup.Opener, page.ID)
	}
	if !strings.Contains(popup.URL, "/window") {
		t.Errorf("popup url = %q, want the /window target", popup.URL)
	}

	listed := b.Pages()
	found := false
	for _, p := range listed {
		if p.ID == popup.SessionID && p.Opener == page.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("Pages() = %+v, want %q with opener %q", listed, popup.SessionID, page.ID)
	}
}
