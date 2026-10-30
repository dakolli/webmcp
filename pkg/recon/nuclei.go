package recon

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Nuclei scan defaults and bounds.
const (
	// NucleiMaxTargets is the most targets one template scan may hold.
	NucleiMaxTargets = 200
	// NucleiDefaultRateLimit is the per-second request budget when none is set.
	NucleiDefaultRateLimit = 20
	// NucleiMaxRateLimit is the ceiling a caller's rate limit is clamped to.
	NucleiMaxRateLimit = 100
	// NucleiDefaultConcurrency is the template concurrency when none is set.
	NucleiDefaultConcurrency = 10
	// nucleiSeverityDefault is the severity set nuclei scans when none is named.
	nucleiSeverityDefault = "critical,high,medium"
	// nucleiFindingClip bounds one description or extracted value.
	nucleiFindingClip = 120
	// NucleiRedacted is the marker written over a credential-shaped value.
	NucleiRedacted = "<redacted>"
)

// NucleiSpec describes the pinned nuclei v3 scanner. Pin v3.11.1 is the newest
// stable release resolved on 2026-10-30 with
// `go list -m -versions github.com/projectdiscovery/nuclei/v3`.
var NucleiSpec = Spec{
	ID:          "recon_templates",
	Module:      "github.com/projectdiscovery/nuclei/v3/cmd/nuclei",
	Pin:         "v3.11.1",
	BinName:     "nuclei",
	VersionArgs: []string{"-version"},
	JSONArgs:    []string{"-jsonl", "-silent", "-nc"},
	Doc:         NucleiDoc,
}

func init() { Register(NucleiSpec) }

// NucleiDoc is the single LLM-facing description of recon_templates. Both the
// shared recon surface and the typed tool read it, so the contract cannot drift.
const NucleiDoc = `Scans targets with pinned nuclei v3 templates and returns one compact finding per match.

Arguments (typed surface):
| arg | type | meaning |
| --- | --- | --- |
| targets | []string, required | URLs or hosts to scan, max 200. |
| severity | []string | nuclei severities; default critical,high,medium; -severity. |
| tags | []string | template tags to include; -tags. |
| templates | []string | absolute, existing template directory or file; -t; a relative or nonexistent path is refused. |
| exclude_tags | []string | template tags to exclude; -etags. |
| rate_limit | int | per-second request budget; default 20, ceiling 100; -rl. |
| concurrency | int | template concurrency; default 10; -c. |
| headless | bool | run headless templates; default false; -headless. |
| max_entries | int | decoded findings returned; default 200. |
| output_file | string | absolute path for the raw JSONL report, mode 0600. |
| extra_args | []string | nuclei flags appended verbatim. |
| scope | []string | host allowlist; defaults to the literal target hosts. |
| authorized | bool, required | consent; a call without it fails. |

On the shared recon surface only targets, extra_args, scope, authorized, output_file, max_entries and timeout_ms are top-level keys; pass the rest as nuclei flags in extra_args, for example {"targets":["https://scanme.sh"],"extra_args":["-severity","critical,high"],"authorized":true}.

Example: {"targets":["https://scanme.sh"],"severity":["critical","high"],"authorized":true}.

Errors: invalid_args for missing consent, a target outside scope or over the 200-target cap, a relative template path, or a missing template repository; timeout when the run budget expires; unknown when nuclei exits with its own error text.

Consent: authorized:true is required and is never defaulted. A template that exploits runs only when you name it in templates. Out-of-band callbacks (interactsh) stay off unless you pass their flags in extra_args, so no callback leaves the host by default.

Template download: the first scan needs the nuclei template repository. When nuclei reports no templates found, pass templates with an absolute directory or file, or run "nuclei -update-templates" once to download the built-in set. This tool never downloads templates itself.

`

// NucleiRequest is one template scan's typed input. It carries no MCP dependency
// so the engine stays reusable by the CLI and the tool layer alike.
type NucleiRequest struct {
	Targets     []string
	Severity    []string
	Tags        []string
	Templates   []string
	ExcludeTags []string
	RateLimit   int
	Concurrency int
	Headless    bool
}

// NucleiFinding is one compact nuclei match. Extracted values are clipped and
// credential-shaped secrets are replaced before they reach the caller.
type NucleiFinding struct {
	Template    string   `json:"template"`
	Name        string   `json:"name,omitempty"`
	Severity    string   `json:"severity,omitempty"`
	URL         string   `json:"url,omitempty"`
	Matcher     string   `json:"matcher,omitempty"`
	Type        string   `json:"type,omitempty"`
	Host        string   `json:"host,omitempty"`
	IP          string   `json:"ip,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	Extracted   []string `json:"extracted,omitempty"`
}

// NucleiResult is the bounded, compact outcome of one template scan. The cap's
// truncated and dropped counts are carried through from the raw run.
type NucleiResult struct {
	Entries    []NucleiFinding `json:"entries"`
	Count      int             `json:"count"`
	Truncated  int             `json:"truncated"`
	Dropped    int             `json:"dropped"`
	OutputFile string          `json:"output_file,omitempty"`
}

// Validate refuses an empty or over-cap target list, a relative or missing
// template path, and a negative rate limit or concurrency.
func (r NucleiRequest) Validate() error {
	if len(r.Targets) == 0 {
		return fmt.Errorf(
			"recon_templates: invalid arguments: targets is required — pass at least one URL or host",
		)
	}
	if len(r.Targets) > NucleiMaxTargets {
		return fmt.Errorf(
			"recon_templates: invalid arguments: %d targets exceeds the %d-target cap — split the call or pass output_file to write the full report to an absolute path",
			len(r.Targets),
			NucleiMaxTargets,
		)
	}
	for _, template := range r.Templates {
		path := strings.TrimSpace(template)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf(
				"recon_templates: invalid arguments: templates entry %q must be an absolute template directory or file",
				path,
			)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf(
				"recon_templates: invalid arguments: templates entry %q does not exist or is not readable: %w — pass an existing absolute template directory or file, or run \"nuclei -update-templates\" once",
				path,
				err,
			)
		}
	}
	if r.RateLimit < 0 {
		return fmt.Errorf(
			"recon_templates: invalid arguments: rate_limit must be >= 0, got %d",
			r.RateLimit,
		)
	}
	if r.Concurrency < 0 {
		return fmt.Errorf(
			"recon_templates: invalid arguments: concurrency must be >= 0, got %d",
			r.Concurrency,
		)
	}
	return nil
}

// Argv renders the nuclei argv for the request: typed flags first, then the
// targets. Passthrough flags belong to Options.ExtraArgs, which Run appends.
func (r NucleiRequest) Argv() []string {
	argv := make([]string, 0, 16+len(r.Targets))
	argv = append(argv, "-severity", r.severity())
	argv = append(argv, "-rl", strconv.Itoa(r.rateLimit()))
	argv = append(argv, "-c", strconv.Itoa(r.concurrency()))
	argv = nucleiAppendList(argv, "-tags", r.Tags)
	argv = nucleiAppendList(argv, "-etags", r.ExcludeTags)
	argv = nucleiAppendList(argv, "-t", r.Templates)
	if r.Headless {
		argv = append(argv, "-headless")
	}
	for _, target := range r.Targets {
		if target = strings.TrimSpace(target); target != "" {
			argv = append(argv, target)
		}
	}
	return argv
}

// severity renders the severity list, falling back to nuclei's default set.
func (r NucleiRequest) severity() string {
	if len(r.Severity) == 0 {
		return nucleiSeverityDefault
	}
	return strings.Join(r.Severity, ",")
}

// rateLimit resolves the request budget, applying the default and the ceiling.
func (r NucleiRequest) rateLimit() int {
	switch {
	case r.RateLimit <= 0:
		return NucleiDefaultRateLimit
	case r.RateLimit > NucleiMaxRateLimit:
		return NucleiMaxRateLimit
	default:
		return r.RateLimit
	}
}

// concurrency resolves the template concurrency, applying the default.
func (r NucleiRequest) concurrency() int {
	if r.Concurrency <= 0 {
		return NucleiDefaultConcurrency
	}
	return r.Concurrency
}

// CompactNuclei renders a bounded run as compact findings, preserving the entry
// cap's truncated and dropped counts.
func CompactNuclei(result Result) NucleiResult {
	findings := make([]NucleiFinding, 0, len(result.Entries))
	for _, entry := range result.Entries {
		findings = append(findings, nucleiFinding(entry))
	}
	return NucleiResult{
		Entries:    findings,
		Count:      len(findings),
		Truncated:  result.Truncated,
		Dropped:    result.Dropped,
		OutputFile: result.OutputFile,
	}
}

// TemplatesMissing reports whether err is nuclei's missing-template-repo
// failure, so the tool layer can name the download step instead of the raw
// scanner error.
func TemplatesMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range nucleiTemplateMissingMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// HostsOfTargets reduces targets to their unique lowercase hosts. The templates
// tool uses it as the scope when the caller names none.
func HostsOfTargets(targets []string) []string {
	seen := make(map[string]struct{}, len(targets))
	hosts := make([]string, 0, len(targets))
	for _, target := range targets {
		host := targetHost(target)
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

// nucleiTemplateMissingMarkers are the stderr shapes nuclei prints when it has
// no template repository to scan with.
var nucleiTemplateMissingMarkers = []string{
	"no templates found",
	"no templates provided",
	"no templates were found",
	"no templates to run",
	"no template provided",
	"no templates loaded",
	"could not find any templates",
	"templates not found",
}

// Credential shapes redacted inside an extracted nuclei value. The rule mirrors
// the browser lane's small set: assignment to a sensitive key and the four
// common token shapes.
var (
	nucleiBearer     = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{4,}`)
	nucleiAssignment = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd|session|csrf)\s*[:=]\s*[^\s"'&,;]{4,}`)
	nucleiAWSKey     = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	nucleiJWT        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
)

// nucleiFinding compacts one decoded nuclei JSONL object.
func nucleiFinding(entry map[string]any) NucleiFinding {
	info, _ := entry["info"].(map[string]any)
	return NucleiFinding{
		Template:    nucleiText(entry, "template-id"),
		Name:        nucleiText(info, "name"),
		Severity:    nucleiText(info, "severity"),
		URL:         nucleiText(entry, "matched-at"),
		Matcher:     nucleiText(entry, "matcher-name"),
		Type:        nucleiText(entry, "type"),
		Host:        nucleiText(entry, "host"),
		IP:          nucleiText(entry, "ip"),
		Tags:        nucleiList(info, "tags"),
		Description: clipText(nucleiText(info, "description"), nucleiFindingClip),
		Extracted:   nucleiExtracted(entry["extracted-results"]),
	}
}

// nucleiText reads a trimmed string field, tolerating a nil map.
func nucleiText(entry map[string]any, key string) string {
	value, _ := entry[key].(string)
	return strings.TrimSpace(value)
}

// nucleiList reads a string list field from decoded JSON.
func nucleiList(entry map[string]any, key string) []string {
	raw, _ := entry[key].([]any)
	if len(raw) == 0 {
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// nucleiExtracted clips and redacts the extracted-results values.
func nucleiExtracted(raw any) []string {
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			continue
		}
		values = append(values, nucleiRedact(value))
	}
	return values
}

// nucleiRedact clips one extracted value and replaces a credential-shaped
// secret inside it with the redaction marker.
func nucleiRedact(value string) string {
	value = clipText(strings.TrimSpace(value), nucleiFindingClip)
	value = nucleiBearer.ReplaceAllString(value, "$1 "+NucleiRedacted)
	value = nucleiAssignment.ReplaceAllString(value, "$1="+NucleiRedacted)
	value = nucleiAWSKey.ReplaceAllString(value, NucleiRedacted)
	value = nucleiJWT.ReplaceAllString(value, NucleiRedacted)
	return value
}

// nucleiAppendList appends flag once per non-empty value.
func nucleiAppendList(argv []string, flag string, values []string) []string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			argv = append(argv, flag, value)
		}
	}
	return argv
}
