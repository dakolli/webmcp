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
