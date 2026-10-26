package browsers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// identityTestBrowser builds an unlaunched Browser with a stub incognito seam:
// every identity gets its own rod browser context id, as Chromium does for a
// real Target.createBrowserContext call.
func identityTestBrowser(t *testing.T) *Browser {
	t.Helper()
	b := NewBrowser("browser-1", context.Background(), false, false, "", "/profiles/default")
	b.rod = &rod.Browser{}
	var n int
	b.incognito = func(*rod.Browser) (*rod.Browser, error) {
		n++
		return &rod.Browser{BrowserContextID: proto.BrowserBrowserContextID(fmt.Sprintf("ctx-%d", n))}, nil
	}
	return b
}

// TestIdentityIsolationSeparatesCookiesAndPages is the isolation proof: each
// identity receives a distinct rod browser context (the unit Chromium scopes
// cookies and storage to) and its own pages map, so a page opened in one
// identity is neither listed nor addressable inside another.
func TestIdentityIsolationSeparatesCookiesAndPages(t *testing.T) {
	b := identityTestBrowser(t)

	victim, err := b.NewIdentity("victim")
	if err != nil {
		t.Fatalf("NewIdentity(victim): %v", err)
	}
	attacker, err := b.NewIdentity("attacker")
	if err != nil {
		t.Fatalf("NewIdentity(attacker): %v", err)
	}

	if victim.rod.BrowserContextID == "" || attacker.rod.BrowserContextID == "" {
		t.Fatal("identity has no browser context id; cookies would leak into the parent jar")
	}
	if victim.rod.BrowserContextID == attacker.rod.BrowserContextID {
		t.Fatalf("identities share context %q; cookies would mix", victim.rod.BrowserContextID)
	}
	if victim.rod.BrowserContextID == b.rod.BrowserContextID {
		t.Fatal("identity shares the parent context; cookies would leak into default")
	}

	b.pages["page-1"] = &Page{ID: "page-1", URL: "https://app.test/orders/1"}
	victim.pages["page-2"] = &Page{ID: "page-2", URL: "https://app.test/orders/1"}

	if _, ok := victim.pages["page-1"]; ok {
		t.Fatal("default's page leaked into the victim identity")
	}
	if _, ok := b.pages["page-2"]; ok {
		t.Fatal("victim's page leaked into the default identity")
	}

	pages := b.Pages()
	if len(pages) != 2 {
		t.Fatalf("Pages() = %d entries, want 2", len(pages))
	}
	want := map[PageID]string{"page-1": "default", "page-2": "victim"}
	for _, p := range pages {
		if p.Context != want[p.ID] {
			t.Errorf("page %s context = %q, want %q", p.ID, p.Context, want[p.ID])
		}
	}
}

// TestIdentityUnknownListsLiveNames pins the lookup-only contract: a typo is
// refused with every live identity name and never creates a silent clone.
func TestIdentityUnknownListsLiveNames(t *testing.T) {
	b := identityTestBrowser(t)
	if _, err := b.NewIdentity("victim"); err != nil {
		t.Fatalf("NewIdentity(victim): %v", err)
	}

	_, err := b.Identity("ghost")
	if err == nil {
		t.Fatal("Identity(ghost) succeeded, want an unknown-identity error")
	}
	for _, want := range []string{`unknown identity "ghost"`, "known: default, victim"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Identity error = %q, want it to contain %q", err, want)
		}
	}
	if len(b.identities) != 1 {
		t.Fatalf("identities = %d, want 1 (no auto-create)", len(b.identities))
	}
}

// TestIdentityPageIDsShareParentCounter pins globally unique session ids: a
// page opened in an identity and one in the parent never collide.
func TestIdentityPageIDsShareParentCounter(t *testing.T) {
	b := identityTestBrowser(t)
	victim, err := b.NewIdentity("victim")
	if err != nil {
		t.Fatalf("NewIdentity(victim): %v", err)
	}

	ids := map[PageID]bool{
		b.mintPageID():      true,
		victim.mintPageID(): true,
		b.mintPageID():      true,
	}
	if len(ids) != 3 {
		t.Fatalf("minted ids = %v, want three distinct ids", ids)
	}
}

// TestCloseCascadesToIdentities pins that closing the parent drops every
// identity and its pages, so a closed browser cannot keep a live context.
func TestCloseCascadesToIdentities(t *testing.T) {
	b := identityTestBrowser(t)
	victim, err := b.NewIdentity("victim")
	if err != nil {
		t.Fatalf("NewIdentity(victim): %v", err)
	}
	victim.pages["page-1"] = &Page{ID: "page-1"}
	victim.rod = nil // no live CDP socket in a unit test
	b.rod = nil

	if err := b.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(b.identities) != 0 {
		t.Fatalf("identities after close = %d, want 0", len(b.identities))
	}
	if len(victim.pages) != 0 {
		t.Fatalf("identity pages after close = %d, want 0", len(victim.pages))
	}
}
