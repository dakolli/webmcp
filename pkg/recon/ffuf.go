package recon

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/config"
)

// fuzzCommonWordlist is the embedded default wordlist for a call that names no
// wordlist: short, high-value paths with no offensive payloads.
//
//go:embed wordlists/common.txt
var fuzzCommonWordlist []byte

const (
	fuzzDefaultMatchCodes = "200,204,301,302,307,401,403"
	fuzzDefaultRate       = 20
	fuzzMaxRate           = 100
	fuzzDefaultThreads    = 40
	fuzzMaxThreads        = 200
)

// fuzzSpec is the pinned ffuf scanner descriptor. v2.3.0 is the newest stable
// release of github.com/ffuf/ffuf/v2 at build time.
var fuzzSpec = Spec{
	ID:          "recon_fuzz",
	Module:      "github.com/ffuf/ffuf/v2",
	Pin:         "v2.3.0",
	BinName:     "ffuf",
	VersionArgs: []string{"-V"},
	JSONArgs:    []string{"-json", "-s"},
	Doc: "Content discovery and parameter fuzzing with ffuf. Fuzz a path, a " +
		"vhost, or one query parameter value; each hit returns url, status, " +
		"length and words. Fuzzing is loud and sends many requests to the target.",
}

func init() { Register(fuzzSpec) }

// fuzzMode is one of ffuf's three fuzzing placements.
type fuzzMode string

const (
	fuzzDir   fuzzMode = "dir"
	fuzzVhost fuzzMode = "vhost"
	fuzzParam fuzzMode = "param"
)

// FuzzArgs is the typed argument surface for one ffuf run. URL is required;
// Wordlist is an absolute path and defaults to the embedded common list.
type FuzzArgs struct {
	URL        string   `json:"url"`
	Mode       string   `json:"mode"`
	Wordlist   string   `json:"wordlist"`
	Params     string   `json:"params"`
	Method     string   `json:"method"`
	Headers    []string `json:"headers"`
	MatchCodes string   `json:"match_codes"`
	FilterSize string   `json:"filter_size"`
	Rate       int      `json:"rate"`
	Threads    int      `json:"threads"`
	MaxEntries int      `json:"max_entries"`
	TimeoutMS  int      `json:"timeout_ms"`
	OutputFile string   `json:"output_file"`
	ExtraArgs  []string `json:"extra_args"`
	Scope      []string `json:"scope"`
}

// FuzzResult is the bounded outcome of one ffuf run. Mode names the placement
// that ran; the embedded Result carries the hits, cap and raw report path.
type FuzzResult struct {
	Result
	Mode string `json:"mode"`
}

// RunFuzz runs ffuf against one fuzz target. The wordlist defaults to the
// embedded common list, written to a 0600 temp file under the config dir and
// removed afterwards. Scope defaults to the target host; for vhost mode the
// FUZZ. marker is stripped so scope checks the parent domain.
func RunFuzz(ctx context.Context, args FuzzArgs) (result FuzzResult, err error) {
	target, mode, err := resolveFuzzTarget(args)
	if err != nil {
		return FuzzResult{}, err
	}

	host := hostnameOf(target)
	if mode == fuzzVhost {
		host = fuzzHost(target)
	}
	if host == "" {
		return FuzzResult{}, fmt.Errorf(
			"recon_fuzz: invalid arguments: cannot derive a host from url %q — pass an absolute http(s) URL",
			args.URL,
		)
	}
	scope := args.Scope
	if len(scope) == 0 {
		scope = []string{host}
	}
	guard := Guard{Scope: scope}
	if err := guard.CheckHosts([]string{host}); err != nil {
		return FuzzResult{}, err
	}

	wordlist, removeWordlist, err := resolveWordlist(args.Wordlist)
	if err != nil {
		return FuzzResult{}, err
	}
	defer func() { err = errors.Join(err, removeWordlist()) }()

	report, removeReport, err := resolveReport(args.OutputFile)
	if err != nil {
		return FuzzResult{}, err
	}
	defer func() { err = errors.Join(err, removeReport()) }()

	bin, err := Ensure(ctx, fuzzSpec)
	if err != nil {
		return FuzzResult{}, err
	}

	ran, err := executeFFUF(ctx, bin, args, target, wordlist, report)
	if err != nil {
		return FuzzResult{}, err
	}
	return FuzzResult{Result: ran, Mode: string(mode)}, nil
}

// resolveFuzzTarget validates the FUZZ placement for the selected mode and
// returns the URL ffuf receives. An empty mode is inferred from params and the
// URL host.
func resolveFuzzTarget(args FuzzArgs) (string, fuzzMode, error) {
	raw := strings.TrimSpace(args.URL)
	if raw == "" {
		return "", "", fmt.Errorf(
			"recon_fuzz: invalid arguments: url is required — pass an absolute URL containing FUZZ, e.g. https://example.com/FUZZ",
		)
	}

	params := strings.TrimSpace(args.Params)
	mode, err := fuzzModeFor(args.Mode, raw, params)
	if err != nil {
		return "", "", err
	}

	switch mode {
	case fuzzDir:
		if params != "" {
			return "", "", fmt.Errorf(
				"recon_fuzz: invalid arguments: params is only valid in param mode — remove params or set mode:\"param\"",
			)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("recon_fuzz: invalid arguments: url %q is not parseable: %w", raw, err)
		}
		if !strings.Contains(u.Path, "FUZZ") && !strings.Contains(u.RawQuery, "FUZZ") {
			return "", "", fmt.Errorf(
				"recon_fuzz: invalid arguments: dir mode requires the literal FUZZ marker in the url path or query (example: https://example.com/FUZZ) — got %q",
				raw,
			)
		}
		return raw, fuzzDir, nil

	case fuzzVhost:
		if params != "" {
			return "", "", fmt.Errorf(
				"recon_fuzz: invalid arguments: params is only valid in param mode — remove params or set mode:\"param\"",
			)
		}
		if !strings.Contains(strings.ToLower(hostnameOf(raw)), "fuzz.") {
			return "", "", fmt.Errorf(
				"recon_fuzz: invalid arguments: vhost mode requires FUZZ. in the url host (example: https://FUZZ.example.com) — got %q",
				raw,
			)
		}
		if fuzzHost(raw) == "" {
			return "", "", fmt.Errorf(
				"recon_fuzz: invalid arguments: vhost mode requires a host after FUZZ. (example: https://FUZZ.example.com) — got %q",
				raw,
			)
		}
		return raw, fuzzVhost, nil

	case fuzzParam:
		if params == "" {
			if !strings.Contains(raw, "FUZZ") {
				return "", "", fmt.Errorf(
					"recon_fuzz: invalid arguments: param mode requires params to name a query parameter, or the literal FUZZ marker in the url (example: https://example.com/search?q=FUZZ) — got %q",
					raw,
				)
			}
			return raw, fuzzParam, nil
		}
		if strings.ContainsAny(params, "&=") {
			return "", "", fmt.Errorf(
				"recon_fuzz: invalid arguments: params %q must be a bare query parameter name, without & or =",
				params,
			)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("recon_fuzz: invalid arguments: url %q is not parseable: %w", raw, err)
		}
		query := u.Query()
		query.Set(params, "FUZZ")
		u.RawQuery = query.Encode()
		return u.String(), fuzzParam, nil

	default:
		return "", "", fmt.Errorf(
			"recon_fuzz: invalid arguments: mode %q is not dir, vhost, or param",
			mode,
		)
	}
}

// fuzzModeFor resolves an explicit mode or infers one: params means param,
// FUZZ. in the host means vhost, and anything else means dir.
func fuzzModeFor(
	rawMode string,
	rawURL string,
	params string,
) (fuzzMode, error) {
	switch strings.ToLower(strings.TrimSpace(rawMode)) {
	case "":
		switch {
		case params != "":
			return fuzzParam, nil
		case strings.Contains(strings.ToLower(hostnameOf(rawURL)), "fuzz."):
			return fuzzVhost, nil
		default:
			return fuzzDir, nil
		}
	case string(fuzzDir):
		return fuzzDir, nil
	case string(fuzzVhost):
		return fuzzVhost, nil
	case string(fuzzParam):
		return fuzzParam, nil
	default:
		return "", fmt.Errorf(
			"recon_fuzz: invalid arguments: mode %q is not dir, vhost, or param",
			rawMode,
		)
	}
}

// hostnameOf returns a URL's lowercase host, or "" when the URL is unparseable.
func hostnameOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// fuzzHost returns the host a fuzz target resolves to, with every FUZZ. marker
// removed so scope checks the parent domain.
func fuzzHost(raw string) string {
	host := strings.ReplaceAll(hostnameOf(raw), "fuzz.", "")
	return strings.TrimSuffix(host, ".")
}

// resolveWordlist returns an absolute wordlist path. An empty argument writes
// the embedded list to a 0600 temp file under the config dir; the returned
// cleanup removes it.
func resolveWordlist(raw string) (string, func() error, error) {
	path := strings.TrimSpace(raw)
	if path != "" {
		if !filepath.IsAbs(path) {
			return "", nil, fmt.Errorf(
				"recon_fuzz: invalid arguments: wordlist %q must be absolute — pass an absolute path",
				path,
			)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", nil, fmt.Errorf("recon_fuzz: inspect wordlist %q: %w", path, err)
		}
		if info.IsDir() {
			return "", nil, fmt.Errorf("recon_fuzz: invalid arguments: wordlist %q is a directory — pass a file", path)
		}
		return path, func() error { return nil }, nil
	}

	cfgDir, err := config.ConfigDir()
	if err != nil {
		return "", nil, fmt.Errorf("recon_fuzz: resolve config dir: %w", err)
	}
	dir := filepath.Join(cfgDir, "recon")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, fmt.Errorf("recon_fuzz: create wordlist dir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "fuzz-wordlist-*.txt")
	if err != nil {
		return "", nil, fmt.Errorf("recon_fuzz: create generated wordlist: %w", err)
	}
	generated := f.Name()
	if err := f.Chmod(0600); err != nil {
		return "", nil, errors.Join(
			fmt.Errorf("recon_fuzz: chmod generated wordlist %q: %w", generated, err),
			f.Close(),
		)
	}
	if _, err := f.Write(fuzzCommonWordlist); err != nil {
		return "", nil, errors.Join(
			fmt.Errorf("recon_fuzz: write generated wordlist %q: %w", generated, err),
			f.Close(),
		)
	}
	if err := f.Close(); err != nil {
		return "", nil, fmt.Errorf("recon_fuzz: close generated wordlist %q: %w", generated, err)
	}
	remove := func() error {
		if err := os.Remove(generated); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recon_fuzz: remove generated wordlist %q: %w", generated, err)
		}
		return nil
	}
	return generated, remove, nil
}

// resolveReport returns the ffuf report path. An empty output_file creates a
// temp file whose cleanup removes it; a caller path must be absolute.
func resolveReport(raw string) (string, func() error, error) {
	path := strings.TrimSpace(raw)
	if path != "" {
		if !filepath.IsAbs(path) {
			return "", nil, fmt.Errorf(
				"recon_fuzz: invalid arguments: output_file %q must be absolute — pass an absolute path",
				path,
			)
		}
		return path, func() error { return nil }, nil
	}

	f, err := os.CreateTemp("", "recon-fuzz-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("recon_fuzz: create report temp file: %w", err)
	}
	path = f.Name()
	if err := f.Close(); err != nil {
		return "", nil, fmt.Errorf("recon_fuzz: close report temp file %q: %w", path, err)
	}
	remove := func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recon_fuzz: remove report temp file %q: %w", path, err)
		}
		return nil
	}
	return path, remove, nil
}

// fuzzArgv builds the ffuf argv for one run. The JSON report goes to a file
// because ffuf does not emit the report on stdout.
func fuzzArgv(
	args FuzzArgs,
	target string,
	wordlist string,
	report string,
) []string {
	argv := make([]string, 0, 24+len(args.Headers)+len(args.ExtraArgs))
	argv = append(argv,
		"-noninteractive",
		"-s",
		"-u", target,
		"-w", wordlist,
		"-of", "json",
		"-o", report,
	)

	method := strings.TrimSpace(args.Method)
	if method == "" {
		method = "GET"
	}
	argv = append(argv, "-X", method)

	for _, header := range args.Headers {
		if h := strings.TrimSpace(header); h != "" {
			argv = append(argv, "-H", h)
		}
	}

	argv = append(argv, "-mc", fuzzMatchCodes(args.MatchCodes))
	if size := strings.TrimSpace(args.FilterSize); size != "" {
		argv = append(argv, "-fs", size)
	}
	argv = append(argv,
		"-rate", strconv.Itoa(fuzzRate(args.Rate)),
		"-t", strconv.Itoa(fuzzThreads(args.Threads)),
	)
	return append(argv, args.ExtraArgs...)
}

// fuzzMatchCodes resolves the -mc value, defaulting an empty list.
func fuzzMatchCodes(raw string) string {
	if codes := strings.TrimSpace(raw); codes != "" {
		return codes
	}
	return fuzzDefaultMatchCodes
}

// fuzzRate resolves the -rate value: default 20 and a hard ceiling of 100.
func fuzzRate(n int) int {
	if n <= 0 {
		return fuzzDefaultRate
	}
	if n > fuzzMaxRate {
		return fuzzMaxRate
	}
	return n
}

// fuzzThreads resolves the -t value: default 40 and a ceiling of 200.
func fuzzThreads(n int) int {
	if n <= 0 {
		return fuzzDefaultThreads
	}
	if n > fuzzMaxThreads {
		return fuzzMaxThreads
	}
	return n
}

// executeFFUF runs the resolved ffuf binary, reads its JSON report and maps the
// hits into the bounded Result.
func executeFFUF(
	ctx context.Context,
	bin string,
	args FuzzArgs,
	target string,
	wordlist string,
	report string,
) (Result, error) {
	budget := timeoutFor(args.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, fuzzArgv(args, target, wordlist, report)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if runCtx.Err() != nil {
		return Result{}, fmt.Errorf("recon_fuzz: exceeded its %s budget: %w", budget, runCtx.Err())
	}

	raw, readErr := os.ReadFile(report)
	if readErr != nil {
		if runErr != nil {
			return Result{}, fuzzRunError(stderr.String(), runErr)
		}
		return Result{}, fmt.Errorf("recon_fuzz: read ffuf report %q: %w", report, readErr)
	}
	if runErr != nil && len(bytes.TrimSpace(raw)) == 0 {
		return Result{}, fuzzRunError(stderr.String(), runErr)
	}

	entries, err := parseFFUFReport(raw)
	if err != nil {
		return Result{}, err
	}
	max := maxEntries(args.MaxEntries)
	truncated, dropped := 0, 0
	if len(entries) > max {
		truncated, dropped = 1, len(entries)-max
		entries = entries[:max]
	}
	return Result{
		Raw:        raw,
		Entries:    entries,
		Count:      len(entries),
		Truncated:  truncated,
		Dropped:    dropped,
		OutputFile: strings.TrimSpace(args.OutputFile),
	}, nil
}

// fuzzRunError builds the failure error, surfacing ffuf's stderr when it has any
// and always preserving the process error.
func fuzzRunError(stderr string, runErr error) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("recon_fuzz: ffuf failed: %s: %w", clipText(msg, stderrClip), runErr)
	}
	return fmt.Errorf("recon_fuzz: ffuf failed: %w", runErr)
}

// ffufResult is one entry in ffuf's JSON report.
type ffufResult struct {
	Input       map[string]string `json:"input"`
	Status      int               `json:"status"`
	Length      int               `json:"length"`
	Words       int               `json:"words"`
	Lines       int               `json:"lines"`
	ContentType string            `json:"content-type"`
	Redirect    string            `json:"redirectlocation"`
	Duration    int64             `json:"duration"`
	URL         string            `json:"url"`
}

// ffufReport is the top-level shape of ffuf's JSON report.
type ffufReport struct {
	Results []ffufResult `json:"results"`
}

// parseFFUFReport maps ffuf's JSON report to compact hits. An entry with no URL
// is a filtered or blank row and is skipped.
func parseFFUFReport(raw []byte) ([]map[string]any, error) {
	var report ffufReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("recon_fuzz: decode ffuf report: %w", err)
	}

	entries := make([]map[string]any, 0, len(report.Results))
	for _, hit := range report.Results {
		if strings.TrimSpace(hit.URL) == "" {
			continue
		}
		entry := map[string]any{
			"url":    hit.URL,
			"status": hit.Status,
			"length": hit.Length,
			"words":  hit.Words,
		}
		if input := firstFFUFInput(hit.Input); input != "" {
			entry["input"] = input
		}
		if hit.Lines > 0 {
			entry["lines"] = hit.Lines
		}
		if hit.ContentType != "" {
			entry["content_type"] = hit.ContentType
		}
		if hit.Redirect != "" {
			entry["redirect"] = hit.Redirect
		}
		if hit.Duration > 0 {
			entry["duration_ms"] = hit.Duration / int64(time.Millisecond)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// firstFFUFInput returns the matched wordlist value from ffuf's input map. It
// prefers the default FUZZ keyword and ignores ffuf's internal FFUFHASH entry,
// which is bookkeeping rather than a wordlist value.
func firstFFUFInput(input map[string]string) string {
	if len(input) == 0 {
		return ""
	}
	if value := strings.TrimSpace(input["FUZZ"]); value != "" {
		return value
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		if key == "FFUFHASH" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := strings.TrimSpace(input[key]); value != "" {
			return value
		}
	}
	return ""
}
