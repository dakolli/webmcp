// Package skill installs the embedded web-mcp agent skill into a harness
// config directory or an explicit destination.
package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"webmcp"
)

const (
	// skillRoot is the embedded directory holding the skill tree.
	skillRoot = ".agents/skills/web-mcp"
	// skipFile is the maintainer file that is never installed.
	skipFile = "AGENTS.md"
)

// Target is one harness preset: a name, a ~-based directory, and a short note.
type Target struct {
	Name string
	Dir  string
	Note string
}

// Result describes one install: how many files were written, where, and which
// embedded entries were deliberately skipped.
type Result struct {
	Files   int
	Dir     string
	Skipped []string
}

// Targets returns the known harness presets in display order.
func Targets() []Target {
	return []Target{
		{Name: "claude", Dir: "~/.claude/skills/web-mcp", Note: "Claude Code"},
		{Name: "opencode", Dir: "~/.config/opencode/skills/web-mcp", Note: "OpenCode"},
		{Name: "codex", Dir: "~/.codex/skills/web-mcp", Note: "OpenAI Codex"},
		{Name: "pi", Dir: "~/.pi/skills/web-mcp", Note: "pi"},
	}
}

// Resolve maps a command argument to an install directory: a harness preset, "."
// (the current project's .agents/skills), an existing directory, or a path with
// a separator. An unknown bare name fails with the preset list.
func Resolve(arg string) (string, error) {
	if arg == "." {
		info, err := os.Stat(".agents/skills")
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("cannot install into ./: .agents/skills not found — run from a project root that has it, or pass an explicit path")
		}
		return filepath.Join(".agents", "skills", "web-mcp"), nil
	}

	for _, t := range Targets() {
		if t.Name != arg {
			continue
		}
		dir, err := expandHome(t.Dir)
		if err != nil {
			return "", fmt.Errorf("resolving the %s preset: %w", t.Name, err)
		}
		if err := withinHome(dir, t.Name); err != nil {
			return "", err
		}
		return dir, nil
	}

	if strings.ContainsRune(arg, os.PathSeparator) || strings.HasPrefix(arg, "~") {
		return expandHome(arg)
	}

	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		return arg, nil
	}

	return "", fmt.Errorf("unknown harness %q — use one of %s, a path with a separator, an existing directory, or '.'",
		arg, presetNames())
}

// Install copies the embedded skill into dst. A non-empty destination is
// refused unless force is set; force overwrites skill files but never deletes
// anything else. With dryRun, it reports the plan and writes nothing.
func Install(dst string, force, dryRun bool) (Result, error) {
	dst, err := expandHome(dst)
	if err != nil {
		return Result{}, err
	}

	files, skipped, err := walk()
	if err != nil {
		return Result{}, err
	}
	res := Result{Files: len(files), Dir: dst, Skipped: skipped}
	if dryRun {
		return res, nil
	}

	info, err := os.Stat(dst)
	switch {
	case err == nil && !info.IsDir():
		return Result{}, fmt.Errorf("destination %q is a file, not a directory — pass a directory path", dst)
	case err == nil:
		entries, rerr := os.ReadDir(dst)
		if rerr != nil {
			return Result{}, fmt.Errorf("reading destination %q: %w", dst, rerr)
		}
		if len(entries) > 0 && !force {
			return Result{}, fmt.Errorf("destination %q already contains files — pass --force to overwrite, or pick another directory", dst)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return Result{}, fmt.Errorf("checking destination %q: %w", dst, err)
	}

	for _, rel := range files {
		data, err := webmcp.SkillFS.ReadFile(path.Join(skillRoot, rel))
		if err != nil {
			return Result{}, fmt.Errorf("reading embedded %q: %w", rel, err)
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Result{}, fmt.Errorf("creating directory for %q: %w", target, err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return Result{}, fmt.Errorf("writing %q: %w", target, err)
		}
	}
	return res, nil
}

// List returns the skill file paths relative to the install directory, sorted.
func List() ([]string, error) {
	files, _, err := walk()
	return files, err
}

// walk collects installable files and skipped entries from the embedded tree.
func walk() (files, skipped []string, err error) {
	err = fs.WalkDir(webmcp.SkillFS, skillRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, skillRoot+"/")
		if rel == skipFile || strings.HasPrefix(path.Base(rel), ".") {
			skipped = append(skipped, rel)
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walking embedded skill %q: %w", skillRoot, err)
	}
	sort.Strings(files)
	sort.Strings(skipped)
	return files, skipped, nil
}

// expandHome replaces a leading "~" with the current user's home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory for %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// withinHome verifies a preset directory resolves under $HOME.
func withinHome(dir, name string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory for the %s preset: %w", name, err)
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("the %s preset resolves outside $HOME (%s) — pass an explicit path instead", name, dir)
	}
	return nil
}

// presetNames joins the preset names for error messages.
func presetNames() string {
	names := make([]string, 0, len(Targets()))
	for _, t := range Targets() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}
