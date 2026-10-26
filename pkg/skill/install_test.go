package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstall(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "web-mcp")

	res, err := Install(dst, false, false)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	files, err := List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Files != len(files) {
		t.Fatalf("installed %d files, want %d", res.Files, len(files))
	}
	if res.Files == 0 {
		t.Fatal("installed no files")
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "references", "tools", "index.md")); err != nil {
		t.Fatalf("references/tools/index.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "references", "examples", "index.md")); err != nil {
		t.Fatalf("references/examples/index.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md must not be installed, stat err = %v", err)
	}

	if _, err := Install(dst, false, true); err != nil {
		t.Fatalf("dry run on an existing destination: %v", err)
	}

	if _, err := Install(dst, false, false); err == nil {
		t.Fatal("second install without force should fail")
	} else if !strings.Contains(err.Error(), dst) {
		t.Fatalf("error should name the directory, got %q", err)
	}

	if _, err := Install(dst, true, false); err != nil {
		t.Fatalf("install with force: %v", err)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "web-mcp")

	res, err := Install(dst, false, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Files == 0 {
		t.Fatal("dry run reported no files")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dry run created %q, stat err = %v", dst, err)
	}
}

func TestResolveDotWithoutTree(t *testing.T) {
	t.Chdir(t.TempDir())

	if _, err := Resolve("."); err == nil {
		t.Fatal(`Resolve(".") should fail without .agents/skills`)
	} else if !strings.Contains(err.Error(), ".agents/skills") {
		t.Fatalf("error should name .agents/skills, got %q", err)
	}
}

func TestResolvePath(t *testing.T) {
	if got, err := Resolve("./somewhere"); err != nil || got != "./somewhere" {
		t.Fatalf(`Resolve("./somewhere") = %q, %v`, got, err)
	}
	dir := t.TempDir()
	if got, err := Resolve(dir); err != nil || got != dir {
		t.Fatalf("Resolve(%q) = %q, %v", dir, got, err)
	}
}

func TestResolvePreset(t *testing.T) {
	dir, err := Resolve("claude")
	if err != nil {
		t.Fatalf("resolve claude: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join(".claude", "skills", "web-mcp")) {
		t.Fatalf("claude preset resolved to %q", dir)
	}

	if _, err := Resolve("nosuchharness"); err == nil {
		t.Fatal("unknown bare name should fail")
	} else if !strings.Contains(err.Error(), "claude") {
		t.Fatalf("error should list presets, got %q", err)
	}
}
