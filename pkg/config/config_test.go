package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestResolveProfile(t *testing.T) {
	tmpConfig := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpConfig)
	profilesDir := filepath.Join(tmpConfig, DirName, "profiles")
	ephemeralDir := filepath.Join(profilesDir, ".ephemeral")

	t.Run("bare name persists", func(t *testing.T) {
		dir, ephemeral, err := ResolveProfile("Audit", "", "browser-2", true)
		if err != nil {
			t.Fatalf("ResolveProfile: %v", err)
		}
		want := filepath.Join(profilesDir, "audit")
		if dir != want {
			t.Errorf("dir = %q, want %q", dir, want)
		}
		if ephemeral {
			t.Error("bare name with persist=true reported ephemeral")
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("profile directory %s was not created: %v", dir, err)
		}
	})

	t.Run("bare name is ephemeral without persist", func(t *testing.T) {
		dir, ephemeral, err := ResolveProfile("audit", "", "browser-2", false)
		if err != nil {
			t.Fatalf("ResolveProfile: %v", err)
		}
		if !ephemeral {
			t.Error("bare name with persist=false reported persistent")
		}
		if filepath.Dir(dir) != ephemeralDir {
			t.Errorf("dir = %q, want it under %q", dir, ephemeralDir)
		}
		if !strings.HasPrefix(filepath.Base(dir), "audit-") {
			t.Errorf("dir base = %q, want prefix %q", filepath.Base(dir), "audit-")
		}
		if dir == filepath.Join(profilesDir, "audit") {
			t.Error("ephemeral profile reused the persistent named profile")
		}
	})

	t.Run("empty persists to default", func(t *testing.T) {
		dir, ephemeral, err := ResolveProfile("", "", "browser-1", true)
		if err != nil {
			t.Fatalf("ResolveProfile: %v", err)
		}
		want := filepath.Join(profilesDir, "default")
		if dir != want {
			t.Errorf("dir = %q, want %q", dir, want)
		}
		if ephemeral {
			t.Error("empty profile with persist=true reported ephemeral")
		}
	})

	t.Run("empty is ephemeral without persist", func(t *testing.T) {
		dir, ephemeral, err := ResolveProfile("", "", "browser-2", false)
		if err != nil {
			t.Fatalf("ResolveProfile: %v", err)
		}
		if !ephemeral {
			t.Error("empty profile with persist=false reported persistent")
		}
		if filepath.Dir(dir) != ephemeralDir {
			t.Errorf("dir = %q, want it under %q", dir, ephemeralDir)
		}
		if !strings.HasPrefix(filepath.Base(dir), "browser-2-") {
			t.Errorf("dir base = %q, want prefix %q", filepath.Base(dir), "browser-2-")
		}
	})

	t.Run("absolute path stays persistent", func(t *testing.T) {
		want := filepath.Join(t.TempDir(), "absolute-profile")
		dir, ephemeral, err := ResolveProfile(want, "", "browser-2", false)
		if err != nil {
			t.Fatalf("ResolveProfile: %v", err)
		}
		if dir != want {
			t.Errorf("dir = %q, want %q", dir, want)
		}
		if ephemeral {
			t.Error("absolute path reported ephemeral")
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("absolute profile directory %s was not created: %v", dir, err)
		}
	})

	for _, arg := range []string{"foo/bar", "./audit", "../audit"} {
		t.Run("relative path rejected "+arg, func(t *testing.T) {
			if _, _, err := ResolveProfile(arg, "", "browser-2", true); err == nil {
				t.Errorf("ResolveProfile(%q) succeeded, want error", arg)
			}
		})
	}
}

func TestResolveProfilePathWithBinRejectsRelativePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if _, err := ResolveProfilePathWithBin("foo/bar", ""); err == nil {
		t.Fatal("ResolveProfilePathWithBin(\"foo/bar\") succeeded, want error")
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
