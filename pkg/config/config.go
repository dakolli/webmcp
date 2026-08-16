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
)

// DirName is the base configuration directory name under user config.
const DirName = "browser-mcp"

// Config holds user-configurable options persisted in ~/.config/browser-mcp/config.json.
type Config struct {
	// DefaultProfile is the fallback profile name if none is specified (default "default").
	DefaultProfile string `json:"default_profile,omitempty"`
	// BinPaths maps browser names ("brave", "chrome", "firefox") to custom binary paths.
	BinPaths map[string]string `json:"bin_paths,omitempty"`
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

// ResolveProfilePath takes a profile name or directory path and resolves it.
// When profileArg is empty (""), it defaults to the matching profile directory:
// - If binPath contains "brave", resolves to ~/.config/browser-mcp/profiles/brave.
// - If binPath contains "chrome", resolves to ~/.config/browser-mcp/profiles/chrome.
// - Otherwise, resolves to ~/.config/browser-mcp/profiles/<default_profile> (default: "default").
// Shorthand names ("brave", "chrome", "firefox", "default") resolve under ~/.config/browser-mcp/profiles/<name>.
// Absolute or relative paths (containing filepath separators or starting with ".") are used directly.
// The resolved directory is created with 0700 permissions if it does not exist.
func ResolveProfilePath(profileArg string) (string, error) {
	return ResolveProfilePathWithBin(profileArg, "")
}

// ResolveProfilePathWithBin resolves profileArg, inferring the default profile name from binPath when profileArg is empty.
func ResolveProfilePathWithBin(profileArg, binPath string) (string, error) {
	profileArg = strings.TrimSpace(profileArg)
	if profileArg == "" {
		lowerBin := strings.ToLower(binPath)
		switch {
		case strings.Contains(lowerBin, "brave"):
			profileArg = "brave"
		case strings.Contains(lowerBin, "chrome"):
			profileArg = "chrome"
		case strings.Contains(lowerBin, "firefox"):
			profileArg = "firefox"
		default:
			cfg, err := LoadConfig()
			if err == nil && cfg.DefaultProfile != "" {
				profileArg = cfg.DefaultProfile
			} else {
				profileArg = "default"
			}
		}
	}

	var targetDir string
	if strings.Contains(profileArg, string(filepath.Separator)) || strings.HasPrefix(profileArg, ".") {
		absPath, err := filepath.Abs(profileArg)
		if err != nil {
			return "", fmt.Errorf("config: resolve profile path %q: %w", profileArg, err)
		}
		targetDir = absPath
	} else {
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
