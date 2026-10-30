package recon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/config"
)

const (
	// bootstrapBudget bounds one `go install` so a cold build cannot hang a call.
	bootstrapBudget = 300 * time.Second
	// probeBudget bounds one identity probe so a wedged binary cannot hang.
	probeBudget = 30 * time.Second
	// reconSubdir is the private GOBIN directory under the config dir.
	reconSubdir = "recon/bin"
)

// versionElement matches a Go module major-version suffix such as "v2".
var versionElement = regexp.MustCompile(`^v[0-9]+$`)

// BinDir returns <config dir>/recon/bin and creates it with mode 0700. The
// directory is the only place a scanner binary is ever resolved from.
func BinDir() (string, error) {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return "", fmt.Errorf("recon: resolve config dir: %w", err)
	}
	dir := filepath.Join(cfgDir, "recon", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("recon: create bin dir %s: %w", dir, err)
	}
	return dir, nil
}

// Ensure resolves <BinDir>/<BinName>. When the file is absent it installs the
// pinned module into that GOBIN, enabling CGO only when the spec asks for it,
// then verifies the binary's identity. A bare name is never resolved from PATH.
func Ensure(ctx context.Context, spec Spec) (string, error) {
	dir, err := BinDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, spec.BinName)
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("recon: inspect binary %s: %w", path, err)
		}
		if err := bootstrap(ctx, spec, dir); err != nil {
			return "", err
		}
	}
	if err := probe(ctx, spec, path); err != nil {
		return "", err
	}
	return path, nil
}

// bootstrap installs spec.Module@spec.Pin into dir with GOBIN set, reporting one
// actionable sentence plus the first meaningful installer line on failure.
func bootstrap(ctx context.Context, spec Spec, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, bootstrapBudget)
	defer cancel()

	target := spec.Module + "@" + spec.Pin
	cmd := exec.CommandContext(ctx, "go", "install", target)
	cmd.Env = installEnv(dir, spec.CGO)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return newInstallError(spec, target, stderr.String(), err)
	}
	return nil
}

// installEnv returns the process environment with GOBIN pointed at dir and CGO
// enabled only when the spec needs it, replacing any inherited values of either
// key. CGO_ENABLED=0 stays the default because every other pinned scanner builds
// a smaller, portable binary without it.
func installEnv(dir string, cgo bool) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOBIN=") || strings.HasPrefix(kv, "CGO_ENABLED=") {
			continue
		}
		env = append(env, kv)
	}
	cgoValue := "CGO_ENABLED=0"
	if cgo {
		cgoValue = "CGO_ENABLED=1"
	}
	return append(env, "GOBIN="+dir, cgoValue)
}

// installError names one failed bootstrap in one sentence plus the first
// meaningful log line. The full installer log is kept in log for callers that
// need the detail but is never printed.
type installError struct {
	// id is the scanner ID, e.g. "recon_ports".
	id string
	// target is the module@version that failed to install.
	target string
	// reason is the first meaningful installer line.
	reason string
	// log is the full installer stderr.
	log string
	// cause is the installer's exit error.
	cause error
}

// newInstallError builds the actionable bootstrap failure from installer output.
func newInstallError(spec Spec, target, output string, cause error) *installError {
	reason := firstMeaningfulLine(output)
	if reason == "" {
		reason = cause.Error()
	}
	return &installError{
		id:     spec.ID,
		target: target,
		reason: reason,
		log:    output,
		cause:  cause,
	}
}

// Error reports the scanner, its pinned module and the first meaningful line.
func (e *installError) Error() string {
	return fmt.Sprintf("recon: installing %s (%s) failed: %s", e.id, e.target, e.reason)
}

// Hint tells the caller where to confirm the pin and the Go toolchain.
func (e *installError) Hint() string {
	return "check `browser recon list` for the pinned version and confirm the Go toolchain (`go version`) can build it, then retry"
}

// Unwrap exposes the installer's exit error.
func (e *installError) Unwrap() error { return e.cause }

// firstMeaningfulLine returns the first installer line that is not download
// noise, trimmed and clipped, or "" when the log holds only such noise.
func firstMeaningfulLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "go: downloading ") ||
			strings.HasPrefix(line, "go: finding ") || strings.HasPrefix(line, "go: extracting ") {
			continue
		}
		return clipText(line, 200)
	}
	return ""
}

// probe runs spec.VersionArgs and requires the module identity in the output.
// A binary whose output never names the module is refused with its path and the
// first non-empty output line.
func probe(ctx context.Context, spec Spec, path string) error {
	ctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()

	args := spec.VersionArgs
	if len(args) == 0 {
		args = []string{"-version"}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("recon: identity probe for %s failed: %w", path, err)
	}
	text := string(output)
	if identityMatches(spec.Module, text) {
		return nil
	}
	return fmt.Errorf(
		"recon: identity probe refused %q: the binary printed %q, which does not name %q — it is not %s",
		path,
		firstLine(text),
		spec.Module,
		spec.ID,
	)
}

// identityMatches reports whether the probe output names the module. The full
// module path and each meaningful path element are accepted, case-insensitively.
func identityMatches(module, output string) bool {
	lower := strings.ToLower(output)
	for _, token := range identityTokens(module) {
		if token != "" && strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// identityTokens returns the module path plus each path element that is not a
// host, a "cmd" segment or a major-version suffix.
func identityTokens(module string) []string {
	module = strings.ToLower(strings.TrimSpace(module))
	if module == "" {
		return nil
	}
	seen := map[string]struct{}{module: {}}
	tokens := []string{module}
	for _, element := range strings.Split(module, "/") {
		if element == "" || element == "cmd" || versionElement.MatchString(element) {
			continue
		}
		if strings.Contains(element, ".") {
			continue
		}
		if _, ok := seen[element]; ok {
			continue
		}
		seen[element] = struct{}{}
		tokens = append(tokens, element)
	}
	return tokens
}

// firstLine returns the first non-empty output line, trimmed and clipped.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return clipText(line, 200)
		}
	}
	return "(no output)"
}

// clipText bounds s to at most n bytes on a UTF-8 boundary.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !isUTF8Start(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

// isUTF8Start reports whether b is not a UTF-8 continuation byte.
func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }
