package browsers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestReleaseProfileRemovesEphemeralAtLastOwner(t *testing.T) {
	m := NewManager(WithTTL(0))
	dir := filepath.Join(t.TempDir(), "ephemeral")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create ephemeral dir: %v", err)
	}

	m.retainProfile(dir, true)
	m.retainProfile(dir, true)

	m.releaseProfile(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("ephemeral dir removed with an owner still live: %v", err)
	}

	m.releaseProfile(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("ephemeral dir still present after last release: %v", err)
	}
	if len(m.profileRefs) != 0 {
		t.Fatalf("profile refs = %d, want 0", len(m.profileRefs))
	}
}

func TestReleaseProfileKeepsPersistentDir(t *testing.T) {
	m := NewManager(WithTTL(0))
	dir := filepath.Join(t.TempDir(), "persistent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create persistent dir: %v", err)
	}

	m.retainProfile(dir, false)
	m.releaseProfile(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("persistent dir removed: %v", err)
	}
}

func TestBrowserCloseRunsProfileCleanupOnce(t *testing.T) {
	b := NewBrowser("browser-9", context.Background(), false, false, "", "")
	var calls int
	b.profileCleanup = func() { calls++ }

	for range 2 {
		if err := b.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("profile cleanup calls = %d, want 1", calls)
	}
}

func TestLaunchRefusesUnresolvedProfile(t *testing.T) {
	b := NewBrowser("browser-2", context.Background(), false, false, "", "")

	err := b.Launch(context.Background())
	if err == nil {
		t.Fatal("Launch() succeeded without a resolved profile directory")
	}
	if !strings.Contains(err.Error(), "no profile directory resolved") {
		t.Fatalf("Launch() error = %q, want the missing-profile error", err)
	}
}
