package cmd

// Table-driven pins for the browser focus --state normalization (WEB-49): the
// documented CLI vocabulary — "maximize" as the user-facing alias for the
// engine value "maximized", plus normal/fullscreen pass-through — and the
// exact error for anything else.

import (
	"context"
	"io"
	"testing"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
)

// TestExplicitBrowserID pins WEB-133's flag-explicitness rule: an omitted
// --browser-id yields "", so the command keeps its default target, while the
// flag is honored both before and after the subcommand name.
func TestExplicitBrowserID(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"omitted keeps the default", []string{"fetch", "https://example.com"}, ""},
		{"flag before subcommand", []string{"--browser-id", "browser-7", "fetch", "https://example.com"}, "browser-7"},
		{"flag after subcommand", []string{"fetch", "https://example.com", "--browser-id", "browser-7"}, "browser-7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			root := NewRootCmd(nil)
			fetch, _, err := root.Find([]string{"fetch"})
			if err != nil {
				t.Fatalf("find fetch command: %v", err)
			}
			fetch.RunE = func(cmd *cobra.Command, _ []string) error {
				got = explicitBrowserID(cmd)
				return nil
			}
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(tt.args)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}
			if got != tt.want {
				t.Errorf("explicitBrowserID(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestOpenTargetBrowserUnknownID pins the engine-backed half of the rule: a
// pinned id that is not live fails with the live id list instead of falling
// back to browser-1 or to a terminal it creates on the spot.
func TestOpenTargetBrowserUnknownID(t *testing.T) {
	mgr := browsers.NewManager()
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })

	_, err := openTargetBrowser(context.Background(), mgr, "browser-99", "fetch")
	if err == nil {
		t.Fatal("openTargetBrowser(browser-99) = nil error, want unknown-browser error")
	}
	want := `fetch: browser "browser-99" not found (running: none)`
	if err.Error() != want {
		t.Errorf("openTargetBrowser(browser-99) error = %q, want %q", err.Error(), want)
	}

	_, err = browserPages(context.Background(), mgr, nil, "browser-99", "page list")
	if err == nil {
		t.Fatal("browserPages(browser-99) = nil error, want unknown-browser error")
	}
	want = `page list: browser "browser-99" not found (running: none)`
	if err.Error() != want {
		t.Errorf("browserPages(browser-99) error = %q, want %q", err.Error(), want)
	}
}

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
