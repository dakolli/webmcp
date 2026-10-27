package browsers

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
)

// ScopeOpen is the browser_list scope value for a browser with no allowlist:
// navigation is unrestricted.
const ScopeOpen = "open"

// DefaultRateRPS is the sustained per-host request rate a browser gets when no
// rate limit is configured.
const DefaultRateRPS = 5.0

// DefaultRateBurst is the per-host token bucket depth a browser gets when no
// rate limit is configured.
const DefaultRateBurst = 10

// RateLimit is a browser's per-host request budget: RPS is the sustained
// requests per second and Burst is how many requests may go out at once.
type RateLimit struct {
	// RPS is the sustained requests per second allowed to one host.
	RPS float64 `json:"rps"`
	// Burst is the token bucket depth, i.e. the simultaneous request allowance.
	Burst int `json:"burst"`
}

// Validate reports whether the limit can be enforced.
func (l RateLimit) Validate() error {
	if !(l.RPS > 0) || math.IsInf(l.RPS, 0) || math.IsNaN(l.RPS) {
		return fmt.Errorf("invalid arguments: rps must be a finite number greater than 0 (got %v)", l.RPS)
	}
	if l.Burst < 1 {
		return fmt.Errorf("invalid arguments: burst must be at least 1 (got %d)", l.Burst)
	}
	return nil
}

// HostLimit is one host's observed limit state in a browser listing.
type HostLimit struct {
	// Host is the host whose bucket has been used.
	Host string `json:"host"`
	// Throttled counts the requests to Host that had to wait for a token.
	Throttled int64 `json:"throttled"`
}

// LimitReport is the effective request policy of a browser: the configured
// budget plus the hosts whose buckets have seen traffic.
type LimitReport struct {
	// RPS is the sustained requests per second allowed to one host.
	RPS float64 `json:"rps"`
	// Burst is the token bucket depth.
	Burst int `json:"burst"`
	// Hosts lists the hosts with a live bucket, sorted by host.
	Hosts []HostLimit `json:"hosts"`
}

// RateLimitError reports a request the per-host token bucket could not admit
// before the caller's context expired.
type RateLimitError struct {
	// Host is the host that had no token available.
	Host string
	// Wait is how long the next token needed.
	Wait time.Duration
	// Err is the context cause.
	Err error
}

// Error names the host, the remaining wait and the fix. It wraps the context
// cause so callers can classify the failure as a timeout.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf(
		"web-mcp: host %q is rate limited: the next request slot is %s away, beyond this call's deadline — lower rps, raise burst, or retry with a longer timeout: %v",
		e.Host,
		e.Wait.Round(time.Millisecond),
		e.Err,
	)
}

// Unwrap exposes the context cause.
func (e *RateLimitError) Unwrap() error { return e.Err }

// Hint is the one-line remediation for a rate refusal: the two knobs that fit
// the wait, or a longer call budget. The arguments sent to the tool are valid.
func (e *RateLimitError) Hint() string {
	return "the per-host rate limiter held the call past its deadline — lower rps, raise burst, or retry with a longer timeout_ms"
}

// ScopeError reports a navigation or engine-issued request the browser's
// allowlist does not permit. The call arguments may be correct; the browser's
// scope must change.
type ScopeError struct {
	// Host is the host the allowlist does not cover.
	Host string
	// Allowed is the rendered allowlist, or "none" for a strict empty list.
	Allowed string
	// Scope is the live scope state.
	Scope string
	// Strict reports whether an empty allowlist denies every navigation.
	Strict bool
}

// Error names the host, the live scope state, the allowed hosts and the fix.
func (e *ScopeError) Error() string {
	return fmt.Sprintf(
		"web-mcp: invalid arguments: host %q is outside the scope allowlist (allowed: %s; scope=%s, scope_strict=%t) — add it with browser_create scope=[%q]",
		e.Host,
		e.Allowed,
		e.Scope,
		e.Strict,
		e.Host,
	)
}

// Hint is the one-line remediation for a scope refusal: the arguments are
// valid, so change the browser's allowlist instead of retrying the same call.
func (e *ScopeError) Hint() string {
	return fmt.Sprintf(
		"the arguments are valid — the browser's scope refuses host %q; call browser_create with scope including %q, or reuse a browser whose scope already allows it",
		e.Host,
		e.Host,
	)
}

// tokenBucket is one host's token count and the virtual time of its last
// reservation. A last time in the future queues further requests behind it.
type tokenBucket struct {
	tokens    float64
	last      time.Time
	throttled int64
}

// hostLimiter is a per-host token bucket shared by every page of one browser.
// now is a field so tests can drive the clock without sleeping.
type hostLimiter struct {
	mu    sync.Mutex
	rps   float64
	burst float64
	now   func() time.Time
	hosts map[string]*tokenBucket
}

// newHostLimiter builds a limiter for limit. A non-positive rps or burst
// disables enforcement.
func newHostLimiter(limit RateLimit) *hostLimiter {
	return &hostLimiter{
		rps:   limit.RPS,
		burst: float64(limit.Burst),
		now:   time.Now,
		hosts: make(map[string]*tokenBucket),
	}
}

// wait blocks until host has a token or ctx ends. It returns a RateLimitError
// naming the host and the remaining wait when the deadline cannot fit the wait.
func (l *hostLimiter) wait(ctx context.Context, host string) error {
	if l == nil || l.rps <= 0 || l.burst <= 0 {
		return nil
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil
	}
	delay, err := l.reserve(ctx, host)
	if err != nil || delay <= 0 {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return &RateLimitError{Host: host, Wait: delay, Err: ctx.Err()}
	}
}

// reserve claims the next slot for host, reporting how long the caller must
// wait. A wait the deadline cannot fit is refused before the slot is claimed.
func (l *hostLimiter) reserve(ctx context.Context, host string) (time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.hosts[host]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.hosts[host] = b
	}
	delay := l.pendingLocked(b, now)
	if delay > 0 {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
			return 0, &RateLimitError{Host: host, Wait: delay, Err: context.DeadlineExceeded}
		}
		b.throttled++
	}
	l.commitLocked(b, delay)
	return delay, nil
}

// pendingLocked refills the bucket up to now (or its queued reservation time)
// and returns the delay the next request needs without consuming a token.
func (l *hostLimiter) pendingLocked(b *tokenBucket, now time.Time) time.Duration {
	base := now
	if b.last.After(base) {
		base = b.last
	}
	if b.tokens < l.burst {
		b.tokens = math.Min(l.burst, b.tokens+base.Sub(b.last).Seconds()*l.rps)
		b.last = base
	}
	if b.tokens >= 1 {
		return 0
	}
	seconds := (1 - b.tokens) / l.rps
	return time.Duration(seconds * float64(time.Second))
}

// commitLocked consumes one token for an immediate request, or reserves the
// queued slot at the end of the bucket for a delayed one.
func (l *hostLimiter) commitLocked(b *tokenBucket, delay time.Duration) {
	if delay <= 0 {
		b.tokens--
		return
	}
	b.tokens = 0
	b.last = b.last.Add(delay)
}

// report renders the effective policy and the hosts that have seen traffic.
func (l *hostLimiter) report() LimitReport {
	if l == nil {
		return LimitReport{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	hosts := make([]HostLimit, 0, len(l.hosts))
	for host, b := range l.hosts {
		hosts = append(hosts, HostLimit{Host: host, Throttled: b.throttled})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Host < hosts[j].Host })
	return LimitReport{RPS: l.rps, Burst: int(l.burst), Hosts: hosts}
}

// ScopeStrictNone is the browser_list scope value for a strict browser whose
// allowlist is empty: every navigation is refused.
const ScopeStrictNone = "strict"

// Scope returns the browser's navigation allowlist state for listings and
// errors: ScopeOpen when unrestricted, ScopeStrictNone for a strict empty
// allowlist, else the configured patterns joined by ", ".
func (b *Browser) Scope() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return scopeState(b.scope, b.scopeStrict)
}

// ScopeStrict reports whether an empty allowlist denies every navigation.
func (b *Browser) ScopeStrict() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.scopeStrict
}

// CheckScope reports whether this browser may navigate to rawURL. A URL with
// no host (a relative navigation) is always allowed because it cannot leave the
// current origin. The allowlist is checked against the host and, when a pattern
// carries one, the effective port.
func (b *Browser) CheckScope(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("web-mcp: invalid URL %q: %w", rawURL, err)
	}
	if u.Hostname() == "" {
		return nil
	}
	return b.inScope(u)
}

// Limits returns the browser's effective request policy and the hosts whose
// buckets have seen traffic, so an operator can read the limit before a sweep.
func (b *Browser) Limits() LimitReport {
	b.mu.Lock()
	l := b.limits
	b.mu.Unlock()
	return l.report()
}

// CheckRequestScope reports whether an engine-issued request may reach rawURL.
// It applies the same allowlist as navigation, and is the gate every outbound
// tool request passes before it is sent.
func (b *Browser) CheckRequestScope(rawURL string) error {
	if b == nil {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid arguments: request URL %q is not parseable: %w", rawURL, err)
	}
	if u.Hostname() == "" {
		return nil
	}
	return b.inScope(u)
}

// awaitRequest enforces the allowlist and waits for a per-host token before an
// engine-issued request is sent. It never sends first and checks after.
func (b *Browser) awaitRequest(ctx context.Context, rawURL string) error {
	if err := b.CheckRequestScope(rawURL); err != nil {
		return err
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	b.mu.Lock()
	limits := b.limits
	b.mu.Unlock()
	return limits.wait(ctx, u.Hostname())
}

// CheckPageScope verifies the browser that owns pageID allows navigating to
// rawURL. An unknown page is left to the caller's own lookup error.
func (m *Manager) CheckPageScope(id PageID, rawURL string) error {
	m.mu.Lock()
	browsers := make([]*Browser, 0, len(m.browsers))
	for _, b := range m.browsers {
		browsers = append(browsers, b)
	}
	m.mu.Unlock()

	for _, b := range browsers {
		if owner, _, ok := b.lookupPage(id); ok {
			return owner.CheckScope(rawURL)
		}
	}
	return nil
}

// inScope applies the browser's allowlist to u, returning an actionable refusal
// naming the host, the live state, the allowed hosts and the fix. An empty
// allowlist is unrestricted unless scope_strict is set, in which case it denies
// every navigation.
func (b *Browser) inScope(u *url.URL) error {
	b.mu.Lock()
	scope := append([]string(nil), b.scope...)
	strict := b.scopeStrict
	b.mu.Unlock()

	if len(scope) > 0 && scopeAllowsHost(u, scope) {
		return nil
	}
	if len(scope) == 0 && !strict {
		return nil
	}
	return scopeRefusal(u.Hostname(), scope, strict)
}

// scopeAllowsHost reports whether u's host (and, for a host:port pattern, its
// effective port) matches any allowlist entry. A bare host covers its
// subdomains; a leading "*." is accepted as the same intent; "*" allows all.
func scopeAllowsHost(u *url.URL, patterns []string) bool {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := effectivePort(u)
	for _, raw := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}
		if pattern == "*" {
			return true
		}
		pattern = strings.TrimPrefix(pattern, "*.")
		patternHost, patternPort := pattern, ""
		if h, p, err := net.SplitHostPort(pattern); err == nil {
			patternHost, patternPort = h, p
		}
		patternHost = strings.TrimSuffix(patternHost, ".")
		if patternPort != "" && patternPort != port {
			continue
		}
		if host == patternHost || strings.HasSuffix(host, "."+patternHost) {
			return true
		}
	}
	return false
}

// effectivePort returns u's explicit port, else the scheme default.
func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

// scopeRefusal builds the pinned out-of-scope error: it names the host, the
// live scope state, the allowed hosts and the exact fix.
func scopeRefusal(host string, scope []string, strict bool) error {
	state := scopeState(scope, strict)
	allowed := state
	if len(scope) == 0 && strict {
		allowed = "none"
	}
	return &ScopeError{
		Host:    host,
		Allowed: allowed,
		Scope:   state,
		Strict:  strict,
	}
}

// scopeState renders the scope state for listings and errors.
func scopeState(scope []string, strict bool) string {
	if len(scope) == 0 {
		if strict {
			return ScopeStrictNone
		}
		return ScopeOpen
	}
	return strings.Join(scope, ", ")
}

// cloneHeaders copies a header map so a caller can mutate its own copy without
// changing the browser.
func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		out[name] = value
	}
	return out
}

// canonicalHeaders renders headers as a stable launch-key identity.
func canonicalHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+headers[name])
	}
	return strings.Join(parts, "\n")
}

// canonicalScope renders an allowlist as a stable launch-key identity,
// ignoring order, duplicates and blank entries.
func canonicalScope(scope []string) string {
	seen := make(map[string]struct{}, len(scope))
	patterns := make([]string, 0, len(scope))
	for _, raw := range scope {
		pattern := strings.ToLower(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	return strings.Join(patterns, "\n")
}

// applyExtraHeaders installs the browser's extra request headers on a freshly
// created page, before any navigation, and returns rod's cleanup.
func (b *Browser) applyExtraHeaders(ctx context.Context, page *rod.Page) (func(), error) {
	b.mu.Lock()
	headers := cloneHeaders(b.extraHeaders)
	b.mu.Unlock()
	if len(headers) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	dict := make([]string, 0, len(names)*2)
	for _, name := range names {
		dict = append(dict, name, headers[name])
	}

	cleanup, err := page.Context(ctx).SetExtraHeaders(dict)
	if err != nil {
		return nil, fmt.Errorf("browsers: set extra headers: %w", err)
	}
	return cleanup, nil
}

// trackHeaderCleanup records rod's per-page header cleanup for teardown.
func (b *Browser) trackHeaderCleanup(page *rod.Page, cleanup func()) {
	if cleanup == nil {
		return
	}
	b.mu.Lock()
	b.headerCleanups[page] = cleanup
	b.mu.Unlock()
}

// stopHeaderCleanup runs and forgets a page's header cleanup, if any.
func (b *Browser) stopHeaderCleanup(page *rod.Page) {
	if page == nil {
		return
	}
	b.mu.Lock()
	cleanup := b.headerCleanups[page]
	delete(b.headerCleanups, page)
	b.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}
