package browsers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dakolli/webmcp/pkg/config"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// DefaultBinPath detects the default browser binary on PATH:
// it prefers Brave ("brave-browser", "brave"), then Google Chrome / Chromium.
// Returns empty string if no known browser binary is found on PATH.
func DefaultBinPath() string {
	for _, name := range []string{"brave-browser", "brave", "google-chrome", "chrome", "chromium"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// DefaultIdentity names the parent browser's own cookie jar. A page opened
// without an identity lives here; incognito identities are separate contexts
// inside the same Chromium process.
const DefaultIdentity = "default"

// Browser owns a single live Chromium instance (a *rod.Browser) and the map of
// its live Pages (browser tabs, a.k.a. MCP sessions).
//
// An identity clone is a Browser backed by a rod incognito context: it shares
// the parent's Chromium process and launch options, but owns its own pages and
// cookie jar. The parent's identities map is the only registry; a clone points
// back at its parent and dies with it.
//
// Thread safety: the pages map, the identities map, the idle clock and the rod
// handle are guarded by the Browser mutex; each Page carries its own mutex
// because rod Pages are not thread-safe (see session.go).
type Browser struct {
	// id is the unique BrowserID assigned by Manager (e.g. "browser-1").
	id BrowserID
	// mu guards the pages map, the rod browser pointer, the profile directory
	// and the idle clock.
	mu sync.Mutex
	// rod is the live go-rod browser handle; nil until Launch succeeds.
	rod *rod.Browser
	// pages maps page IDs to their live pages.
	pages map[PageID]*Page
	// popups tracks the targets this browser wrapped (normal pages and popups,
	// keyed by CDP target id) and the waiters blocked on WaitOpen. Only the
	// root browser uses it; a clone's tracker stays zero. Guarded by the root's
	// mu.
	popups popupTracker
	// identities maps identity names to live incognito clones. Only a root
	// browser owns a non-nil registry; a clone's map is nil. Guarded by the
	// root's mu and cleared by its Close.
	identities map[string]*Browser
	// identity is the incognito name this Browser represents ("default" for the
	// root). Immutable.
	identity string
	// parent points at the root browser for a clone; nil for a root browser.
	// Immutable.
	parent *Browser
	// incognito creates an incognito clone from a rod browser. It is a field so
	// tests can stub the CDP round trip; nil uses rod's Browser.Incognito.
	incognito func(*rod.Browser) (*rod.Browser, error)
	// nextID is the counter used to mint fresh PageIDs in NewPage. A clone
	// mints from its parent's counter so ids stay unique across identities.
	nextID int
	// owner is the caller identity recorded at creation (UnownedOwner when the
	// surface had none). Informational only: browsers are closed by id.
	owner string
	// sessionOwner is the surface identity that created the browser, preserved
	// even when an explicit owner label replaced the owner key. It lets a later
	// call from the same surface address the browser without treating the label
	// as the lookup key. Immutable.
	sessionOwner string
	// ownerLabel is the human-friendly identity shown in listings and errors
	// (e.g. an MCP client name); owner stays the ownership key. Immutable.
	ownerLabel string
	// key is the canonical launch-request identity used for create idempotency.
	// Immutable after creation.
	key launchKey
	// createdAt is when the Browser was constructed. Immutable.
	createdAt time.Time
	// lastAccess is the last time the browser was used (page created, page
	// closed, page or browser looked up). The manager's sweeper closes browsers
	// idle for longer than the browser TTL. Guarded by mu.
	lastAccess time.Time
	// ttl is the browser idle TTL reported in Info (0 means sweeping is
	// disabled). Set by Manager, immutable afterwards.
	ttl time.Duration
	// headless selects headless Chromium at launch time (default: headful).
	headless bool
	// stealth applies go-rod/stealth to every new page (default: on).
	stealth bool
	// chromeMajor is the major version of the launched Chromium build, probed
	// once at launch and reused for the default User-Agent and client hints. It
	// falls back to fallbackChromeMajor when the probe fails. Guarded by mu.
	chromeMajor int
	// binPath specifies a custom browser executable path.
	binPath string
	// profileDir specifies a custom browser user data profile directory path.
	profileDir string
	// ephemeral marks a profile owned by this browser and removed on close.
	ephemeral bool
	// profileCleanup releases the profile ownership recorded by Manager. It is
	// nil for persistent profiles.
	profileCleanup func()
	// cleanupOnce guarantees the profile cleanup runs at most once.
	cleanupOnce sync.Once
	// position specifies initial window placement ("left", "right", "max", "normal").
	position string
	// proxy is the validated upstream proxy (host:port or scheme://host:port);
	// empty leaves the browser on a direct connection.
	proxy string
	// proxyBypass is the comma-separated proxy bypass list; empty keeps
	// Chromium's default bypass behavior.
	proxyBypass string
	// ignoreCerts makes Chromium accept invalid TLS certificates (e.g. the
	// MITM CA of an intercept proxy).
	ignoreCerts bool
	// chromiumFlags are the extra Chromium command-line switches applied at
	// launch. Immutable after creation; copied for identity clones.
	chromiumFlags []string
	// extraHeaders are sent with every request from every page of this browser,
	// first navigation included. Immutable after creation.
	extraHeaders map[string]string
	// scope is the navigation allowlist. Empty means unrestricted unless
	// scopeStrict is set.
	scope []string
	// scopeStrict makes an empty allowlist deny every navigation.
	scopeStrict bool
	// limits is the per-host token bucket shared by every page of this browser
	// and its incognito clones. It is immutable after creation.
	limits *hostLimiter
	// headerCleanups holds rod's per-page header cleanup, keyed by the rod page
	// handle. Guarded by mu; entries are removed on every close path.
	headerCleanups map[*rod.Page]func()
	// proxyAuth holds the proxy.json credentials for this browser; nil when no
	// proxy is configured. Set once at launch, immutable afterwards.
	proxyAuth *proxyAuth
	// authPages maps each live rod page to its armed proxy-auth handler. Guarded
	// by mu; entries are added in the pre-registration window and removed on
	// every close path.
	authPages map[*rod.Page]*proxyAuthPage
	// logger records best-effort background failures (proxy auth). Defaults to
	// a no-op logger; the manager injects its own.
	logger *slog.Logger
	// baseCtx is the manager-owned, process-lifetime context the rod browser's
	// default ctx is bound to (WEB-59): a transient tool-call ctx can never
	// poison it, so canceling a request aborts a call but never the browser
	// itself. Every engine rod call chains its own per-call ctx via
	// .Context(callCtx); derived timeouts (closes, orphan cleanup) hang off
	// this base so teardown outlives the request that triggered it.
	baseCtx context.Context
	// human is the browser's randomized timing source, seeded once at
	// construction and shared by every page and identity clone so two pages
	// never draw the same delay or pointer path in lockstep. Immutable.
	human *humanizer
	// rawActions disables humanized pointer and keyboard timing for this
	// browser (the manager's raw option). Set before launch; immutable.
	rawActions bool
}

// NewBrowser constructs a Browser ready to be launched. id is the unique BrowserID;
// baseCtx is the manager-owned, process-lifetime context the rod browser's default ctx will
// be bound to at Launch (never a transient tool-call ctx — see WEB-59);
// headless selects headless Chromium (headful is the default); stealth enables
// go-rod/stealth on every new page; binPath specifies a custom browser executable path; profileDir specifies a custom profile directory. No browser process is started until Launch.
func NewBrowser(id BrowserID, baseCtx context.Context, headless, stealth bool, binPath, profileDir string, position ...string) *Browser {
	pos := ""
	if len(position) > 0 {
		pos = position[0]
	}
	return &Browser{
		id:             id,
		pages:          make(map[PageID]*Page),
		identities:     make(map[string]*Browser),
		authPages:      make(map[*rod.Page]*proxyAuthPage),
		headerCleanups: make(map[*rod.Page]func()),
		headless:       headless,
		stealth:        stealth,
		chromeMajor:    fallbackChromeMajor,
		binPath:        binPath,
		profileDir:     profileDir,
		position:       pos,
		createdAt:      time.Now(),
		lastAccess:     time.Now(),
		limits:         newHostLimiter(RateLimit{RPS: DefaultRateRPS, Burst: DefaultRateBurst}),
		logger:         slog.New(slog.DiscardHandler),
		baseCtx:        baseCtx,
		human:          newHumanizer(),
	}
}

// Position returns the Browser's window position preset ("left", "right", "max", "normal").
func (b *Browser) Position() string {
	return b.position
}

// ID returns the Browser's unique BrowserID (e.g. "browser-1").
func (b *Browser) ID() BrowserID {
	return b.id
}

// Owner returns the owner key recorded when the Browser was created
// (UnownedOwner when the surface had no client identity).
func (b *Browser) Owner() string {
	if b.owner == "" {
		return UnownedOwner
	}
	return b.owner
}

// OwnerLabel returns the human-friendly owner label for listings and errors,
// falling back to the owner key when no label was recorded.
func (b *Browser) OwnerLabel() string {
	return ownerDisplay(b.ownerLabel, b.Owner())
}

// touch records that the browser was just used, resetting its idle clock so the
// manager's sweeper does not close it.
func (b *Browser) touch() {
	b.mu.Lock()
	b.lastAccess = time.Now()
	b.mu.Unlock()
}

// idleFor returns how long the browser has gone unused.
func (b *Browser) idleFor() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Since(b.lastAccess)
}

// root returns the owning root browser: itself for a parent, its parent for an
// incognito clone.
func (b *Browser) root() *Browser {
	if b.parent != nil {
		return b.parent
	}
	return b
}

// IdentityName returns the incognito identity this browser represents, or
// DefaultIdentity for the parent browser.
func (b *Browser) IdentityName() string {
	if b.identity == "" {
		return DefaultIdentity
	}
	return b.identity
}

// NewIdentity returns the live incognito clone named name, creating it on first
// use. A clone shares this browser's Chromium process and launch options but
// has its own cookie jar and storage; closing the parent disposes every clone.
// An already-live name is returned as-is, so repeated calls reuse one context.
func (b *Browser) NewIdentity(name string) (*Browser, error) {
	name = strings.TrimSpace(name)
	if err := validateIdentityName(name); err != nil {
		return nil, err
	}
	if name == DefaultIdentity {
		return b.root(), nil
	}

	root := b.root()
	root.mu.Lock()
	if existing, ok := root.identities[name]; ok {
		root.mu.Unlock()
		return existing, nil
	}
	rb := root.rod
	incognito := root.incognito
	root.mu.Unlock()
	if rb == nil {
		return nil, fmt.Errorf("browsers: create identity %q: browser not launched", name)
	}
	if incognito == nil {
		incognito = func(rb *rod.Browser) (*rod.Browser, error) { return rb.Incognito() }
	}

	clone, err := incognito(rb)
	if err != nil {
		return nil, fmt.Errorf("browsers: create identity %q: %w", name, err)
	}
	child := root.newIdentity(name, clone)

	root.mu.Lock()
	if existing, ok := root.identities[name]; ok {
		root.mu.Unlock()
		_ = clone.Context(root.baseCtx).Close()
		return existing, nil
	}
	if root.identities == nil {
		root.identities = make(map[string]*Browser)
	}
	root.identities[name] = child
	root.mu.Unlock()
	return child, nil
}

// Identity resolves an existing identity without creating one: DefaultIdentity
// (or an empty name) is the parent, a live name is its clone, and an unknown
// name fails with the live names so a typo cannot silently fork a session.
func (b *Browser) Identity(name string) (*Browser, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == DefaultIdentity {
		return b.root(), nil
	}

	root := b.root()
	root.mu.Lock()
	child, ok := root.identities[name]
	known := root.identityNamesLocked()
	root.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("browsers: unknown identity %q (known: %s)", name, strings.Join(known, ", "))
	}
	return child, nil
}

// identityNamesLocked returns the sorted live identity names, including
// DefaultIdentity. The caller must hold the root browser's mu.
func (b *Browser) identityNamesLocked() []string {
	names := make([]string, 0, len(b.identities)+1)
	names = append(names, DefaultIdentity)
	for name := range b.identities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateIdentityName reports whether name is an accepted identity label (1
// to 64 characters from A-Z, a-z, 0-9, '.', '_' and '-'); DefaultIdentity is
// accepted.
func ValidateIdentityName(name string) error {
	return validateIdentityName(strings.TrimSpace(name))
}

// validateIdentityName applies the accepted identity-label shape: 1 to 64
// characters from A-Z, a-z, 0-9, '.', '_' and '-'.
func validateIdentityName(name string) error {
	if name == "" {
		return fmt.Errorf("browsers: identity name is required — pass a label such as \"victim\"")
	}
	if len(name) > 64 {
		return fmt.Errorf("browsers: invalid identity %q — use 1 to 64 characters from A-Z, a-z, 0-9, '.', '_' and '-' only", name)
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("browsers: invalid identity %q — use 1 to 64 characters from A-Z, a-z, 0-9, '.', '_' and '-' only", name)
		}
	}
	return nil
}

// newIdentity builds the engine Browser for one incognito rod clone, copying
// the parent's launch options and sharing its process-lifetime context.
func (b *Browser) newIdentity(name string, clone *rod.Browser) *Browser {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &Browser{
		id:             b.id,
		rod:            clone,
		pages:          make(map[PageID]*Page),
		identity:       name,
		parent:         b,
		owner:          b.owner,
		sessionOwner:   b.sessionOwner,
		ownerLabel:     b.ownerLabel,
		key:            b.key,
		createdAt:      b.createdAt,
		lastAccess:     time.Now(),
		ttl:            b.ttl,
		headless:       b.headless,
		stealth:        b.stealth,
		chromeMajor:    b.chromeMajor,
		binPath:        b.binPath,
		profileDir:     b.profileDir,
		position:       b.position,
		proxy:          b.proxy,
		proxyBypass:    b.proxyBypass,
		ignoreCerts:    b.ignoreCerts,
		chromiumFlags:  append([]string(nil), b.chromiumFlags...),
		extraHeaders:   cloneHeaders(b.extraHeaders),
		scope:          append([]string(nil), b.scope...),
		scopeStrict:    b.scopeStrict,
		limits:         b.limits,
		headerCleanups: make(map[*rod.Page]func()),
		proxyAuth:      b.proxyAuth,
		authPages:      make(map[*rod.Page]*proxyAuthPage),
		logger:         b.logger,
		baseCtx:        b.baseCtx,
		human:          b.human,
		rawActions:     b.rawActions,
	}
}

// mintPageID allocates the next page id from the root counter, so ids stay
// unique across the parent and every identity.
func (b *Browser) mintPageID() PageID {
	root := b.root()
	root.mu.Lock()
	defer root.mu.Unlock()
	root.nextID++
	return PageID(fmt.Sprintf("page-%d", root.nextID))
}

// lookupPage returns the browser whose pages map holds id and that page. The
// root owns the identity registry, so a page opened inside a clone is still
// addressable through the parent.
func (b *Browser) lookupPage(id PageID) (*Browser, *Page, bool) {
	b.mu.Lock()
	if p, ok := b.pages[id]; ok {
		b.mu.Unlock()
		return b, p, true
	}
	identities := make([]*Browser, 0, len(b.identities))
	for _, child := range b.identities {
		identities = append(identities, child)
	}
	b.mu.Unlock()

	for _, child := range identities {
		child.mu.Lock()
		p, ok := child.pages[id]
		child.mu.Unlock()
		if ok {
			return child, p, true
		}
	}
	return nil, nil, false
}

// BrowserInfo is a JSON-ready snapshot of one live browser's lifecycle state.
// It excludes the live rod handle and is safe to serialize from the MCP, CLI
// and IPC surfaces.
type BrowserInfo struct {
	// BrowserID is the live browser id (e.g. "browser-1").
	BrowserID BrowserID `json:"browser_id"`
	// Owner is the caller's display label recorded at creation, or UnownedOwner
	// when the surface supplied none. It never exposes the raw owner key.
	Owner string `json:"owner"`
	// Bin is the configured browser executable (empty means the default).
	Bin string `json:"bin,omitempty"`
	// ProfileDir is the resolved profile directory.
	ProfileDir string `json:"profile_dir"`
	// Persist reports whether the profile directory survives close.
	Persist bool `json:"persist"`
	// Ephemeral reports whether the profile directory is deleted on close.
	Ephemeral bool `json:"ephemeral"`
	// CreatedAt is when the browser was created.
	CreatedAt time.Time `json:"created_at"`
	// IdleFor is how long the browser has gone unused, rounded to seconds.
	IdleFor string `json:"idle_for"`
	// TTL is the browser idle timeout that closes it ("0s" when disabled).
	TTL string `json:"ttl"`
	// Scope is the navigation allowlist: "open" when unrestricted, "strict" for
	// a strict empty allowlist, else the patterns joined by ", ".
	Scope string `json:"scope"`
	// ScopeStrict reports whether an empty allowlist denies every navigation.
	ScopeStrict bool `json:"scope_strict"`
	// Limits is the effective per-host request policy and host state.
	Limits LimitReport `json:"limits"`
	// PageCount is the number of live pages.
	PageCount int `json:"page_count"`
	// CreateKey summarizes the launch options this browser was created with.
	CreateKey string `json:"create_key"`
}

// Info returns a JSON-ready snapshot of the Browser's lifecycle state.
func (b *Browser) Info() BrowserInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := b.ownerLabel
	if owner == "" {
		owner = UnownedOwner
	}
	pageCount := len(b.pages)
	for _, child := range b.identities {
		child.mu.Lock()
		pageCount += len(child.pages)
		child.mu.Unlock()
	}
	return BrowserInfo{
		BrowserID:   b.id,
		Owner:       owner,
		Bin:         b.binPath,
		ProfileDir:  b.profileDir,
		Persist:     !b.ephemeral,
		Ephemeral:   b.ephemeral,
		CreatedAt:   b.createdAt,
		IdleFor:     time.Since(b.lastAccess).Round(time.Second).String(),
		TTL:         b.ttl.String(),
		Scope:       scopeState(b.scope, b.scopeStrict),
		ScopeStrict: b.scopeStrict,
		Limits:      b.limits.report(),
		PageCount:   pageCount,
		CreateKey:   b.key.Summary(),
	}
}

// BinPath returns the Browser's configured executable path.
func (b *Browser) BinPath() string {
	return b.binPath
}

// ProfileDir returns the Browser's configured profile directory path.
func (b *Browser) ProfileDir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.profileDir
}

// IsAlive checks whether the Browser has been launched and its rod connection is active.
func (b *Browser) IsAlive(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rod != nil
}

// Launch connects the Browser to a Chromium instance via the go-rod launcher.
// Chromium runs headful by default (`.Headless(false)`) unless the Browser was
// constructed with headless=true; NoSandbox is always set, as required in
// containers. Launch is idempotent: it is a no-op once the browser is up.
//
// Launch installs the coherent realism defaults: the AutomationControlled
// feature is disabled, the browser locale is en-US, and the launched
// Chromium's major version is probed ONCE and cached for the per-page default
// User-Agent and client hints. A failed version probe degrades to
// fallbackChromeMajor and never fails the launch. The Accept-Language header is
// owned entirely by the per-page User-Agent override; no launcher flag writes
// it, so the two sources can never combine into a duplicated quality value.
//
// The rod browser's DEFAULT ctx is bound to the manager-owned baseCtx the
// Browser was constructed with — never to the caller's ctx — so a transient
// tool-call ctx cannot poison the browser (WEB-58: a poisoned default ctx
// leaked one ~100 MB renderer per fetch_page and made browser_close report
// "context canceled"). Every engine rod call afterwards chains its own
// per-call ctx via .Context(callCtx) instead. The launch-time operations
// (first-run Chromium download, early bail) stay bounded by the caller's ctx.
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

	// Pages launch PLAIN by default (per user decision 2026-08-14): no
	// window-size / device-scale / emulation launcher flags here. Screen-size
	// emulation is page-scoped and opt-in — a caller passes an explicit
	// viewport to NewPage, or applies a go-rod devices.X preset via
	// Page.Emulate after creation. The launcher only carries the browser
	// locale; the User-Agent, Accept-Language and their client hints are
	// applied per page before navigation, so the header has a single writer.
	// The launcher is ctx-bound (l.Context) so a hung first-run Chromium
	// download honors the caller's deadline instead of running on a background
	// ctx.
	l := launcher.New().Headless(b.headless).Context(ctx)   // headful by default
	l.Delete("enable-automation")                           // remove "controlled by automated test software" infobar & webdriver flag
	l.Set("disable-blink-features", "AutomationControlled") // hide navigator.webdriver from anti-bot detectors
	l.Delete("disable-site-isolation-trials")               // remove "unsupported command-line flag: --disable-site-isolation-trials" infobar
	l.Set("disable-infobars")                               // suppress warning infobars
	l.Set("no-default-browser-check")                       // suppress "Chromium isn't your default browser" prompt
	l.Set("lang", defaultLanguage)
	if err := applyChromiumFlags(l, b.chromiumFlags); err != nil {
		return err
	}
	if b.proxy != "" {
		server, host, err := parseProxy(b.proxy)
		if err != nil {
			return err
		}
		creds, err := config.LoadProxyCredentials()
		if err != nil {
			return fmt.Errorf("browsers: load proxy credentials: %w", err)
		}
		cred, ok := creds[host]
		b.proxyAuth = &proxyAuth{
			host: host,
			cred: cred,
			ok:   ok,
			file: proxyConfigFile(),
		}
		l.Proxy(server)
	}
	if b.proxyBypass != "" {
		l.Set("proxy-bypass-list", b.proxyBypass)
	}
	if b.ignoreCerts {
		l.Set("ignore-certificate-errors")
	}
	binPath := b.binPath
	if binPath == "" {
		binPath = DefaultBinPath()
	}
	if binPath != "" {
		l.Bin(binPath)
	}
	profileDir := b.profileDir
	if profileDir == "" {
		return fmt.Errorf("browsers: launch %s: no profile directory resolved; the manager must resolve one before launch", b.id)
	}
	l.UserDataDir(profileDir)
	if !b.headless {
		switch b.position {
		case "right":
			l.Delete("start-maximized")
			l.Delete("no-startup-window")
			l.Set("window-position", "1280,0")
			l.Set("window-size", "1280,1400")
		case "max", "maximized":
			l.Set("start-maximized")
			l.Delete("no-startup-window")
		case "left", "":
			fallthrough
		default:
			l.Delete("start-maximized")
			l.Delete("no-startup-window")
			l.Set("window-position", "0,0")
			l.Set("window-size", "1280,1400")
		}
	}
	u, err := l.Launch() // error form; no Must*
	if err != nil {
		if strings.Contains(err.Error(), "SingletonLock") || strings.Contains(err.Error(), "Opening in existing browser session") {
			return fmt.Errorf(
				"browsers: launch chromium: profile %q is already in use by another running browser (owned by %s) — close the browser holding it with browser_close {browser_id: \"<id>\"} or pick a different profile name: %w",
				profileDir,
				b.OwnerLabel(),
				err,
			)
		}
		return fmt.Errorf("browsers: launch chromium with profile %q: %w", profileDir, err)
	}
	// The Chromium process is now live (WEB-60): if anything below fails — a
	// caller ctx that expired during launch, or a failed websocket connect —
	// kill it best-effort so a failed launch never orphans a background
	// browser process.
	connected := false
	defer func() {
		if !connected {
			l.Kill() // best-effort; kills the just-launched Chromium process
		}
	}()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browsers: launch browser: %w", err)
	}

	// The rod browser's default ctx is the manager-owned baseCtx, not the
	// caller's tool-call ctx: a canceled request must abort calls, never the
	// browser itself (WEB-59). The websocket dial inside Connect is a fast
	// localhost op; the launcher above is where real blocking (first-run
	// download) happens, and it stays caller-bounded.
	rb := rod.New().Context(b.baseCtx).ControlURL(u).NoDefaultDevice()
	if err := rb.Connect(); err != nil {
		return fmt.Errorf("browsers: connect: %w", err)
	}
	connected = true // Chromium is wired up; the launch is a success

	major := probeChromeMajor(ctx, rb)

	b.mu.Lock()
	b.rod = rb
	b.chromeMajor = major
	b.profileDir = profileDir
	b.mu.Unlock()
	b.startTargetTracking()
	return nil
}

// NewPage opens a new tab in the Browser, navigates it to url (when non-empty)
// and registers it in the pages map under a fresh PageID. The page's rod
// default ctx and browser binding hang off the manager-owned baseCtx — never
// the caller's ctx — so the tab persists (and stays probeable) long after the
// request that created it returns (WEB-63); the post-create ops
// (stealth/viewport/device/UA/navigate/wait) are chained to the caller's ctx,
// so a hung call still times out. Stealth is
// applied to every new page by default and skipped only when the Browser was
// constructed with stealth=false. The page launches PLAIN — no screen-size
// override of any kind unless the caller explicitly passes one — and every
// optional override is applied BEFORE navigating, so it governs the load
// itself (no re-navigate needed), in this order:
//
//  1. viewport (when vp is non-nil): a MANUAL width × height override via
//     Page.SetViewport (Emulation.setDeviceMetricsOverride, ctx-bound, with
//     matching screen metrics); nil leaves rod's own defaults in place.
//  2. device (when device is non-empty): a go-rod device preset via
//     Page.Emulate — the primary, opt-in emulation mechanism (device UA +
//     metrics + touch). An unknown name fails with the pinned
//     "unknown device %q" error; a non-empty preset also makes the manual
//     viewport (1) moot for metrics, but both steps run in order.
//  3. default User-Agent (when userAgent is empty and no device preset is
//     selected): the browser's coherent UA, Accept-Language and Sec-CH-UA
//     client hints via Network.setUserAgentOverride, built from the launched
//     Chromium major and the host platform.
//  4. userAgent (when userAgent is non-empty): an explicit User-Agent override
//     via rod's SetUserAgent, applied after any device preset or default so an
//     explicit UA always wins.
//
// Empty strings skip their optional step; a plain call (nil viewport, empty
// device, empty userAgent) still applies the stealth script and the coherent
// default User-Agent. Any failure closes the unregistered tab (no leak) and
// wraps the error with %w: a create/attach failure is already cleaned up by
// rod's own leak-prevention defer inside Browser.Page (it closes the fresh
// target when the attach fails), so only post-create failures
// (stealth/viewport/device/UA/navigate) call closeUnregisteredPage.
func (b *Browser) NewPage(
	ctx context.Context,
	url string,
	vp *Viewport,
	device,
	userAgent string,
) (*Page, error) {
	b.mu.Lock()
	rb := b.rod
	major := b.chromeMajor
	b.mu.Unlock()
	if rb == nil {
		return nil, fmt.Errorf("browsers: browser not launched")
	}
	if url != "" {
		if err := b.CheckScope(url); err != nil {
			return nil, err
		}
	}
	if err := b.checkPageCap(); err != nil {
		return nil, err
	}

	// Create the tab with rod's own pipeline (Browser.Page: Target.createTarget
	// + attach + rod's built-in leak-prevention defer that closes the fresh
	// target if the attach fails). The call runs on the baseCtx-bound rod
	// browser — NEVER rb.Context(ctx) — so the page's default ctx (sessionCtx)
	// and its browser binding hang off the manager-owned baseCtx (WEB-63): a
	// page bound to a per-request ctx dies when the request returns — the next
	// lookup's probe routes Info() through p.browser whose ctx is the cancelled
	// request ctx, so it fails, evicts the tab and reports "page-N not found".
	// An empty URL makes Page() return WITHOUT navigating (about:blank), so
	// stealth and the viewport/device/UA overrides below still apply pre-load.
	// On error rod's defer has already closed the target — just wrap and return.
	p, err := rb.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("browsers: new page: %w", err)
	}

	// Wrap the rod handle in the engine Page so every operation below goes
	// through the engine seams (Page.SetViewport owns the page lock, the
	// nil-Rod guard and the "browsers: set viewport" error wrap). The page is
	// not yet registered in the pages map, so no other goroutine can see it;
	// ID, URL and lastAccess are filled at registration below.
	page := &Page{Rod: p, browser: b}
	page.startObservation()
	page.startDialogs()
	b.startProxyAuth(p)

	// Install the browser's extra request headers before stealth and before any
	// navigation, so the first request already carries them. rod's cleanup
	// disables the per-page network override and is stored for teardown.
	cleanup, err := b.applyExtraHeaders(ctx, p)
	if err != nil {
		b.closeUnregisteredPage(page)
		return nil, err
	}
	b.trackHeaderCleanup(p, cleanup)

	// Apply the stealth preload script (the same JS stealth.Page injects,
	// error form; no Must*) immediately after creation — before the
	// viewport/device/UA overrides and any navigation — so it governs every
	// load. Chained to the per-call ctx so the round trip times out with the
	// tool call. On failure close the unregistered tab: rod's leak-prevention
	// defer inside Browser.Page stops at Page()'s return, so a stealth failure
	// (a non-registered page) would otherwise orphan the about:blank target
	// until browser_close.
	if b.stealth {
		if _, err := p.Context(ctx).EvalOnNewDocument(stealth.JS); err != nil {
			b.closeUnregisteredPage(page)
			return nil, fmt.Errorf("browsers: apply stealth: %w", err)
		}
	}

	// Apply a manual viewport override in the same pre-registration window as
	// Navigate: the fresh page is not yet in the pages map, so no other
	// goroutine can see it and no new lock is needed — Page.SetViewport takes
	// it. The call is ctx-bound so a hung CDP call cannot outlive the tool
	// call (the previous review flagged the un-bound SetViewport). The
	// override replaces both the emulated viewport and the screen metrics
	// (proto ScreenWidth/ScreenHeight are *int); nil = plain launch (rod
	// defaults, no manual screen-size override — device presets via
	// Page.Emulate are the opt-in path).
	if vp != nil {
		if err := page.SetViewport(ctx, vp); err != nil {
			b.closeUnregisteredPage(page) // do not leak the orphaned tab (mirror the navigate failure path)
			return nil, err
		}
	}

	// Apply a device preset pre-navigation via the engine seam (Page.Emulate
	// owns the page lock and the nil-Rod guard and resolves the name
	// case-insensitively, failing with the pinned "unknown device %q" error).
	// Same pre-registration window as the viewport branch above: the page is
	// not yet in the pages map, so no other goroutine can see it.
	if device != "" {
		if err := page.Emulate(ctx, device); err != nil {
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, err
		}
	} else if vp == nil {
		if err := page.Emulate(ctx, "clear"); err != nil {
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, err
		}
		if !b.headless {
			_ = p.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateMaximized})
		}
	}

	// Apply an explicit UA override pre-navigation via rod's own SetUserAgent
	// (the same helper Page.Emulate uses internally for the preset UA; an
	// explicit userAgent thus wins over a device preset's UA). Without one, a
	// page with no device preset gets the browser's coherent default UA,
	// Accept-Language and Sec-CH-UA client hints. ctx-bound so a hung CDP call
	// cannot outlive the tool call. In the pre-registration window no lock is
	// needed — no other goroutine can see the page yet.
	if userAgent != "" {
		if err := p.Context(ctx).SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: userAgent}); err != nil {
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: set user agent: %w", err)
		}
	} else if !hasDevicePreset(device) {
		if err := applyDefaultUserAgent(ctx, p, major); err != nil {
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, err
		}
	}

	if url != "" {
		// Bound to ctx so a hung navigation cannot outlive the tool call.
		pc := p.Context(ctx)
		if err := pc.Navigate(url); err != nil {
			authErr := b.takeProxyAuthError(p)
			if authErr != nil {
				b.closeUnregisteredPage(page) // do not leak the orphaned tab
				return nil, authErr
			}
			if IsInvalidAuthCredential(err) {
				b.registerNewPage(page, url)
				return nil, fmt.Errorf("browsers: %w", InvalidAuthCredentialError(url, page.ID))
			}
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: navigate: %w", err)
		}
		if err := pc.WaitLoad(); err != nil {
			authErr := b.takeProxyAuthError(p)
			if authErr != nil {
				b.closeUnregisteredPage(page) // do not leak the orphaned tab
				return nil, authErr
			}
			if IsInvalidAuthCredential(err) {
				b.registerNewPage(page, url)
				return nil, fmt.Errorf("browsers: %w", InvalidAuthCredentialError(url, page.ID))
			}
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, fmt.Errorf("browsers: wait load: %w", err)
		}
		if authErr := b.takeProxyAuthError(p); authErr != nil {
			b.closeUnregisteredPage(page) // do not leak the orphaned tab
			return nil, authErr
		}
	}

	b.registerNewPage(page, url)
	return page, nil
}

// registerNewPage inserts a freshly created page into its browser and records
// its target id. It is the success path and the kept-page path for a navigation
// that only lacked credentials.
func (b *Browser) registerNewPage(page *Page, url string) {
	targetID := page.Rod.TargetID
	pageID := b.mintPageID()
	b.mu.Lock()
	page.ID = pageID
	page.URL = url
	page.touch()
	b.pages[pageID] = page
	b.mu.Unlock()
	b.root().touch()
	b.root().trackTarget(page, targetID)
}

// closeUnregisteredPage best-effort closes a freshly created page whose session
// was never registered, disarming its capture subscription first, and bounding
// the close off the manager-owned baseCtx so a canceled per-call ctx cannot skip
// the close (WEB-59). The page's default ctx hangs off the manager-owned
// baseCtx too (WEB-63: NewPage attaches through the baseCtx-bound browser via
// rod's Browser.Page), so rod's Page.Close on the base handle would already be
// delivered — the fresh closePageTimeout derived from the baseCtx still
// guarantees the close is bounded and outlives any single request. Only called
// for post-create failures (stealth/viewport/device/UA/navigate) — rod's
// built-in leak-prevention defer inside Browser.Page already covers
// create/attach failures, so this never needs to run on Page()'s own error path.
// Best-effort by design.
func (b *Browser) closeUnregisteredPage(page *Page) {
	b.stopProxyAuth(page.Rod)
	b.stopHeaderCleanup(page.Rod)
	page.stopDialogs()
	page.stopObservation()
	closeCtx, cancel := context.WithTimeout(b.baseCtx, closePageTimeout)
	defer cancel()
	_ = page.Rod.Context(closeCtx).Close()
}

// Page returns the live page with the given ID, or an error when no page with
// that ID is open (e.g. it was closed or swept). Every successful lookup
// verifies the tab is still alive (a single crashed tab keeps its map entry —
// the rod handle object survives — while its CDP target is gone) and refreshes
// the page's last-access time (under the same browser mutex the sweeper
// iterates under), so the manager's TTL sweeper never closes a page that is in
// use.
//
// The liveness probe is a network call (Target.getTargetInfo via p.Rod.Info),
// so it never runs under b.mu or m.mu. It is non-blocking: a page whose
// operation lock is held is alive by definition, so a busy page is returned
// without a probe instead of making every lookup wait behind it. On probe
// failure the dead entry is evicted race-safely (WEB-39 pattern): the handle
// is captured and nilled under the locks and closed strictly afterwards, so a
// concurrent sweepOnce or Page lookup never double-closes or panics.
func (b *Browser) Page(ctx context.Context, id PageID) (*Page, error) {
	owner, p, ok := b.lookupPage(id)
	if !ok {
		return nil, fmt.Errorf("browsers: page %q not found", id)
	}
	p.touch()
	b.touch()

	if err := probePage(p); err != nil {
		// The tab is dead: evict it (WEB-39 TOCTOU-safe capture pattern).
		var handle *rod.Page
		evicted := false
		if unlock, locked := p.TryLock(); locked {
			owner.mu.Lock()
			if cur, ok := owner.pages[id]; ok && cur == p {
				p.stopObservation()
				handle = p.Rod
				p.Rod = nil
				delete(owner.pages, id)
				evicted = true
			}
			owner.mu.Unlock()
			unlock()
			p.stopDialogs()
		}
		if evicted {
			owner.forgetTarget(p)
		}
		if handle != nil {
			owner.stopProxyAuth(handle)
			owner.stopHeaderCleanup(handle)
			// Close the captured handle outside the locks, via closeRodPage
			// (the sweeper's path): it honors a bound close ctx and converts a
			// concurrent-close panic into an error — an in-flight tool call on
			// the same tab (goroutine-per-request MCP callers) can race this
			// close. Best-effort: the target is already gone.
			closeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			_ = closeRodPage(closeCtx, handle)
		}
		return nil, fmt.Errorf("browsers: page %q not found", id)
	}
	return p, nil
}

// probePage verifies the page's CDP target is still alive with an Info() round
// trip, the call that surfaces -32602 "No target with given id found" when the
// tab crashed while its rod handle survived. A page whose operation lock is
// busy is alive and needs no probe, so this never waits on an in-flight
// operation. The probe is bound to a short timeout and is deliberately NOT
// derived from the caller's ctx (mirroring probeBrowser): rod cannot
// distinguish a caller-cancelled ctx from a dead websocket, so a caller whose
// ctx expired would wrongly evict a healthy tab.
func probePage(p *Page) error {
	unlock, ok := p.TryLock()
	if !ok {
		return nil
	}
	defer unlock()
	if p.Rod == nil {
		return fmt.Errorf("browsers: page rod handle is nil")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	_, err := p.Rod.Context(probeCtx).Info()
	return err
}

// WindowState is the desired OS window state for Browser.Focus. The engine
// owns the vocabulary and maps it to proto.BrowserWindowState internally, so
// the IPC/CLI layers never import rod.
type WindowState string

const (
	// WindowStateNormal restores a minimized window and re-asserts its bounds
	// as the standard best-effort raise. This is the default.
	WindowStateNormal WindowState = "normal"
	// WindowStateMaximized maximizes the browser window (a stronger raise on
	// window managers that re-layout the window on top).
	WindowStateMaximized WindowState = "maximized"
	// WindowStateFullscreen makes the browser window fullscreen.
	WindowStateFullscreen WindowState = "fullscreen"
)

// Focus raises the Browser's OS window to the foreground via pure CDP (no
// wmctrl/xdotool): it resolves the OS window of the window containing any
// live page's devtools target (all pages live in the same window) with rod's
// Page.GetWindow, then applies the requested window state with rod's
// Page.SetWindow(bounds). The default state (normal) restores a minimized
// window and re-asserts its current bounds — the standard best-effort raise;
// maximize/fullscreen are state-only, because the proto doc forbids combining
// those states with left/top/width/height.
//
// Focus is NOT a launch trigger and never closes the browser: with no browser
// launched it fails (mirroring the NewPage guard), and with no live page it
// fails — the --no-startup-window case, where Chromium may have no window
// until a page opens.
func (b *Browser) Focus(ctx context.Context, state WindowState) error {
	// Fail fast on a bogus state before any network round trip (the IPC/CLI
	// layers validate too; this guards direct engine callers).
	switch state {
	case WindowStateNormal, WindowStateMaximized, WindowStateFullscreen:
	default:
		return fmt.Errorf("browsers: focus: unknown window state %q (must be one of normal, maximized, fullscreen)", state)
	}

	b.mu.Lock()
	rb := b.rod
	if rb == nil {
		b.mu.Unlock()
		return fmt.Errorf("browsers: focus: browser not launched")
	}
	// Snapshot EVERY live page while holding only b.mu: map iteration is
	// unordered, and a crashed tab whose rod handle survived (the WEB-47 case)
	// would fail GetWindow with "No target with given id found". The candidates
	// are tried in order below until one resolves the OS window, so a single
	// dead or busy tab cannot block the focus.
	pages := make([]*Page, 0, len(b.pages))
	for _, p := range b.pages {
		pages = append(pages, p)
	}
	b.mu.Unlock()
	if len(pages) == 0 {
		return fmt.Errorf("browsers: focus: no pages open; open a page first")
	}

	// Bound to the caller's ctx (rod's ctx-aware form; no Must*). The first
	// candidate that resolves the OS window wins; a dead or busy target is
	// skipped so a healthy tab still resolves the window (all pages share the
	// same browser window).
	var win *Page
	var winBounds *proto.BrowserBounds
	var lastErr error
	for _, p := range pages {
		unlock, ok := p.TryLock()
		if !ok {
			lastErr = fmt.Errorf("browsers: focus: page is busy")
			continue
		}
		if p.Rod == nil {
			lastErr = fmt.Errorf("browsers: focus: page rod handle is nil")
			unlock()
			continue
		}
		bounds, err := p.Rod.Context(ctx).GetWindow()
		unlock()
		if err != nil {
			lastErr = err
			continue
		}
		win = p
		winBounds = bounds
		break
	}
	if win == nil {
		return fmt.Errorf("browsers: focus: no live page window: %w", lastErr)
	}

	if err := win.LockContext(ctx, "focus"); err != nil {
		return err
	}
	defer win.Unlock()
	if win.Rod == nil {
		return fmt.Errorf("browsers: focus: page rod handle is nil")
	}
	page := win.Rod.Context(ctx)

	switch state {
	case WindowStateNormal:
		// Re-asserting the returned bounds (position/size echoed from the get
		// result) is the standard best-effort raise. Bounds fields may be nil
		// (e.g. minimized); nil fields are left unchanged per the proto doc.
		bounds := &proto.BrowserBounds{WindowState: proto.BrowserWindowStateNormal}
		if winBounds != nil {
			bounds.Left = winBounds.Left
			bounds.Top = winBounds.Top
			bounds.Width = winBounds.Width
			bounds.Height = winBounds.Height
		}
		if err := page.SetWindow(bounds); err != nil {
			return fmt.Errorf("browsers: focus: set window bounds: %w", err)
		}
		return nil
	case WindowStateMaximized:
		// State-only: the proto doc forbids combining maximized with
		// left/top/width/height.
		if err := page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateMaximized}); err != nil {
			return fmt.Errorf("browsers: focus: set window state: %w", err)
		}
		return nil
	case WindowStateFullscreen:
		// State-only, same proto constraint as maximized.
		if err := page.SetWindow(&proto.BrowserBounds{WindowState: proto.BrowserWindowStateFullscreen}); err != nil {
			return fmt.Errorf("browsers: focus: set window state: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("browsers: focus: unknown window state %q (must be one of normal, maximized, fullscreen)", state)
	}
}

// Pages returns a read-only snapshot of the Browser's live pages (id, live url,
// title and owning identity), safe to marshal and iterate; it never exposes the
// live *rod.Page handles. URL and title are read from each live tab within one
// shared budget, so they follow navigations and redirects; a busy or unreadable
// tab falls back to the stored URL with Stale set instead of waiting on a page
// operation. A root browser includes the pages of every identity.
func (b *Browser) Pages() []PageInfo {
	ctx, cancel := context.WithTimeout(context.Background(), listingInfoBudget)
	defer cancel()

	infos := b.pageInfos(ctx)

	b.mu.Lock()
	identities := make([]*Browser, 0, len(b.identities))
	for _, child := range b.identities {
		identities = append(identities, child)
	}
	b.mu.Unlock()

	for _, child := range identities {
		infos = append(infos, child.pageInfos(ctx)...)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

// pageOwners returns this browser and every identity clone, so shared sweeps
// can visit each pages map with the owning browser's own teardown state.
func (b *Browser) pageOwners() []*Browser {
	b.mu.Lock()
	defer b.mu.Unlock()
	owners := make([]*Browser, 0, len(b.identities)+1)
	owners = append(owners, b)
	for _, child := range b.identities {
		owners = append(owners, child)
	}
	return owners
}

// pageInfos snapshots this browser's own pages, tagged with its identity name.
// It copies the page pointers under b.mu and reads each tab outside the browser
// lock, so the live URL/title round trip never blocks browser operations.
func (b *Browser) pageInfos(ctx context.Context) []PageInfo {
	b.mu.Lock()
	pages := make([]*Page, 0, len(b.pages))
	for _, p := range b.pages {
		pages = append(pages, p)
	}
	identity := b.IdentityName()
	b.mu.Unlock()

	infos := make([]PageInfo, 0, len(pages))
	for _, p := range pages {
		infos = append(infos, p.snapshot(ctx, identity))
	}
	return infos
}

// ClosePage closes the page with the given ID, removing it from the pages map
// and releasing its browser tab. The session is evicted unconditionally — even
// if the underlying tab close fails — so a wedged/dead CDP connection cannot
// leave a phantom session that session_list keeps reporting (WEB-60 MED-1).
// The map entry is dropped under b.mu only, so a busy page can never block
// other browser operations. The operation lock is taken with a try-lock first
// and then a short bounded wait off the manager-owned baseCtx (never the
// caller's ctx), so a stuck tab cannot wedge the close and a canceled request
// cannot abort a teardown that already succeeded. When the page is still held
// by an operation after that wait, its tab is left to Browser.Close: the session
// is already evicted, so session_list stops reporting it immediately (WEB-60
// MED-1, WEB-166).
func (b *Browser) ClosePage(ctx context.Context, id PageID) error {
	_, err := b.closePage(ctx, id)
	return err
}

// closePage performs ClosePage and reports whether the tab handle was released.
// A false result with a nil error means an operation still held the page: the
// registry entry was removed and the tab is left to the browser teardown.
func (b *Browser) closePage(ctx context.Context, id PageID) (bool, error) {
	return b.closePageTree(ctx, id, make(map[PageID]bool))
}

// closePageTree closes id and every popup opened by it, recursively, so an
// orphan target never outlives its opener. seen keeps a shared close request
// from visiting a page twice.
func (b *Browser) closePageTree(
	ctx context.Context,
	id PageID,
	seen map[PageID]bool,
) (released bool, err error) {
	for _, pid := range b.popupTree(id) {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		closed, closeErr := b.closeOnePage(ctx, pid)
		if pid == id {
			released = closed
		}
		err = errors.Join(err, closeErr)
	}
	return released, err
}

// closeOnePage closes one page, removing it from its owning browser's registry
// and releasing its tab. It does not cascade to popups.
func (b *Browser) closeOnePage(ctx context.Context, id PageID) (released bool, err error) {
	owner, p, ok := b.lookupPage(id)
	if !ok {
		return false, fmt.Errorf("browsers: page %q not found", id)
	}

	owner.mu.Lock()
	if cur, ok := owner.pages[id]; ok && cur == p {
		delete(owner.pages, id)
	}
	owner.mu.Unlock()
	owner.forgetTarget(p)
	b.root().touch()

	unlock, locked := p.TryLock()
	if !locked {
		waitCtx, waitCancel := context.WithTimeout(b.baseCtx, closePageLockWait)
		defer waitCancel()
		if err := p.LockContext(waitCtx, "close page"); err != nil {
			return false, nil
		}
		unlock = p.Unlock
	}
	p.stopObservation()
	handle := p.Rod
	p.Rod = nil
	unlock()
	p.stopDialogs()

	if handle == nil {
		return true, nil
	}
	owner.stopProxyAuth(handle)
	owner.stopHeaderCleanup(handle)
	closeCtx, cancel := context.WithTimeout(b.baseCtx, teardownBudget)
	defer cancel()
	if err := closeRodPage(closeCtx, handle); err != nil {
		return false, fmt.Errorf("browsers: close page %q: %w", id, err)
	}
	return true, nil
}

// Close closes the Browser and all of its live pages, releasing the underlying
// Chromium process. After Close the Browser must not be reused.
//
// Every live page handle and the rod browser are captured under the locks and
// closed strictly afterwards, outside any lock (mirroring the sweeper's
// capture-under-lock/close-outside pattern): the closes are network round
// trips that would block every other browser operation if done under b.mu, and
// an in-flight tool call on a page can race the close (closeRodPage's
// panic-recover absorbs that). The whole teardown runs on ONE shared budget
// (teardownBudget) derived from the manager-owned baseCtx — never the caller's
// ctx — so a canceled request ctx cannot abort a teardown that already
// succeeded (HIGH-2: browser_close reported "context canceled" despite
// successful Chromium teardown), and N stuck pages × per-close timeouts can no
// longer blow a single call deadline, letting the manager's probe-based
// self-heal complete inside one request (WEB-72).
func (b *Browser) Close(ctx context.Context) error {
	// Snapshot the pages and the rod browser under b.mu only, then capture each
	// handle under its own operation lock. A page held by an in-flight
	// operation is skipped: closing the browser below tears its renderer down
	// and unblocks that operation, so teardown never waits on a stuck page.
	b.mu.Lock()
	pages := make([]*Page, 0, len(b.pages))
	for _, p := range b.pages {
		pages = append(pages, p)
	}
	b.pages = make(map[PageID]*Page)
	identities := make([]*Browser, 0, len(b.identities))
	for _, child := range b.identities {
		identities = append(identities, child)
	}
	b.identities = make(map[string]*Browser)
	authPages := b.authPages
	b.authPages = make(map[*rod.Page]*proxyAuthPage)
	headerCleanups := b.headerCleanups
	b.headerCleanups = make(map[*rod.Page]func())
	targetStop := b.popups.stop
	b.stopTargetTrackingLocked()
	rb := b.rod
	b.rod = nil
	b.mu.Unlock()
	if targetStop != nil {
		targetStop()
	}

	// Identity contexts are disposed first: each clone owns a separate cookie
	// jar and tab set, so its Close is the only clean way to release them. A
	// clone close never touches the parent process.
	var identityErr error
	for _, child := range identities {
		if err := child.Close(ctx); err != nil && identityErr == nil {
			identityErr = err
		}
	}

	handles := make([]*rod.Page, 0, len(pages))
	for _, p := range pages {
		unlock, ok := p.TryLock()
		if !ok {
			continue
		}
		if p.Rod != nil {
			p.stopObservation()
			handles = append(handles, p.Rod)
			p.Rod = nil
		}
		unlock()
		p.stopDialogs()
	}

	for _, cleanup := range headerCleanups {
		if cleanup != nil {
			cleanup()
		}
	}
	for _, a := range authPages {
		a.cancel()
	}

	// Close the captured pages outside the locks, all bounded by ONE shared
	// teardown budget derived from the manager-owned baseCtx (the caller's ctx
	// may already be canceled — it must not abort teardown): N stuck tabs ×
	// per-close timeouts would blow any single call deadline, so the entire
	// teardown (every page close plus the browser close below) shares one
	// teardownBudget deadline. Best-effort per-page, like the sweeper: a
	// wedged tab cannot block the rest of the teardown.
	teardownCtx, cancel := context.WithTimeout(b.baseCtx, teardownBudget)
	defer cancel()
	for _, h := range handles {
		_ = closeRodPage(teardownCtx, h)
	}

	var closeErr error
	if rb != nil {
		closeErr = rb.Context(teardownCtx).Close() // error form; no Must*
	}

	b.cleanupOnce.Do(func() {
		if b.profileCleanup != nil {
			b.profileCleanup()
		}
	})
	if closeErr == nil {
		return identityErr
	}
	return closeErr
}

// applyChromiumFlags sets each configured switch on the launcher. A switch
// carrying an "=" passes its value to Set; a bare switch passes none.
func applyChromiumFlags(l *launcher.Launcher, switches []string) error {
	for _, switchValue := range switches {
		name, value, hasValue, err := parseChromiumFlag(switchValue)
		if err != nil {
			return err
		}
		if hasValue {
			l.Set(flags.Flag(name), value)
			continue
		}
		l.Set(flags.Flag(name))
	}
	return nil
}

// parseChromiumFlag splits a "--name" or "--name=value" switch into the
// launcher name and its optional value. A switch without the "--" prefix is
// rejected so a typo surfaces at launch instead of being silently ignored.
func parseChromiumFlag(switchValue string) (name, value string, hasValue bool, err error) {
	if !strings.HasPrefix(switchValue, "--") {
		return "", "", false, fmt.Errorf("browsers: chromium flag %q must start with --", switchValue)
	}
	body := strings.TrimPrefix(switchValue, "--")
	if body == "" {
		return "", "", false, fmt.Errorf("browsers: chromium flag %q must start with --", switchValue)
	}
	if idx := strings.IndexByte(body, '='); idx >= 0 {
		name = body[:idx]
		if name == "" {
			return "", "", false, fmt.Errorf("browsers: chromium flag %q must start with --", switchValue)
		}
		return name, body[idx+1:], true, nil
	}
	return body, "", false, nil
}
