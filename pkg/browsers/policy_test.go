package browsers

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInScope(t *testing.T) {
	tests := []struct {
		name    string
		scope   []string
		strict  bool
		target  string
		wantErr bool
	}{
		{"no allowlist is open", nil, false, "https://evil.example/path", false},
		{"strict empty denies", nil, true, "https://example.com", true},
		{"bare host exact", []string{"example.com"}, false, "https://example.com/a", false},
		{"bare host subdomain", []string{"example.com"}, false, "https://api.example.com", false},
		{"bare host lookalike", []string{"example.com"}, false, "https://badexample.com", true},
		{"wildcard glob apex", []string{"*.example.com"}, false, "https://example.com", false},
		{"wildcard glob subdomain", []string{"*.example.com"}, false, "https://www.example.com", false},
		{"port match", []string{"example.com:8443"}, false, "https://example.com:8443", false},
		{"port mismatch", []string{"example.com:8443"}, false, "https://example.com", true},
		{"default https port", []string{"example.com:443"}, false, "https://example.com", false},
		{"star allows all", []string{"*"}, true, "https://anything.test", false},
		{"case and trailing dot", []string{"Example.COM"}, false, "https://API.example.com.", false},
		{"other listed host", []string{"example.com", "iana.org"}, false, "https://www.iana.org", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tt.target)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.target, err)
			}
			b := &Browser{scope: tt.scope, scopeStrict: tt.strict}
			err = b.inScope(u)
			if tt.wantErr && err == nil {
				t.Fatalf("inScope(%q) = nil, want refusal", tt.target)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("inScope(%q) error = %v, want nil", tt.target, err)
			}
		})
	}
}

func TestScopeRefusalNamesHostStateAndFix(t *testing.T) {
	b := &Browser{scope: []string{"example.com"}, scopeStrict: false}
	err := b.CheckScope("https://evil.test/x")
	if err == nil {
		t.Fatal("CheckScope(evil.test) = nil, want refusal")
	}
	for _, want := range []string{
		`host "evil.test" is outside the scope allowlist`,
		"allowed: example.com",
		"scope=example.com",
		"scope_strict=false",
		`add it with browser_create scope=["evil.test"]`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
}

// TestScopeErrorHintNamesThePolicy pins the WEB-226 rule: a scope refusal is a
// policy decision, so its hint names browser_create and scope instead of the
// schema remediation that sends the agent to retry the same call.
func TestScopeErrorHintNamesThePolicy(t *testing.T) {
	b := &Browser{scope: []string{"example.com"}, scopeStrict: false}
	err := b.CheckScope("https://evil.test/x")
	var refused *ScopeError
	if !errors.As(err, &refused) {
		t.Fatalf("CheckScope() error = %v, want *ScopeError", err)
	}
	hint := refused.Hint()
	for _, want := range []string{"browser_create", "scope"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
	if strings.Contains(hint, "InputSchema") {
		t.Errorf("hint %q carries the schema remediation, want the policy fix", hint)
	}
}

// TestRateLimitErrorHintNamesTheKnobs pins the other per-browser policy refusal:
// the hint names rps and burst, never the schema remediation.
func TestRateLimitErrorHintNamesTheKnobs(t *testing.T) {
	err := &RateLimitError{Host: "example.test", Wait: time.Second, Err: context.DeadlineExceeded}
	hint := err.Hint()
	for _, want := range []string{"rps", "burst"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
	if strings.Contains(hint, "InputSchema") {
		t.Errorf("hint %q carries the schema remediation, want the policy fix", hint)
	}
}

func TestCheckScopeAllowsRelativeNavigation(t *testing.T) {
	b := &Browser{scope: []string{"example.com"}}
	if err := b.CheckScope("/account"); err != nil {
		t.Fatalf("CheckScope(relative) error = %v, want nil", err)
	}
}

func TestScopeState(t *testing.T) {
	tests := []struct {
		name   string
		scope  []string
		strict bool
		want   string
	}{
		{"open", nil, false, ScopeOpen},
		{"strict empty", nil, true, ScopeStrictNone},
		{"patterns", []string{"example.com", "*.iana.org"}, false, "example.com, *.iana.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Browser{scope: tt.scope, scopeStrict: tt.strict}
			if got := b.Scope(); got != tt.want {
				t.Errorf("Scope() = %q, want %q", got, tt.want)
			}
			if got := b.ScopeStrict(); got != tt.strict {
				t.Errorf("ScopeStrict() = %t, want %t", got, tt.strict)
			}
		})
	}
}

func TestCheckRequestScope(t *testing.T) {
	tests := []struct {
		name    string
		scope   []string
		strict  bool
		target  string
		wantErr bool
	}{
		{"open allows any host", nil, false, "https://evil.example/a", false},
		{"strict empty denies", nil, true, "https://example.com", true},
		{"listed host allows", []string{"example.com"}, false, "https://api.example.com", false},
		{"unlisted host denies", []string{"example.com"}, false, "https://evil.example", true},
		{"data URL has no host and is allowed", []string{"example.com"}, false, "data:text/plain,hi", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &Browser{scope: tt.scope, scopeStrict: tt.strict}
			err := b.CheckRequestScope(tt.target)
			if tt.wantErr && err == nil {
				t.Fatalf("CheckRequestScope(%q) = nil, want refusal", tt.target)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("CheckRequestScope(%q) error = %v, want nil", tt.target, err)
			}
		})
	}
}

func TestRateLimitValidate(t *testing.T) {
	tests := []struct {
		name    string
		limit   RateLimit
		wantErr bool
	}{
		{name: "default is valid", limit: RateLimit{RPS: DefaultRateRPS, Burst: DefaultRateBurst}},
		{name: "zero rps is refused", limit: RateLimit{RPS: 0, Burst: 1}, wantErr: true},
		{name: "negative rps is refused", limit: RateLimit{RPS: -1, Burst: 1}, wantErr: true},
		{name: "zero burst is refused", limit: RateLimit{RPS: 1, Burst: 0}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.limit.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestHostLimiterSpacesRequestsPerHost(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	l := newHostLimiter(RateLimit{RPS: 1, Burst: 1})
	l.now = func() time.Time { return now }
	ctx := context.Background()

	if delay, err := l.reserve(ctx, "example.com"); err != nil || delay != 0 {
		t.Fatalf("first reserve = %s/%v, want 0/nil", delay, err)
	}
	if delay, err := l.reserve(ctx, "example.com"); err != nil || delay != time.Second {
		t.Fatalf("second reserve = %s/%v, want 1s/nil", delay, err)
	}
	if delay, err := l.reserve(ctx, "other.test"); err != nil || delay != 0 {
		t.Fatalf("other host reserve = %s/%v, want 0/nil (buckets are per host)", delay, err)
	}

	now = now.Add(2 * time.Second)
	if delay, err := l.reserve(ctx, "example.com"); err != nil || delay != 0 {
		t.Fatalf("reserve after the interval = %s/%v, want 0/nil", delay, err)
	}
}

func TestHostLimiterRefusesWaitBeyondDeadline(t *testing.T) {
	l := newHostLimiter(RateLimit{RPS: 1, Burst: 1})
	if err := l.wait(context.Background(), "example.com"); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := l.wait(ctx, "example.com")
	if err == nil {
		t.Fatal("wait() = nil, want a rate-limit refusal")
	}
	var limited *RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("wait() error = %v, want *RateLimitError", err)
	}
	if limited.Host != "example.com" || limited.Wait < 900*time.Millisecond {
		t.Fatalf("RateLimitError = host %q wait %s, want example.com and about 1s", limited.Host, limited.Wait)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait() error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	for _, want := range []string{"rate limited", "example.com", "next request slot"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
}

func TestHostLimiterWaitsThenProceeds(t *testing.T) {
	l := newHostLimiter(RateLimit{RPS: 100, Burst: 1})
	ctx := context.Background()
	if err := l.wait(ctx, "example.com"); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	start := time.Now()
	if err := l.wait(ctx, "example.com"); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 8*time.Millisecond {
		t.Fatalf("second wait returned after %s, want it to wait for the next token", elapsed)
	}
	if got := l.report(); len(got.Hosts) != 1 || got.Hosts[0].Throttled != 1 {
		t.Fatalf("report = %+v, want one throttled host", got)
	}
}

func TestLimitReport(t *testing.T) {
	l := newHostLimiter(RateLimit{RPS: 2, Burst: 3})
	l.now = func() time.Time { return time.Unix(0, 0) }
	ctx := context.Background()
	for range 3 {
		if _, err := l.reserve(ctx, "b.example"); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}
	if _, err := l.reserve(ctx, "b.example"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := l.reserve(ctx, "a.example"); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	got := l.report()
	if got.RPS != 2 || got.Burst != 3 {
		t.Fatalf("report budget = %v/%d, want 2/3", got.RPS, got.Burst)
	}
	if len(got.Hosts) != 2 || got.Hosts[0].Host != "a.example" || got.Hosts[1].Host != "b.example" {
		t.Fatalf("report hosts = %+v, want a.example then b.example", got.Hosts)
	}
	if got.Hosts[1].Throttled != 1 {
		t.Fatalf("b.example throttled = %d, want 1", got.Hosts[1].Throttled)
	}
	if got.Hosts[0].Throttled != 0 {
		t.Fatalf("a.example throttled = %d, want 0", got.Hosts[0].Throttled)
	}
}
