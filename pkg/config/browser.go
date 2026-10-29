package config

import (
	"errors"
	"fmt"
	"os"
)

// DefaultQuietFlags is the built-in Chromium quiet set applied when
// [browser] quiet is true. It suppresses background telemetry, update and
// sync traffic so page reads show only the traffic the caller asked for.
var DefaultQuietFlags = []string{
	"--disable-background-networking",
	"--disable-component-update",
	"--disable-domain-reliability",
	"--disable-client-side-phishing-detection",
	"--safebrowsing-disable-auto-update",
	"--no-pings",
	"--disable-sync",
	"--disable-breakpad",
	"--no-first-run",
	"--no-default-browser-check",
	"--disable-features=OptimizationHints,MediaRouter,Translate",
}

// BrowserFile holds the resolved browser settings and the file they came from.
type BrowserFile struct {
	// ChromiumFlags is the ordered, deduplicated Chromium switch list.
	ChromiumFlags []string
	// Quiet reports whether the built-in quiet set is enabled.
	Quiet bool
	// Path is the webmcp.toml path the settings were read from.
	Path string
}

// LoadBrowser reads the [browser] section of webmcp.toml. A missing file yields
// the quiet default with no error; only the file's own flags are returned, not
// the built-in quiet set.
func LoadBrowser() (BrowserFile, error) {
	path := CaidoConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BrowserFile{Quiet: true, Path: path}, nil
		}
		return BrowserFile{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	doc, err := ParseTOML(data)
	if err != nil {
		return BrowserFile{}, err
	}

	bf := BrowserFile{Quiet: true, Path: path}
	if doc["browser"] == nil {
		return bf, nil
	}
	quiet, ok, err := doc.Bool("browser", "quiet")
	if err != nil {
		return BrowserFile{}, fmt.Errorf("config: webmcp.toml browser.quiet: %w", err)
	}
	if ok {
		bf.Quiet = quiet
	}
	flags, ok, err := doc.Strings("browser", "chromium_flags")
	if err != nil {
		return BrowserFile{}, err
	}
	if ok {
		bf.ChromiumFlags = flags
	}
	return bf, nil
}

// ResolveBrowser composes the built-in quiet set, the [browser] chromium_flags
// and the command-line flags in that order, then dedupes on the exact string
// with the first occurrence winning.
func ResolveBrowser(flagFlags []string) (BrowserFile, error) {
	bf, err := LoadBrowser()
	if err != nil {
		return BrowserFile{}, err
	}
	ordered := make([]string, 0, len(DefaultQuietFlags)+len(bf.ChromiumFlags)+len(flagFlags))
	if bf.Quiet {
		ordered = append(ordered, DefaultQuietFlags...)
	}
	ordered = append(ordered, bf.ChromiumFlags...)
	ordered = append(ordered, flagFlags...)
	bf.ChromiumFlags = dedupeFlags(ordered)
	return bf, nil
}

// dedupeFlags keeps the first occurrence of each exact flag and preserves order.
func dedupeFlags(flags []string) []string {
	seen := make(map[string]struct{}, len(flags))
	out := make([]string, 0, len(flags))
	for _, flag := range flags {
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		out = append(out, flag)
	}
	return out
}
