package browsers

import (
	"context"
	"errors"
	"testing"
)

// TestPageSnapshotsNeverMixTwoPages pins the atomic listing read: each row's URL
// and title come from ONE read of that page, so a second page's title can never
// leak into the first row and a split read is caught.
func TestPageSnapshotsNeverMixTwoPages(t *testing.T) {
	b := NewBrowser("browser-1", context.Background(), false, false, "", "")
	first := &Page{ID: "page-1", URL: "https://first.test/"}
	second := &Page{ID: "page-2", URL: "https://second.test/"}

	firstCalls := 0
	first.info = func(context.Context) (string, string, error) {
		firstCalls++
		if firstCalls > 1 {
			return "https://second.test/", "Second", nil
		}
		return "https://first.test/", "First", nil
	}
	second.info = func(context.Context) (string, string, error) {
		return "https://second.test/", "Second", nil
	}
	b.pages[first.ID] = first
	b.pages[second.ID] = second

	infos := b.Pages()
	if firstCalls != 1 {
		t.Fatalf("first page was read %d times, want one atomic read", firstCalls)
	}

	want := map[PageID]PageInfo{
		"page-1": {ID: "page-1", URL: "https://first.test/", Title: "First", Context: DefaultIdentity},
		"page-2": {ID: "page-2", URL: "https://second.test/", Title: "Second", Context: DefaultIdentity},
	}
	if len(infos) != len(want) {
		t.Fatalf("Pages() = %+v, want %d rows", infos, len(want))
	}
	for _, got := range infos {
		if got.Stale {
			t.Errorf("page %s marked stale, want a live pair", got.ID)
		}
		if got.URL != want[got.ID].URL || got.Title != want[got.ID].Title {
			t.Errorf("page %s = (%q, %q), want (%q, %q)",
				got.ID, got.URL, got.Title, want[got.ID].URL, want[got.ID].Title)
		}
	}
}

// TestPageSnapshotNeverPairsStoredURLWithLiveTitle pins that a partial live
// read (empty URL, present title) is reported stale instead of pairing the
// stored URL with another document's title.
func TestPageSnapshotNeverPairsStoredURLWithLiveTitle(t *testing.T) {
	p := &Page{ID: "page-1", URL: "https://old.test/"}
	p.info = func(context.Context) (string, string, error) {
		return "", "Old Title", nil
	}

	info := p.snapshot(context.Background(), DefaultIdentity)
	if info.URL != "https://old.test/" {
		t.Fatalf("url = %q, want the stored URL", info.URL)
	}
	if info.Title != "" {
		t.Fatalf("title = %q, want no title paired with the stored URL", info.Title)
	}
	if !info.Stale {
		t.Fatal("stale = false, want true for a partial pair")
	}
}

// TestPageSnapshotStaleDropsTitle pins that a failed live read keeps the stored
// URL and never carries a title from the unreadable document.
func TestPageSnapshotStaleDropsTitle(t *testing.T) {
	p := &Page{ID: "page-1", URL: "https://old.test/"}
	p.info = func(context.Context) (string, string, error) {
		return "https://new.test/", "New", errors.New("target navigated or closed")
	}

	info := p.snapshot(context.Background(), DefaultIdentity)
	if !info.Stale {
		t.Fatal("stale = false, want true for a failed read")
	}
	if info.URL != "https://old.test/" || info.Title != "" {
		t.Fatalf("pair = (%q, %q), want the stored URL with no title", info.URL, info.Title)
	}
}
