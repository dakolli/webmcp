package browsers

import (
	"strings"
	"testing"
)

// TestBuildUserAgent pins the coherent default User-Agent: the launched
// Chromium major and the platform token must appear verbatim, and a missing
// major degrades to the fallback rather than emitting Chrome/0.
func TestBuildUserAgent(t *testing.T) {
	t.Parallel()
	platform := browserPlatform{ua: "X11; Linux x86_64"}
	cases := []struct {
		name  string
		major int
		want  string
	}{
		{
			name:  "launched major",
			major: 131,
			want:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
		{
			name:  "fallback major",
			major: 0,
			want:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := buildUserAgent(tt.major, platform); got != tt.want {
				t.Fatalf("buildUserAgent(%d) = %q, want %q", tt.major, got, tt.want)
			}
		})
	}
}

// TestDefaultAcceptLanguage pins the default Accept-Language header and the bare
// language list handed to Network.setUserAgentOverride: every range carries at
// most one quality value, no range repeats, and the list matches
// navigator.languages.
func TestDefaultAcceptLanguage(t *testing.T) {
	t.Parallel()
	if want := "en-US,en;q=0.9"; defaultAcceptLanguage != want {
		t.Fatalf("defaultAcceptLanguage = %q, want %q", defaultAcceptLanguage, want)
	}
	ranges := strings.Split(defaultAcceptLanguage, ",")
	if len(ranges) != len(defaultLanguages) {
		t.Fatalf("defaultAcceptLanguage has %d ranges, want %d to match navigator.languages", len(ranges), len(defaultLanguages))
	}
	seen := make(map[string]bool, len(ranges))
	for _, r := range ranges {
		lang, _, _ := strings.Cut(r, ";")
		if seen[lang] {
			t.Fatalf("defaultAcceptLanguage repeats the range %q", lang)
		}
		seen[lang] = true
		if got := strings.Count(r, ";q="); got > 1 {
			t.Fatalf("defaultAcceptLanguage range %q carries %d quality values, want at most one", r, got)
		}
	}
	list := acceptLanguageList(defaultAcceptLanguage)
	if want := strings.Join(defaultLanguages, ","); list != want {
		t.Fatalf("acceptLanguageList(defaultAcceptLanguage) = %q, want %q", list, want)
	}
	if strings.Contains(list, "q=") {
		t.Fatalf("acceptLanguageList(defaultAcceptLanguage) = %q, want no quality values", list)
	}
}

// TestAcceptLanguageList pins the quality-value stripping on the list handed to
// Network.setUserAgentOverride, which re-adds the q ladder itself.
func TestAcceptLanguageList(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "standard ladder",
			header: "en-US,en;q=0.9",
			want:   "en-US,en",
		},
		{
			name:   "quality on the first range",
			header: "en-US;q=0.6,en;q=0.4",
			want:   "en-US,en",
		},
		{
			name:   "bare list",
			header: "en-US,en",
			want:   "en-US,en",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := acceptLanguageList(tt.header); got != tt.want {
				t.Fatalf("acceptLanguageList(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

// TestAssertStealthProbe pins every fingerprint check: the coherent probe
// passes, and each automation or incoherence tell fails with its own reason.
func TestAssertStealthProbe(t *testing.T) {
	t.Parallel()
	valid := StealthProbe{
		Webdriver:     false,
		Chrome:        true,
		Plugins:       5,
		Languages:     []string{"en-US", "en"},
		Platform:      "Linux x86_64",
		UserAgent:     "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		WebGLVendor:   "Google Inc. (Intel)",
		WebGLRenderer: "ANGLE (Intel, Mesa Intel(R) UHD Graphics, OpenGL 4.6)",
	}
	cases := []struct {
		name    string
		probe   StealthProbe
		major   int
		wantErr string
	}{
		{
			name:  "coherent probe",
			probe: valid,
			major: 131,
		},
		{
			name: "unknown launched major skips the UA check",
			probe: StealthProbe{
				Chrome:        true,
				Plugins:       1,
				Languages:     []string{"en-US", "en"},
				UserAgent:     "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
				WebGLRenderer: "ANGLE (Intel)",
			},
			major: 0,
		},
		{
			name:    "webdriver exposed",
			probe:   withProbe(valid, func(p *StealthProbe) { p.Webdriver = true }),
			major:   131,
			wantErr: "navigator.webdriver is true",
		},
		{
			name:    "chrome missing",
			probe:   withProbe(valid, func(p *StealthProbe) { p.Chrome = false }),
			major:   131,
			wantErr: "window.chrome is missing",
		},
		{
			name:    "plugins empty",
			probe:   withProbe(valid, func(p *StealthProbe) { p.Plugins = 0 }),
			major:   131,
			wantErr: "navigator.plugins is empty",
		},
		{
			name:    "languages incoherent",
			probe:   withProbe(valid, func(p *StealthProbe) { p.Languages = []string{"fr-FR"} }),
			major:   131,
			wantErr: "navigator.languages",
		},
		{
			name:    "user agent major mismatch",
			probe:   valid,
			major:   132,
			wantErr: "does not contain the launched Chrome major",
		},
		{
			name:    "swiftshader renderer",
			probe:   withProbe(valid, func(p *StealthProbe) { p.WebGLRenderer = "Google SwiftShader" }),
			major:   131,
			wantErr: "software rasterizer",
		},
		{
			name:    "llvmpipe renderer",
			probe:   withProbe(valid, func(p *StealthProbe) { p.WebGLRenderer = "llvmpipe (LLVM 15.0.7, 256 bits)" }),
			major:   131,
			wantErr: "software rasterizer",
		},
		{
			name:    "no webgl renderer",
			probe:   withProbe(valid, func(p *StealthProbe) { p.WebGLRenderer = "" }),
			major:   131,
			wantErr: "no WebGL renderer",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := AssertStealthProbe(tt.probe, tt.major)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("AssertStealthProbe() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("AssertStealthProbe() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("AssertStealthProbe() = %q, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

// withProbe returns a copy of probe with mutate applied.
func withProbe(probe StealthProbe, mutate func(*StealthProbe)) StealthProbe {
	mutate(&probe)
	return probe
}
