package recon

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// HTTPID is the MCP tool name and registry key for HTTP probing.
const HTTPID = "recon_http"

const (
	// httpxPin is the newest stable httpx release, resolved with
	// `go list -m -versions github.com/projectdiscovery/httpx` on 2026-10-30.
	httpxPin = "v1.12.0"
	// httpTitleClip bounds a returned page title in bytes on a UTF-8 boundary.
	httpTitleClip = 200
	// httpIdentity is the organization named in the httpx -version banner. The
	// full module path is never printed, but projectdiscovery.io always is; the
	// Python httpx that shadows the name on PATH prints neither.
	httpIdentity = "projectdiscovery"
)

// httpANSI matches a terminal colour escape so a decoded JSON string stays clean.
var httpANSI = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// httpxSpec pins projectdiscovery httpx and registers recon_http from init.
var httpxSpec = Spec{
	ID:            HTTPID,
	Module:        "github.com/projectdiscovery/httpx/cmd/httpx",
	Pin:           httpxPin,
	BinName:       "httpx",
	VersionArgs:   []string{"-version"},
	JSONArgs:      []string{"-json", "-silent", "-nc"},
	RequireOutput: true,
	Doc:           httpxDoc,
}

func init() {
	Register(httpxSpec)
}

// httpxDoc is the scanner-specific description shown on the generic recon tool.
const httpxDoc = "probes hosts and URLs with the pinned projectdiscovery httpx binary and returns " +
	"one compact entry per answered URL. Consent: set authorized:true only for hosts you are " +
	"permitted to probe. On this generic surface reach the scanner flags through extra_args, e.g. " +
	"[\"-status-code\",\"-title\",\"-td\"]; the typed recon_http tool exposes them as fields. " +
	"Every target is passed as -u: this version has no bare positional input. A clean exit with " +
	"empty stdout is reported as a failure, not a zero-entry scan. " +
	"httpx on PATH is a Python script: the binary resolves from the private recon GOBIN and its " +
	"-version banner must name projectdiscovery, so an identity refusal means the false binary must be removed."

// HTTPDoc is the LLM-facing description of the typed recon_http tool.
const HTTPDoc = "recon_http: probes hosts and URLs with projectdiscovery httpx and returns one " +
	"compact entry per answered URL.\n\n" +
	"Arguments:\n" +
	"| arg | type | meaning |\n" +
	"| targets | []string, required | hosts or URLs to probe, at most 500 |\n" +
	"| ports | []string | ports to try, e.g. [\"80\",\"443\",\"8080\"] (-ports) |\n" +
	"| status_code | bool | return the HTTP status (-status-code) |\n" +
	"| title | bool | return the page title, clipped at 200 characters (-title) |\n" +
	"| tech | bool | return detected technologies, default true (-td) |\n" +
	"| tls | bool | return the certificate subject, issuer and expiry (-tls-grab) |\n" +
	"| follow_redirects | bool | follow redirects (-fr) |\n" +
	"| threads | int | worker threads (-threads) |\n" +
	"| rate_limit | int | requests per second (-rl) |\n" +
	"| timeout_ms | int | run budget, default 120000, ceiling 1800000 |\n" +
	"| max_entries | int | decoded entries returned, default 200 |\n" +
	"| output_file | string | absolute path for the raw JSONL report, mode 0600 |\n" +
	"| extra_args | []string | httpx flags appended verbatim after the typed ones |\n" +
	"| scope | []string | host allowlist; defaults to the literal target hosts |\n" +
	"| authorized | bool, required | consent gate; must be true |\n\n" +
	"Only the fields you request are returned.\n" +
	"Example: {\"targets\":[\"example.com\"],\"ports\":[\"80\",\"443\",\"8080\"],\"status_code\":true,\"title\":true,\"authorized\":true}\n" +
	"Errors: invalid_args (missing authorized, an out-of-scope target, an over-cap list, or a bad " +
	"argument), timeout (the run budget expired), unknown (the scanner failed and its stderr is in " +
	"the message). A run that exits 0 with no JSON is an error, never count:0: a host that answered " +
	"always yields one entry. An identity refusal names the binary path and its first -version line: " +
	"httpx on PATH is a Python script, so remove the false binary and retry.\n" +
	"Consent: pass authorized:true only for hosts you are permitted to scan."

// HTTPOptions shapes one httpx invocation. Only the requested fields are emitted
// per entry, so an unset StatusCode, Title, Tech or TLS keeps the payload small.
type HTTPOptions struct {
	// Targets are the hosts or URLs to probe, capped at MaxHosts.
	Targets []string
	// Ports are probed ports passed to httpx -ports.
	Ports []string
	// StatusCode requests the HTTP status of each probe.
	StatusCode bool
	// Title requests the page title.
	Title bool
	// Tech requests detected technologies.
	Tech bool
	// TLS requests the certificate subject, issuer and expiry.
	TLS bool
	// FollowRedirects asks httpx to follow redirects.
	FollowRedirects bool
	// Threads is the worker count; zero leaves httpx's default.
	Threads int
	// RateLimit is requests per second; zero leaves httpx's default.
	RateLimit int
	// MaxEntries is the entry cap; zero uses the runner default.
	MaxEntries int
	// TimeoutMS is the run budget in milliseconds; zero uses the runner default.
	TimeoutMS int
	// OutputFile is an absolute path for the raw JSONL report.
	OutputFile string
	// ExtraArgs are appended verbatim after the typed httpx arguments.
	ExtraArgs []string
	// Scope is the host allowlist; empty defaults to the literal target hosts.
	Scope []string
}

// HTTPEntry is one compact probe result: the URL plus only the fields requested.
// Raw preserves a stdout line that failed to decode so the failure stays visible.
type HTTPEntry struct {
	URL    string   `json:"url,omitempty"`
	Status int      `json:"status,omitempty"`
	Title  string   `json:"title,omitempty"`
	Tech   []string `json:"tech,omitempty"`
	TLS    *HTTPTLS `json:"tls,omitempty"`
	Raw    string   `json:"raw,omitempty"`
}

// HTTPTLS is the compact certificate view returned by -tls-grab.
type HTTPTLS struct {
	Subject string `json:"subject,omitempty"`
	Issuer  string `json:"issuer,omitempty"`
	Expiry  string `json:"expiry,omitempty"`
}

// HTTPResult is the bounded outcome of one httpx run. Truncated and Dropped count
// entries the runner cap omitted; OutOfScope counts returned URLs the allowlist
// dropped, which is how a redirect off-scope is refused after the fact.
type HTTPResult struct {
	Entries    []HTTPEntry `json:"entries"`
	Count      int         `json:"count"`
	Truncated  int         `json:"truncated"`
	Dropped    int         `json:"dropped"`
	OutOfScope int         `json:"out_of_scope"`
	OutputFile string      `json:"output_file,omitempty"`
}

// RunHTTP probes the targets with the pinned httpx binary and returns one compact
// entry per answered URL. It enforces the target and port caps, derives the scope
// from the targets when none is given, and drops any returned URL outside it.
func RunHTTP(ctx context.Context, opts HTTPOptions) (HTTPResult, error) {
	targets := cleanHTTPTargets(opts.Targets)
	if len(targets) == 0 {
		return HTTPResult{}, fmt.Errorf(
			"%s: invalid arguments: targets is required — pass at least one host or URL",
			HTTPID,
		)
	}
	if len(targets) > MaxHosts {
		return HTTPResult{}, capError(len(targets), "targets", MaxHosts)
	}

	scope := opts.Scope
	if len(scope) == 0 {
		scope = httpScopeTargets(targets)
	}
	guard := Guard{Scope: scope}
	if err := guard.CheckPorts(opts.Ports); err != nil {
		return HTTPResult{}, err
	}
	if err := guard.CheckTargets(targets); err != nil {
		return HTTPResult{}, err
	}

	bin, err := Ensure(ctx, httpxSpec)
	if err != nil {
		return HTTPResult{}, err
	}
	if err := verifyHTTPSpec(ctx, bin); err != nil {
		return HTTPResult{}, err
	}

	result, err := Run(ctx, httpxSpec, httpxArgs(targets, opts), Options{
		MaxEntries: opts.MaxEntries,
		TimeoutMS:  opts.TimeoutMS,
		OutputFile: strings.TrimSpace(opts.OutputFile),
		ExtraArgs:  opts.ExtraArgs,
	})
	if err != nil {
		return HTTPResult{}, err
	}
	return buildHTTPResult(result, scope, opts), nil
}

// verifyHTTPSpec runs the pinned binary's -version banner and requires the
// projectdiscovery identity. It refuses the Python httpx that shadows the name on
// PATH, naming both the binary path and its first output line.
func verifyHTTPSpec(ctx context.Context, path string) error {
	probeCtx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()

	output, runErr := exec.CommandContext(probeCtx, path, "-version").CombinedOutput()
	text := string(output)
	if strings.Contains(strings.ToLower(text), httpIdentity) {
		return nil
	}
	if runErr != nil && strings.TrimSpace(text) == "" {
		return fmt.Errorf("recon: httpx identity probe for %q failed: %w", path, runErr)
	}
	return fmt.Errorf(
		"recon: identity probe refused %q: it printed %q, which never names projectdiscovery — "+
			"httpx on PATH is a Python script, not %s; remove the non-projectdiscovery binary at this path and retry",
		path,
		firstLine(text),
		httpxSpec.Module,
	)
}

// buildHTTPResult compacts decoded JSONL, drops out-of-scope URLs and keeps the
// runner's cap counts honest.
func buildHTTPResult(
	result Result,
	scope []string,
	opts HTTPOptions,
) HTTPResult {
	guard := Guard{Scope: scope}
	out := HTTPResult{
		Entries:    make([]HTTPEntry, 0, len(result.Entries)),
		Truncated:  result.Truncated,
		Dropped:    result.Dropped,
		OutputFile: result.OutputFile,
	}
	for _, raw := range result.Entries {
		entry, ok := compactHTTPEntry(raw, opts)
		if !ok {
			continue
		}
		if entry.URL != "" && guard.CheckHosts([]string{entry.URL}) != nil {
			out.OutOfScope++
			continue
		}
		out.Entries = append(out.Entries, entry)
	}
	out.Count = len(out.Entries)
	return out
}

// compactHTTPEntry keeps only the fields the caller requested from one decoded
// httpx object. A raw line survives so a decode failure is not silent.
func compactHTTPEntry(raw map[string]any, opts HTTPOptions) (HTTPEntry, bool) {
	if line, ok := raw["raw"].(string); ok {
		return HTTPEntry{Raw: line}, true
	}
	url := httpStringField(raw, "url")
	if url == "" {
		return HTTPEntry{}, false
	}
	entry := HTTPEntry{URL: url}
	if opts.StatusCode {
		entry.Status = httpIntField(raw, "status_code")
	}
	if opts.Title {
		entry.Title = clipText(httpStripANSI(httpStringField(raw, "title")), httpTitleClip)
	}
	if opts.Tech {
		entry.Tech = httpStringSlice(raw["tech"])
	}
	if opts.TLS {
		entry.TLS = httpTLS(raw["tls"])
	}
	return entry, true
}

// httpxArgs builds the scanner argv. Every target goes through -u because httpx
// v1.12.0 accepts no bare positional targets: it exits 0 with empty stdout and
// empty stderr, which reads as a zero-entry scan. The JSON form stays
// "-json -silent -nc"; -j and -json are aliases, and -nc is this version's
// -no-color switch.
func httpxArgs(targets []string, opts HTTPOptions) []string {
	args := make([]string, 0, 2*len(targets)+16)
	if ports := cleanHTTPPorts(opts.Ports); len(ports) > 0 {
		args = append(args, "-ports", strings.Join(ports, ","))
	}
	if opts.StatusCode {
		args = append(args, "-status-code")
	}
	if opts.Title {
		args = append(args, "-title")
	}
	if opts.Tech {
		args = append(args, "-td")
	}
	if opts.TLS {
		args = append(args, "-tls-grab")
	}
	if opts.FollowRedirects {
		args = append(args, "-fr")
	}
	if opts.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(opts.Threads))
	}
	if opts.RateLimit > 0 {
		args = append(args, "-rl", strconv.Itoa(opts.RateLimit))
	}
	for _, target := range targets {
		args = append(args, "-u", target)
	}
	return args
}

// cleanHTTPTargets trims and drops empty targets.
func cleanHTTPTargets(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if target := strings.TrimSpace(entry); target != "" {
			out = append(out, target)
		}
	}
	return out
}

// cleanHTTPPorts trims and deduplicates a port list.
func cleanHTTPPorts(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		port := strings.TrimSpace(entry)
		if port == "" {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		out = append(out, port)
	}
	return out
}

// httpScopeTargets derives the implicit allowlist from the literal targets.
func httpScopeTargets(targets []string) []string {
	seen := make(map[string]struct{}, len(targets))
	out := make([]string, 0, len(targets))
	for _, raw := range targets {
		host := targetHost(raw)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	return out
}

// httpStringField reads a trimmed string field from a decoded httpx object.
func httpStringField(raw map[string]any, key string) string {
	value, _ := raw[key].(string)
	return strings.TrimSpace(value)
}

// httpIntField reads an integer field from a decoded httpx object.
func httpIntField(raw map[string]any, key string) int {
	switch value := raw[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

// httpStringSlice reads a string array, strips colour and drops empty entries.
func httpStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		if text = strings.TrimSpace(httpStripANSI(text)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// httpTLS compacts the -tls-grab object to subject, issuer and expiry.
func httpTLS(value any) *HTTPTLS {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	cert := HTTPTLS{
		Subject: httpFirstNonEmpty(httpStringField(raw, "subject_dn"), httpStringField(raw, "subject_cn")),
		Issuer:  httpFirstNonEmpty(httpStringField(raw, "issuer_dn"), httpStringField(raw, "issuer_cn")),
		Expiry:  httpStringField(raw, "not_after"),
	}
	if cert.Subject == "" && cert.Issuer == "" && cert.Expiry == "" {
		return nil
	}
	return &cert
}

// httpStripANSI removes terminal colour sequences from a decoded string.
func httpStripANSI(s string) string {
	return httpANSI.ReplaceAllString(s, "")
}

// httpFirstNonEmpty returns the first non-empty value.
func httpFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
