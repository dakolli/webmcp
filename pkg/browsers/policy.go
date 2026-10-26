package browsers

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/go-rod/rod"
)

// ScopeOpen is the browser_list scope value for a browser with no allowlist:
// navigation is unrestricted.
const ScopeOpen = "open"

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
	return fmt.Errorf(
		"web-mcp: host %q is outside the scope allowlist (allowed: %s; scope=%s, scope_strict=%t) — add it with browser_create scope=[%q]",
		host,
		allowed,
		state,
		strict,
		host,
	)
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
