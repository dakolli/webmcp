package browsers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCloseBrowserUnknownID(t *testing.T) {
	m := NewManager(WithTTL(0))

	err := m.CloseBrowser(context.Background(), "browser-42")
	if !errors.Is(err, ErrUnknownBrowser) {
		t.Fatalf("CloseBrowser() error = %v, want ErrUnknownBrowser", err)
	}
	if !strings.Contains(err.Error(), "running: none") {
		t.Fatalf("CloseBrowser() error = %q, want running: none", err)
	}
}

func TestCloseBrowserUnknownIDListsRunning(t *testing.T) {
	m := NewManager(WithTTL(0))
	m.browsers["browser-1"] = NewBrowser("browser-1", m.baseCtx, false, false, "", "")

	err := m.CloseBrowser(context.Background(), "browser-42")
	if !errors.Is(err, ErrUnknownBrowser) {
		t.Fatalf("CloseBrowser() error = %v, want ErrUnknownBrowser", err)
	}
	if !strings.Contains(err.Error(), "running: browser-1") {
		t.Fatalf("CloseBrowser() error = %q, want running: browser-1", err)
	}
	if _, ok := m.browsers["browser-1"]; !ok {
		t.Fatal("unknown-id close removed the running browser")
	}
}
