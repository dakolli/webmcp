package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-rod/rod"
)

// SessionID uniquely identifies a live browser session owned by a
// SessionManager. It is minted by Open and referenced from every page_fetch /
// page_act / session_close call, e.g. "sess-01J5…".
type SessionID string

// Session represents a single live browser tab owned by a SessionManager.
//
// A session is created by SessionManager.Open (which lazily launches the shared
// browser on first use) and referenced by its unique SessionID thereafter.
// One live rod page maps to exactly one MCP session.
//
// Thread safety: rod Pages are NOT thread-safe, so all access to Page must be
// serialized with the session's mutex. Tools call Lock before touching the page
// and Unlock after the page call completes; SessionManager does the same for its
// own lifecycle operations (Close, sweep, Shutdown).
type Session struct {
	// ID uniquely identifies this session.
	ID SessionID
	// Page is the live rod page for this tab. Only valid while the session is open.
	Page *rod.Page
	// URL is the URL the page was last navigated to.
	URL string
	// Title is the document title at last read.
	Title string
	// CreatedAt is when the session was opened.
	CreatedAt time.Time
	// LastUsed is the most recent access time; it drives the idle-TTL sweep.
	LastUsed time.Time

	// mu serializes access to the underlying rod.Page (rod is not thread-safe).
	mu sync.Mutex
}

// Lock serializes access to the session's live page. Pair with Unlock.
func (s *Session) Lock() {
	s.mu.Lock()
}

// Unlock releases the per-session lock taken by Lock.
func (s *Session) Unlock() {
	s.mu.Unlock()
}

// SessionInfo is a read-only snapshot of a session's metadata, safe to copy and
// marshal. It deliberately excludes the live *rod.Page.
type SessionInfo struct {
	// ID uniquely identifies the session.
	ID SessionID `json:"id"`
	// URL is the URL the page was last navigated to.
	URL string `json:"url"`
	// Title is the document title at last read.
	Title string `json:"title"`
	// CreatedAt is when the session was opened.
	CreatedAt time.Time `json:"created_at"`
	// LastUsed is the most recent access time.
	LastUsed time.Time `json:"last_used"`
}

// SessionManager owns the shared *rod.Browser and the map of live sessions.
//
// Lifecycle:
//
//   - The browser is launched lazily on the first Open call (see launchBrowser)
//     and closed by Shutdown, which main() is expected to call on server
//     shutdown for graceful cleanup.
//   - Every session is a single browser tab. Per-session access is serialized
//     by the session mutex because rod Pages are not thread-safe; the map
//     itself is guarded by the manager mutex.
//   - Sessions idle longer than the configured TTL are closed and removed by
//     sweepExpired, invoked opportunistically from Open/Get.
//   - Close, CloseAll, Shutdown and sweepExpired all close the underlying tabs
//     so no rod resources leak.
type SessionManager struct {
	// mu guards the sessions map and the browser pointer.
	mu sync.Mutex
	// browser is the lazily-launched shared browser; nil until first Open.
	browser *rod.Browser
	// sessions maps session IDs to their live sessions.
	sessions map[SessionID]*Session
	// ttl is the idle timeout after which a session is eligible for cleanup.
	ttl time.Duration
	// headless controls whether the launcher runs Chromium headless.
	headless bool
	// browserURL, when set, connects to an already-running browser instead of
	// launching a new one.
	browserURL string
}

// Option configures a SessionManager at construction time. Apply via
// NewSessionManager(WithTTL(...), WithHeadless(...), ...).
type Option func(*SessionManager)

// WithTTL sets the idle time-to-live after which a session with no activity is
// closed and removed by the sweep. Zero or negative means "never sweep".
func WithTTL(ttl time.Duration) Option {
	return func(m *SessionManager) { m.ttl = ttl }
}

// WithHeadless controls whether Chromium runs in headless mode.
func WithHeadless(headless bool) Option {
	return func(m *SessionManager) { m.headless = headless }
}

// WithBrowserURL makes the manager connect to an already-running browser at the
// given WebSocket control URL instead of launching a new Chromium process.
func WithBrowserURL(url string) Option {
	return func(m *SessionManager) { m.browserURL = url }
}

// NewSessionManager constructs a SessionManager with the given options applied
// over sensible defaults (headless browser, 5-minute idle TTL, no sessions).
// The browser itself is NOT launched here — that happens lazily on the first
// Open call (see launchBrowser).
func NewSessionManager(opts ...Option) *SessionManager {
	m := &SessionManager{
		sessions: make(map[SessionID]*Session),
		ttl:      5 * time.Minute,
		headless: true,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// OpenOption configures an individual Open call.
type OpenOption func(*openOptions)

// openOptions carries the per-call settings gathered from OpenOptions.
type openOptions struct {
	width, height int
	userAgent     string
	timeout       time.Duration
}

// WithViewport sets the viewport size (CSS pixels) for the new session's page.
func WithViewport(width, height int) OpenOption {
	return func(o *openOptions) { o.width, o.height = width, height }
}

// WithUserAgent overrides the User-Agent header for the new session's page.
func WithUserAgent(ua string) OpenOption {
	return func(o *openOptions) { o.userAgent = ua }
}

// WithOpenTimeout bounds how long Open may spend navigating the page.
func WithOpenTimeout(d time.Duration) OpenOption {
	return func(o *openOptions) { o.timeout = d }
}

// Open creates a new browser session, navigates it to url, and returns the
// session for the caller to read and drive. This is the entry point behind the
// fetch_page tool.
//
// The browser is launched lazily on the first Open call (see launchBrowser);
// subsequent Opens reuse the same browser as new tabs.
func (m *SessionManager) Open(ctx context.Context, url string, opts ...OpenOption) (*Session, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   1. m.Lock(); if m.browser == nil { launchBrowser(ctx) }; m.Unlock()
	//   2. page, err := m.browser.Page(proto.TargetCreateTarget{})
	//   3. apply OpenOptions: page.SetViewport / page.SetUserAgent
	//   4. navigate + wait (delegated to the Fetch tool via waitForPage)
	//   5. mint a SessionID, store *Session in m.sessions, set CreatedAt/LastUsed
	//   6. return the session
	return nil, fmt.Errorf("SessionManager.Open: not implemented")
}

// Get returns the live session for id, refreshing its LastUsed timestamp.
// It returns an error if the session does not exist (or has been swept).
func (m *SessionManager) Get(ctx context.Context, id SessionID) (*Session, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow: lock the map, look up id, bump LastUsed, opportunistically
	// call sweepExpired when the manager mutex is available.
	return nil, fmt.Errorf("SessionManager.Get: not implemented")
}

// Close closes the session with the given id: it removes the session from the
// map and closes its underlying rod tab, releasing the browser tab.
func (m *SessionManager) Close(ctx context.Context, id SessionID) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow: lock the map, delete the entry, then closeSession(ctx, s)
	// to run page.Close() under the session's own mutex.
	return fmt.Errorf("SessionManager.Close: not implemented")
}

// CloseAll closes every live session, emptying the session map. It leaves the
// browser running so new sessions can still be opened.
func (m *SessionManager) CloseAll(ctx context.Context) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow: snapshot the session map under the manager mutex, then
	// closeSession each one; report the first error that occurs.
	return fmt.Errorf("SessionManager.CloseAll: not implemented")
}

// Shutdown closes every live session and then closes the shared browser itself.
// It is the graceful-teardown entry point, intended to be called from main()
// when the server shuts down. After Shutdown the manager must not be reused.
func (m *SessionManager) Shutdown(ctx context.Context) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow: CloseAll(ctx), then m.browser.Close(), then nil out the
	// browser pointer under the manager mutex.
	return fmt.Errorf("SessionManager.Shutdown: not implemented")
}

// List returns a snapshot of all live sessions ordered by creation time. The
// returned []SessionInfo is safe to marshal and iterate; it never exposes the
// live *rod.Page handles.
func (m *SessionManager) List(ctx context.Context) []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, SessionInfo{
			ID:        s.ID,
			URL:       s.URL,
			Title:     s.Title,
			CreatedAt: s.CreatedAt,
			LastUsed:  s.LastUsed,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})
	return infos
}

// launchBrowser lazily creates (and connects to) the shared browser via the
// go-rod launcher. It is a no-op when a browser is already running. Called with
// the manager mutex held by Open.
func (m *SessionManager) launchBrowser(ctx context.Context) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   var u string
	//   switch {
	//   case m.browserURL != "":
	//       u = m.browserURL                       // connect to a running browser
	//   default:
	//       l := launcher.New().Headless(m.headless).NoSandbox(true)
	//       u, err = l.Launch()                    // error-returning form; no Must*
	//   }
	//   m.browser = rod.New().ControlURL(u)
	//   return m.browser.Connect()                 // error-returning form
	return fmt.Errorf("SessionManager.launchBrowser: not implemented")
}

// sweepExpired closes and removes every session whose idle time (now minus
// LastUsed) exceeds the configured TTL. A non-positive TTL disables sweeping.
// Intended to be invoked opportunistically from Open/Get.
func (m *SessionManager) sweepExpired(ctx context.Context) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow: snapshot expired sessions under the manager mutex, then
	// closeSession each one outside the lock.
	return fmt.Errorf("SessionManager.sweepExpired: not implemented")
}

// closeSession closes a single session's underlying rod tab and removes it from
// the map. Shared by Close, CloseAll, Shutdown and sweepExpired.
func (m *SessionManager) closeSession(ctx context.Context, s *Session) error {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow: s.Lock(); err := s.Page.Close(); s.Unlock(); then delete
	// the session from the map under the manager mutex.
	return fmt.Errorf("SessionManager.closeSession: not implemented")
}
