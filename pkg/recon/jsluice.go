package recon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// jsluice has no upstream semver tags, so Pin is the newest resolvable version
// (the v0.0.0-2024 pseudo-version) as resolved on 2026-10-30. CGO is required:
// jsluice parses with smacker/go-tree-sitter, which has no pure-Go build.
var JSLuice = Spec{
	ID:          "recon_js",
	Module:      "github.com/BishopFox/jsluice/cmd/jsluice",
	Pin:         "v0.0.0-20240110145140-0ddfab153e06",
	BinName:     "jsluice",
	VersionArgs: []string{"--help"},
	JSONArgs:    nil,
	CGO:         true,
	Doc: "Analyzes JavaScript with jsluice's Tree-sitter AST for URLs, paths and secrets a regex misses. " +
		"jsluice is subcommand based: this generic path runs `jsluice <mode> <file>`, so targets are " +
		"[mode, absolute-file] and extra_args follow. Over MCP prefer recon_js, which adds mode, urls, " +
		"js_files, fetching and secret redaction.",
}

// jsluice limits. The fetch budget and byte ceiling bound a remote script before
// it reaches the parser; the number of JSONL lines one file may emit stays
// bounded so a pathological bundle cannot exhaust memory.
const (
	jsluiceFetchBudget = 20 * time.Second
	jsluiceByteCeiling = 5 << 20
	jsluiceLineCap     = 1_000_000
	jsluicePrefix      = 8
	jsluiceSuffix      = 4
)

// JSLuiceArgs is the typed input to RunJSLuice. Exactly one of URLs or JSFiles
// is required. Scope, ScopeStrict, RPS and Burst shape the consent guard; an
// empty scope defaults to the literal hosts of URLs.
type JSLuiceArgs struct {
	Mode        string
	URLs        []string
	JSFiles     []string
	MaxBytes    int
	Scope       []string
	ScopeStrict bool
	RPS         float64
	Burst       int
}

// jsInput is one script to analyze: a local file, or a fetched file plus the
// source URL that relative paths resolve against.
type jsInput struct {
	file      string
	sourceURL string
	local     bool
}

// init registers the jsluice spec so recon listings and the CLI forwarder see it
// without a shared table edit.
func init() {
	Register(JSLuice)
}

// RunJSLuice analyzes local or fetched JavaScript with jsluice and returns
// normalized, redacted entries. The scope guard runs on the URLs before any
// fetch and on every redirect hop.
func RunJSLuice(
	ctx context.Context,
	args JSLuiceArgs,
	opts Options,
) (Result, error) {
	modes, err := jsluiceModes(args.Mode)
	if err != nil {
		return Result{}, err
	}
	if len(args.URLs) > 0 && len(args.JSFiles) > 0 {
		return Result{}, fmt.Errorf(
			"recon_js: invalid arguments: urls and js_files are mutually exclusive — pass one input kind",
		)
	}
	if len(args.URLs) == 0 && len(args.JSFiles) == 0 {
		return Result{}, fmt.Errorf(
			"recon_js: invalid arguments: one input is required — pass urls or js_files",
		)
	}

	guard := Guard{
		Scope:       args.Scope,
		ScopeStrict: args.ScopeStrict,
		RPS:         args.RPS,
		Burst:       args.Burst,
	}
	if len(guard.Scope) == 0 && !guard.ScopeStrict {
		guard.Scope = jsluiceLiteralHosts(args.URLs)
	}
	if err := guard.CheckURLs(args.URLs); err != nil {
		return Result{}, err
	}

	limit, err := jsluiceByteLimit(args.MaxBytes, args.JSFiles)
	if err != nil {
		return Result{}, err
	}

	bin, err := Ensure(ctx, JSLuice)
	if err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeoutFor(opts.TimeoutMS))
	defer cancel()

	inputs, cleanup, err := jsluiceInputs(runCtx, args, limit, &guard)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	all := make([]map[string]any, 0, 64)
	for _, in := range inputs {
		for _, mode := range modes {
			entries, err := jsluiceRun(runCtx, bin, mode, in, opts.ExtraArgs)
			if err != nil {
				return Result{}, err
			}
			all = append(all, normalizeJSLuice(mode, entries, in)...)
		}
	}

	max := maxEntries(opts.MaxEntries)
	truncated, dropped := 0, 0
	entries := all
	if len(all) > max {
		entries = all[:max]
		truncated = 1
		dropped = len(all) - max
	}

	raw, err := marshalEntries(all)
	if err != nil {
		return Result{}, err
	}
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

// jsluiceModes expands the caller mode into the subcommands to run.
func jsluiceModes(mode string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "urls":
		return []string{"urls"}, nil
	case "secrets":
		return []string{"secrets"}, nil
	case "both":
		return []string{"urls", "secrets"}, nil
	default:
		return nil, fmt.Errorf(
			"recon_js: invalid arguments: mode %q must be urls, secrets, or both",
			mode,
		)
	}
}

// jsluiceLiteralHosts reduces URL inputs to their distinct lowercase hosts, the
// default allowlist when the caller passes no scope.
func jsluiceLiteralHosts(raw []string) []string {
	hosts := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, target := range raw {
		u, err := url.Parse(strings.TrimSpace(target))
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts
}

// jsluiceByteLimit resolves the per-file ceiling, clamping an over-large request
// to the hard 5 MB cap and refusing a local file already over it.
func jsluiceByteLimit(requested int, files []string) (int, error) {
	limit := requested
	if limit <= 0 || limit > jsluiceByteCeiling {
		limit = jsluiceByteCeiling
	}
	for _, path := range files {
		if !filepath.IsAbs(path) {
			return 0, fmt.Errorf(
				"recon_js: invalid arguments: js_files entry %q must be an absolute path",
				path,
			)
		}
		info, err := os.Stat(path)
		if err != nil {
			return 0, fmt.Errorf("recon_js: read js_files entry %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf(
				"recon_js: invalid arguments: js_files entry %q is not a regular file",
				path,
			)
		}
		if info.Size() > int64(limit) {
			return 0, fmt.Errorf(
				"recon_js: invalid arguments: js_files entry %q is %d bytes, over the %d-byte max_bytes ceiling — raise max_bytes or split the file",
				path,
				info.Size(),
				limit,
			)
		}
	}
	return limit, nil
}

// jsluiceInputs resolves local files or fetches every URL into a temporary
// directory. The returned cleanup removes the temporary directory.
func jsluiceInputs(
	ctx context.Context,
	args JSLuiceArgs,
	limit int,
	guard *Guard,
) ([]jsInput, func(), error) {
	if len(args.JSFiles) > 0 {
		inputs := make([]jsInput, 0, len(args.JSFiles))
		for _, path := range args.JSFiles {
			inputs = append(inputs, jsInput{file: path, local: true})
		}
		return inputs, func() {}, nil
	}

	dir, err := os.MkdirTemp("", "webmcp-jsluice-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("recon_js: create fetch dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	client := &http.Client{
		Timeout: jsluiceFetchBudget,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return guard.CheckURLs([]string{req.URL.String()})
		},
	}

	inputs := make([]jsInput, 0, len(args.URLs))
	for i, target := range args.URLs {
		path, err := jsluiceFetch(ctx, client, target, dir, i, limit)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		inputs = append(inputs, jsInput{file: path, sourceURL: target})
	}
	return inputs, cleanup, nil
}

// jsluiceFetch downloads one script into dir, enforcing the byte ceiling.
func jsluiceFetch(
	ctx context.Context,
	client *http.Client,
	target string,
	dir string,
	index int,
	limit int,
) (string, error) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf(
			"recon_js: invalid arguments: url %q is not an absolute http(s) URL",
			target,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("recon_js: build request for %q: %w", target, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("recon_js: fetch %q: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf(
			"recon_js: fetch %q returned HTTP %d — pass a reachable script URL",
			target,
			resp.StatusCode,
		)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", fmt.Errorf("recon_js: read %q: %w", target, err)
	}
	if len(data) > limit {
		return "", fmt.Errorf(
			"recon_js: %q is larger than the %d-byte max_bytes ceiling — raise max_bytes or fetch a smaller script",
			target,
			limit,
		)
	}

	path := filepath.Join(dir, fmt.Sprintf("script-%d.js", index))
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("recon_js: write fetched script %q: %w", target, err)
	}
	return path, nil
}

// jsluiceRun executes one subcommand against one input. jsluice rejects the
// shared --no-color switch, so it is executed directly rather than via Run.
func jsluiceRun(
	ctx context.Context,
	bin string,
	mode string,
	in jsInput,
	extra []string,
) ([]map[string]any, error) {
	argv := make([]string, 0, 4+len(extra))
	argv = append(argv, mode)
	if mode == "urls" && in.sourceURL != "" {
		argv = append(argv, "-R", in.sourceURL)
	}
	argv = append(argv, in.file)
	argv = append(argv, extra...)

	cmd := exec.CommandContext(ctx, bin, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if ctx.Err() != nil {
		return nil, fmt.Errorf(
			"recon_js: exceeded its run budget analyzing %q: %w",
			in.label(),
			ctx.Err(),
		)
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if runErr != nil && len(raw) == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		return nil, fmt.Errorf(
			"recon_js: jsluice %s failed on %q: %s",
			mode,
			in.label(),
			clipText(msg, stderrClip),
		)
	}
	entries, _, _ := decodeEntries(stdout.Bytes(), jsluiceLineCap)
	return entries, nil
}

// label names the analyzed script for errors and entries.
func (in jsInput) label() string {
	if in.sourceURL != "" {
		return in.sourceURL
	}
	return in.file
}

// normalizeJSLuice re-shapes raw jsluice objects into the compact entry contract
// and redacts every secret value.
func normalizeJSLuice(
	mode string,
	entries []map[string]any,
	in jsInput,
) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if mode == "secrets" {
			if secret, ok := normalizeSecret(entry, in); ok {
				out = append(out, secret)
			}
			continue
		}
		if found, ok := normalizeURL(entry, in); ok {
			out = append(out, found)
		}
	}
	return out
}

// normalizeURL shapes one urls entry and marks unresolved relative paths found
// in a local file.
func normalizeURL(entry map[string]any, in jsInput) (map[string]any, bool) {
	urlText, _ := entry["url"].(string)
	if urlText == "" {
		return nil, false
	}
	method, _ := entry["method"].(string)
	context, _ := entry["type"].(string)
	kind := jsluiceURLKind(urlText)

	found := map[string]any{
		"url":      urlText,
		"method":   method,
		"context":  context,
		"kind":     kind,
		"filename": in.label(),
	}
	if params, ok := entry["queryParams"].([]any); ok && len(params) > 0 {
		found["query_params"] = params
	}
	if in.local && kind == "relative" {
		found["relative"] = true
	}
	return found, true
}

// normalizeSecret shapes one secrets entry with a middle-redacted value. It
// reports false when no secret string can be extracted.
func normalizeSecret(entry map[string]any, in jsInput) (map[string]any, bool) {
	kind, _ := entry["kind"].(string)
	value := secretValue(entry["data"])
	if value == "" {
		return nil, false
	}
	display, prefix, length := redactSecret(value)
	severity, _ := entry["severity"].(string)

	secret := map[string]any{
		"kind":         kind,
		"value":        display,
		"value_prefix": prefix,
		"value_length": length,
		"redacted":     true,
		"filename":     in.label(),
		"severity":     severity,
		"context":      severity,
	}
	return secret, true
}

// secretValue extracts the secret string from jsluice's data object or string.
func secretValue(data any) string {
	switch value := data.(type) {
	case string:
		return value
	case map[string]any:
		for _, key := range []string{"key", "value", "secret", "token", "password", "privateKey"} {
			if s, ok := value[key].(string); ok && s != "" {
				return s
			}
		}
		for _, raw := range value {
			if s, ok := raw.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// redactSecret returns a display value that keeps only a prefix and, for long
// secrets, a suffix; the middle is always replaced.
func redactSecret(value string) (display, prefix string, length int) {
	runes := []rune(value)
	length = len(runes)
	keep := jsluicePrefix
	if length <= keep {
		keep = length / 2
	}
	prefix = string(runes[:keep])
	suffix := ""
	if length >= 2*(keep+jsluiceSuffix) {
		suffix = string(runes[length-jsluiceSuffix:])
	}
	return prefix + "…" + suffix, prefix, length
}

// jsluiceURLKind reports whether a URL carries its own scheme and host.
func jsluiceURLKind(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme != "" && u.Host != "" {
		return "absolute"
	}
	return "relative"
}

// marshalEntries renders normalized entries as JSONL, the report written to
// output_file and stored in Result.Raw.
func marshalEntries(entries []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("recon_js: encode entry: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}
