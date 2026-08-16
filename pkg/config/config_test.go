package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDirAndProfilesDir(t *testing.T) {
	tmpConfig := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpConfig)

	cfgDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	expectedCfgDir := filepath.Join(tmpConfig, DirName)
	if cfgDir != expectedCfgDir {
		t.Errorf("ConfigDir got %q, want %q", cfgDir, expectedCfgDir)
	}

	info, err := os.Stat(cfgDir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", cfgDir)
	}

	profDir, err := ProfilesDir()
	if err != nil {
		t.Fatalf("ProfilesDir: %v", err)
	}
	expectedProfDir := filepath.Join(expectedCfgDir, "profiles")
	if profDir != expectedProfDir {
		t.Errorf("ProfilesDir got %q, want %q", profDir, expectedProfDir)
	}
}

func TestResolveProfilePath(t *testing.T) {
	tmpConfig := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpConfig)

	// Empty string defaults to "default" profile directory
	resEmpty, err := ResolveProfilePath("")
	if err != nil {
		t.Fatalf("ResolveProfilePath(\"\"): %v", err)
	}
	expectedDefault := filepath.Join(tmpConfig, DirName, "profiles", "default")
	if resEmpty != expectedDefault {
		t.Errorf("ResolveProfilePath(\"\") got %q, want %q", resEmpty, expectedDefault)
	}

	// Empty string with Brave bin defaults to "brave" profile directory
	resBraveBin, err := ResolveProfilePathWithBin("", "/usr/bin/brave-browser")
	if err != nil {
		t.Fatalf("ResolveProfilePathWithBin(\"\", \"brave\"): %v", err)
	}
	expectedBraveBin := filepath.Join(tmpConfig, DirName, "profiles", "brave")
	if resBraveBin != expectedBraveBin {
		t.Errorf("ResolveProfilePathWithBin(\"\", \"brave\") got %q, want %q", resBraveBin, expectedBraveBin)
	}

	// Shorthand name "brave"
	resBrave, err := ResolveProfilePath("brave")
	if err != nil {
		t.Fatalf("ResolveProfilePath(\"brave\"): %v", err)
	}
	expectedBrave := filepath.Join(tmpConfig, DirName, "profiles", "brave")
	if resBrave != expectedBrave {
		t.Errorf("ResolveProfilePath(\"brave\") got %q, want %q", resBrave, expectedBrave)
	}
	if info, err := os.Stat(resBrave); err != nil || !info.IsDir() {
		t.Errorf("Brave profile directory %s was not created properly", resBrave)
	}

	// Shorthand name "chrome"
	resChrome, err := ResolveProfilePath("chrome")
	if err != nil {
		t.Fatalf("ResolveProfilePath(\"chrome\"): %v", err)
	}
	expectedChrome := filepath.Join(tmpConfig, DirName, "profiles", "chrome")
	if resChrome != expectedChrome {
		t.Errorf("ResolveProfilePath(\"chrome\") got %q, want %q", resChrome, expectedChrome)
	}

	// Custom path
	customDir := filepath.Join(t.TempDir(), "custom_profile")
	resCustom, err := ResolveProfilePath(customDir)
	if err != nil {
		t.Fatalf("ResolveProfilePath(%q): %v", customDir, err)
	}
	if resCustom != customDir {
		t.Errorf("ResolveProfilePath custom path got %q, want %q", resCustom, customDir)
	}
}

func TestLoadSaveConfig(t *testing.T) {
	tmpConfig := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpConfig)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig initial: %v", err)
	}
	if cfg.DefaultProfile != "default" {
		t.Errorf("DefaultProfile got %q, want \"default\"", cfg.DefaultProfile)
	}

	cfg.DefaultProfile = "brave"
	cfg.BinPaths["brave"] = "/usr/bin/brave-browser"

	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	loaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig re-read: %v", err)
	}
	if loaded.DefaultProfile != "brave" {
		t.Errorf("Loaded DefaultProfile got %q, want \"brave\"", loaded.DefaultProfile)
	}
	if loaded.BinPaths["brave"] != "/usr/bin/brave-browser" {
		t.Errorf("Loaded BinPaths[\"brave\"] got %q, want \"/usr/bin/brave-browser\"", loaded.BinPaths["brave"])
	}
}
