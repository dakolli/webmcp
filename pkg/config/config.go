// Package config handles browser-mcp configuration directories, profile resolution,
// and user settings under ~/.config/browser-mcp (or $XDG_CONFIG_HOME/browser-mcp).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DirName is the base configuration directory name under user config.
const DirName = "browser-mcp"

// Config holds user-configurable options persisted in ~/.config/browser-mcp/config.json.
type Config struct {
	// DefaultProfile is the fallback profile name if none is specified (default "default").
	DefaultProfile string `json:"default_profile,omitempty"`
	// BinPaths maps browser names ("brave", "chrome", "firefox") to custom binary paths.
	BinPaths map[string]string `json:"bin_paths,omitempty"`
	// BrowserTTL is the browser idle timeout as a Go duration string (e.g.
	// "30m"); "0" or a negative value disables browser sweeping. Absent or
	// unparsable values fall back to the engine default.
	BrowserTTL string `json:"browser_ttl,omitempty"`
}

// BrowserIdleTTL parses BrowserTTL, returning def when the key is absent or
// unparsable. A parsed zero or negative duration disables browser sweeping.
func (c *Config) BrowserIdleTTL(def time.Duration) time.Duration {
	if c == nil {
		return def
	}
	raw := strings.TrimSpace(c.BrowserTTL)
	if raw == "" {
		return def
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return ttl
}

// ConfigDir returns the absolute path to ~/.config/browser-mcp (or $XDG_CONFIG_HOME/browser-mcp).
// It creates the directory with 0700 permissions if it does not already exist.
func ConfigDir() (string, error) {
	baseDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve user config dir: %w", err)
	}
	dir := filepath.Join(baseDir, DirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("config: create config dir %s: %w", dir, err)
	}
	return dir, nil
}

// StateDBPath returns the absolute path to the embedded state database,
// ~/.config/browser-mcp/state.db. The config directory is created with 0700
// permissions; the database file itself is created by pkg/store.
func StateDBPath() (string, error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfgDir, "state.db"), nil
}

// ProfilesDir returns the absolute path to ~/.config/browser-mcp/profiles.
// It creates the directory with 0700 permissions if it does not already exist.
func ProfilesDir() (string, error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cfgDir, "profiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("config: create profiles dir %s: %w", dir, err)
	}
	return dir, nil
}

// ResolveProfilePath takes a profile name or absolute directory path and resolves it.
// When profileArg is empty (""), it defaults to the matching profile directory:
// - If binPath contains "brave", resolves to ~/.config/browser-mcp/profiles/brave.
// - If binPath contains "chrome", resolves to ~/.config/browser-mcp/profiles/chrome.
// - Otherwise, resolves to ~/.config/browser-mcp/profiles/<default_profile> (default: "default").
// Shorthand names ("brave", "chrome", "firefox", "default") resolve under ~/.config/browser-mcp/profiles/<name>.
// Absolute paths are used as given; relative paths are rejected.
// The resolved directory is created with 0700 permissions if it does not exist.
func ResolveProfilePath(profileArg string) (string, error) {
	return ResolveProfilePathWithBin(profileArg, "")
}

// ResolveProfilePathWithBin resolves profileArg, inferring the default profile name from binPath when profileArg is empty.
// Absolute paths are used as given; relative paths containing a separator are rejected; bare names resolve
// under ~/.config/browser-mcp/profiles/<name>. The resolved directory is created with 0700 permissions.
func ResolveProfilePathWithBin(profileArg, binPath string) (string, error) {
	profileArg = strings.TrimSpace(profileArg)
	if profileArg == "" {
		profileArg = defaultProfileName(binPath)
	}

	var targetDir string
	switch {
	case filepath.IsAbs(profileArg):
		targetDir = filepath.Clean(profileArg)
	case isRelativeProfilePath(profileArg):
		return "", fmt.Errorf(
			"config: profile %q is a relative path; pass an absolute path or a bare name (resolved under %s/profiles)",
			profileArg,
			DirName,
		)
	default:
		profBase, err := ProfilesDir()
		if err != nil {
			return "", err
		}
		targetDir = filepath.Join(profBase, strings.ToLower(profileArg))
	}

	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return "", fmt.Errorf("config: create profile directory %s: %w", targetDir, err)
	}
	return targetDir, nil
}

// ResolveProfile resolves a profile argument to a profile directory and reports
// whether that directory is ephemeral (owned by one browser and removed on close).
//
// Absolute paths are always persistent and used as given. Relative paths are
// rejected. A bare name resolves under the profiles dir when persist is true and
// to a unique directory under profiles/.ephemeral when persist is false. An empty
// argument resolves to the default profile when persist is true and to a unique
// ephemeral directory otherwise.
func ResolveProfile(profileArg, binPath, browserID string, persist bool) (dir string, ephemeral bool, err error) {
	profileArg = strings.TrimSpace(profileArg)
	if profileArg == "" {
		if persist {
			dir, err = ResolveProfilePathWithBin("", binPath)
			if err != nil {
				return "", false, err
			}
			return dir, false, nil
		}
		dir, err = newEphemeralProfileDir(safeProfileLabel(browserID))
		if err != nil {
			return "", false, err
		}
		return dir, true, nil
	}

	switch {
	case filepath.IsAbs(profileArg):
		dir = filepath.Clean(profileArg)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", false, fmt.Errorf("config: create profile directory %s: %w", dir, err)
		}
		return dir, false, nil
	case isRelativeProfilePath(profileArg):
		return "", false, fmt.Errorf(
			"config: profile %q is a relative path; pass an absolute path or a bare name (resolved under %s/profiles)",
			profileArg,
			DirName,
		)
	}

	name := strings.ToLower(profileArg)
	if persist {
		dir, err = ResolveProfilePathWithBin(name, binPath)
		if err != nil {
			return "", false, err
		}
		return dir, false, nil
	}

	dir, err = newEphemeralProfileDir(safeProfileLabel(name))
	if err != nil {
		return "", false, err
	}
	return dir, true, nil
}

// defaultProfileName returns the profile name inferred from binPath, or the
// configured default when the binary matches no known shorthand.
func defaultProfileName(binPath string) string {
	lowerBin := strings.ToLower(binPath)
	switch {
	case strings.Contains(lowerBin, "brave"):
		return "brave"
	case strings.Contains(lowerBin, "chrome"):
		return "chrome"
	case strings.Contains(lowerBin, "firefox"):
		return "firefox"
	}
	cfg, err := LoadConfig()
	if err == nil && cfg.DefaultProfile != "" {
		return cfg.DefaultProfile
	}
	return "default"
}

// isRelativeProfilePath reports whether arg is a relative filesystem path rather
// than a bare profile name. Relative paths are never resolved against the
// process working directory.
func isRelativeProfilePath(arg string) bool {
	return strings.ContainsAny(arg, `/\`) || strings.HasPrefix(arg, ".")
}

// newEphemeralProfileDir creates a unique profile directory under the profiles
// directory's .ephemeral subdirectory with label as the name prefix.
func newEphemeralProfileDir(label string) (string, error) {
	profBase, err := ProfilesDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(profBase, ".ephemeral")
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("config: create ephemeral profiles directory %s: %w", base, err)
	}
	dir, err := os.MkdirTemp(base, label+"-")
	if err != nil {
		return "", fmt.Errorf("config: create ephemeral profile under %s: %w", base, err)
	}
	return dir, nil
}

// safeProfileLabel reduces s to a filesystem-safe name prefix: letters, digits,
// '-' and '_' are kept and every other rune becomes '-'.
func safeProfileLabel(s string) string {
	var label strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			label.WriteRune(r)
		default:
			label.WriteRune('-')
		}
	}
	if label.Len() == 0 {
		return "profile"
	}
	return label.String()
}

// LoadConfig reads config.json from the browser-mcp config directory.
// Returns a default Config struct if config.json does not exist.
func LoadConfig() (*Config, error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(cfgDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{
				DefaultProfile: "default",
				BinPaths:       make(map[string]string),
			}, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.BinPaths == nil {
		cfg.BinPaths = make(map[string]string)
	}
	return &cfg, nil
}

// SaveConfig writes cfg to config.json in the browser-mcp config directory.
func SaveConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("config: nil config provided")
	}
	cfgDir, err := ConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(cfgDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}
