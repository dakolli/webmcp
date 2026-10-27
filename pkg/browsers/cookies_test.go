package browsers

import "testing"

func TestNormalizeAuthSite(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "example.org", want: "example.org"},
		{in: "https://Example.ORG/path?q=1", want: "example.org"},
		{in: "example.org:8443", want: "example.org"},
		{in: ".example.org", want: "example.org"},
		{in: "  example.org  ", want: "example.org"},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeAuthSite(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizeAuthSite(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeAuthSite(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeAuthSite(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCookieMatchesSite(t *testing.T) {
	tests := []struct {
		domain string
		site   string
		want   bool
	}{
		{domain: ".example.org", site: "example.org", want: true},
		{domain: "example.org", site: "example.org", want: true},
		{domain: "sub.example.org", site: "example.org", want: true},
		{domain: "evil-example.org", site: "example.org", want: false},
		{domain: "example.org", site: "sub.example.org", want: false},
		{domain: "", site: "example.org", want: false},
	}
	for _, tt := range tests {
		if got := cookieMatchesSite(tt.domain, tt.site); got != tt.want {
			t.Errorf("cookieMatchesSite(%q, %q) = %v, want %v", tt.domain, tt.site, got, tt.want)
		}
	}
}

func TestCookieParamPreservesScopeAndFlags(t *testing.T) {
	t.Run("domain cookie keeps its leading dot", func(t *testing.T) {
		param, err := cookieParam(AuthCookie{
			Name:     "sid",
			Value:    "secret",
			Domain:   ".example.org",
			Path:     "/app",
			Secure:   true,
			HTTPOnly: true,
			SameSite: "Lax",
			Expires:  1_700_000_000,
		})
		if err != nil {
			t.Fatalf("cookieParam: %v", err)
		}
		if param.Domain != ".example.org" {
			t.Errorf("domain = %q, want .example.org (never broaden to a parent or strip the dot)", param.Domain)
		}
		if param.URL != "https://example.org/app" {
			t.Errorf("url = %q, want https://example.org/app", param.URL)
		}
		if param.Secure != true || param.HTTPOnly != true || param.SameSite != "Lax" {
			t.Errorf("flags = secure:%v httpOnly:%v sameSite:%q, want true/true/Lax", param.Secure, param.HTTPOnly, param.SameSite)
		}
		if float64(param.Expires) != 1_700_000_000 {
			t.Errorf("expires = %v, want 1700000000", float64(param.Expires))
		}
	})

	t.Run("host-only cookie omits Domain", func(t *testing.T) {
		param, err := cookieParam(AuthCookie{
			Name:   "session",
			Value:  "secret",
			Domain: "example.org",
			Path:   "/",
		})
		if err != nil {
			t.Fatalf("cookieParam: %v", err)
		}
		if param.Domain != "" {
			t.Errorf("domain = %q, want empty so the cookie stays host-only", param.Domain)
		}
		if param.URL != "http://example.org/" {
			t.Errorf("url = %q, want http://example.org/", param.URL)
		}
		if float64(param.Expires) != 0 {
			t.Errorf("expires = %v, want 0 for a session cookie", float64(param.Expires))
		}
	})

	t.Run("unknown SameSite is dropped", func(t *testing.T) {
		param, err := cookieParam(AuthCookie{
			Name:     "sid",
			Domain:   "example.org",
			SameSite: "bogus",
		})
		if err != nil {
			t.Fatalf("cookieParam: %v", err)
		}
		if param.SameSite != "" {
			t.Errorf("sameSite = %q, want empty", param.SameSite)
		}
	})

	t.Run("empty name or domain is rejected", func(t *testing.T) {
		if _, err := cookieParam(AuthCookie{Domain: "example.org"}); err == nil {
			t.Error("empty name was accepted")
		}
		if _, err := cookieParam(AuthCookie{Name: "sid"}); err == nil {
			t.Error("empty domain was accepted")
		}
	})
}
