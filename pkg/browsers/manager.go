// Package browsers implements the core browser engine that powers the MCP
// tools. A Manager owns the live Browsers (one Chromium instance each via
// go-rod), and each Browser owns its live Pages (browser tabs, a.k.a. MCP
// sessions). This package is rod-only: it owns all go-rod/Chromium logic and
// never imports pkg/mcp; the MCP layer translates calls into this package.
package browsers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"

	"github.com/dakolli/webmcp/pkg/config"
)

// BrowserID uniquely identifies a live Browser instance (e.g. "browser-1", "browser-2").
type BrowserID string

// ErrUnknownBrowser reports that a browser id does not match a live browser.
var ErrUnknownBrowser = errors.New("unknown browser")

// errManagerShutdown reports a create or open against a terminated manager.
var errManagerShutdown = errors.New("browsers: manager has been shut down")

// Browser close reasons recorded on the audit log line. A browser can leave the
// live registry by an explicit close, the idle sweep, a crashed Chromium
// process, or manager shutdown; the reason makes a vanished browser traceable.
const (
	closeReasonExplicit = "browser_close"
	closeReasonSweep    = "idle_sweep"
	closeReasonCrash    = "crash"
	closeReasonShutdown = "shutdown"
)

// PageLookupKind classifies why a page id did not resolve for the caller.
type PageLookupKind string

const (
	// PageLookupForeign means the id exists only outside the declared owner.
	PageLookupForeign PageLookupKind = "foreign"
	// PageLookupAmbiguous means the page id cannot be pinned to one browser.
	PageLookupAmbiguous PageLookupKind = "ambiguous"
	// PageLookupMissing means no live browser holds the id.
	PageLookupMissing PageLookupKind = "missing"
)

// PageLookupError reports a page id that the qualifier-aware lookup refused.
// Message is the exact human sentence; Kind is the machine branch the surfaces
// map to a result-envelope error type.
type PageLookupError struct {
	// ID is the page id that failed to resolve.
	ID PageID
	// Kind classifies the refusal.
	Kind PageLookupKind
	// Message is the human sentence returned to the caller.
	Message string
}

// Error returns the human sentence for the lookup failure.
func (e *PageLookupError) Error() string { return e.Message }

// defaultTTL is the per-page idle timeout applied by NewManager when no
// WithTTL option is given. Pages that go unused for longer than the TTL are
// closed by the manager's background sweeper.
const defaultTTL = 15 * time.Minute

// DefaultBrowserTTL is the per-browser idle timeout applied by NewManager when
// no WithBrowserTTL option is given. A browser idle for longer than the TTL is
// closed by the manager's background sweeper, which releases its profile
// (deleting an ephemeral directory, keeping a persistent one).
const DefaultBrowserTTL = 30 * time.Minute

// UnownedOwner is the owner key recorded for a browser created by a surface
// with no client identity (direct HTTP, CLI in-process, IPC). Such browsers
// live in the shared pool and are addressed by explicit browser id.
const UnownedOwner = "unowned"

// closePageTimeout bounds each individual page close performed by the
// background sweeper, so a single hung tab cannot stall the sweep loop.
const closePageTimeout = 30 * time.Second

// teardownBudget bounds the WHOLE Browser.Close teardown (all per-page closes
// plus the browser close, sharing one deadline) and each ClosePage call: N
// stuck pages × a per-close timeout would blow any single call deadline, which
// is why probe-based self-heal appeared hung (WEB-72). 10s keeps the entire
// teardown inside a 30s tool-call deadline with room for the manager's 2s
// liveness probes. Sweeper per-page closes keep closePageTimeout — the sweep is
// background and never holds a request deadline.
const teardownBudget = 10 * time.Second

// closePageLockWait bounds how long a close waits for a page operation to
// release the page lock. A try-lock runs first; only a page still held after
// this short wait is evicted from the registry and left to the browser teardown
// (WEB-166), so closing a stuck page never wedges the caller.
const closePageLockWait = 2 * time.Second

// probeTimeout bounds every liveness probe (browser-level and per-page): rod
// has no auto-reconnect, so on a dead CDP websocket a Call blocks until its
// ctx deadline. Probes therefore always derive a short timeout off the caller's
// ctx, failing the probe instead of hanging the caller.
const probeTimeout = 2 * time.Second

// Option configures a Manager at construction time. Apply via
// NewManager(WithHeadless(...), WithStealth(...), ...).
type Option func(*Manager)

// profileRef is the ownership record for one profile directory: how many live
// browsers use it, whether it must be removed when the last one closes, and
// which browser claimed it first.
type profileRef struct {
	count      int
	ephemeral  bool
	browserID  BrowserID
	owner      string
	ownerLabel string
}

// ownerCtxKey is the context key carrying the calling surface's owner identity.
type ownerCtxKey struct{}

// ownerLabelCtxKey is the context key carrying a human-readable owner label.
type ownerLabelCtxKey struct{}

// explicitOwnerCtxKey marks an owner as a caller-chosen label rather than an
// ambient session identity.
type explicitOwnerCtxKey struct{}

// sessionOwnerCtxKey preserves the ambient surface identity when
// WithExplicitOwner replaces the owner key with a caller-chosen label.
type sessionOwnerCtxKey struct{}

// WithOwnerKey returns a context that records owner as the caller's identity.
// The engine reads it in CreateBrowser and OpenBrowser, so one MCP session can
// tag every browser it touches without threading the value through each call.
// An empty owner falls back to UnownedOwner.
func WithOwnerKey(ctx context.Context, owner string) context.Context {
	if owner == "" {
		owner = UnownedOwner
	}
	return context.WithValue(ctx, ownerCtxKey{}, owner)
}

// OwnerFrom returns the owner identity recorded on ctx, or UnownedOwner when
// the caller carries no identity.
func OwnerFrom(ctx context.Context) string {
	if owner, ok := ctx.Value(ownerCtxKey{}).(string); ok && owner != "" {
		return owner
	}
	return UnownedOwner
}

// WithOwnerLabel returns a context carrying a human-readable owner label, such
// as an MCP client name. The label is display-only: ownership and create
// idempotency still key off WithOwnerKey.
func WithOwnerLabel(ctx context.Context, label string) context.Context {
	if label == "" {
		return ctx
	}
	return context.WithValue(ctx, ownerLabelCtxKey{}, label)
}

// OwnerLabelFrom returns the display label recorded on ctx, or "".
func OwnerLabelFrom(ctx context.Context) string {
	label, _ := ctx.Value(ownerLabelCtxKey{}).(string)
	return label
}

// WithExplicitOwner returns a context that records owner as the caller's own
// explicit choice: it sets both the ownership key (which joins the create key)
// and the display label, and it stops an omitted browser id from resolving to
// the shared unowned browser. The ambient surface identity is preserved under
// sessionOwnerCtxKey so a browser created under the label can still be closed by
// the session that created it (see SessionOwnerFrom).
func WithExplicitOwner(ctx context.Context, owner string) context.Context {
	ctx = context.WithValue(ctx, sessionOwnerCtxKey{}, OwnerFrom(ctx))
	ctx = WithOwnerKey(ctx, owner)
	ctx = WithOwnerLabel(ctx, owner)
	return context.WithValue(ctx, explicitOwnerCtxKey{}, true)
}

// SessionOwnerFrom returns the surface identity that made the call: the ambient
// owner key preserved across WithExplicitOwner, or OwnerFrom(ctx) when no
// explicit label replaced it. It is the key a browser records at creation so a
// later call from the same surface can address the browser even when the
// browser's owner key is an explicit label.
func SessionOwnerFrom(ctx context.Context) string {
	if session, ok := ctx.Value(sessionOwnerCtxKey{}).(string); ok && session != "" {
		return session
	}
	return OwnerFrom(ctx)
}

// HasExplicitOwner reports whether ctx carries a caller-chosen owner.
func HasExplicitOwner(ctx context.Context) bool {
	explicit, _ := ctx.Value(explicitOwnerCtxKey{}).(bool)
	return explicit
}

// WithHeadless controls whether the launcher runs Chromium in headless mode.
// The default is headful (headless=false); pass WithHeadless(true) for
// headless Chromium.
func WithHeadless(headless bool) Option {
	return func(m *Manager) { m.headless = headless }
}

// WithStealth controls whether go-rod/stealth is applied to every new page.
// Stealth is on by default; pass WithStealth(false) to disable it.
func WithStealth(stealth bool) Option {
	return func(m *Manager) { m.stealth = stealth }
}

// WithProxy configures the upstream proxy for browsers created without an
// explicit proxy (host:port or scheme://host:port).
func WithProxy(proxy string) Option {
	return func(m *Manager) { m.proxy = proxy }
}

// WithProxyBypass configures the comma-separated hosts that skip the proxy.
func WithProxyBypass(bypass string) Option {
	return func(m *Manager) { m.proxyBypass = bypass }
}

// WithIgnoreCerts makes Chromium accept invalid TLS certificates, such as an
// intercept proxy's MITM CA. It is independent of WithProxy.
func WithIgnoreCerts(ignore bool) Option {
	return func(m *Manager) { m.ignoreCerts = ignore }
}

// WithExtraHeaders sets request headers sent with every request from every page
// of a browser created with this option, the first navigation included. The map
// is copied, so the caller may reuse or mutate its own.
func WithExtraHeaders(headers map[string]string) Option {
	return func(m *Manager) { m.extraHeaders = cloneHeaders(headers) }
}

// WithScope sets the navigation allowlist: bare hosts (which also cover their
// subdomains), leading "*." globs, "host:port" entries, or "*" for all. An empty
// list leaves navigation unrestricted unless WithScopeStrict is also set.
func WithScope(scope []string) Option {
	return func(m *Manager) { m.scope = normalizeScope(scope) }
}

// normalizeScope trims allowlist entries and drops blanks.
func normalizeScope(scope []string) []string {
	out := make([]string, 0, len(scope))
	for _, raw := range scope {
		if pattern := strings.TrimSpace(raw); pattern != "" {
			out = append(out, pattern)
		}
	}
	return out
}

// WithScopeStrict controls whether an empty allowlist denies every navigation.
// The default is false (an empty allowlist means no limit).
func WithScopeStrict(strict bool) Option {
	return func(m *Manager) { m.scopeStrict = strict }
}

// WithRateLimit sets the per-host request budget for browsers created with this
// option: RPS is the sustained requests per second and Burst is the token
// bucket depth. The default is DefaultRateRPS/DefaultRateBurst.
func WithRateLimit(limit RateLimit) Option {
	return func(m *Manager) { m.rateLimit = limit }
}

// WithBin configures a custom browser binary executable path (e.g. "/usr/bin/brave-browser",
// "brave", "google-chrome", or "chrome"). When empty, the launcher defaults to the
// downloaded Chromium binary.
func WithBin(binPath string) Option {
	return func(m *Manager) { m.binPath = binPath }
}

// WithProfileDir configures a custom user data profile directory path for the browser.
// The path is expected to be already resolved (absolute); it is always treated as persistent.
func WithProfileDir(profileDir string) Option {
	return func(m *Manager) { m.profileDir = profileDir }
}

// WithProfileName configures a profile name (or absolute path) that the engine
// resolves through pkg/config before launch. Bare names resolve under the
// browser-mcp profiles directory, never against the process working directory.
func WithProfileName(name string) Option {
	return func(m *Manager) { m.profileName = name }
}

// WithPersist controls profile persistence. As a manager option it applies to
// the default browser only; passed to CreateBrowser it applies to that browser.
// Unnamed secondary browsers otherwise get a unique ephemeral profile that is
// removed on close.
func WithPersist(persist bool) Option {
	return func(m *Manager) { m.persist = persist }
}

// WithPersistentDefault sets whether browsers created without an explicit
// persist choice get a persistent profile. It is the construction-time spelling
// of WithPersist that makes the daemon's default an explicit decision: the MCP
// server passes false (ephemeral profiles), the human CLI daemon passes true.
func WithPersistentDefault(persist bool) Option {
	return func(m *Manager) { m.persist = persist }
}

// WithPosition configures the initial window placement ("left", "right", "max", "normal").
func WithPosition(position string) Option {
	return func(m *Manager) { m.position = position }
}

// WithTTL sets the per-page idle timeout enforced by the manager's background
// sweeper: pages that go unused for longer than ttl are closed and removed,
// reclaiming leaked tabs. The default is 15 minutes; the sweep interval is
// derived from the TTL (see sweepIntervalFor). A non-positive ttl disables page
// sweeping; browser sweeping is governed separately by WithBrowserTTL.
func WithTTL(ttl time.Duration) Option {
	return func(m *Manager) { m.ttl = ttl }
}

// WithBrowserTTL sets the per-browser idle timeout enforced by the same
// background sweeper: a browser with no page access for longer than d is closed
// and removed, releasing its profile directory (ephemeral directories are
// deleted, persistent ones kept). The default is DefaultBrowserTTL (30
// minutes); a non-positive d disables browser sweeping.
func WithBrowserTTL(d time.Duration) Option {
	return func(m *Manager) { m.browserTTL = d }
}

// WithOwner sets the owner key recorded on a browser created by CreateBrowser,
// overriding the identity carried on the call's context. It is informational:
// browsers are closed by id, never gated on ownership.
func WithOwner(owner string) Option {
	return func(m *Manager) { m.owner = owner }
}

// WithLogger sets the logger used for the manager's best-effort background
// logging (currently the sweeper's page-close failures). The default is a
// no-op logger that discards everything, keeping pkg/browsers a quiet library
// unless the caller opts in; pass WithLogger(slog.Default()) to route to the
// process logger. A nil logger is ignored (the default no-op is kept).
func WithLogger(logger *slog.Logger) Option {
	return func(m *Manager) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// WithRawActions controls humanized pointer and keyboard timing for browsers
// created by the manager. The default (false) steps the pointer and pauses
// before and after click, click_at, input, hover, hover_at and press_key; true
// restores instant actions. It is the manager-level spelling of the
// WEB_MCP_RAW_ACTIONS environment hatch and can be overridden per call with
// browsers.WithRawCall.
func WithRawActions(raw bool) Option {
	return func(m *Manager) { m.rawActions = raw }
}

// NewLogger builds the logger every production entry point installs on a
// Manager with WithLogger: structured text records go to sink, and when console
// is non-nil each record is mirrored there too so a foreground run shows it
// while the log file keeps it. Unlike the engine's default discard logger, the
// result accepts info records, so a wired manager can never lose a browser
// close reason.
func NewLogger(sink, console io.Writer) *slog.Logger {
	if sink == nil {
		sink = io.Discard
	}
	writers := []io.Writer{sink}
	if console != nil {
		writers = append(writers, console)
	}
	return slog.New(slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// Manager owns the live Browsers of one daemon.
//
// Lifecycle:
//
//   - Browsers are launched lazily on first Open or created on demand via
//     CreateBrowser (through the go-rod launcher, headful by default).
//   - The background sweeper closes pages idle past the page TTL and browsers
//     idle past the browser TTL, releasing their profile directories.
//   - Close tears every Browser down so fresh ones can be opened later;
//     Shutdown is the terminal variant — after Shutdown the manager must not
//     be reused.
type Manager struct {
	// mu guards browsers map, shutdown, and the sweeper stop channel.
	mu sync.Mutex
	// browsers maps browser IDs (e.g. "browser-1", "browser-2") to live Browser instances.
	browsers map[BrowserID]*Browser
	// nextBrowserID is the counter used to mint fresh BrowserIDs.
	nextBrowserID int
	// flights holds the single in-flight create per launch key. Concurrent
	// callers with the same key join the leader's flight instead of launching a
	// second Chromium; guarded by mu. The leader deletes its entry on completion.
	flights map[launchKey]*createFlight
	// relaunched is set when the manager observed a Browser's Chromium
	// process die and (re)started it.
	relaunched bool
	// headless controls default launcher headless mode (default: headful).
	headless bool
	// stealth controls per-page stealth (default: on).
	stealth bool
	// binPath specifies a custom default browser executable path (e.g. Brave, Chrome).
	binPath string
	// profileDir specifies a custom default user profile directory path.
	profileDir string
	// profileName is the default profile name (or absolute path) resolved through
	// pkg/config for browsers created without an explicit WithProfileDir.
	profileName string
	// owner is the default owner key recorded on created browsers; empty means
	// the call's context identity (see WithOwnerKey) or UnownedOwner.
	owner string
	// persist keeps the DEFAULT browser's profile after close. Secondary
	// browsers start ephemeral unless their own CreateBrowser call opts in:
	// named and default profiles are shared, so inheriting this flag would
	// point a second live browser at the directory browser-1 already holds
	// (WEB-132).
	persist bool
	// profileRefs tracks how many live browsers own each profile directory,
	// whether the directory is ephemeral, and which browser claimed it.
	profileRefs map[string]*profileRef
	// position specifies default window placement ("left", "right", "max", "normal").
	position string
	// proxy is the default upstream proxy for browsers created without an
	// explicit proxy (host:port or scheme://host:port).
	proxy string
	// proxyBypass is the default comma-separated proxy bypass list.
	proxyBypass string
	// ignoreCerts makes Chromium accept invalid TLS certificates by default.
	ignoreCerts bool
	// extraHeaders are the default request headers for created browsers.
	extraHeaders map[string]string
	// scope is the default navigation allowlist for created browsers.
	scope []string
	// scopeStrict is the default strict-empty-allowlist behavior.
	scopeStrict bool
	// rateLimit is the default per-host request budget for created browsers.
	rateLimit RateLimit
	// shutdown marks the manager as terminated; Open rejects further calls.
	shutdown bool
	// ttl is the per-page idle timeout enforced by the background sweeper
	// (default: 15m; a non-positive value disables page sweeping).
	ttl time.Duration
	// browserTTL is the per-browser idle timeout enforced by the same sweeper
	// (default: DefaultBrowserTTL; a non-positive value disables browser
	// sweeping).
	browserTTL time.Duration
	// sweepInterval is how often the background sweeper runs; derived from the
	// shortest enabled TTL at construction (see sweepIntervalFor).
	sweepInterval time.Duration
	// sweepStop, when non-nil, is the stop channel of a running sweeper
	// goroutine; nil means no sweeper is running. Guarded by mu.
	sweepStop chan struct{}
	// probe checks whether a browser's Chromium process is still alive. It is a
	// field so tests can stub the CDP round trip; production uses probeBrowser.
	probe func(*Browser) error
	// launch starts a new Browser's Chromium process. It is a field so tests can
	// stub the launch; production launches through Browser.Launch.
	launch func(context.Context, *Browser) error
	// logger is the sink for the manager's best-effort background logging
	// (e.g. sweeper close failures). Defaults to a no-op discard logger;
	// inject one with WithLogger. Read-only after construction, so it is safe
	// to use without holding mu.
	logger *slog.Logger
	// baseCtx is the manager-owned, process-lifetime context every Browser's
	// rod default ctx is bound to (WEB-59): canceled only by Shutdown, so no
	// transient tool-call ctx can ever poison the browser (a poisoned default
	// ctx leaked ~100 MB per fetch_page and made closes report "context
	// canceled"). Relaunches after Close reuse it, so a fresh Browser is never
	// re-poisoned.
	baseCtx context.Context
	// rawActions is the default humanized-timing switch for created browsers:
	// false humanizes pointer and keyboard actions.
	rawActions bool
	// cancelBase releases baseCtx; called exactly once by Shutdown.
	cancelBase context.CancelFunc
}

// NewManager constructs a Manager with the given options applied over sensible
// defaults: headful Chromium, stealth on for every page, a 15-minute per-page
// idle TTL and a 30-minute per-browser idle TTL, both enforced by one
// background sweeper. Browsers are launched lazily or created on demand via
// CreateBrowser / Open.
func NewManager(opts ...Option) *Manager {
	// The manager-owned lifetime ctx: every Browser's rod default ctx hangs
	// off it (WEB-59), so no tool-call ctx can ever poison the browser. It is
	// canceled only by Shutdown — Close keeps it alive so a relaunch after
	// Close is never re-poisoned.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	m := &Manager{
		browsers:      make(map[BrowserID]*Browser),
		nextBrowserID: 1,
		flights:       make(map[launchKey]*createFlight),
		stealth:       true,
		persist:       true,
		ttl:           defaultTTL,
		browserTTL:    DefaultBrowserTTL,
		sweepInterval: sweepIntervalFor(defaultTTL, DefaultBrowserTTL),
		probe:         probeBrowser,
		launch:        func(ctx context.Context, b *Browser) error { return b.Launch(ctx) },
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		profileRefs:   make(map[string]*profileRef),
		rateLimit:     RateLimit{RPS: DefaultRateRPS, Burst: DefaultRateBurst},
		baseCtx:       baseCtx,
		cancelBase:    cancelBase,
	}
	for _, o := range opts {
		o(m)
	}
	// Re-derive the sweep interval after options so custom TTLs also get a
	// proportional polling cadence.
	m.sweepInterval = sweepIntervalFor(m.ttl, m.browserTTL)
	return m
}

// sweepIntervalFor derives the background sweep interval from the shortest
// enabled TTL: half that TTL, floored at 1s so even a very short TTL yields a
// sane polling cadence. With both TTLs disabled no sweeper starts, but the
// interval is still given a harmless 1s floor.
func sweepIntervalFor(pageTTL, browserTTL time.Duration) time.Duration {
	shortest := pageTTL
	switch {
	case shortest <= 0:
		shortest = browserTTL
	case browserTTL > 0 && browserTTL < shortest:
		shortest = browserTTL
	}
	return sweepIntervalForTTL(shortest)
}

// sweepIntervalForTTL derives the background sweep interval from a TTL: half
// the TTL, floored at 1s so even a very short TTL yields a sane polling
// cadence.
func sweepIntervalForTTL(ttl time.Duration) time.Duration {
	interval := ttl / 2
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

// DefaultBrowserID is the default BrowserID used when none is specified.
const DefaultBrowserID BrowserID = "browser-1"

// launchKey is the canonical identity of a browser launch request: two create
// requests with the same owner and the same key mean the same browser. The
// key is compared by value, so every launch-affecting option must be a field.
type launchKey struct {
	Owner       string
	Bin         string
	ProfileDir  string
	ProfileName string
	Persist     bool
	Headless    bool
	Stealth     bool
	Position    string
	Proxy       string
	ProxyBypass string
	IgnoreCerts bool
	Headers     string
	Scope       string
	ScopeStrict bool
	RateRPS     float64
	RateBurst   int
}

// launchKeyOf builds the canonical launch key from a manager's launch fields.
func launchKeyOf(owner string, m *Manager) launchKey {
	return launchKey{
		Owner:       owner,
		Bin:         m.binPath,
		ProfileDir:  m.profileDir,
		ProfileName: m.profileName,
		Persist:     m.persist,
		Headless:    m.headless,
		Stealth:     m.stealth,
		Position:    m.position,
		Proxy:       m.proxy,
		ProxyBypass: m.proxyBypass,
		IgnoreCerts: m.ignoreCerts,
		Headers:     canonicalHeaders(m.extraHeaders),
		Scope:       canonicalScope(m.scope),
		ScopeStrict: m.scopeStrict,
		RateRPS:     m.rateLimit.RPS,
		RateBurst:   m.rateLimit.Burst,
	}
}

// effectivePersist reports whether a create for id should use a persistent
// profile. A caller-requested persist always wins; otherwise the manager's
// persistent default applies to the default browser only — secondary browsers
// stay ephemeral so they never collide on a shared profile (WEB-132).
func effectivePersist(requested bool, id BrowserID, defaultPersist bool) bool {
	if id == DefaultBrowserID {
		return defaultPersist || requested
	}
	return requested
}

// Summary renders a compact, LLM-readable form of the launch request for
// browser listings.
func (k launchKey) Summary() string {
	profile := k.ProfileName
	if profile == "" {
		profile = k.ProfileDir
	}
	if profile == "" {
		profile = "default"
	}
	return fmt.Sprintf(
		"bin=%q headless=%t profile=%q persist=%t",
		k.Bin,
		k.Headless,
		profile,
		k.Persist,
	)
}

// Created is the outcome of a create request: the live browser and whether the
// call matched an existing one instead of launching a new process.
type Created struct {
	ID      BrowserID
	Browser *Browser
	Reused  bool
}

// createFlight is one in-flight create shared by every caller with the same
// launch key. The leader writes result and err, then closes done; each follower
// waits on done and reads the fields with the close as its happens-before edge.
type createFlight struct {
	done   chan struct{}
	result Created
	err    error
}

// CreateBrowser creates and launches a new Browser instance with custom options
// (e.g. WithBin, WithProfileDir, WithHeadless, WithStealth). It is the
// compatibility form of Create that drops the reuse flag.
func (m *Manager) CreateBrowser(ctx context.Context, opts ...Option) (BrowserID, *Browser, error) {
	created, err := m.Create(ctx, opts...)
	return created.ID, created.Browser, err
}

// Create creates and launches a new Browser instance with custom options
// (e.g. WithBin, WithProfileDir, WithHeadless, WithStealth). It assigns a fresh
// BrowserID (e.g. "browser-1", "browser-2") and registers it in the manager.
//
// Creation is idempotent: when a live browser already matches the same owner
// and canonical launch options (see launchKey), that browser is returned with
// Reused set instead of minting a new id. Concurrent creates with the same key
// join one single-flight: the leader launches while followers wait for its
// result, so one owner and one option set always map to one browser. No lock is
// held across the Chromium launch.
//
// The owner comes from WithOwner when given, then from the identity on ctx (see
// WithOwnerKey), then from UnownedOwner. A live browser already holding the
// resolved profile directory fails the create before any Chromium launch.
func (m *Manager) Create(ctx context.Context, opts ...Option) (Created, error) {
	effective, defaultProfileDir, defaultPersist, err := m.createOptions(opts)
	if err != nil {
		return Created{}, err
	}
	owner := effective.owner
	if owner == "" {
		owner = OwnerFrom(ctx)
	}
	key := launchKeyOf(owner, effective)
	if effective.proxy != "" {
		if _, _, err := parseProxy(effective.proxy); err != nil {
			return Created{}, err
		}
	}

	if b := m.reusableBrowser(ctx, key); b != nil {
		return Created{ID: b.id, Browser: b, Reused: true}, nil
	}

	flight, leader, err := m.joinCreate(key)
	if err != nil {
		return Created{}, err
	}
	if !leader {
		select {
		case <-flight.done:
			created := flight.result
			if flight.err == nil {
				created.Reused = true
			}
			return created, flight.err
		case <-ctx.Done():
			return Created{}, ctx.Err()
		}
	}

	created, err := m.launchCreatedBrowser(
		ctx,
		effective,
		owner,
		SessionOwnerFrom(ctx),
		key,
		defaultProfileDir,
		defaultPersist,
	)
	m.finishCreate(key, flight, created, err)
	return created, err
}

// createOptions snapshots the manager's launch defaults under the lock and
// applies the per-call options over them. It fails when the manager is shut
// down. The returned profile fields are the manager defaults Create needs to
// resolve the profile after the id is minted.
func (m *Manager) createOptions(opts []Option) (*Manager, string, bool, error) {
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil, "", false, errManagerShutdown
	}
	effective := &Manager{
		headless:     m.headless,
		stealth:      m.stealth,
		binPath:      m.binPath,
		profileName:  m.profileName,
		position:     m.position,
		proxy:        m.proxy,
		proxyBypass:  m.proxyBypass,
		ignoreCerts:  m.ignoreCerts,
		extraHeaders: cloneHeaders(m.extraHeaders),
		scope:        append([]string(nil), m.scope...),
		scopeStrict:  m.scopeStrict,
		rateLimit:    m.rateLimit,
		owner:        m.owner,
		browserTTL:   m.browserTTL,
		rawActions:   m.rawActions,
	}
	defaultProfileDir := m.profileDir
	defaultPersist := m.persist
	m.mu.Unlock()

	for _, o := range opts {
		o(effective)
	}
	return effective, defaultProfileDir, defaultPersist, nil
}

// joinCreate returns the in-flight create for key. The first caller becomes the
// leader and must launch; later callers wait on the shared done channel. The
// lock is held only to inspect and record the flight, never across a launch.
func (m *Manager) joinCreate(key launchKey) (*createFlight, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return nil, false, errManagerShutdown
	}
	if flight, ok := m.flights[key]; ok {
		return flight, false, nil
	}
	flight := &createFlight{done: make(chan struct{})}
	m.flights[key] = flight
	return flight, true, nil
}

// finishCreate publishes the leader's outcome to every follower and retires the
// flight under the manager lock, so a later create starts a fresh flight.
func (m *Manager) finishCreate(key launchKey, flight *createFlight, created Created, err error) {
	m.mu.Lock()
	flight.result = created
	flight.err = err
	if m.flights[key] == flight {
		delete(m.flights, key)
	}
	close(flight.done)
	m.mu.Unlock()
}

// launchCreatedBrowser reuses a matching live browser or launches and registers
// a fresh one. It is the leader-only body of Create: the caller owns the flight
// for key, so no concurrent create with the same options can run it. The
// Chromium launch happens with no manager lock held.
func (m *Manager) launchCreatedBrowser(
	ctx context.Context,
	effective *Manager,
	owner, sessionOwner string,
	key launchKey,
	defaultProfileDir string,
	defaultPersist bool,
) (Created, error) {
	if b := m.reusableBrowser(ctx, key); b != nil {
		return Created{ID: b.id, Browser: b, Reused: true}, nil
	}

	m.mu.Lock()
	id := BrowserID(fmt.Sprintf("browser-%d", m.nextBrowserID))
	m.nextBrowserID++
	for m.browsers[id] != nil {
		id = BrowserID(fmt.Sprintf("browser-%d", m.nextBrowserID))
		m.nextBrowserID++
	}
	m.mu.Unlock()

	persist := effectivePersist(effective.persist, id, defaultPersist)

	profileDir := effective.profileDir
	ephemeral := false
	if profileDir == "" {
		if effective.profileName == "" && id == DefaultBrowserID && defaultProfileDir != "" {
			profileDir = defaultProfileDir
		} else {
			resolved, eph, err := config.ResolveProfile(effective.profileName, effective.binPath, string(id), persist)
			if err != nil {
				return Created{}, fmt.Errorf("browsers: resolve profile for %s: %w", id, err)
			}
			profileDir, ephemeral = resolved, eph
		}
	}

	ownerLabel := OwnerLabelFrom(ctx)
	if err := m.retainProfile(profileDir, ephemeral, id, owner, ownerLabel); err != nil {
		return Created{}, err
	}

	b := NewBrowser(id, m.baseCtx, effective.headless, effective.stealth, effective.binPath, profileDir, effective.position)
	b.owner = owner
	b.sessionOwner = sessionOwner
	b.ownerLabel = ownerLabel
	b.key = key
	b.ttl = effective.browserTTL
	b.proxy = effective.proxy
	b.proxyBypass = effective.proxyBypass
	b.ignoreCerts = effective.ignoreCerts
	b.extraHeaders = cloneHeaders(effective.extraHeaders)
	b.scope = append([]string(nil), effective.scope...)
	b.scopeStrict = effective.scopeStrict
	b.limits = newHostLimiter(effective.rateLimit)
	b.logger = m.logger
	b.ephemeral = ephemeral
	b.rawActions = effective.rawActions
	b.profileCleanup = func() { m.releaseProfile(profileDir) }
	if err := m.launch(ctx, b); err != nil {
		m.releaseProfile(profileDir)
		return Created{}, fmt.Errorf("browsers: launch browser %s: %w", id, err)
	}

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		_ = b.Close(ctx)
		return Created{}, errManagerShutdown
	}
	m.browsers[id] = b
	m.startSweeperLocked()
	m.mu.Unlock()

	return Created{ID: id, Browser: b}, nil
}

// reusableBrowser returns the live browser whose canonical launch key matches
// key, or nil. A matching browser that fails its liveness probe is evicted and
// closed first, so a dead browser is never handed back as a successful create.
func (m *Manager) reusableBrowser(ctx context.Context, key launchKey) *Browser {
	m.mu.Lock()
	var candidate *Browser
	for _, b := range m.browsers {
		if b.key == key {
			candidate = b
			break
		}
	}
	m.mu.Unlock()
	if candidate == nil {
		return nil
	}

	if err := m.probe(candidate); err != nil {
		var dead bool
		m.mu.Lock()
		if cur, ok := m.browsers[candidate.id]; ok && cur == candidate {
			delete(m.browsers, candidate.id)
			dead = true
		}
		if len(m.browsers) == 0 {
			m.stopSweeperLocked()
		}
		m.mu.Unlock()
		if dead {
			m.logBrowserClosed(candidate, closeReasonCrash)
			_ = candidate.Close(ctx)
		}
		return nil
	}

	candidate.touch()
	return candidate
}

// ownerDefaultID resolves an empty browser id to a live browser the caller may
// use: its own browser first, then — unless the owner was explicitly chosen —
// the unowned default. It never returns a browser owned by another client.
func (m *Manager) ownerDefaultID(owner string, allowUnowned bool) (BrowserID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.preferredIDLocked(func(b *Browser) bool { return b.Owner() == owner }); ok {
		return id, true
	}
	if allowUnowned && owner != UnownedOwner {
		if id, ok := m.preferredIDLocked(func(b *Browser) bool { return b.Owner() == UnownedOwner }); ok {
			return id, true
		}
	}
	return "", false
}

// preferredIDLocked returns the default browser id when it matches, else any
// matching live browser id. The caller must hold m.mu.
func (m *Manager) preferredIDLocked(match func(*Browser) bool) (BrowserID, bool) {
	if b, ok := m.browsers[DefaultBrowserID]; ok && match(b) {
		return DefaultBrowserID, true
	}
	for id, b := range m.browsers {
		if match(b) {
			return id, true
		}
	}
	return "", false
}

// createForOwner mints a browser for a caller with no reusable default. Create
// uses the manager's persistent default, so an MCP manager yields an ephemeral
// profile while the human daemon keeps its persistent default.
func (m *Manager) createForOwner(ctx context.Context) (*Browser, error) {
	_, b, err := m.CreateBrowser(ctx)
	return b, err
}

// Open lazily opens or returns the default browser for the caller: its own
// browser first, then the unowned default, else a new browser for the caller.
func (m *Manager) Open(ctx context.Context) (*Browser, error) {
	return m.OpenBrowser(ctx, "")
}

// OpenBrowser lazily opens or returns a Browser with the specified ID.
// When id is empty it resolves to a live browser the caller may use: the
// caller's own browser first, then (unless the owner was explicitly chosen with
// WithExplicitOwner) the unowned default, else a new browser for the caller. A
// browser owned by another client is never returned. The caller's owner
// identity is recorded on a browser this call launches (see WithOwnerKey).
func (m *Manager) OpenBrowser(ctx context.Context, id BrowserID) (*Browser, error) {
	owner := OwnerFrom(ctx)
	sessionOwner := SessionOwnerFrom(ctx)

	if id == "" {
		resolved, ok := m.ownerDefaultID(owner, !HasExplicitOwner(ctx))
		if !ok {
			return m.createForOwner(ctx)
		}
		id = resolved
	}

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil, errManagerShutdown
	}

	if b, ok := m.browsers[id]; ok {
		m.mu.Unlock()
		if err := m.probe(b); err == nil {
			b.touch()
			return b, nil
		}
		m.mu.Lock()
		delete(m.browsers, id)
		m.relaunched = true
		m.mu.Unlock()
		m.logBrowserClosed(b, closeReasonCrash)
		_ = b.Close(ctx)
	} else {
		m.mu.Unlock()
	}

	m.mu.Lock()
	profileDir := ""
	if id == DefaultBrowserID {
		profileDir = m.profileDir
	}
	binPath := m.binPath
	headless := m.headless
	stealth := m.stealth
	position := m.position
	proxy := m.proxy
	proxyBypass := m.proxyBypass
	ignoreCerts := m.ignoreCerts
	extraHeaders := cloneHeaders(m.extraHeaders)
	scope := append([]string(nil), m.scope...)
	scopeStrict := m.scopeStrict
	rateLimit := m.rateLimit
	browserTTL := m.browserTTL
	rawActions := m.rawActions
	m.mu.Unlock()

	// Mirrors a bare CreateBrowser request so both paths share one launch key.
	key := launchKeyOf(owner, &Manager{
		binPath:      binPath,
		profileName:  "",
		headless:     headless,
		stealth:      stealth,
		position:     position,
		proxy:        proxy,
		proxyBypass:  proxyBypass,
		ignoreCerts:  ignoreCerts,
		extraHeaders: extraHeaders,
		scope:        scope,
		scopeStrict:  scopeStrict,
		rateLimit:    rateLimit,
	})

	if proxy != "" {
		if _, _, err := parseProxy(proxy); err != nil {
			return nil, err
		}
	}
	ephemeral := false
	if profileDir == "" {
		resolved, eph, err := config.ResolveProfile("", binPath, string(id), id == DefaultBrowserID)
		if err != nil {
			return nil, fmt.Errorf("browsers: resolve profile for %s: %w", id, err)
		}
		profileDir, ephemeral = resolved, eph
	}

	ownerLabel := OwnerLabelFrom(ctx)
	if err := m.retainProfile(profileDir, ephemeral, id, owner, ownerLabel); err != nil {
		m.mu.Lock()
		winner, ok := m.browsers[id]
		m.mu.Unlock()
		if ok {
			return winner, nil
		}
		return nil, err
	}

	b := NewBrowser(id, m.baseCtx, headless, stealth, binPath, profileDir, position)
	b.owner = owner
	b.sessionOwner = sessionOwner
	b.ownerLabel = ownerLabel
	b.key = key
	b.ttl = browserTTL
	b.proxy = proxy
	b.proxyBypass = proxyBypass
	b.ignoreCerts = ignoreCerts
	b.extraHeaders = extraHeaders
	b.scope = scope
	b.scopeStrict = scopeStrict
	b.limits = newHostLimiter(rateLimit)
	b.logger = m.logger
	b.ephemeral = ephemeral
	b.rawActions = rawActions
	b.profileCleanup = func() { m.releaseProfile(profileDir) }
	if err := m.launch(ctx, b); err != nil {
		m.releaseProfile(profileDir)
		return nil, fmt.Errorf("browsers: launch browser %s: %w", id, err)
	}

	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		_ = b.Close(ctx)
		return nil, errManagerShutdown
	}
	if winner, ok := m.browsers[id]; ok {
		m.mu.Unlock()
		_ = b.Close(ctx)
		return winner, nil
	}
	m.browsers[id] = b
	m.startSweeperLocked()
	m.mu.Unlock()
	return b, nil
}

// retainProfile records one more live browser owning dir. It fails when the
// directory is already owned by a live browser (Chromium cannot share one
// profile directory across processes): the error names the holder and its
// owner so the caller can close it or pick another profile.
func (m *Manager) retainProfile(dir string, ephemeral bool, id BrowserID, owner, ownerLabel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ref, ok := m.profileRefs[dir]
	if ok && ref.count > 0 {
		return fmt.Errorf(
			"profile %q is already in use by %s (owned by %s) — close it with browser_close {browser_id: %q} or pick a different profile name",
			dir,
			ref.browserID,
			ownerDisplay(ref.ownerLabel, ref.owner),
			ref.browserID,
		)
	}
	if !ok {
		ref = &profileRef{}
		m.profileRefs[dir] = ref
	}
	ref.count++
	ref.ephemeral = ref.ephemeral || ephemeral
	ref.browserID = id
	ref.owner = owner
	ref.ownerLabel = ownerLabel
	return nil
}

// ownerDisplay returns the human-readable owner label when one exists, else the
// owner key.
func ownerDisplay(label, key string) string {
	if label != "" {
		return label
	}
	return key
}

// releaseProfile releases one owner of dir. The directory is deleted once no
// live browser uses it and it was created as an ephemeral profile; cleanup
// failures are logged and never surfaced as close errors.
func (m *Manager) releaseProfile(dir string) {
	m.mu.Lock()
	ref, ok := m.profileRefs[dir]
	if !ok {
		m.mu.Unlock()
		return
	}
	ref.count--
	if ref.count > 0 {
		m.mu.Unlock()
		return
	}
	ephemeral := ref.ephemeral
	delete(m.profileRefs, dir)
	m.mu.Unlock()

	if !ephemeral {
		return
	}
	// Chromium can still write a singleton file for a short time after the CDP
	// close returns. Retry until the directory stays gone, so an ephemeral
	// profile does not survive as an empty shell.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.RemoveAll(dir)
		if err == nil {
			if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
				return
			}
		}
		if time.Now().After(deadline) {
			m.logger.Warn("browsers: failed to remove ephemeral profile",
				slog.String("profile_dir", dir),
				slog.String("error", fmt.Sprint(err)),
			)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Browser returns the live Browser with the given ID, or the default/first open
// Browser when id is empty. Does NOT lazily launch a new browser.
func (m *Manager) Browser(id BrowserID) (*Browser, error) {
	m.mu.Lock()
	var target *Browser
	if id == "" {
		if b, ok := m.browsers[DefaultBrowserID]; ok {
			target = b
		} else {
			for _, b := range m.browsers {
				target = b
				break
			}
		}
	} else {
		target = m.browsers[id]
	}
	running := m.runningIDsLocked()
	m.mu.Unlock()

	if target == nil {
		if id == "" {
			return nil, fmt.Errorf("no browser running (create one with browser_create): %w", ErrUnknownBrowser)
		}
		return nil, fmt.Errorf("browser %q not found (running: %s): %w", id, running, ErrUnknownBrowser)
	}

	if err := m.probe(target); err != nil {
		m.mu.Lock()
		delete(m.browsers, target.id)
		m.relaunched = true
		if len(m.browsers) == 0 {
			m.stopSweeperLocked()
		}
		m.mu.Unlock()
		m.logBrowserClosed(target, closeReasonCrash)
		closeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		_ = target.Close(closeCtx)
		return nil, fmt.Errorf("browsers: browser %s died", target.id)
	}
	target.touch()
	return target, nil
}
func probeBrowser(b *Browser) error {
	b.mu.Lock()
	rb := b.rod
	b.mu.Unlock()
	if rb == nil {
		return errors.New("browsers: browser not launched")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	_, err := rb.Context(probeCtx).Version()
	return err
}

// Browsers returns a snapshot map of all currently open Browser instances.
func (m *Manager) Browsers() map[BrowserID]*Browser {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[BrowserID]*Browser, len(m.browsers))
	for id, b := range m.browsers {
		result[id] = b
	}
	return result
}

// Sessions returns a read-only snapshot of every live page across every live
// browser and identity, sorted by page id. Each entry reports the tab's live URL
// and title, falling back to the stored URL with Stale set when the tab is busy
// or unreadable. A non-empty identity narrows the result to that identity and
// fails with every live name when it matches none. It never launches, creates,
// or probes a browser: listing cannot start a Chromium process.
func (m *Manager) Sessions(identity string) ([]PageInfo, error) {
	live := m.Browsers()
	pages := make([]PageInfo, 0, len(live))
	for _, b := range live {
		pages = append(pages, b.Pages()...)
	}

	identity = strings.TrimSpace(identity)
	if identity == "" {
		sortPageInfos(pages)
		return pages, nil
	}

	names := liveIdentityNames(live)
	if !hasIdentity(names, identity) {
		return nil, fmt.Errorf("browsers: unknown identity %q (known: %s)", identity, namesOrNone(names))
	}
	filtered := make([]PageInfo, 0, len(pages))
	for _, p := range pages {
		if p.Context == identity {
			filtered = append(filtered, p)
		}
	}
	sortPageInfos(filtered)
	return filtered, nil
}

// sortPageInfos orders a page snapshot by page id so listings are stable.
func sortPageInfos(pages []PageInfo) {
	sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
}

// liveIdentityNames returns the sorted identity names live in at least one
// browser, always including "default" (the parent's own cookie jar) for every
// live browser. No live browser means no names.
func liveIdentityNames(live map[BrowserID]*Browser) []string {
	seen := make(map[string]struct{})
	for _, b := range live {
		b.mu.Lock()
		names := b.identityNamesLocked()
		b.mu.Unlock()
		for _, name := range names {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// hasIdentity reports whether name is one of names.
func hasIdentity(names []string, name string) bool {
	for _, live := range names {
		if live == name {
			return true
		}
	}
	return false
}

// namesOrNone renders a name list for an error message, "none" when empty.
func namesOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// runningIDsLocked returns a sorted comma-separated list of live browser ids
// with their owner labels, or "none" when nothing is running. The caller must
// hold m.mu.
func (m *Manager) runningIDsLocked() string {
	ids := make([]string, 0, len(m.browsers))
	for id, b := range m.browsers {
		ids = append(ids, fmt.Sprintf("%s (owner: %s)", id, b.OwnerLabel()))
	}
	if len(ids) == 0 {
		return "none"
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// PurgeProfile closes the live browser id (when still running) and deletes its
// profile directory. It refuses any path that resolves outside the config
// profiles directory. This is the only path that deletes a named persistent
// profile; prune never touches one.
func (m *Manager) PurgeProfile(ctx context.Context, id BrowserID) error {
	m.mu.Lock()
	b, ok := m.browsers[id]
	running := m.runningIDsLocked()
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("browser %q not found (running: %s): %w", id, running, ErrUnknownBrowser)
	}

	dir := b.ProfileDir()
	checked, err := profileDirWithinProfiles(dir)
	if err != nil {
		return err
	}

	closeErr := m.CloseBrowser(ctx, id)
	if err := os.RemoveAll(checked); err != nil {
		return errors.Join(closeErr, fmt.Errorf("browsers: purge profile %q: %w", checked, err))
	}
	return closeErr
}

// PruneProfiles closes every live browser, then removes leftover directories
// under the profiles .ephemeral directory that no live browser owns. Named
// persistent profiles live outside .ephemeral and are never touched.
func (m *Manager) PruneProfiles(ctx context.Context) ([]string, error) {
	closeErr := m.CloseBrowser(ctx, "")

	profiles, err := config.ProfilesDir()
	if err != nil {
		return nil, errors.Join(closeErr, err)
	}
	root, err := profileDirWithinProfiles(filepath.Join(profiles, ".ephemeral"))
	if err != nil {
		return nil, errors.Join(closeErr, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, closeErr
		}
		return nil, errors.Join(closeErr, fmt.Errorf("browsers: read ephemeral profiles %q: %w", root, err))
	}

	var removed []string
	var pruneErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if m.profileInUse(dir) {
			continue
		}
		checked, err := profileDirWithinProfiles(dir)
		if err != nil {
			pruneErr = errors.Join(pruneErr, err)
			continue
		}
		if err := os.RemoveAll(checked); err != nil {
			pruneErr = errors.Join(pruneErr, fmt.Errorf("browsers: prune profile %q: %w", checked, err))
			continue
		}
		removed = append(removed, checked)
	}
	return removed, errors.Join(closeErr, pruneErr)
}

// profileInUse reports whether a live browser currently owns dir.
func (m *Manager) profileInUse(dir string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.profileRefs[dir]
	return ok
}

// profileDirWithinProfiles resolves dir and verifies it is strictly inside the
// config profiles directory, returning the cleaned absolute path. It refuses
// the profiles root itself and anything that resolves outside it.
func profileDirWithinProfiles(dir string) (string, error) {
	profiles, err := config.ProfilesDir()
	if err != nil {
		return "", fmt.Errorf("browsers: resolve profiles directory: %w", err)
	}
	root, err := filepath.Abs(profiles)
	if err != nil {
		return "", fmt.Errorf("browsers: resolve profiles directory: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("browsers: resolve profile path %q: %w", dir, err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf(
			"browsers: refuse to delete profile %q: it resolves outside the profiles directory %q — only profiles under %s/profiles can be purged",
			dir,
			root,
			config.DirName,
		)
	}
	return abs, nil
}

// Page resolves the live page with the given PageID inside the declared browser
// or owner. browserID pins the lookup to one browser; owner restricts it to
// browsers carrying that owner; when neither is given the id resolves only if
// exactly one browser is live. A page held outside the declaration is never
// returned. A cold manager keeps the historical lazy-open behavior for the
// unqualified single-browser path.
func (m *Manager) Page(
	ctx context.Context,
	id PageID,
	browserID BrowserID,
	owner string,
) (*Page, error) {
	if browserID == "" && owner == "" && len(m.Browsers()) == 0 {
		if _, err := m.Open(ctx); err != nil {
			return nil, err
		}
	}
	b, err := m.resolvePage(id, browserID, owner)
	if err != nil {
		return nil, err
	}
	return b.Page(ctx, id)
}

// resolvePage resolves id inside the declared browser or owner. The order is an
// explicit browser_id, then an explicit owner, then the only live browser. Two
// or more live browsers without a qualifier are ambiguous and refused; an id
// held outside the declared owner is reported as not found with the holding
// browsers named and no page content.
func (m *Manager) resolvePage(
	id PageID,
	browserID BrowserID,
	owner string,
) (*Browser, error) {
	if browserID != "" {
		return m.resolvePageIn(id, browserID)
	}

	m.mu.Lock()
	live := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		live = append(live, b)
	}
	relaunched := m.relaunched
	m.mu.Unlock()

	sort.Slice(live, func(i, j int) bool { return live[i].id < live[j].id })

	if owner != "" {
		holders := make([]*Browser, 0, 2)
		foreign := make([]*Browser, 0, 2)
		for _, b := range live {
			if _, _, ok := b.lookupPage(id); !ok {
				continue
			}
			if b.Owner() == owner {
				holders = append(holders, b)
				continue
			}
			foreign = append(foreign, b)
		}
		switch len(holders) {
		case 1:
			return holders[0], nil
		case 0:
			if len(foreign) > 0 {
				return nil, foreignSessionError(id, owner, foreign)
			}
			return nil, m.pageLookupError(id, relaunched)
		default:
			return nil, ambiguousSessionError(id, holders)
		}
	}

	if len(live) >= 2 {
		return nil, &PageLookupError{
			ID:   id,
			Kind: PageLookupAmbiguous,
			Message: fmt.Sprintf(
				"session %q needs a browser — %d browsers are live: %s; pass browser_id or owner",
				id,
				len(live),
				browserOwners(live),
			),
		}
	}
	if len(live) == 1 {
		if _, _, ok := live[0].lookupPage(id); !ok {
			return nil, m.pageLookupError(id, relaunched)
		}
		return live[0], nil
	}
	return nil, m.pageLookupError(id, relaunched)
}

// resolvePageIn resolves id inside the explicitly named browser, crossing the
// owner declaration on purpose. An unknown browser or page is refused.
func (m *Manager) resolvePageIn(id PageID, browserID BrowserID) (*Browser, error) {
	m.mu.Lock()
	target := m.browsers[browserID]
	running := m.runningIDsLocked()
	m.mu.Unlock()

	if target == nil {
		return nil, fmt.Errorf(
			"browser %q not found (running: %s): %w",
			browserID,
			running,
			ErrUnknownBrowser,
		)
	}
	if _, _, ok := target.lookupPage(id); !ok {
		return nil, &PageLookupError{
			ID:   id,
			Kind: PageLookupMissing,
			Message: fmt.Sprintf(
				"session %q not found in browser %s (live: %s)",
				id,
				browserID,
				pageIDsOrNone(m.livePageIDs()),
			),
		}
	}
	return target, nil
}

// pageLookupError renders the not-found error for a session id, naming every
// live page id and explaining a pre-restart id.
func (m *Manager) pageLookupError(id PageID, relaunched bool) error {
	live := pageIDsOrNone(m.livePageIDs())
	message := fmt.Sprintf("session %q not found (live: %s)", id, live)
	if relaunched {
		message = fmt.Sprintf(
			"session %q no longer exists: browser was restarted; call fetch_page to re-open URLs (live: %s)",
			id,
			live,
		)
	}
	return &PageLookupError{ID: id, Kind: PageLookupMissing, Message: message}
}

// foreignSessionError reports that id exists only outside the declared owner,
// naming the holding browsers and owners without returning any page content.
func foreignSessionError(id PageID, owner string, holders []*Browser) error {
	return &PageLookupError{
		ID:   id,
		Kind: PageLookupForeign,
		Message: fmt.Sprintf(
			"session %q is not in owner %s — held by %s; pass browser_id to reach it on purpose",
			id,
			owner,
			browserOwners(holders),
		),
	}
}

// ambiguousSessionError reports that id cannot be pinned to one browser,
// naming each holder and owner so the caller can narrow it.
func ambiguousSessionError(id PageID, holders []*Browser) error {
	return &PageLookupError{
		ID:   id,
		Kind: PageLookupAmbiguous,
		Message: fmt.Sprintf(
			"session %q needs a browser — %d browsers hold it: %s; pass browser_id",
			id,
			len(holders),
			browserOwners(holders),
		),
	}
}

// browserOwners renders "browser-N (owner X)" entries for a lookup message.
func browserOwners(list []*Browser) string {
	listed := make([]string, len(list))
	for i, b := range list {
		listed[i] = fmt.Sprintf("%s (owner %s)", b.id, b.OwnerLabel())
	}
	return strings.Join(listed, ", ")
}

// PageClose reports how Manager.ClosePage carried out a close.
type PageClose struct {
	// Browser is the browser that owned the closed page.
	Browser BrowserID
	// Evicted reports that an operation still held the page after the bounded
	// wait, so only its registry entry was removed and its tab is left to the
	// browser teardown.
	Evicted bool
}

// ClosePage closes the live page id through the browser that holds it, without
// probing the tab. It resolves the id with the same qualifier-aware lookup as
// Page, so it never falls back to a default or guessed browser and never
// touches a page outside the declared browser or owner. An unknown id fails
// naming every live page id.
func (m *Manager) ClosePage(
	ctx context.Context,
	id PageID,
	browserID BrowserID,
	owner string,
) (PageClose, error) {
	b, err := m.resolvePage(id, browserID, owner)
	if err != nil {
		return PageClose{}, err
	}
	return m.closeResolvedPage(ctx, b, id)
}

// closeResolvedPage closes id through the browser resolved for it.
func (m *Manager) closeResolvedPage(ctx context.Context, b *Browser, id PageID) (PageClose, error) {
	pageOwner, _, ok := b.lookupPage(id)
	if !ok {
		return PageClose{}, fmt.Errorf(
			"session %q not found (live: %s)",
			id,
			pageIDsOrNone(m.livePageIDs()),
		)
	}

	released, err := pageOwner.closePage(ctx, id)
	if err != nil {
		return PageClose{Browser: pageOwner.id}, err
	}
	return PageClose{Browser: pageOwner.id, Evicted: !released}, nil
}

// livePageIDs returns every page id registered in a live browser or identity,
// read from the registry without probing a tab, sorted for stable messages.
func (m *Manager) livePageIDs() []PageID {
	m.mu.Lock()
	browsers := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		browsers = append(browsers, b)
	}
	m.mu.Unlock()

	var ids []PageID
	for _, b := range browsers {
		for _, owner := range b.pageOwners() {
			owner.mu.Lock()
			for id := range owner.pages {
				ids = append(ids, id)
			}
			owner.mu.Unlock()
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// pageIDsOrNone renders a page-id list for an error message, "none" when empty.
func pageIDsOrNone(ids []PageID) string {
	if len(ids) == 0 {
		return "none"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	return strings.Join(parts, ", ")
}

// logBrowserClosed records why a browser left the live registry. The reason
// distinguishes an explicit close, the idle sweep, a crashed Chromium process
// and shutdown, so a browser that vanished mid-run is never silent. Close
// reasons log at info and crashes at warn. The sink is the manager logger
// installed with WithLogger (NewLogger for production); the default discards.
func (m *Manager) logBrowserClosed(b *Browser, reason string, attrs ...slog.Attr) {
	level := slog.LevelInfo
	if reason == closeReasonCrash {
		level = slog.LevelWarn
	}
	base := []slog.Attr{
		slog.String("browser_id", string(b.id)),
		slog.String("reason", reason),
		slog.String("owner", b.OwnerLabel()),
	}
	m.logger.LogAttrs(
		context.Background(),
		level,
		"browsers: browser closed",
		append(base, attrs...)...,
	)
}

// CloseBrowser closes a specific Browser by BrowserID and all of its pages.
// If id is empty, it closes ALL open browsers. Each close is recorded on the
// manager log with reason browser_close.
func (m *Manager) CloseBrowser(ctx context.Context, id BrowserID) error {
	return m.closeBrowser(ctx, id, closeReasonExplicit)
}

// closeBrowser implements CloseBrowser with an explicit audit reason so
// shutdown records itself distinctly from a caller-driven close.
func (m *Manager) closeBrowser(ctx context.Context, id BrowserID, reason string) error {
	m.mu.Lock()
	if id == "" {
		m.stopSweeperLocked()
		targets := m.browsers
		m.browsers = make(map[BrowserID]*Browser)
		m.mu.Unlock()
		var firstErr error
		for _, b := range targets {
			m.logBrowserClosed(b, reason)
			if err := b.Close(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	b, ok := m.browsers[id]
	if !ok {
		running := m.runningIDsLocked()
		m.mu.Unlock()
		return fmt.Errorf("browser %q not found (running: %s): %w", id, running, ErrUnknownBrowser)
	}
	delete(m.browsers, id)
	if len(m.browsers) == 0 {
		m.stopSweeperLocked()
	}
	m.mu.Unlock()

	m.logBrowserClosed(b, reason)
	return b.Close(ctx)
}

// Close closes all open Browser instances and their pages.
func (m *Manager) Close(ctx context.Context) error {
	return m.CloseBrowser(ctx, "")
}

// Shutdown gracefully closes all browsers and cancels the manager context.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shutdown = true
	m.mu.Unlock()

	err := m.closeBrowser(ctx, "", closeReasonShutdown)
	m.cancelBase()
	return err
}

// startSweeperLocked launches the background TTL sweep goroutine unless one is
// already running. It must be called with m.mu held. With both TTLs
// non-positive, sweeping is disabled and no goroutine is started at all.
func (m *Manager) startSweeperLocked() {
	if m.ttl <= 0 && m.browserTTL <= 0 {
		return // sweeping disabled: no ticker goroutine at all
	}
	if m.sweepStop != nil {
		return // a sweeper is already running
	}
	stop := make(chan struct{})
	m.sweepStop = stop
	go m.sweep(stop)
}

// stopSweeperLocked stops a running TTL sweep goroutine. It is idempotent — a
// second call is a no-op — and must be called with m.mu held. The sweeper
// observes the closed stop channel on its next iteration and exits, so no
// goroutine is leaked.
func (m *Manager) stopSweeperLocked() {
	if m.sweepStop == nil {
		return
	}
	close(m.sweepStop)
	m.sweepStop = nil
}

// sweep runs the background TTL sweep loop until stop is closed: on every tick
// it closes browsers idle past the browser TTL and pages idle past the page TTL.
func (m *Manager) sweep(stop <-chan struct{}) {
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.sweepOnce(context.Background())
		}
	}
}

// sweepOnce closes every live browser idle longer than the browser TTL (which
// also closes its pages and releases its profile), then every remaining live
// page idle longer than the page TTL. Closing a browser removes it from the map
// under m.mu and runs Browser.Close strictly afterwards, outside every lock, so
// a slow teardown never blocks lookups. Ephemeral profiles are deleted by the
// existing releaseProfile path; persistent profiles stay on disk.
func (m *Manager) sweepOnce(ctx context.Context) {
	m.mu.Lock()
	browsers := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		browsers = append(browsers, b)
	}
	pageTTL := m.ttl
	browserTTL := m.browserTTL
	m.mu.Unlock()

	for _, b := range browsers {
		if browserTTL > 0 && m.sweepIdleBrowser(ctx, b, browserTTL) {
			continue
		}
		if pageTTL <= 0 {
			continue
		}
		m.sweepIdlePages(ctx, b, pageTTL)
	}
}

// sweepIdleBrowser closes b when it has been idle longer than ttl, reporting
// whether it was closed. The map entry is removed under m.mu and Browser.Close
// (which releases the profile, deleting an ephemeral directory) runs outside
// the lock. A browser touched after the first idle check is spared.
func (m *Manager) sweepIdleBrowser(ctx context.Context, b *Browser, ttl time.Duration) bool {
	if b.idleFor() <= ttl {
		return false
	}

	m.mu.Lock()
	if cur, ok := m.browsers[b.id]; !ok || cur != b {
		m.mu.Unlock()
		return false
	}
	idle := b.idleFor()
	if idle <= ttl {
		m.mu.Unlock()
		return false
	}
	delete(m.browsers, b.id)
	if len(m.browsers) == 0 {
		m.stopSweeperLocked()
	}
	m.mu.Unlock()

	closeCtx, cancel := context.WithTimeout(ctx, teardownBudget)
	err := b.Close(closeCtx)
	cancel()

	m.logBrowserClosed(b, closeReasonSweep, slog.Duration("idle_for", idle))
	if err != nil {
		m.logger.Warn("sweeper: failed to close idle browser",
			slog.String("browser_id", string(b.id)),
			slog.String("error", err.Error()),
		)
	}
	return true
}

// sweepIdlePages closes every page of b that has been idle longer than ttl.
// Each candidate is removed from the pages map — and its rod handle captured
// and nilled — while still holding the browser and page locks, so the actual tab
// close happens strictly afterwards, outside any lock: a Browser.Page lookup
// racing the sweep either sees the page (and touches it, sparing it) or
// correctly reports "page not found" — never a page whose tab is mid-close.
// Each close is best-effort, bounded by closePageTimeout, and never retried, so
// a wedged tab cannot stall the sweep loop or resurface.
func (m *Manager) sweepIdlePages(ctx context.Context, b *Browser, ttl time.Duration) {
	for _, owner := range b.pageOwners() {
		m.sweepIdleOwnerPages(ctx, owner, ttl)
	}
}

// sweepIdleOwnerPages closes the idle pages of one owner — the parent browser or
// one identity clone — using that owner's proxy-auth and header cleanup state.
func (m *Manager) sweepIdleOwnerPages(ctx context.Context, b *Browser, ttl time.Duration) {
	// Snapshot the idle candidates under the browser mutex. Idle time is
	// atomic, so this never waits on a page operation lock.
	b.mu.Lock()
	var idle []PageID
	for id, p := range b.pages {
		if p.idleFor() > ttl {
			idle = append(idle, id)
		}
	}
	b.mu.Unlock()

	idle = b.withPopupTree(idle)

	for _, id := range idle {
		var handle *rod.Page
		b.mu.Lock()
		p, ok := b.pages[id]
		var unlock func()
		if ok {
			unlock, ok = p.TryLock()
		}
		if ok && p.idleFor() > ttl {
			p.stopObservation()
			handle = p.Rod
			p.Rod = nil
			delete(b.pages, id)
		}
		if unlock != nil {
			unlock()
		}
		b.mu.Unlock()
		if handle == nil {
			continue
		}

		p.stopDialogs()
		closeCtx, cancel := context.WithTimeout(ctx, closePageTimeout)
		b.stopProxyAuth(handle)
		b.stopHeaderCleanup(handle)
		err := closeRodPage(closeCtx, handle)
		cancel()
		if err != nil {
			m.logger.Warn("sweeper: failed to close idle page",
				slog.String("page_id", string(id)),
				slog.String("browser_id", string(b.id)),
				slog.String("error", err.Error()),
			)
		}
	}
}

// closeRodPage closes a *rod.Page handle captured by the sweeper. rod's Close
// has no ctx-aware form, so a ctx-bound clone honors the caller's timeout
// (closePageTimeout). The recover is a safety net for the sweeper path: the
// handle's page may be raced by an in-flight tool call that still holds a
// reference, and a concurrent close can surface as a panic; converting it to an
// error keeps the sweep loop alive.
func closeRodPage(ctx context.Context, handle *rod.Page) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("browsers: close page: panic: %v", r)
		}
	}()
	return handle.Context(ctx).Close()
}
