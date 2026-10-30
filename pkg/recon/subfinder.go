package recon

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
)

// SubdomainsID is the MCP tool name and registry key for passive enumeration.
const SubdomainsID = "recon_subdomains"

const (
	// subfinderPin is the newest stable subfinder release, resolved with
	// `go list -m -versions github.com/projectdiscovery/subfinder/v2` on 2026-10-30.
	subfinderPin = "v2.17.0"
	// defaultSubdomainEntries bounds the hosts one recon_subdomains call returns.
	defaultSubdomainEntries = 500
	// maxSubdomainTimeoutMS is the run-budget ceiling for recon_subdomains.
	maxSubdomainTimeoutMS = 600_000
)

// subfinderDoc is the short scanner description carried by the registered Spec.
const subfinderDoc = "recon_subdomains: passive subdomain enumeration with subfinder. " +
	"It queries public passive sources, so the target never sees a request, and it needs one " +
	"registrable domain, e.g. example.com. Consent: set authorized:true only for domains you " +
	"are permitted to enumerate."

// subfinderSpec pins subfinder and registers recon_subdomains from init. Its
// JSONArgs make subfinder emit one JSON object per host-source pair.
var subfinderSpec = Spec{
	ID:          SubdomainsID,
	Module:      "github.com/projectdiscovery/subfinder/v2/cmd/subfinder",
	Pin:         subfinderPin,
	BinName:     "subfinder",
	VersionArgs: []string{"-version"},
	JSONArgs:    []string{"-json", "-silent", "-nc"},
	Doc:         subfinderDoc,
}

func init() {
	Register(subfinderSpec)
}

// SubdomainsArgs is one typed recon_subdomains request.
type SubdomainsArgs struct {
	Target     string
	AllSources bool
	Recursive  bool
	Sources    []string
	TimeoutMS  int
	MaxEntries int
	OutputFile string
	ExtraArgs  []string
	Scope      []string
	Authorized bool
}

// Subdomain is one enumerated host and the passive sources that reported it.
type Subdomain struct {
	Host    string   `json:"host"`
	Sources []string `json:"sources"`
}

// SubdomainsResult is the bounded outcome of one subfinder run. Truncated and
// Dropped count hosts the entry cap omitted; OutOfScope counts hosts the
// allowlist dropped.
type SubdomainsResult struct {
	Entries    []Subdomain `json:"entries"`
	Count      int         `json:"count"`
	Truncated  int         `json:"truncated"`
	Dropped    int         `json:"dropped"`
	OutOfScope int         `json:"out_of_scope"`
	OutputFile string      `json:"output_file,omitempty"`
}

// RunSubdomains validates consent and scope, runs subfinder passively, and
// returns one compact entry per host with the sources that reported it.
func RunSubdomains(
	ctx context.Context,
	a SubdomainsArgs,
) (SubdomainsResult, error) {
	if !a.Authorized {
		return SubdomainsResult{}, fmt.Errorf(
			"%s: invalid arguments: authorized:true is required — passive enumeration still queries public sources for a domain you must be permitted to assess, so set it explicitly",
			SubdomainsID,
		)
	}
	domain, err := subdomainTarget(a.Target)
	if err != nil {
		return SubdomainsResult{}, err
	}
	if a.TimeoutMS > maxSubdomainTimeoutMS {
		return SubdomainsResult{}, fmt.Errorf(
			"%s: invalid arguments: timeout_ms %d exceeds the %d ms ceiling — lower it or pass output_file for a longer run",
			SubdomainsID,
			a.TimeoutMS,
			maxSubdomainTimeoutMS,
		)
	}
	outputFile := strings.TrimSpace(a.OutputFile)
	if outputFile != "" && !filepath.IsAbs(outputFile) {
		return SubdomainsResult{}, fmt.Errorf(
			"%s: invalid arguments: output_file %q must be absolute — pass an absolute path",
			SubdomainsID,
			outputFile,
		)
	}

	scope := a.Scope
	if len(scope) == 0 {
		scope = []string{domain}
	}
	guard := Guard{Scope: scope}
	if err := guard.CheckHosts([]string{domain}); err != nil {
		return SubdomainsResult{}, err
	}

	result, err := Run(ctx, subfinderSpec, subfinderArgs(domain, a), Options{
		TimeoutMS:  a.TimeoutMS,
		OutputFile: outputFile,
		ExtraArgs:  a.ExtraArgs,
	})
	if err != nil {
		return SubdomainsResult{}, err
	}
	return buildSubdomains(result.Raw, scope, a.MaxEntries, result.OutputFile), nil
}

// subdomainTarget normalises and validates one registrable domain.
func subdomainTarget(raw string) (string, error) {
	target := strings.ToLower(strings.TrimSpace(raw))
	target = strings.TrimSuffix(target, ".")
	if target == "" {
		return "", fmt.Errorf(
			"%s: invalid arguments: target is required — pass one registrable domain, e.g. example.com",
			SubdomainsID,
		)
	}
	if strings.ContainsAny(target, "/ \t\r\n") || strings.Contains(target, ":") || strings.Contains(target, "*") {
		return "", fmt.Errorf(
			"%s: invalid arguments: target %q must be a bare registrable domain, not a URL, host:port or wildcard — pass example.com",
			SubdomainsID,
			raw,
		)
	}
	if net.ParseIP(target) != nil {
		return "", fmt.Errorf(
			"%s: invalid arguments: target %q is an IP address — subfinder enumerates domains, so pass a registrable domain",
			SubdomainsID,
			raw,
		)
	}
	if !strings.Contains(target, ".") {
		return "", fmt.Errorf(
			"%s: invalid arguments: target %q has no dot — pass a registrable domain, e.g. example.com",
			SubdomainsID,
			raw,
		)
	}
	return target, nil
}

// subfinderArgs builds the scanner argv for one validated domain.
func subfinderArgs(domain string, a SubdomainsArgs) []string {
	args := []string{"-d", domain}
	if a.AllSources {
		args = append(args, "-all")
	}
	if a.Recursive {
		args = append(args, "-recursive")
	}
	if sources := cleanSources(a.Sources); len(sources) > 0 {
		args = append(args, "-sources", strings.Join(sources, ","))
	}
	return args
}

// cleanSources lowercases, trims and deduplicates a source list.
func cleanSources(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		source := strings.ToLower(strings.TrimSpace(entry))
		if source == "" {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, source)
	}
	return out
}

// buildSubdomains groups decoded JSONL by host, drops out-of-scope hosts and
// applies the host cap with honest Truncated and Dropped counts.
func buildSubdomains(
	raw []byte,
	scope []string,
	max int,
	outputFile string,
) SubdomainsResult {
	lineCount := bytes.Count(raw, []byte("\n")) + 1
	entries, _, _ := decodeEntries(raw, lineCount)

	order := make([]string, 0, len(entries))
	sources := make(map[string]map[string]struct{}, len(entries))
	outOfScope := make(map[string]struct{})

	for _, entry := range entries {
		host := entryHost(entry)
		if host == "" {
			continue
		}
		if !scopeAllowsHost(host, scope) {
			outOfScope[host] = struct{}{}
			continue
		}
		set, ok := sources[host]
		if !ok {
			set = make(map[string]struct{})
			sources[host] = set
			order = append(order, host)
		}
		for _, source := range entrySources(entry) {
			if source != "" {
				set[source] = struct{}{}
			}
		}
	}
	sort.Strings(order)

	limit := max
	if limit <= 0 {
		limit = defaultSubdomainEntries
	}
	if limit > len(order) {
		limit = len(order)
	}
	result := SubdomainsResult{
		Entries:    make([]Subdomain, 0, limit),
		Count:      limit,
		OutOfScope: len(outOfScope),
		OutputFile: outputFile,
	}
	for _, host := range order[:limit] {
		result.Entries = append(result.Entries, Subdomain{
			Host:    host,
			Sources: sortedKeys(sources[host]),
		})
	}
	if omitted := len(order) - limit; omitted > 0 {
		result.Truncated = 1
		result.Dropped = omitted
	}
	return result
}

// entryHost reads the host field of one decoded subfinder object.
func entryHost(entry map[string]any) string {
	host, _ := entry["host"].(string)
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// entrySources reads both JSON shapes subfinder emits: a singular source per
// line by default, and a sources array with -collect-sources.
func entrySources(entry map[string]any) []string {
	out := make([]string, 0, 2)
	if list, ok := entry["sources"].([]any); ok {
		for _, value := range list {
			if source, ok := value.(string); ok {
				out = append(out, strings.ToLower(strings.TrimSpace(source)))
			}
		}
	}
	if source, ok := entry["source"].(string); ok {
		out = append(out, strings.ToLower(strings.TrimSpace(source)))
	}
	return out
}

// sortedKeys returns a set's members in ascending order, never nil.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
