package recon

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-call target caps. A larger sweep must use output_file and a narrower call.
const (
	// MaxHosts is the most hosts one call may target.
	MaxHosts = 500
	// MaxURLs is the most URLs one call may target.
	MaxURLs = 5000
	// MaxPorts is the most ports one call may scan.
	MaxPorts = 200
)

// Default per-host request budget, matching the browser engine's defaults.
const (
	// DefaultRPS is the sustained per-host request rate when none is set.
	DefaultRPS = 5.0
	// DefaultBurst is the per-host token bucket depth when none is set.
	DefaultBurst = 10
)

// Guard enforces the recon lane's consent, scope and rate limits. Scope is a
// host allowlist using the same vocabulary as browser_create: a bare host covers
// its subdomains, a leading "*." is accepted, and "*" allows everything. An
// empty scope allows everything unless ScopeStrict is set.
type Guard struct {
	// Scope is the host allowlist; empty means open unless ScopeStrict is set.
	Scope []string
	// ScopeStrict makes an empty allowlist deny every target.
	ScopeStrict bool
	// RPS is the sustained per-host request rate; a non-positive value uses
	// DefaultRPS.
	RPS float64
	// Burst is the per-host token bucket depth; a value below 1 uses DefaultBurst.
	Burst int

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// tokenBucket is one host's token count and the virtual time of its last
// reservation.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// ScopeError reports a target the allowlist does not cover.
type ScopeError struct {
	// Host is the target the allowlist refuses.
	Host string
	// Allowed is the rendered allowlist, or "none" for a strict empty list.
	Allowed string
}

// Error names the refused target and the allowlist that refuses it.
func (e *ScopeError) Error() string {
	return fmt.Sprintf(
		"recon: invalid arguments: host %q is outside the scope allowlist (allowed: %s) — add it to scope",
		e.Host,
		e.Allowed,
	)
}

// Hint is the one-line remediation for a scope refusal.
func (e *ScopeError) Hint() string {
	return fmt.Sprintf("the target is valid — add %q to scope, or pass scope:[%q], then retry", e.Host, e.Host)
}

// RateLimitError reports a request the per-host bucket could not admit before
// the caller's context expired.
type RateLimitError struct {
	// Host is the host with no token available.
	Host string
	// Wait is how long the next token needed.
	Wait time.Duration
	// Err is the context cause.
	Err error
}

// Error names the host, the remaining wait and the cause.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf(
		"recon: host %q is rate limited: the next request slot is %s away, beyond this call's deadline — lower rps, raise burst, or retry with a longer timeout: %v",
		e.Host,
		e.Wait.Round(time.Millisecond),
		e.Err,
	)
}

// Unwrap exposes the context cause so the envelope classifies a timeout.
func (e *RateLimitError) Unwrap() error { return e.Err }

// CheckHosts refuses a host list over MaxHosts, then refuses any host outside
// the scope. An entry that is a URL or host:port is reduced to its host first.
func (g *Guard) CheckHosts(hosts []string) error {
	if len(hosts) > MaxHosts {
		return capError(len(hosts), "hosts", MaxHosts)
	}
	for _, raw := range hosts {
		host := targetHost(raw)
		if host == "" {
			continue
		}
		if !g.allowsHost(host) {
			return g.scopeRefusal(host)
		}
	}
	return nil
}

// CheckURLs refuses a URL list over MaxURLs, then refuses any URL whose host is
// outside the scope. A URL with no host is skipped.
func (g *Guard) CheckURLs(urls []string) error {
	if len(urls) > MaxURLs {
		return capError(len(urls), "URLs", MaxURLs)
	}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("recon: invalid arguments: URL %q is not parseable: %w", raw, err)
		}
		host := strings.ToLower(u.Hostname())
		if host == "" {
			continue
		}
		if !g.allowsHost(host) {
			return g.scopeRefusal(host)
		}
	}
	return nil
}

// CheckPorts refuses a port list over MaxPorts, then refuses any entry that is
// not a TCP/UDP port number.
func (g *Guard) CheckPorts(ports []string) error {
	if len(ports) > MaxPorts {
		return capError(len(ports), "ports", MaxPorts)
	}
	for _, raw := range ports {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("recon: invalid arguments: port %q is not a number in 1..65535", raw)
		}
	}
	return nil
}

// CheckTargets classifies each target as a URL, a port or a host, applies the
// matching cap, and checks it against the scope. It is the one gate every
// surface calls before a scanner starts.
func (g *Guard) CheckTargets(targets []string) error {
	var hosts, urls, ports []string
	for _, raw := range targets {
		target := strings.TrimSpace(raw)
		if target == "" {
			continue
		}
		if u, err := url.Parse(target); err == nil && u.Scheme != "" && u.Host != "" {
			urls = append(urls, target)
			continue
		}
		if _, err := strconv.Atoi(target); err == nil {
			ports = append(ports, target)
			continue
		}
		hosts = append(hosts, target)
	}
	if err := g.CheckHosts(hosts); err != nil {
		return err
	}
	if err := g.CheckURLs(urls); err != nil {
		return err
	}
	return g.CheckPorts(ports)
}

// Wait blocks until host has a token or ctx ends. An empty host is unthrottled.
func (g *Guard) Wait(ctx context.Context, host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil
	}
	delay := g.reserve(host, time.Now())
	if delay <= 0 {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
		return &RateLimitError{Host: host, Wait: delay, Err: context.DeadlineExceeded}
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

// reserve claims the next slot for host and returns how long the caller must
// wait. It is the clock-injectable core behind Wait.
func (g *Guard) reserve(host string, now time.Time) time.Duration {
	rps, burst := g.rate()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buckets == nil {
		g.buckets = make(map[string]*tokenBucket)
	}
	b := g.buckets[host]
	if b == nil {
		b = &tokenBucket{tokens: burst, last: now}
		g.buckets[host] = b
	}
	base := now
	if b.last.After(base) {
		base = b.last
	}
	if b.tokens < burst {
		b.tokens = math.Min(burst, b.tokens+base.Sub(b.last).Seconds()*rps)
		b.last = base
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	delay := time.Duration((1 - b.tokens) / rps * float64(time.Second))
	b.tokens = 0
	b.last = b.last.Add(delay)
	return delay
}

// rate returns the effective per-host budget, applying the defaults.
func (g *Guard) rate() (float64, float64) {
	rps := g.RPS
	if !(rps > 0) || math.IsInf(rps, 0) || math.IsNaN(rps) {
		rps = DefaultRPS
	}
	burst := g.Burst
	if burst < 1 {
		burst = DefaultBurst
	}
	return rps, float64(burst)
}

// allowsHost reports whether the scope permits host.
func (g *Guard) allowsHost(host string) bool {
	if len(g.Scope) == 0 {
		return !g.ScopeStrict
	}
	return scopeAllowsHost(host, g.Scope)
}

// scopeRefusal builds the out-of-scope error naming the target and allowlist.
func (g *Guard) scopeRefusal(host string) error {
	allowed := "none"
	if len(g.Scope) > 0 {
		allowed = strings.Join(g.Scope, ", ")
	}
	return &ScopeError{Host: host, Allowed: allowed}
}

// targetHost reduces a bare host, host:port or URL to its lowercase host name.
func targetHost(raw string) string {
	target := strings.TrimSpace(raw)
	if target == "" {
		return ""
	}
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	if host, _, err := net.SplitHostPort(target); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.TrimSuffix(target, "."))
}

// scopeAllowsHost reports whether host matches any allowlist pattern. A bare
// host covers its subdomains, a leading "*." is accepted, and "*" allows all.
func scopeAllowsHost(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, raw := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}
		if pattern == "*" {
			return true
		}
		pattern = strings.TrimPrefix(pattern, "*.")
		patternHost := pattern
		if h, _, err := net.SplitHostPort(pattern); err == nil {
			patternHost = h
		}
		patternHost = strings.TrimSuffix(patternHost, ".")
		if host == patternHost || strings.HasSuffix(host, "."+patternHost) {
			return true
		}
	}
	return false
}

// capError renders an over-cap refusal with the count and the output_file hint.
func capError(count int, unit string, max int) error {
	return fmt.Errorf(
		"recon: invalid arguments: %d %s exceeds the %d-%s cap — split the call or pass output_file to write the full report to an absolute path",
		count,
		unit,
		max,
		unit,
	)
}
