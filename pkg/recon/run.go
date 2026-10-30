package recon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// defaultMaxEntries bounds the entries one Run returns when none is set.
	defaultMaxEntries = 200
	// defaultTimeoutMS is the run budget when none is set.
	defaultTimeoutMS = 120_000
	// maxTimeoutMS is the ceiling a caller's budget is clamped to.
	maxTimeoutMS = 1_800_000
	// noColorFlag disables ANSI colour so JSONL stays parseable.
	noColorFlag = "--no-color"
	// stderrClip is how much stderr a failed run surfaces.
	stderrClip = 400
)

// Options shapes one scanner run.
type Options struct {
	// MaxEntries is the entry cap; zero uses defaultMaxEntries.
	MaxEntries int
	// TimeoutMS is the run budget in milliseconds; zero uses the 120 s default
	// and any value above 1800 s is clamped.
	TimeoutMS int
	// OutputFile is an absolute path for the raw stdout report, written 0600.
	OutputFile string
	// ExtraArgs are appended verbatim after the caller args.
	ExtraArgs []string
	// DedupeKey names one entry's identity; a repeated identity is dropped
	// before the entry cap applies. A nil key or an empty result keeps the entry.
	DedupeKey func(map[string]any) string
}

// Run executes spec's pinned binary and decodes its JSONL stdout into entries.
// Binary resolution happens through Ensure, so PATH is never consulted.
func Run(ctx context.Context, spec Spec, args []string, opts Options) (Result, error) {
	if opts.OutputFile != "" && !filepath.IsAbs(opts.OutputFile) {
		return Result{}, fmt.Errorf(
			"recon: invalid arguments: output_file %q must be absolute — pass an absolute path",
			opts.OutputFile,
		)
	}

	bin, err := Ensure(ctx, spec)
	if err != nil {
		return Result{}, err
	}

	budget := timeoutFor(opts.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, assembleArgs(spec, args, opts.ExtraArgs)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if runCtx.Err() != nil {
		return Result{}, fmt.Errorf("recon: %s exceeded its %s budget: %w", spec.ID, budget, runCtx.Err())
	}

	raw := stdout.Bytes()
	if err := exitFailure(spec, raw, stderr.Bytes(), runErr); err != nil {
		return Result{}, err
	}
	if err := emptyOutputError(spec, args, raw, runErr, opts.OutputFile); err != nil {
		return Result{}, err
	}

	parsed := dedupeEntries(parseEntries(raw), opts.DedupeKey)
	entries, truncated, dropped := capEntries(parsed, maxEntries(opts.MaxEntries))
	if opts.OutputFile != "" {
		if err := writeOutput(opts.OutputFile, raw); err != nil {
			return Result{}, err
		}
	}
	return Result{
		Raw:        raw,
		Entries:    entries,
		Count:      len(entries),
		Truncated:  truncated,
		Dropped:    dropped,
		OutputFile: opts.OutputFile,
	}, nil
}

// exitFailure turns a non-zero scanner exit into an error. A scanner that writes
// a diagnostic and exits non-zero failed even when stdout carries text, so no
// stdout line is decoded as an entry; the message prefers stderr, then stdout,
// then the exit error.
func exitFailure(
	spec Spec,
	stdout []byte,
	stderr []byte,
	runErr error,
) error {
	if runErr == nil {
		return nil
	}
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(stdout))
	}
	if msg == "" {
		msg = runErr.Error()
	}
	return fmt.Errorf("recon: %s failed: %s", spec.ID, clipText(msg, stderrClip))
}

// emptyOutputError refuses a scanner that exited 0 with no stdout when its spec
// says an answer was expected. The message names the probed targets and both
// plausible causes — silence on that port or flags that never consumed the
// input — so neither is blamed. Scanners whose empty answer is real (nuclei zero
// findings, subfinder or dnsx no records, ffuf no matches, katana no URLs, naabu
// no open ports) leave RequireOutput false and keep returning count:0. An
// explicit output_file request is exempt because the caller owns that artifact.
func emptyOutputError(
	spec Spec,
	args []string,
	raw []byte,
	runErr error,
	outputFile string,
) error {
	if runErr != nil || !spec.RequireOutput || outputFile != "" || len(bytes.TrimSpace(raw)) > 0 {
		return nil
	}
	subject := spec.ID
	if targets := argvTargets(args); len(targets) > 0 {
		subject = spec.ID + " targets " + strings.Join(targets, ", ")
	}
	return fmt.Errorf(
		"%s: exited 0 with no output — either the targets answered nothing on that port, or the pinned %s flags never consumed them; check extra_args if you passed any",
		subject,
		spec.BinName,
	)
}

// argvTargets collects the values passed to the -u switch so an empty-output
// failure can name what the scanner was asked to probe.
func argvTargets(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "-u" || i+1 >= len(args) {
			continue
		}
		if target := strings.TrimSpace(args[i+1]); target != "" {
			out = append(out, target)
		}
		i++
	}
	return out
}

// assembleArgs builds the argv: the tool's JSON flags, the caller args, then the
// extra args, with the colour switch guaranteed.
func assembleArgs(spec Spec, args, extra []string) []string {
	argv := make([]string, 0, len(spec.JSONArgs)+len(args)+len(extra)+1)
	argv = append(argv, spec.JSONArgs...)
	argv = append(argv, args...)
	argv = append(argv, extra...)
	return withNoColor(argv)
}

// withNoColor appends the colour switch unless the caller already supplied it.
func withNoColor(args []string) []string {
	for _, arg := range args {
		if arg == noColorFlag || arg == "-no-color" {
			return args
		}
	}
	return append(args, noColorFlag)
}

// timeoutFor resolves the run budget: absent uses the default and the ceiling
// clamps a longer request.
func timeoutFor(ms int) time.Duration {
	if ms <= 0 {
		return defaultTimeoutMS * time.Millisecond
	}
	if ms > maxTimeoutMS {
		return maxTimeoutMS * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

// maxEntries resolves the entry cap, defaulting a non-positive value.
func maxEntries(n int) int {
	if n <= 0 {
		return defaultMaxEntries
	}
	return n
}

// decodeEntries parses stdout line by line and applies the entry cap. A line
// that is not a JSON object is preserved as {"raw": line}.
func decodeEntries(raw []byte, max int) (entries []map[string]any, truncated, dropped int) {
	return capEntries(parseEntries(raw), max)
}

// parseEntries decodes each non-empty stdout line into a JSON object, keeping a
// non-JSON line as {"raw": line} so a decode failure stays visible.
func parseEntries(raw []byte) []map[string]any {
	lines := bytes.Split(raw, []byte("\n"))
	parsed := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		text := strings.TrimSpace(string(line))
		if text == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(text), &entry); err != nil || entry == nil {
			entry = map[string]any{"raw": text}
		}
		parsed = append(parsed, entry)
	}
	return parsed
}

// dedupeEntries drops a repeated identity while preserving order. A nil key or
// an empty key keeps the entry, so a malformed or identity-less line is never
// lost.
func dedupeEntries(
	entries []map[string]any,
	key func(map[string]any) string,
) []map[string]any {
	if key == nil {
		return entries
	}
	seen := make(map[string]struct{}, len(entries))
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		identity := key(entry)
		if identity == "" {
			out = append(out, entry)
			continue
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		out = append(out, entry)
	}
	return out
}

// capEntries applies the entry cap. When more entries are parsed than max,
// truncated is 1 and dropped is the number the cap omitted.
func capEntries(parsed []map[string]any, max int) (entries []map[string]any, truncated, dropped int) {
	if max < 1 {
		max = defaultMaxEntries
	}
	if len(parsed) <= max {
		return parsed, 0, 0
	}
	return parsed[:max], 1, len(parsed) - max
}

// writeOutput writes the raw report to an absolute path with mode 0600.
func writeOutput(path string, raw []byte) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("recon: invalid arguments: output_file %q must be absolute", path)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("recon: write output_file %q: %w", path, err)
	}
	return nil
}
