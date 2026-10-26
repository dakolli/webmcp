package cmd

// Table-driven pins for the browser focus --state normalization (WEB-49): the
// documented CLI vocabulary — "maximize" as the user-facing alias for the
// engine value "maximized", plus normal/fullscreen pass-through — and the
// exact error for anything else.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
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

	_, err = browserPages(context.Background(), mgr, nil, "browser-99", "", "page list")
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

// TestValidateCloseCleanup pins the CLI half of browser_close's cleanup rules:
// the three refusal sentences match the MCP tool, and a plain close is allowed.
func TestValidateCloseCleanup(t *testing.T) {
	tests := []struct {
		name       string
		idExplicit bool
		purge      bool
		prune      bool
		wantErr    string
	}{
		{"plain close", false, false, false, ""},
		{"purge with id", true, true, false, ""},
		{"prune alone", false, false, true, ""},
		{"purge and prune", true, true, true, "purge and prune are mutually exclusive — pass purge with a browser_id, or prune alone"},
		{"prune with id", true, false, true, "prune takes no browser_id — it closes all browsers and cleans orphan ephemeral profiles; use purge with browser_id to delete one profile"},
		{"purge without id", false, true, false, "purge requires a browser_id — pass the browser whose profile to delete, or use prune to clean orphan ephemeral profiles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCloseCleanup(tt.idExplicit, tt.purge, tt.prune)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCloseCleanup = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("validateCloseCleanup = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestBrowserCreateHeadlessFlag pins WEB-141: `browser create --headless` is a
// real flag (not an unknown-flag failure), defaults to headful, is marked
// changed when set (the trigger that sends the headless param and mints a
// separate browser), and appears in the help example.
func TestBrowserCreateHeadlessFlag(t *testing.T) {
	cmd := NewBrowserCreateCmd(nil)
	flag := cmd.Flags().Lookup("headless")
	if flag == nil {
		t.Fatal("browser create has no --headless flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--headless default = %q, want false (the CLI stays headful by default)", flag.DefValue)
	}
	if err := cmd.Flags().Set("headless", "true"); err != nil {
		t.Fatalf("set --headless: %v", err)
	}
	if !cmd.Flags().Changed("headless") {
		t.Error("--headless is not marked changed after being set")
	}
	if !strings.Contains(cmd.Example, "--headless") {
		t.Errorf("browser create help example does not show --headless:\n%s", cmd.Example)
	}
}

// TestPrintBrowserEntries pins the human list shape: owner, persist, ttl and
// profile_dir are all present, and pages stay indented under their browser.
func TestPrintBrowserEntries(t *testing.T) {
	var buf bytes.Buffer
	printBrowserEntries(&buf, []ipc.BrowserEntry{{
		BrowserID:  "browser-2",
		Owner:      "scraper-a",
		Bin:        "brave",
		ProfileDir: "profiles/.ephemeral/browser-2-x",
		Persist:    false,
		TTL:        "30m0s",
		Pages:      []browsers.PageInfo{{ID: "page-1", URL: "https://example.com"}},
	}})

	got := buf.String()
	for _, want := range []string{
		"browser_id: browser-2 (owner: scraper-a, persist: false, ttl: 30m0s, scope: open, scope_strict: false, bin: brave)",
		"  profile_dir: profiles/.ephemeral/browser-2-x",
		"  page-1\thttps://example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
