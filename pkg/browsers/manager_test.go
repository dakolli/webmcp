package browsers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"

	"webmcp/pkg/config"
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

func TestReleaseProfileRemovesEphemeral(t *testing.T) {
	m := NewManager(WithTTL(0))
	dir := filepath.Join(t.TempDir(), "ephemeral")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create ephemeral dir: %v", err)
	}

	if err := m.retainProfile(dir, true, "browser-1", UnownedOwner, ""); err != nil {
		t.Fatalf("retainProfile: %v", err)
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

	if err := m.retainProfile(dir, false, "browser-1", UnownedOwner, ""); err != nil {
		t.Fatalf("retainProfile: %v", err)
	}
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

func TestCreateBrowserKeyIdempotent(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0), WithBin("/usr/bin/brave"))
	m.probe = func(*Browser) error { return nil }

	existing := NewBrowser("browser-4", m.baseCtx, false, true, "/usr/bin/brave", "/profiles/brave")
	existing.owner = UnownedOwner
	existing.key = launchKey{
		Owner:    UnownedOwner,
		Bin:      "/usr/bin/brave",
		Stealth:  true,
		Headless: false,
	}
	m.browsers["browser-4"] = existing

	id, b, err := m.CreateBrowser(context.Background())
	if err != nil {
		t.Fatalf("CreateBrowser: %v", err)
	}
	if id != "browser-4" {
		t.Fatalf("CreateBrowser id = %q, want the matching live browser browser-4", id)
	}
	if b != existing {
		t.Fatal("CreateBrowser returned a different browser than the one matching the create key")
	}
	if len(m.browsers) != 1 {
		t.Fatalf("live browsers = %d, want 1 (no new id minted)", len(m.browsers))
	}
}

func TestCreateBrowserKeyCoversLaunchOptions(t *testing.T) {
	base := launchKey{
		Owner:       "client-a",
		Bin:         "/usr/bin/brave",
		ProfileDir:  "/profiles/a",
		ProfileName: "audit",
		Persist:     true,
		Headless:    true,
		Stealth:     true,
		Position:    "left",
		Proxy:       "127.0.0.1:8080",
		ProxyBypass: "localhost",
		IgnoreCerts: true,
	}

	cases := []struct {
		name string
		mut  func(*launchKey)
	}{
		{"owner", func(k *launchKey) { k.Owner = "client-b" }},
		{"bin", func(k *launchKey) { k.Bin = "/usr/bin/chrome" }},
		{"profile dir", func(k *launchKey) { k.ProfileDir = "/profiles/b" }},
		{"profile name", func(k *launchKey) { k.ProfileName = "other" }},
		{"persist", func(k *launchKey) { k.Persist = false }},
		{"headless", func(k *launchKey) { k.Headless = false }},
		{"stealth", func(k *launchKey) { k.Stealth = false }},
		{"position", func(k *launchKey) { k.Position = "right" }},
		{"proxy", func(k *launchKey) { k.Proxy = "127.0.0.1:9090" }},
		{"proxy bypass", func(k *launchKey) { k.ProxyBypass = "example.com" }},
		{"ignore certs", func(k *launchKey) { k.IgnoreCerts = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.mut(&changed)
			if changed == base {
				t.Errorf("changing %s did not change the create key", tc.name)
			}
		})
	}
}

func TestCreateReportsReusedOnSecondIdenticalCall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(WithTTL(0), WithBrowserTTL(0), WithPersistentDefault(false))
	m.probe = func(*Browser) error { return nil }
	m.launch = func(context.Context, *Browser) error { return nil }

	first, err := m.Create(context.Background())
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if first.Reused {
		t.Fatal("first Create reported a reuse, want a fresh launch")
	}

	second, err := m.Create(context.Background())
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if !second.Reused {
		t.Fatal("second identical Create did not report a reuse")
	}
	if second.ID != first.ID {
		t.Fatalf("second Create id = %q, want the reused %q", second.ID, first.ID)
	}
	if len(m.browsers) != 1 {
		t.Fatalf("live browsers = %d, want 1", len(m.browsers))
	}
}

func TestCreateBrowserFailsFastOnBusyProfile(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	dir := t.TempDir()

	holder := NewBrowser("browser-2", m.baseCtx, false, false, "", dir)
	m.browsers["browser-2"] = holder
	if err := m.retainProfile(dir, false, "browser-2", "session-a", "client-a"); err != nil {
		t.Fatalf("retainProfile: %v", err)
	}

	_, _, err := m.CreateBrowser(context.Background(), WithProfileDir(dir))
	if err == nil {
		t.Fatal("CreateBrowser succeeded for a profile held by a live browser")
	}
	want := `profile "` + dir + `" is already in use by browser-2 (owned by client-a)`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("CreateBrowser error = %q, want it to contain %q", err, want)
	}
	if strings.Contains(err.Error(), "session-a") {
		t.Fatalf("CreateBrowser error = %q, want the owner label, not the raw key", err)
	}
}

func TestBrowserTTLSweep(t *testing.T) {
	const ttl = time.Minute

	setup := func(t *testing.T) *Manager {
		t.Helper()
		return NewManager(WithTTL(0), WithBrowserTTL(ttl))
	}
	register := func(t *testing.T, m *Manager, id BrowserID, dir string, ephemeral bool, idle time.Duration) {
		t.Helper()
		b := NewBrowser(id, m.baseCtx, false, false, "", dir)
		b.owner = "client-a"
		b.ttl = ttl
		b.ephemeral = ephemeral
		b.createdAt = time.Now().Add(-2 * ttl)
		b.lastAccess = time.Now().Add(-idle)
		b.profileCleanup = func() { m.releaseProfile(dir) }
		if err := m.retainProfile(dir, ephemeral, id, "client-a", "client-a"); err != nil {
			t.Fatalf("retainProfile: %v", err)
		}
		m.browsers[id] = b
	}

	t.Run("idle ephemeral browser is closed and purged", func(t *testing.T) {
		m := setup(t)
		dir := filepath.Join(t.TempDir(), "ephemeral")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("create profile dir: %v", err)
		}
		register(t, m, "browser-1", dir, true, 2*ttl)

		m.sweepOnce(context.Background())

		if _, ok := m.browsers["browser-1"]; ok {
			t.Fatal("idle ephemeral browser still registered after sweep")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("ephemeral profile dir still present after sweep: %v", err)
		}
	})

	t.Run("idle persistent browser is closed and its dir kept", func(t *testing.T) {
		m := setup(t)
		dir := filepath.Join(t.TempDir(), "persistent")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("create profile dir: %v", err)
		}
		register(t, m, "browser-2", dir, false, 2*ttl)

		m.sweepOnce(context.Background())

		if _, ok := m.browsers["browser-2"]; ok {
			t.Fatal("idle persistent browser still registered after sweep")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("persistent profile dir removed by sweep: %v", err)
		}
	})

	t.Run("recently used browser is kept", func(t *testing.T) {
		m := setup(t)
		dir := filepath.Join(t.TempDir(), "active")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("create profile dir: %v", err)
		}
		register(t, m, "browser-3", dir, true, 0)

		m.sweepOnce(context.Background())

		if _, ok := m.browsers["browser-3"]; !ok {
			t.Fatal("recently used browser was swept")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("active profile dir removed: %v", err)
		}
	})
}

func TestEffectivePersist(t *testing.T) {
	tests := []struct {
		name           string
		requested      bool
		id             BrowserID
		defaultPersist bool
		want           bool
	}{
		{"ephemeral default keeps default browser ephemeral", false, DefaultBrowserID, false, false},
		{"explicit persist wins on default browser", true, DefaultBrowserID, false, true},
		{"persistent default applies to default browser", false, DefaultBrowserID, true, true},
		{"secondary browser ignores persistent default", false, "browser-2", true, false},
		{"secondary browser can opt into persistence", true, "browser-2", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectivePersist(tt.requested, tt.id, tt.defaultPersist); got != tt.want {
				t.Fatalf("effectivePersist(%t, %q, %t) = %t, want %t",
					tt.requested, tt.id, tt.defaultPersist, got, tt.want)
			}
		})
	}
}

func TestOwnerDefaultResolutionNeverLeaksOtherClients(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	add := func(id BrowserID, owner string) {
		b := NewBrowser(id, m.baseCtx, false, false, "", "")
		b.owner = owner
		m.browsers[id] = b
	}
	add("browser-1", "client-a")
	add("browser-2", "client-b")

	if id, ok := m.ownerDefaultID("client-a", true); !ok || id != "browser-1" {
		t.Fatalf("ownerDefaultID(client-a) = %q, %t; want browser-1", id, ok)
	}
	if id, ok := m.ownerDefaultID("client-c", true); ok {
		t.Fatalf("ownerDefaultID(client-c) resolved %q, want no browser", id)
	}

	add("browser-3", UnownedOwner)
	if id, ok := m.ownerDefaultID("client-c", true); !ok || id != "browser-3" {
		t.Fatalf("ownerDefaultID(client-c) = %q, %t; want the unowned browser-3", id, ok)
	}
	if id, ok := m.ownerDefaultID("client-c", false); ok {
		t.Fatalf("ownerDefaultID(client-c, explicit) resolved %q, want no unowned fallback", id)
	}
}

func TestCreateBrowserOwnerJoinsCreateKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(WithTTL(0), WithBrowserTTL(0), WithPersistentDefault(false))
	m.probe = func(*Browser) error { return nil }
	m.launch = func(context.Context, *Browser) error { return nil }

	a := WithExplicitOwner(context.Background(), "scraper-a")
	b := WithExplicitOwner(context.Background(), "scraper-b")

	firstA, err := m.Create(a)
	if err != nil {
		t.Fatalf("create scraper-a: %v", err)
	}
	if firstA.Reused {
		t.Fatal("first create reported a reuse, want a fresh launch")
	}
	if got := firstA.Browser.Owner(); got != "scraper-a" {
		t.Fatalf("owner = %q, want scraper-a", got)
	}
	if got := firstA.Browser.Info().Owner; got != "scraper-a" {
		t.Fatalf("display owner = %q, want scraper-a", got)
	}

	secondA, err := m.Create(a)
	if err != nil {
		t.Fatalf("recreate scraper-a: %v", err)
	}
	if !secondA.Reused || secondA.ID != firstA.ID {
		t.Fatalf("same owner create = %q reused=%t, want the reused %q", secondA.ID, secondA.Reused, firstA.ID)
	}

	firstB, err := m.Create(b)
	if err != nil {
		t.Fatalf("create scraper-b: %v", err)
	}
	if firstB.ID == firstA.ID {
		t.Fatalf("different owner reused browser %q, want a separate browser", firstA.ID)
	}
	if len(m.browsers) != 2 {
		t.Fatalf("live browsers = %d, want 2", len(m.browsers))
	}
}

func TestOpenBrowserExplicitOwnerDoesNotAdoptUnowned(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(WithTTL(0), WithBrowserTTL(0), WithPersistentDefault(false))
	m.probe = func(*Browser) error { return nil }
	m.launch = func(context.Context, *Browser) error { return nil }

	unowned, err := m.Create(context.Background())
	if err != nil {
		t.Fatalf("create unowned browser: %v", err)
	}

	got, err := m.OpenBrowser(WithExplicitOwner(context.Background(), "scraper-a"), "")
	if err != nil {
		t.Fatalf("OpenBrowser with explicit owner: %v", err)
	}
	if got == unowned.Browser {
		t.Fatal("explicit owner adopted the shared unowned browser")
	}
	if got.Owner() != "scraper-a" {
		t.Fatalf("owner = %q, want scraper-a", got.Owner())
	}
	if len(m.browsers) != 2 {
		t.Fatalf("live browsers = %d, want the unowned browser plus a new one", len(m.browsers))
	}
}

func TestProfileDirGuardRefusesOutsideProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles, err := config.ProfilesDir()
	if err != nil {
		t.Fatalf("ProfilesDir: %v", err)
	}

	inside := filepath.Join(profiles, "audit")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatalf("create profile dir: %v", err)
	}
	if got, err := profileDirWithinProfiles(inside); err != nil || got != inside {
		t.Fatalf("profileDirWithinProfiles(inside) = %q, %v; want %q", got, err, inside)
	}

	if _, err := profileDirWithinProfiles(profiles); err == nil {
		t.Fatal("profileDirWithinProfiles(profiles root) succeeded, want refusal")
	}
	outside := t.TempDir()
	_, err = profileDirWithinProfiles(outside)
	if err == nil || !strings.Contains(err.Error(), "outside the profiles directory") {
		t.Fatalf("profileDirWithinProfiles(outside) error = %v, want an outside-directory refusal", err)
	}
}

func TestPurgeProfileRefusesOutsideDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	outside := t.TempDir()
	m.browsers["browser-1"] = NewBrowser("browser-1", m.baseCtx, false, false, "", outside)

	err := m.PurgeProfile(context.Background(), "browser-1")
	if err == nil || !strings.Contains(err.Error(), "outside the profiles directory") {
		t.Fatalf("PurgeProfile(outside) error = %v, want an outside-directory refusal", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("PurgeProfile deleted a directory outside the profiles dir: %v", statErr)
	}
}

func TestPurgeProfileDeletesUnderProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles, err := config.ProfilesDir()
	if err != nil {
		t.Fatalf("ProfilesDir: %v", err)
	}
	dir := filepath.Join(profiles, "audit")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create profile dir: %v", err)
	}

	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	m.browsers["browser-2"] = NewBrowser("browser-2", m.baseCtx, false, false, "", dir)

	if err := m.PurgeProfile(context.Background(), "browser-2"); err != nil {
		t.Fatalf("PurgeProfile: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("profile dir still present after purge: %v", err)
	}
}

func TestPruneProfilesLeavesNamedProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles, err := config.ProfilesDir()
	if err != nil {
		t.Fatalf("ProfilesDir: %v", err)
	}
	named := filepath.Join(profiles, "audit")
	if err := os.MkdirAll(named, 0700); err != nil {
		t.Fatalf("create named profile: %v", err)
	}
	ephemeralRoot := filepath.Join(profiles, ".ephemeral")
	orphan := filepath.Join(ephemeralRoot, "browser-9-123")
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatalf("create orphan ephemeral profile: %v", err)
	}

	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	removed, err := m.PruneProfiles(context.Background())
	if err != nil {
		t.Fatalf("PruneProfiles: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan ephemeral profile still present: %v", err)
	}
	if _, err := os.Stat(named); err != nil {
		t.Fatalf("prune removed the named persistent profile: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("PruneProfiles removed %d dirs, want 1", len(removed))
	}
}

// TestSessionsListsEveryIdentityWithoutLaunch pins that one read enumerates the
// pages of the parent and every identity, and that listing creates nothing.
func TestSessionsListsEveryIdentityWithoutLaunch(t *testing.T) {
	m := NewManager(WithTTL(0))
	launches := 0
	m.launch = func(context.Context, *Browser) error {
		launches++
		return nil
	}

	root := identityTestBrowser(t)
	root.pages["page-1"] = &Page{ID: "page-1", URL: "https://app.test/orders/1"}
	victim, err := root.NewIdentity("victim")
	if err != nil {
		t.Fatalf("NewIdentity(victim): %v", err)
	}
	victim.pages["page-2"] = &Page{ID: "page-2", URL: "https://app.test/orders/2"}
	m.browsers["browser-1"] = root

	before := len(m.Browsers())
	pages, err := m.Sessions("")
	if err != nil {
		t.Fatalf("Sessions(): %v", err)
	}
	if after := len(m.Browsers()); after != before {
		t.Fatalf("browsers after listing = %d, want %d", after, before)
	}
	if launches != 0 {
		t.Fatalf("listing launched %d browser(s), want 0", launches)
	}

	want := map[PageID]string{"page-1": "default", "page-2": "victim"}
	if len(pages) != len(want) {
		t.Fatalf("Sessions() = %v, want %d pages", pages, len(want))
	}
	for _, p := range pages {
		if p.Context != want[p.ID] {
			t.Errorf("page %s context = %q, want %q", p.ID, p.Context, want[p.ID])
		}
	}
}

// TestSessionsResolvesIdentityAcrossBrowsers pins that both the filter and its
// unknown-name error span every live browser, not just one root.
func TestSessionsResolvesIdentityAcrossBrowsers(t *testing.T) {
	m := NewManager(WithTTL(0))

	first := identityTestBrowser(t)
	first.pages["page-1"] = &Page{ID: "page-1", URL: "https://app.test/default"}
	if _, err := first.NewIdentity("victim"); err != nil {
		t.Fatalf("NewIdentity(victim): %v", err)
	}
	m.browsers["browser-1"] = first

	second := NewBrowser("browser-2", m.baseCtx, false, false, "", "")
	second.rod = &rod.Browser{}
	second.incognito = func(*rod.Browser) (*rod.Browser, error) {
		return &rod.Browser{}, nil
	}
	attacker, err := second.NewIdentity("attacker")
	if err != nil {
		t.Fatalf("NewIdentity(attacker): %v", err)
	}
	attacker.pages["page-2"] = &Page{ID: "page-2", URL: "https://app.test/orders/1"}
	m.browsers["browser-2"] = second

	pages, err := m.Sessions("attacker")
	if err != nil {
		t.Fatalf("Sessions(attacker): %v", err)
	}
	if len(pages) != 1 || pages[0].ID != "page-2" || pages[0].Context != "attacker" {
		t.Fatalf("Sessions(attacker) = %v, want page-2 in attacker", pages)
	}

	_, err = m.Sessions("ghost")
	if err == nil {
		t.Fatal("Sessions(ghost) succeeded, want the live names")
	}
	want := `unknown identity "ghost" (known: attacker, default, victim)`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Sessions(ghost) error = %q, want it to contain %q", err, want)
	}
}

// TestManagerClosePageUsesOwningBrowser is the WEB-166 regression: a listed
// page is closed through the browser that owns it, not through whichever
// browser a default open happens to return.
func TestManagerClosePageUsesOwningBrowser(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	first := NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	second := NewBrowser("browser-2", m.baseCtx, false, false, "", "")
	p := &Page{ID: "page-2", URL: "https://example.com/"}
	second.pages[p.ID] = p
	m.browsers[first.id] = first
	m.browsers[second.id] = second

	closed, err := m.ClosePage(context.Background(), p.ID, "browser-2", "")
	if err != nil {
		t.Fatalf("ClosePage(%q): %v", p.ID, err)
	}
	if closed.Browser != second.id {
		t.Fatalf("ClosePage closed via %q, want the owning browser %q", closed.Browser, second.id)
	}
	if closed.Evicted {
		t.Fatal("ClosePage evicted an idle page, want the tab released")
	}
	if _, ok := second.pages[p.ID]; ok {
		t.Fatalf("ClosePage left %q registered in %s", p.ID, second.id)
	}
	if len(first.pages) != 0 {
		t.Fatalf("ClosePage touched %s, which owned no page", first.id)
	}
}

// TestSessionOwnerPreservedAcrossExplicitOwner pins the WEB-166 key contract:
// an explicit owner label replaces the owner key, but the ambient surface
// identity survives so a browser created under the label stays addressable by
// the session that created it.
func TestSessionOwnerPreservedAcrossExplicitOwner(t *testing.T) {
	session := WithOwnerKey(context.Background(), "sse-1")
	explicit := WithExplicitOwner(session, "scraper-a")
	if got := OwnerFrom(explicit); got != "scraper-a" {
		t.Fatalf("OwnerFrom = %q, want the explicit label", got)
	}
	if got := SessionOwnerFrom(explicit); got != "sse-1" {
		t.Fatalf("SessionOwnerFrom = %q, want the preserved session key", got)
	}
	if got := SessionOwnerFrom(context.Background()); got != UnownedOwner {
		t.Fatalf("SessionOwnerFrom(background) = %q, want %q", got, UnownedOwner)
	}
}

// TestManagerClosePageClosesListedPageWithHangingInfo is the WEB-166
// regression for the live failure: the tab read never returns, session_list
// still reports page-5, and session_close closes exactly that id even though the
// owning browser was created under an explicit owner label rather than the
// caller's session key.
func TestManagerClosePageClosesListedPageWithHangingInfo(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	b := NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	b.owner = "sub-tester"
	b.sessionOwner = "sse-session-1"
	p := &Page{ID: "page-5", URL: "https://www.rfc-editor.org/"}
	p.info = func(ctx context.Context) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	b.pages[p.ID] = p
	m.browsers[b.id] = b

	listed, err := m.Sessions("")
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != p.ID {
		t.Fatalf("Sessions = %+v, want the listed %q", listed, p.ID)
	}
	if !listed[0].Stale {
		t.Fatal("Sessions reported a live read for a tab whose info never returns")
	}

	ctx := WithOwnerKey(context.Background(), "sse-session-1")
	closed, err := m.ClosePage(ctx, listed[0].ID, "", "")
	if err != nil {
		t.Fatalf("ClosePage(%q) from the listing: %v", listed[0].ID, err)
	}
	if closed.Browser != b.id {
		t.Fatalf("ClosePage closed via %q, want the owning %q", closed.Browser, b.id)
	}
	b.mu.Lock()
	_, registered := b.pages[p.ID]
	b.mu.Unlock()
	if registered {
		t.Fatalf("ClosePage left the listed %q registered", p.ID)
	}
}

// TestManagerClosePageEvictsStuckPage pins that a page whose operation lock is
// held (a timed-out navigate) still closes: the close uses the try-lock path,
// waits only its bounded budget, removes the session from the registry, and
// never wedges the caller.
func TestManagerClosePageEvictsStuckPage(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	b := NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	p := &Page{ID: "page-2"}
	b.pages[p.ID] = p
	m.browsers[b.id] = b
	p.Lock()
	defer p.Unlock()

	start := time.Now()
	closed, err := m.ClosePage(context.Background(), p.ID, "", "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ClosePage(%q) on a stuck page: %v", p.ID, err)
	}
	if !closed.Evicted {
		t.Fatal("ClosePage reported a released tab for a page held by an operation")
	}
	if elapsed > closePageLockWait+time.Second {
		t.Fatalf("ClosePage waited %s on a stuck page, want it bounded near %s", elapsed, closePageLockWait)
	}
	b.mu.Lock()
	_, registered := b.pages[p.ID]
	b.mu.Unlock()
	if registered {
		t.Fatalf("ClosePage left the stuck page %q registered", p.ID)
	}
}

// TestManagerClosePageUnknownNamesLivePages pins the failure contract: an
// unknown id still fails and names every live page id.
func TestManagerClosePageUnknownNamesLivePages(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	b := NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	b.pages["page-1"] = &Page{ID: "page-1"}
	b.pages["page-2"] = &Page{ID: "page-2"}
	m.browsers[b.id] = b

	_, err := m.ClosePage(context.Background(), "page-99", "", "")
	if err == nil {
		t.Fatal("ClosePage(page-99) succeeded, want the live ids named")
	}
	for _, want := range []string{`session "page-99" not found`, "live: page-1, page-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ClosePage error = %q, want it to contain %q", err, want)
		}
	}
	if len(b.pages) != 2 {
		t.Fatalf("ClosePage(page-99) removed live pages: %d remain", len(b.pages))
	}
}

// TestManagerClosePageNeverTouchesAnotherOwner pins declarative isolation: a
// page id that belongs to another owner's browser is refused, never closed.
func TestManagerClosePageNeverTouchesAnotherOwner(t *testing.T) {
	m := NewManager(WithTTL(0), WithBrowserTTL(0))
	mine := NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	mine.owner = "agent-a"
	theirs := NewBrowser("browser-2", m.baseCtx, false, false, "", "")
	theirs.owner = "agent-b"
	p := &Page{ID: "page-2"}
	theirs.pages[p.ID] = p
	m.browsers[mine.id] = mine
	m.browsers[theirs.id] = theirs

	if _, err := m.ClosePage(context.Background(), p.ID, "", "agent-a"); err == nil {
		t.Fatal("ClosePage closed a page in another owner's browser")
	}
	if _, ok := theirs.pages[p.ID]; !ok {
		t.Fatal("ClosePage removed another owner's page")
	}
}

// newOwnerPair builds two live browsers owned by task-a and task-b. browser-1
// holds page-1; browser-2 holds a colliding page-1 plus page-2, which only
// task-b owns.
func newOwnerPair(t *testing.T) (m *Manager, alice, bob *Browser) {
	t.Helper()
	m = NewManager(WithTTL(0), WithBrowserTTL(0))
	alice = NewBrowser(DefaultBrowserID, m.baseCtx, false, false, "", "")
	alice.owner = "task-a"
	alice.ownerLabel = "task-a"
	alice.pages["page-1"] = &Page{ID: "page-1", URL: "https://app.test/alice"}
	bob = NewBrowser("browser-2", m.baseCtx, false, false, "", "")
	bob.owner = "task-b"
	bob.ownerLabel = "task-b"
	bob.pages["page-1"] = &Page{ID: "page-1", URL: "https://app.test/bob"}
	bob.pages["page-2"] = &Page{ID: "page-2", URL: "https://app.test/bob-only"}
	m.browsers[alice.id] = alice
	m.browsers[bob.id] = bob
	return m, alice, bob
}

// TestResolvePageOwnerIsolation pins the declarative qualifier rules: an owner
// sees only its own pages, a foreign id is not found with the holder named and
// no content, and a browser_id crosses owners on purpose.
func TestResolvePageOwnerIsolation(t *testing.T) {
	t.Run("page inside the declared owner resolves", func(t *testing.T) {
		m, alice, _ := newOwnerPair(t)
		got, err := m.resolvePage("page-1", "", "task-a")
		if err != nil {
			t.Fatalf("resolvePage(page-1, owner task-a): %v", err)
		}
		if got != alice {
			t.Fatalf("resolvePage(page-1, owner task-a) = %s, want %s", got.id, alice.id)
		}
	})

	t.Run("unique foreign id is not found and names the holder", func(t *testing.T) {
		m, _, bob := newOwnerPair(t)
		_, err := m.resolvePage("page-2", "", "task-a")
		if err == nil {
			t.Fatal("resolvePage(page-2, owner task-a) returned a page, want not found")
		}
		want := `session "page-2" is not in owner task-a — held by browser-2 (owner task-b); pass browser_id to reach it on purpose`
		if err.Error() != want {
			t.Fatalf("resolvePage error = %q, want %q", err, want)
		}
		var lookup *PageLookupError
		if !errors.As(err, &lookup) || lookup.Kind != PageLookupForeign {
			t.Fatalf("resolvePage error kind = %v, want PageLookupForeign", err)
		}
		if strings.Contains(err.Error(), "https://") {
			t.Fatalf("not-found error leaked page content: %q", err)
		}
		if _, _, ok := bob.lookupPage("page-2"); !ok {
			t.Fatal("foreign lookup removed bob's page")
		}
	})

	t.Run("browser_id alone crosses owners", func(t *testing.T) {
		m, _, bob := newOwnerPair(t)
		got, err := m.resolvePage("page-2", "browser-2", "")
		if err != nil {
			t.Fatalf("resolvePage(page-2, browser-2): %v", err)
		}
		if got != bob {
			t.Fatalf("resolvePage(page-2, browser-2) = %s, want %s", got.id, bob.id)
		}
	})

	t.Run("unknown owner reports the live browsers", func(t *testing.T) {
		m, _, _ := newOwnerPair(t)
		_, err := m.resolvePage("page-1", "", "task-z")
		var lookup *PageLookupError
		if !errors.As(err, &lookup) || lookup.Kind != PageLookupForeign {
			t.Fatalf("resolvePage(page-1, owner task-z) = %v, want PageLookupForeign", err)
		}
		for _, want := range []string{"browser-1 (owner task-a)", "browser-2 (owner task-b)"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("an unqualified caller is not a wildcard", func(t *testing.T) {
		m, _, bob := newOwnerPair(t)
		_, err := m.resolvePage("page-2", "", "")
		var lookup *PageLookupError
		if !errors.As(err, &lookup) || lookup.Kind != PageLookupAmbiguous {
			t.Fatalf("resolvePage(page-2) = %v, want PageLookupAmbiguous", err)
		}
		if _, _, ok := bob.lookupPage("page-2"); !ok {
			t.Fatal("an unqualified lookup removed bob's page")
		}
	})
}

// TestResolvePageRequiresABrowserWithTwoOwners pins the ambiguity refusal: two
// live browsers and no qualifier name both browsers and demand a choice.
func TestResolvePageRequiresABrowserWithTwoOwners(t *testing.T) {
	m, _, _ := newOwnerPair(t)
	_, err := m.Page(context.Background(), "page-3", "", "")
	if err == nil {
		t.Fatal("Page(page-3) with two live browsers returned a page, want refusal")
	}
	want := `session "page-3" needs a browser — 2 browsers are live: browser-1 (owner task-a), browser-2 (owner task-b); pass browser_id or owner`
	if err.Error() != want {
		t.Fatalf("Page(page-3) error = %q, want %q", err, want)
	}
	var lookup *PageLookupError
	if !errors.As(err, &lookup) || lookup.Kind != PageLookupAmbiguous {
		t.Fatalf("Page(page-3) error kind = %v, want PageLookupAmbiguous", err)
	}

	if _, err := m.ClosePage(context.Background(), "page-3", "", ""); err == nil {
		t.Fatal("ClosePage(page-3) with two live browsers succeeded, want refusal")
	}
}

// TestResolvePageSingleBrowser pins that the historic single-browser path needs
// no qualifier.
func TestResolvePageSingleBrowser(t *testing.T) {
	m, alice, _ := newOwnerPair(t)
	delete(m.browsers, "browser-2")
	got, err := m.resolvePage("page-1", "", "")
	if err != nil {
		t.Fatalf("resolvePage(page-1) with one browser: %v", err)
	}
	if got != alice {
		t.Fatalf("resolvePage(page-1) = %s, want %s", got.id, alice.id)
	}
}
