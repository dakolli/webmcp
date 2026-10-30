package recon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// dnsxPin is the newest stable dnsx release resolved on 2026-10-30.
const dnsxPin = "v1.3.1"

// dnsxSpec is the registered descriptor for the recon_dns tool.
var dnsxSpec = Spec{
	ID:          "recon_dns",
	Module:      "github.com/projectdiscovery/dnsx/cmd/dnsx",
	Pin:         dnsxPin,
	BinName:     "dnsx",
	VersionArgs: []string{"-version"},
	JSONArgs:    []string{"-json", "-silent", "-nc"},
	Doc:         dnsxDoc,
}

// dnsxRecordFlags maps each accepted record type to its dnsx query flag.
var dnsxRecordFlags = map[string]string{
	"a":     "-a",
	"aaaa":  "-aaaa",
	"cname": "-cname",
	"mx":    "-mx",
	"ns":    "-ns",
	"txt":   "-txt",
	"srv":   "-srv",
	"ptr":   "-ptr",
	"caa":   "-caa",
}

// dnsxRecordOrder is the canonical order of record types in flags and output.
var dnsxRecordOrder = []string{"a", "aaaa", "cname", "mx", "ns", "txt", "srv", "ptr", "caa"}

// dnsxDoc is the scanner-specific description shown on the generic recon tool.
const dnsxDoc = "resolves DNS records with the pinned dnsx binary. " +
	"Two modes: pass targets to resolve a list of hostnames (up to 500), or pass a domain with an " +
	"absolute-path wordlist to brute-force names under that domain. " +
	"Record types default to a and cname. On this generic surface reach extra query flags and " +
	"brute-force inputs through extra_args, e.g. [\"-a\",\"-aaaa\",\"-mx\"] for more record types, " +
	"or [\"-d\",\"example.com\",\"-w\",\"/abs/words.txt\"] to brute-force. " +
	"Output is one compact entry per host carrying only the record values that came back non-empty. " +
	"A null MX is marked with mx_null:true instead of a blank mx value."

func init() {
	Register(dnsxSpec)
}

// DNSOptions shapes one dnsx invocation. Targets resolves a list; Domain with
// Wordlist brute-forces one domain, and the two modes cannot be combined.
type DNSOptions struct {
	// Targets are the hostnames to resolve in resolve mode, capped at MaxHosts.
	Targets []string
	// Domain is the parent domain to expand in brute-force mode.
	Domain string
	// Wordlist is the absolute path to a wordlist; its presence selects
	// brute-force mode.
	Wordlist string
	// RecordTypes are the record types to query; empty defaults to a and cname.
	RecordTypes []string
	// Resolvers are resolver addresses joined into dnsx -r.
	Resolvers []string
	// Threads is the concurrent lookup count; zero leaves dnsx's default 100.
	Threads int
	// MaxEntries is the entry cap; zero uses the runner default.
	MaxEntries int
	// TimeoutMS is the run budget in milliseconds; zero uses the runner default.
	TimeoutMS int
	// OutputFile is an absolute path for the raw JSONL report.
	OutputFile string
	// ExtraArgs are appended verbatim after the typed dnsx arguments.
	ExtraArgs []string
}

// DNSRecordTypes normalizes a requested record-type set: it lowercases and
// trims, rejects unknown names, de-duplicates, restores canonical order, and
// defaults to a and cname when the set is empty.
func DNSRecordTypes(names []string) ([]string, error) {
	want := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := dnsxRecordFlags[name]; !ok {
			return nil, fmt.Errorf(
				"recon_dns: invalid arguments: unknown record_type %q — use a, aaaa, cname, mx, ns, txt, srv, ptr or caa",
				name,
			)
		}
		want[name] = struct{}{}
	}
	if len(want) == 0 {
		return []string{"a", "cname"}, nil
	}
	ordered := make([]string, 0, len(want))
	for _, name := range dnsxRecordOrder {
		if _, ok := want[name]; ok {
			ordered = append(ordered, name)
		}
	}
	return ordered, nil
}

// RunDNS validates one dnsx invocation and runs it, then compacts the entries to
// the non-empty record fields per host.
func RunDNS(ctx context.Context, opts DNSOptions) (Result, error) {
	recordTypes, err := DNSRecordTypes(opts.RecordTypes)
	if err != nil {
		return Result{}, err
	}
	args, cleanup, err := buildDNSArgs(opts, recordTypes)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	result, err := Run(ctx, dnsxSpec, args, Options{
		MaxEntries: opts.MaxEntries,
		TimeoutMS:  opts.TimeoutMS,
		OutputFile: strings.TrimSpace(opts.OutputFile),
		ExtraArgs:  opts.ExtraArgs,
	})
	if err != nil {
		return Result{}, err
	}
	result.Entries = compactDNS(result.Entries, recordTypes)
	result.Count = len(result.Entries)
	return result, nil
}

// buildDNSArgs turns DNSOptions into dnsx argv and a cleanup for the temporary
// resolve list. It refuses an over-cap target list and a bad wordlist.
func buildDNSArgs(opts DNSOptions, recordTypes []string) ([]string, func(), error) {
	wordlist := strings.TrimSpace(opts.Wordlist)
	domain := strings.TrimSpace(opts.Domain)
	targets := cleanTargets(opts.Targets)

	args := make([]string, 0, len(recordTypes)+8)
	cleanup := func() {}

	switch {
	case wordlist != "":
		if domain == "" {
			return nil, nil, fmt.Errorf(
				"recon_dns: invalid arguments: wordlist selects brute-force mode and requires domain — pass the parent domain to expand",
			)
		}
		if len(targets) > 0 {
			return nil, nil, fmt.Errorf(
				"recon_dns: invalid arguments: wordlist brute-force cannot be combined with targets — pass domain with wordlist, or targets alone to resolve",
			)
		}
		path, err := dnsWordlistPath(wordlist)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "-d", domain, "-w", path)
	default:
		if domain != "" {
			return nil, nil, fmt.Errorf(
				"recon_dns: invalid arguments: domain requires wordlist — pass an absolute wordlist path to brute-force, or put the host in targets to resolve it",
			)
		}
		hosts := dnsHosts(opts.Targets)
		if len(hosts) == 0 {
			return nil, nil, fmt.Errorf(
				"recon_dns: invalid arguments: targets is required in resolve mode — pass hostnames, or pass domain with wordlist to brute-force",
			)
		}
		if len(hosts) > MaxHosts {
			return nil, nil, capError(len(hosts), "hosts", MaxHosts)
		}
		path, remove, err := writeTargetList(hosts)
		if err != nil {
			return nil, nil, err
		}
		cleanup = remove
		args = append(args, "-l", path)
	}

	for _, recordType := range recordTypes {
		args = append(args, dnsxRecordFlags[recordType])
	}
	if resolvers := cleanTargets(opts.Resolvers); len(resolvers) > 0 {
		args = append(args, "-r", strings.Join(resolvers, ","))
	}
	if opts.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(opts.Threads))
	}
	return args, cleanup, nil
}

// dnsWordlistPath requires an absolute, readable wordlist file.
func dnsWordlistPath(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf(
			"recon_dns: invalid arguments: wordlist %q must be an absolute path — pass the absolute path to the wordlist file",
			raw,
		)
	}
	info, err := os.Stat(raw)
	if err != nil {
		return "", fmt.Errorf(
			"recon_dns: invalid arguments: wordlist %q is not readable: %w — check the absolute path exists",
			raw,
			err,
		)
	}
	if info.IsDir() {
		return "", fmt.Errorf(
			"recon_dns: invalid arguments: wordlist %q is a directory — pass the wordlist file path",
			raw,
		)
	}
	return raw, nil
}

// writeTargetList writes the resolve targets to a temporary file for dnsx -l and
// returns its path plus a cleanup that removes it.
func writeTargetList(targets []string) (string, func(), error) {
	file, err := os.CreateTemp("", "recon-dns-targets-*.txt")
	if err != nil {
		return "", nil, fmt.Errorf("recon_dns: create target list: %w", err)
	}
	path := file.Name()
	remove := func() { _ = os.Remove(path) }

	for _, target := range targets {
		if _, err := fmt.Fprintln(file, target); err != nil {
			file.Close()
			remove()
			return "", nil, fmt.Errorf("recon_dns: write target list %s: %w", path, err)
		}
	}
	if err := file.Close(); err != nil {
		remove()
		return "", nil, fmt.Errorf("recon_dns: close target list %s: %w", path, err)
	}
	return path, remove, nil
}

// dnsHosts reduces each target to a bare host name, so a URL or host:port
// reaches dnsx as a host while a CIDR range passes through unchanged.
func dnsHosts(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range cleanTargets(values) {
		if host := targetHost(value); host != "" {
			out = append(out, host)
		}
	}
	return out
}

// cleanTargets trims each value and drops empty entries.
func cleanTargets(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// compactDNS keeps host, status_code, resolver and the requested record types,
// dropping empty values. It drops dnsx's raw DNS dump but preserves a raw line
// that the JSONL decoder preserved because it was not JSON.
func compactDNS(entries []map[string]any, recordTypes []string) []map[string]any {
	keep := make(map[string]struct{}, len(recordTypes)+3)
	keep["host"] = struct{}{}
	keep["status_code"] = struct{}{}
	keep["resolver"] = struct{}{}
	for _, recordType := range recordTypes {
		keep[recordType] = struct{}{}
	}

	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		_, hasHost := entry["host"]
		compact := make(map[string]any, len(keep))
		for key, value := range entry {
			if key == "raw" {
				if hasHost {
					continue
				}
			} else if _, ok := keep[key]; !ok {
				continue
			} else if key == "mx" {
				targets, null := dnsMXTargets(value)
				if null {
					compact["mx_null"] = true
				} else if len(targets) > 0 {
					compact[key] = targets
				}
				continue
			}
			if dnsValueEmpty(value) {
				continue
			}
			compact[key] = value
		}
		out = append(out, compact)
	}
	return out
}

// dnsMXTargets returns the non-empty MX targets in value and whether the record
// is a null MX, which dnsx encodes as an array of empty strings.
func dnsMXTargets(value any) (targets []any, null bool) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	targets = make([]any, 0, len(items))
	for _, item := range items {
		if text, isText := item.(string); isText && strings.TrimSpace(text) == "" {
			continue
		}
		targets = append(targets, item)
	}
	return targets, len(targets) == 0
}

// dnsValueEmpty reports whether a decoded JSON value carries no record data.
func dnsValueEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	default:
		return false
	}
}
