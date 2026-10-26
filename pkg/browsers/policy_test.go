package browsers

import (
	"net/url"
	"strings"
	"testing"
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
