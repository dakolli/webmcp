package cmd

// Table-driven pins for the browser focus --state normalization (WEB-49): the
// documented CLI vocabulary — "maximize" as the user-facing alias for the
// engine value "maximized", plus normal/fullscreen pass-through — and the
// exact error for anything else.

import (
	"testing"

	"webmcp/pkg/browsers"
)

// TestNormalizeFocusState pins normalizeFocusState: "maximize" maps to the
// engine constant WindowStateMaximized, the engine value "maximized" passes
// through unchanged (both spellings work), normal/fullscreen pass through, and
// any other value fails with the exact CLI error listing the documented
// vocabulary "maximize, fullscreen, normal".
func TestNormalizeFocusState(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    browsers.WindowState
		wantErr string
	}{
		{"normal", "normal", browsers.WindowStateNormal, ""},
		{"maximize alias", "maximize", browsers.WindowStateMaximized, ""},
		{"maximized engine value", "maximized", browsers.WindowStateMaximized, ""},
		{"fullscreen", "fullscreen", browsers.WindowStateFullscreen, ""},
		{"bogus", "bogus", "", `browser focus: unknown state "bogus" (must be one of maximize, fullscreen, normal)`},
		{"empty", "", "", `browser focus: unknown state "" (must be one of maximize, fullscreen, normal)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeFocusState(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("normalizeFocusState(%q) = %q, nil error; want error %q", tt.in, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("normalizeFocusState(%q) error = %q, want %q", tt.in, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeFocusState(%q) error = %v, want nil", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("normalizeFocusState(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
