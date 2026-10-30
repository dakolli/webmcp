package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	// URLsID is the registry key and MCP tool name for the katana URL crawler.
	URLsID = "recon_urls"
	// urlsPin is the newest stable katana release, resolved with
	// `go list -m -versions github.com/projectdiscovery/katana` on 2026-10-30.
	urlsPin = "v1.8.0"
	// urlsMaxSeeds is the most seed URLs one crawl may start from.
	urlsMaxSeeds = 20
	// urlsMaxDepth is the deepest crawl level a caller may request.
	urlsMaxDepth = 10
	// urlsDefaultDepth is the crawl depth when none is set.
	urlsDefaultDepth = 3
	// urlsDefaultConcurrency is the fetcher count when none is set.
	urlsDefaultConcurrency = 10
)

// URLsDoc is the scanner documentation shared by the dedicated tool and the
// generic recon surface.
const URLsDoc = `recon_urls: crawls seed URLs with katana and returns deduplicated URLs and endpoints as compact JSON.

Two modes: standard (katana's HTTP crawler) and headless (headless:true, -hl). Headless starts katana's own Chromium, so it needs a browser binary on PATH and costs far more time per page.

Arguments:
| arg | type | meaning |
| :-- | :-- | :-- |
| targets | string[], required | Seed URLs or hosts, at most 20. Each seed is scope-checked before katana starts. |
| depth | integer | Crawl depth (-d). Default 3, ceiling 10. Negative values are refused. Every extra level multiplies requests and runtime. |
| js_crawl | boolean | Parse JavaScript for endpoints (-jc). Default true. |
| form_extraction | boolean | Emit form, input, textarea and select elements (-fx). Default false. |
| known_files | string | Crawl known files (-kf): all, robotstxt or sitemapxml. Needs depth >= 3. Empty disables it. |
| headless | boolean | Run katana's headless Chromium crawler (-hl). Default false; starts its own browser and is slower. |
| concurrency | integer | Concurrent fetchers (-c). Default 10. |
| rate_limit | integer | Per-host requests per second (-hrl). Default 5. |
| max_entries | integer | Maximum compact entries returned. Default 200; the crawl parses at most 5000. |
| output_file | string | Absolute path for the raw JSONL report, written mode 0600. A relative path is refused. |
| extra_args | string[] | katana flags appended verbatim after the typed arguments, reaching any CLI flag. |
| scope | string[] | Host allowlist. Empty defaults to the literal seed hosts. A returned URL outside scope is dropped and counted in out_of_scope. |
| authorized | boolean, required | Consent gate; must be true. Active crawling sends traffic. |
| timeout_ms | integer | Run budget in milliseconds. Default 120000, ceiling 1800000. |

On the shared generic MCP/CLI surface, pass the typed flags through extra_args, for example extra_args:["-d","2","-jc"].

Example: {"targets":["https://example.com"],"depth":2,"js_crawl":true,"authorized":true}

Output is one compact entry per URL: {entries:[{url,method?,status?}],count,truncated,dropped,out_of_scope,output_file?}. URLs are deduplicated, out-of-scope URLs are counted in out_of_scope and omitted, truncated is 1 when max_entries cut the list and dropped is the number of in-scope entries omitted. A crawl that finds nothing returns count 0, not an error.

Errors: invalid_args names the missing or invalid argument (targets, authorized, more than 20 seeds, depth above 10, a seed outside scope, a relative output_file, or a negative limit); timeout means the run budget expired; unknown otherwise, with katana's stderr in the message.

Consent: crawling sends active traffic — call it only for targets you are permitted to scan, with authorized:true.`

// katanaSpec is the registry descriptor for the katana crawler.
var katanaSpec = Spec{
	ID:          URLsID,
	Module:      "github.com/projectdiscovery/katana/cmd/katana",
	Pin:         urlsPin,
	BinName:     "katana",
	VersionArgs: []string{"-version"},
	JSONArgs:    []string{"-jsonl", "-silent", "-nc"},
	Doc:         URLsDoc,
}

func init() { Register(katanaSpec) }

// URLsArgs shapes one typed katana crawl.
type URLsArgs struct {
	// Targets are the seed URLs or hosts, capped at 20.
	Targets []string
	// Depth is the crawl depth; zero uses 3 and values above 10 are refused.
	Depth int
	// JSCrawl enables endpoint parsing inside JavaScript files.
	JSCrawl bool
	// FormExtraction emits form, input, textarea and select elements.
	FormExtraction bool
	// KnownFiles crawls known files: all, robotstxt or sitemapxml.
	KnownFiles string
	// Headless runs katana's own headless Chromium crawler.
	Headless bool
	// Concurrency is the fetcher count; zero uses 10.
	Concurrency int
	// RateLimit is the per-host request rate; zero uses the lane default.
	RateLimit int
	// TimeoutMS is the run budget; zero uses the shared default.
	TimeoutMS int
	// MaxEntries is the compact entry cap; zero uses 200.
	MaxEntries int
	// OutputFile is an absolute path for the raw JSONL report.
	OutputFile string
	// ExtraArgs are katana flags appended verbatim after the typed arguments.
	ExtraArgs []string
	// Scope is the host allowlist; empty derives the literal seed hosts.
	Scope []string
	// Authorized is the consent gate and must be true.
	Authorized bool
}

// URLsResult is the bounded outcome of one crawl after scope filtering, URL
// deduplication and the entry cap.
type URLsResult struct {
	// Entries are the compact in-scope URLs.
	Entries []URLEntry `json:"entries"`
	// Count is the number of entries returned.
	Count int `json:"count"`
	// Truncated is 1 when the entry cap cut the result.
	Truncated int `json:"truncated"`
	// Dropped is how many in-scope entries the entry cap omitted.
	Dropped int `json:"dropped"`
	// OutOfScope is how many results the scope filter removed.
	OutOfScope int `json:"out_of_scope"`
	// OutputFile is the absolute raw report path when the caller asked for one.
	OutputFile string `json:"output_file,omitempty"`
}

// URLEntry is one compact crawled URL. Method and Status stay omitted when
// katana did not report them.
type URLEntry struct {
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
	Status int    `json:"status,omitempty"`
}

// RunURLs validates consent, caps and scope, then crawls the targets with katana
// and returns deduplicated in-scope URLs. The raw entry ring is bounded at
// MaxURLs and the compact result at MaxEntries.
func RunURLs(ctx context.Context, args URLsArgs) (URLsResult, error) {
	if !args.Authorized {
		return URLsResult{}, fmt.Errorf(
			"%s: invalid arguments: authorized:true is required — active crawling touches hosts you may not own; set it only for targets you are permitted to scan",
			URLsID,
		)
	}
	if args.TimeoutMS < 0 {
		return URLsResult{}, fmt.Errorf("%s: invalid arguments: timeout_ms must be >= 0", URLsID)
	}
	if args.MaxEntries < 0 {
		return URLsResult{}, fmt.Errorf("%s: invalid arguments: max_entries must be >= 0", URLsID)
	}

	targets, err := urlsTargets(args.Targets)
	if err != nil {
		return URLsResult{}, err
	}
	scope := urlsScope(args.Scope, targets)
	guard := Guard{Scope: scope}
	if err := guard.CheckTargets(targets); err != nil {
		return URLsResult{}, err
	}

	argv, err := urlsArgv(args, targets, scope)
	if err != nil {
		return URLsResult{}, err
	}

	result, err := Run(ctx, katanaSpec, argv, Options{
		MaxEntries: MaxURLs,
		TimeoutMS:  args.TimeoutMS,
		OutputFile: strings.TrimSpace(args.OutputFile),
		ExtraArgs:  args.ExtraArgs,
	})
	if err != nil {
		return URLsResult{}, err
	}

	report := urlsReport(result.Entries, scope, args.MaxEntries)
	report.Dropped += result.Dropped
	if result.Truncated == 1 {
		report.Truncated = 1
	}
	report.OutputFile = result.OutputFile
	return report, nil
}

// urlsTargets trims the seed list and enforces the seed cap.
func urlsTargets(raw []string) ([]string, error) {
	targets := make([]string, 0, len(raw))
	for _, value := range raw {
		if target := strings.TrimSpace(value); target != "" {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf(
			"%s: invalid arguments: targets is required — pass 1 to %d seed URLs",
			URLsID,
			urlsMaxSeeds,
		)
	}
	if len(targets) > urlsMaxSeeds {
		return nil, fmt.Errorf(
			"%s: invalid arguments: %d seeds exceeds the %d-seed cap — split the crawl across calls",
			URLsID,
			len(targets),
			urlsMaxSeeds,
		)
	}
	return targets, nil
}

// urlsScope trims the caller allowlist and falls back to the literal seed hosts.
func urlsScope(raw, targets []string) []string {
	scope := make([]string, 0, len(raw))
	for _, value := range raw {
		if entry := strings.TrimSpace(value); entry != "" {
			scope = append(scope, entry)
		}
	}
	if len(scope) == 0 {
		return katanaScope(targets)
	}
	return scope
}

// urlsArgv validates the crawl knobs, applies defaults and renders the katana
// argv. The JSON flags and extra_args are added by Run.
func urlsArgv(args URLsArgs, targets, scope []string) ([]string, error) {
	if args.Depth < 0 {
		return nil, fmt.Errorf(
			"%s: invalid arguments: depth %d must be >= 0 — omit depth to use the default %d",
			URLsID,
			args.Depth,
			urlsDefaultDepth,
		)
	}
	depth := args.Depth
	if depth == 0 {
		depth = urlsDefaultDepth
	}
	if depth > urlsMaxDepth {
		return nil, fmt.Errorf(
			"%s: invalid arguments: depth %d exceeds the ceiling %d — lower depth or split the crawl",
			URLsID,
			depth,
			urlsMaxDepth,
		)
	}

	knownFiles := strings.ToLower(strings.TrimSpace(args.KnownFiles))
	switch knownFiles {
	case "", "all", "robotstxt", "sitemapxml":
	default:
		return nil, fmt.Errorf(
			"%s: invalid arguments: known_files %q must be all, robotstxt, sitemapxml, or empty",
			URLsID,
			args.KnownFiles,
		)
	}

	concurrency := args.Concurrency
	if concurrency <= 0 {
		concurrency = urlsDefaultConcurrency
	}
	rateLimit := args.RateLimit
	if rateLimit <= 0 {
		rateLimit = int(DefaultRPS)
	}

	argv := make([]string, 0, 10+2*len(scope)+2*len(targets))
	argv = append(argv, "-d", strconv.Itoa(depth))
	if args.JSCrawl {
		argv = append(argv, "-jc")
	}
	if args.FormExtraction {
		argv = append(argv, "-fx")
	}
	if knownFiles != "" {
		argv = append(argv, "-kf", knownFiles)
	}
	if args.Headless {
		argv = append(argv, "-hl")
	}
	argv = append(argv, "-c", strconv.Itoa(concurrency))
	argv = append(argv, "-hrl", strconv.Itoa(rateLimit))
	for _, entry := range scope {
		if pattern := urlsScopeRegex(entry); pattern != "" {
			argv = append(argv, "-cs", pattern)
		}
	}
	for _, target := range targets {
		argv = append(argv, "-u", target)
	}
	return argv, nil
}

// katanaScope returns the distinct literal hosts of targets, in target order.
// It is the default allowlist when the caller passes no scope.
func katanaScope(targets []string) []string {
	seen := make(map[string]struct{}, len(targets))
	scope := make([]string, 0, len(targets))
	for _, raw := range targets {
		host := targetHost(raw)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		scope = append(scope, host)
	}
	return scope
}

// urlsScopeRegex renders one allowlist entry as a katana crawl-scope regex. An
// entry of "*" yields "" because it prunes nothing.
func urlsScopeRegex(entry string) string {
	host := strings.ToLower(strings.TrimSpace(entry))
	host = strings.TrimPrefix(host, "*.")
	host = strings.TrimSuffix(host, ".")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || host == "*" || strings.Contains(host, ":") {
		return ""
	}
	return `(?i)^https?://([a-z0-9-]+\.)*` + regexp.QuoteMeta(host) + `([/:?#]|$)`
}

// urlsReport converts raw katana objects into compact entries. It drops URLs
// outside scope, deduplicates by URL, enriches a duplicate with a method or
// status the first occurrence lacked, and clips to max.
func urlsReport(raw []map[string]any, scope []string, max int) URLsResult {
	if max <= 0 {
		max = defaultMaxEntries
	}
	report := URLsResult{Entries: make([]URLEntry, 0, min(len(raw), max))}
	index := make(map[string]int, len(raw))
	for _, item := range raw {
		endpoint, method, status := katanaResult(item)
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if len(scope) > 0 && !scopeAllowsHost(u.Hostname(), scope) {
			report.OutOfScope++
			continue
		}
		if i, seen := index[endpoint]; seen {
			if report.Entries[i].Method == "" {
				report.Entries[i].Method = method
			}
			if report.Entries[i].Status == 0 {
				report.Entries[i].Status = status
			}
			continue
		}
		index[endpoint] = len(report.Entries)
		report.Entries = append(report.Entries, URLEntry{
			URL:    endpoint,
			Method: method,
			Status: status,
		})
	}
	if len(report.Entries) > max {
		report.Dropped = len(report.Entries) - max
		report.Entries = report.Entries[:max]
		report.Truncated = 1
	}
	report.Count = len(report.Entries)
	return report
}

// katanaResult reads the endpoint, method and status from one raw katana
// object, accepting both the nested request/response shape and a bare url key.
func katanaResult(item map[string]any) (endpoint, method string, status int) {
	if request, ok := item["request"].(map[string]any); ok {
		endpoint = katanaText(request["endpoint"])
		method = strings.ToUpper(katanaText(request["method"]))
	}
	if endpoint == "" {
		endpoint = katanaText(item["url"])
	}
	if response, ok := item["response"].(map[string]any); ok {
		status = katanaNumber(response["status_code"])
	}
	return endpoint, method, status
}

// katanaText returns a trimmed string value, or "" for any other type.
func katanaText(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// katanaNumber returns an integer from the numeric shapes encoding/json and
// katana emit, or 0 for anything else.
func katanaNumber(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0
		}
		return i
	default:
		return 0
	}
}
