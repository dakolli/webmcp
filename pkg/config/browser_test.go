package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseTOMLStringArrays covers the array forms webmcp.toml accepts:
// inline, empty, trailing comma, comments between elements and multi-line.
func TestParseTOMLStringArrays(t *testing.T) {
	data := `[browser]
chromium_flags = [
  "--disable-gpu", # inline comment
  # a comment between elements
  "--no-pings",
]
empty = []
inline = ["--a", "--b"]
`
	doc, err := ParseTOML([]byte(data))
	if err != nil {
		t.Fatalf("ParseTOML: %v", err)
	}
	cases := []struct {
		key  string
		want []string
	}{
		{"chromium_flags", []string{"--disable-gpu", "--no-pings"}},
		{"empty", nil},
		{"inline", []string{"--a", "--b"}},
	}
	for _, tc := range cases {
		got, ok, err := doc.Strings("browser", tc.key)
		if err != nil || !ok {
			t.Fatalf("Strings(%q): ok=%v err=%v", tc.key, ok, err)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("Strings(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestParseTOMLStringArrayBareValueError(t *testing.T) {
	doc, err := ParseTOML([]byte("[browser]\nchromium_flags = \"--no-pings\"\n"))
	if err != nil {
		t.Fatalf("ParseTOML: %v", err)
	}
	_, ok, err := doc.Strings("browser", "chromium_flags")
	if !ok || err == nil {
		t.Fatalf("Strings: ok=%v err=%v, want a bare-value error", ok, err)
	}
	want := "config: webmcp.toml line 2: chromium_flags needs a [ ... ] list"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

func TestResolveBrowserOrderAndDedupe(t *testing.T) {
	writeBrowserConfig(t, `[browser]
chromium_flags = ["--disable-gpu", "--no-pings"]
`)
	bf, err := ResolveBrowser([]string{"--no-pings", "--custom"})
	if err != nil {
		t.Fatalf("ResolveBrowser: %v", err)
	}
	if !bf.Quiet {
		t.Fatal("Quiet = false, want the default true")
	}
	want := append(append([]string{}, DefaultQuietFlags...), "--disable-gpu", "--custom")
	if strings.Join(bf.ChromiumFlags, "\n") != strings.Join(want, "\n") {
		t.Fatalf("flags = %v, want %v", bf.ChromiumFlags, want)
	}
}

func TestResolveBrowserQuietOff(t *testing.T) {
	writeBrowserConfig(t, `[browser]
quiet = false
chromium_flags = ["--disable-gpu"]
`)
	bf, err := ResolveBrowser([]string{"--no-pings"})
	if err != nil {
		t.Fatalf("ResolveBrowser: %v", err)
	}
	if bf.Quiet {
		t.Fatal("Quiet = true, want false")
	}
	want := []string{"--disable-gpu", "--no-pings"}
	if strings.Join(bf.ChromiumFlags, "\n") != strings.Join(want, "\n") {
		t.Fatalf("flags = %v, want %v", bf.ChromiumFlags, want)
	}
}

func TestResolveBrowserMissingFileUsesQuietSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bf, err := ResolveBrowser(nil)
	if err != nil {
		t.Fatalf("ResolveBrowser: %v", err)
	}
	if !bf.Quiet {
		t.Fatal("Quiet = false, want the default true")
	}
	if strings.Join(bf.ChromiumFlags, "\n") != strings.Join(DefaultQuietFlags, "\n") {
		t.Fatalf("flags = %v, want the built-in quiet set", bf.ChromiumFlags)
	}
}

func writeBrowserConfig(t *testing.T, body string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := CaidoConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write webmcp.toml: %v", err)
	}
}
