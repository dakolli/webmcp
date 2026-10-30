package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Stage identifiers, listed in the fixed dependency order the pipeline always
// executes them in.
const (
	StageSubdomains = "subdomains"
	StageDNS        = "dns"
	StageHTTP       = "http"
	StagePorts      = "ports"
	StageURLs       = "urls"
	StageJS         = "js"
	StageFuzz       = "fuzz"
	StageTemplates  = "templates"
)

// Per-stage statuses in a pipeline result.
const (
	StageRan     = "ran"
	StageSkipped = "skipped"
	StageFailed  = "failed"
)

// Pipeline defaults and bounds.
const (
	// PipelineDefaultMaxEntries is the total entry cap across every stage.
	PipelineDefaultMaxEntries = 200
	// PipelineDefaultMaxHosts is the host cap a stage input is trimmed to.
	PipelineDefaultMaxHosts = 100
	// PipelineDefaultMaxURLs is the URL cap a stage input is trimmed to.
	PipelineDefaultMaxURLs = 500
	// PipelineDefaultTimeoutMS is the whole-run budget when none is set.
	PipelineDefaultTimeoutMS = 600_000
	// PipelineMaxTimeoutMS is the ceiling a caller's total budget is clamped to.
	PipelineMaxTimeoutMS = 3_600_000
)

// pipelineStages is the dependency order. A caller's selection is never sorted
// at call time; execution always walks this slice.
var pipelineStages = []string{
	StageSubdomains,
	StageDNS,
	StageHTTP,
	StagePorts,
	StageURLs,
	StageJS,
	StageFuzz,
	StageTemplates,
}

// stageDeps names the earlier stages each stage consumes. A dependency the
// caller did not select is added automatically and reported.
var stageDeps = map[string][]string{
	StageHTTP:      {StageDNS},
	StageURLs:      {StageHTTP},
	StageJS:        {StageHTTP},
	StageFuzz:      {StageHTTP},
	StageTemplates: {StageHTTP},
}

// defaultPipelineStages is the stage selection used when the caller names none.
var defaultPipelineStages = []string{StageSubdomains, StageDNS, StageHTTP}

// PipelineArgs is one recon_run request. Domain and Authorized are required;
// every other field falls back to a documented default.
type PipelineArgs struct {
	Domain      string
	Stages      []string
	Templates   []string
	Authorized  bool
	Scope       []string
	StopOnEmpty bool
	MaxEntries  int
	MaxHosts    int
	MaxURLs     int
	TimeoutMS   int
	OutputFile  string
	ExtraArgs   map[string][]string
	RPS         float64
	Burst       int
}

// StageOutput is one stage's produced targets plus the counts its scanner
// reported. Targets feed the next stage.
type StageOutput struct {
	Targets   []string
	Count     int
	Truncated int
	Dropped   int
}

// StageResult is the per-stage record returned by a pipeline run.
type StageResult struct {
	Stage     string   `json:"stage"`
	Status    string   `json:"status"`
	Count     int      `json:"count"`
	Truncated int      `json:"truncated"`
	Dropped   int      `json:"dropped"`
	Added     []string `json:"added,omitempty"`
	Error     string   `json:"error,omitempty"`
	MS        int64    `json:"ms"`
}

// PipelineResult is one recon_run outcome. Truncated and Dropped aggregate the
// per-stage cuts across the whole run.
type PipelineResult struct {
	Domain     string        `json:"domain"`
	Stages     []StageResult `json:"stages"`
	Truncated  int           `json:"truncated"`
	Dropped    int           `json:"dropped"`
	Warnings   []string      `json:"warnings,omitempty"`
	OutputFile string        `json:"output_file,omitempty"`
}

// StageRunner executes one pipeline stage against its input targets. The
// production implementation shells out to the pinned scanners; tests inject a
// stub so no scanner and no network are touched.
type StageRunner interface {
	Run(ctx context.Context, stage string, targets []string) (StageOutput, error)
}

// RunPipeline selects the requested stages, resolves their dependencies and runs
// them in the fixed dependency order, feeding each artifact to the next.
func RunPipeline(ctx context.Context, args PipelineArgs) (PipelineResult, error) {
	return runPipeline(ctx, args, nil)
}

// runPipeline is RunPipeline with an injectable stage runner.
func runPipeline(
	ctx context.Context,
	args PipelineArgs,
	runner StageRunner,
) (PipelineResult, error) {
	domain, err := pipelineDomain(args.Domain)
	if err != nil {
		return PipelineResult{}, err
	}
	if !args.Authorized {
		return PipelineResult{}, fmt.Errorf(
			"recon_run: invalid arguments: authorized:true is required — the pipeline sends active traffic to targets you must be permitted to assess, so set it explicitly",
		)
	}
	outputFile := strings.TrimSpace(args.OutputFile)
	if outputFile != "" && !filepath.IsAbs(outputFile) {
		return PipelineResult{}, fmt.Errorf(
			"recon_run: invalid arguments: output_file %q must be absolute — pass an absolute path",
			outputFile,
		)
	}

	selected, added, warnings, err := resolvePipelineStages(args.Stages)
	if err != nil {
		return PipelineResult{}, err
	}

	templates, unused, err := pipelineTemplates(args.Templates, selected[StageTemplates])
	if err != nil {
		return PipelineResult{}, err
	}
	args.Templates = templates
	if unused != "" {
		warnings = append(warnings, unused)
	}

	scope := args.Scope
	if len(scope) == 0 {
		scope = []string{domain}
	}
	guard := Guard{Scope: scope, RPS: args.RPS, Burst: args.Burst}
	if err := guard.CheckHosts([]string{domain}); err != nil {
		return PipelineResult{}, err
	}
	if runner == nil {
		runner = &scannerRunner{args: args, guard: &guard}
	}

	budget := pipelineTimeout(args.TimeoutMS)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	maxEntries := pipelineMaxEntries(args.MaxEntries)
	maxHosts := pipelineMaxHosts(args.MaxHosts)
	maxURLs := pipelineMaxURLs(args.MaxURLs)

	result := PipelineResult{
		Domain:     domain,
		Warnings:   warnings,
		OutputFile: outputFile,
	}
	var hosts, urls []string
	total := 0
	halted := ""
	for _, stage := range pipelineStages {
		if !selected[stage] {
			continue
		}
		record := StageResult{Stage: stage, Added: added[stage]}
		if halted != "" {
			record.Status = StageSkipped
			record.Error = halted
			result.Stages = append(result.Stages, record)
			continue
		}

		input := stageInput(stage, hosts, urls, domain, maxHosts, maxURLs)
		if len(input) == 0 {
			record.Status = StageSkipped
			record.Error = skipReason(stage)
			result.Stages = append(result.Stages, record)
			continue
		}
		if err := guard.CheckTargets(input); err != nil {
			record.Status = StageFailed
			record.Error = err.Error()
			result.Stages = append(result.Stages, record)
			continue
		}
		if err := throttle(ctx, &guard, input); err != nil {
			record.Status = StageFailed
			record.Error = err.Error()
			result.Stages = append(result.Stages, record)
			continue
		}

		start := time.Now()
		out, runErr := runner.Run(ctx, stage, input)
		record.MS = time.Since(start).Milliseconds()
		if runErr != nil {
			record.Status = StageFailed
			record.Error = runErr.Error()
			result.Stages = append(result.Stages, record)
			continue
		}

		remaining := maxEntries - total
		if remaining < 0 {
			remaining = 0
		}
		kept, capDropped := capStage(out, remaining)
		kept.Dropped += capDropped
		if capDropped > 0 {
			kept.Truncated = 1
		}
		total += kept.Count
		if kept.Truncated > 0 {
			result.Truncated = 1
		}
		result.Dropped += kept.Dropped

		record.Status = StageRan
		record.Count = kept.Count
		record.Truncated = kept.Truncated
		record.Dropped = kept.Dropped
		updateArtifact(stage, kept.Targets, &hosts, &urls)
		if args.StopOnEmpty && kept.Count == 0 {
			halted = fmt.Sprintf("stop_on_empty: %s returned no entries", stage)
		}
		result.Stages = append(result.Stages, record)
	}

	if outputFile != "" {
		if err := writeStageRecords(outputFile, result.Stages); err != nil {
			return result, err
		}
	}
	return result, nil
}

// resolvePipelineStages validates the caller's selection and closes it over its
// dependencies. The added map and warnings are reported per causing stage.
func resolvePipelineStages(
	requested []string,
) (map[string]bool, map[string][]string, []string, error) {
	selected := make(map[string]bool, len(pipelineStages))
	if len(requested) == 0 {
		for _, stage := range defaultPipelineStages {
			selected[stage] = true
		}
	} else {
		for _, raw := range requested {
			stage := strings.ToLower(strings.TrimSpace(raw))
			if stage == "" {
				continue
			}
			if !isPipelineStage(stage) {
				return nil, nil, nil, fmt.Errorf(
					"recon_run: invalid arguments: unknown stage %q — use one of %s",
					raw,
					strings.Join(pipelineStages, ", "),
				)
			}
			selected[stage] = true
		}
		if len(selected) == 0 {
			for _, stage := range defaultPipelineStages {
				selected[stage] = true
			}
		}
	}

	added := make(map[string][]string)
	var warnings []string
	for changed := true; changed; {
		changed = false
		for _, stage := range pipelineStages {
			if !selected[stage] {
				continue
			}
			for _, dep := range stageDeps[stage] {
				if selected[dep] {
					continue
				}
				selected[dep] = true
				added[stage] = append(added[stage], dep)
				warnings = append(warnings, fmt.Sprintf(
					"%s requires %s; added it to the run",
					stage,
					dep,
				))
				changed = true
			}
		}
	}
	return selected, added, warnings, nil
}

// isPipelineStage reports whether stage is one of the eight known stages.
func isPipelineStage(stage string) bool {
	for _, known := range pipelineStages {
		if stage == known {
			return true
		}
	}
	return false
}

// pipelineTemplates validates the nuclei template paths a run may pass to the
// templates stage. A relative or unreadable path fails naming that path; when
// the templates stage is not selected the paths are accepted but unused, and the
// returned warning says so.
func pipelineTemplates(
	templates []string,
	selected bool,
) ([]string, string, error) {
	cleaned := make([]string, 0, len(templates))
	for _, raw := range templates {
		if path := strings.TrimSpace(raw); path != "" {
			cleaned = append(cleaned, path)
		}
	}
	if len(cleaned) == 0 {
		return nil, "", nil
	}
	if !selected {
		return nil, fmt.Sprintf(
			"templates %v ignored: the templates stage is not selected",
			cleaned,
		), nil
	}
	for _, path := range cleaned {
		if !filepath.IsAbs(path) {
			return nil, "", fmt.Errorf(
				"recon_run: invalid arguments: templates entry %q must be an absolute template directory or file",
				path,
			)
		}
		if _, err := os.Stat(path); err != nil {
			return nil, "", fmt.Errorf(
				"recon_run: invalid arguments: templates entry %q does not exist or is not readable: %w — pass an existing absolute template directory or file, or run \"nuclei -update-templates\" once",
				path,
				err,
			)
		}
	}
	return cleaned, "", nil
}

// stageInput resolves what a stage consumes: the seed, the host artifact or the
// URL artifact. An empty result means the stage has no input and is skipped.
func stageInput(
	stage string,
	hosts, urls []string,
	domain string,
	maxHosts, maxURLs int,
) []string {
	switch stage {
	case StageSubdomains:
		return []string{domain}
	case StageDNS, StageHTTP, StagePorts:
		if len(hosts) > 0 {
			return limit(hosts, maxHosts)
		}
		return []string{domain}
	case StageURLs:
		return limit(urls, maxURLs)
	case StageJS:
		return jsURLs(limit(urls, maxURLs))
	case StageFuzz:
		return firstPerHost(limit(urls, maxURLs), maxHosts)
	case StageTemplates:
		return limit(urls, NucleiMaxTargets)
	default:
		return nil
	}
}

// skipReason explains why a stage had no artifact to consume.
func skipReason(stage string) string {
	switch stage {
	case StageURLs:
		return "no live URLs: http produced none"
	case StageJS:
		return "no JavaScript URLs in the http/urls artifact"
	case StageFuzz:
		return "no live URLs: fuzz needs one http URL per host"
	case StageTemplates:
		return "no live URLs: templates needs an http or urls target"
	default:
		return "no input from an earlier stage"
	}
}

// updateArtifact carries a stage's output to the stages that consume it.
func updateArtifact(stage string, targets []string, hosts, urls *[]string) {
	switch stage {
	case StageSubdomains, StageDNS:
		*hosts = targets
	case StageHTTP, StageURLs:
		*urls = targets
	}
}

// capStage trims a stage output to the run's remaining entry budget.
func capStage(out StageOutput, remaining int) (StageOutput, int) {
	if out.Count <= remaining {
		return out, 0
	}
	kept := out
	dropped := out.Count - remaining
	kept.Count = remaining
	if len(kept.Targets) > remaining {
		kept.Targets = kept.Targets[:remaining]
	}
	return kept, dropped
}

// pipelineTimeout resolves the whole-run budget and clamps it to the ceiling.
func pipelineTimeout(ms int) time.Duration {
	if ms <= 0 {
		return PipelineDefaultTimeoutMS * time.Millisecond
	}
	if ms > PipelineMaxTimeoutMS {
		return PipelineMaxTimeoutMS * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

// pipelineMaxEntries resolves the run-wide entry cap.
func pipelineMaxEntries(n int) int {
	if n <= 0 {
		return PipelineDefaultMaxEntries
	}
	return n
}

// pipelineMaxHosts resolves the host cap a stage input is trimmed to.
func pipelineMaxHosts(n int) int {
	if n <= 0 {
		return PipelineDefaultMaxHosts
	}
	return n
}

// pipelineMaxURLs resolves the URL cap a stage input is trimmed to.
func pipelineMaxURLs(n int) int {
	if n <= 0 {
		return PipelineDefaultMaxURLs
	}
	return n
}

// pipelineDomain normalises and validates the single seed domain.
func pipelineDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if domain == "" {
		return "", fmt.Errorf(
			"recon_run: invalid arguments: domain is required — pass one registrable domain, e.g. example.com",
		)
	}
	if strings.ContainsAny(domain, "/ \t\r\n:") ||
		strings.Contains(domain, "*") ||
		!strings.Contains(domain, ".") {
		return "", fmt.Errorf(
			"recon_run: invalid arguments: domain %q must be a bare registrable domain, not a URL, host:port or wildcard — pass example.com",
			raw,
		)
	}
	return domain, nil
}

// limit trims values to max, leaving the slice untouched when it already fits.
func limit(values []string, max int) []string {
	if max <= 0 || len(values) <= max {
		return values
	}
	return values[:max]
}

// jsURLs keeps the URLs whose path ends in .js.
func jsURLs(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(parsed.Path), ".js") {
			out = append(out, raw)
		}
	}
	return out
}

// firstPerHost keeps the first URL for each host, capped at max hosts.
func firstPerHost(urls []string, max int) []string {
	out := make([]string, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, raw)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

// throttle spends one slot per distinct host in the shared per-host bucket, so
// the rate limit spans every stage of the run instead of resetting per stage.
func throttle(ctx context.Context, guard *Guard, targets []string) error {
	for _, host := range hostList(targets) {
		if err := guard.Wait(ctx, host); err != nil {
			return fmt.Errorf("recon_run: rate limit: %w", err)
		}
	}
	return nil
}

// hostList reduces targets to their unique host names.
func hostList(targets []string) []string {
	hosts := make([]string, 0, len(targets))
	for _, raw := range targets {
		if host := targetHost(raw); host != "" {
			hosts = append(hosts, host)
		}
	}
	return dedupe(hosts)
}

// hostsOfEntries collects the host field from decoded scanner entries.
func hostsOfEntries(entries []map[string]any) []string {
	hosts := make([]string, 0, len(entries))
	for _, entry := range entries {
		host, _ := entry["host"].(string)
		if host = strings.TrimSpace(host); host != "" {
			hosts = append(hosts, host)
		}
	}
	return dedupe(hosts)
}

// dedupe removes repeated values while preserving their first order.
func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// writeStageRecords writes one JSON object per stage to an absolute 0600 file.
func writeStageRecords(path string, stages []StageResult) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("recon_run: write output_file %q: %w", path, err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("recon_run: close output_file %q: %w", path, cerr)
		}
	}()

	encoder := json.NewEncoder(file)
	for _, stage := range stages {
		if err := encoder.Encode(stage); err != nil {
			return fmt.Errorf("recon_run: encode stage %q: %w", stage.Stage, err)
		}
	}
	return nil
}

// scannerRunner is the production StageRunner: it shells out to the pinned
// scanners through their package functions.
type scannerRunner struct {
	args  PipelineArgs
	guard *Guard
}

// Run executes one stage against its input targets.
func (r *scannerRunner) Run(
	ctx context.Context,
	stage string,
	targets []string,
) (StageOutput, error) {
	switch stage {
	case StageSubdomains:
		return r.subdomains(ctx)
	case StageDNS:
		return r.dns(ctx, targets)
	case StageHTTP:
		return r.http(ctx, targets)
	case StagePorts:
		return r.ports(ctx, targets)
	case StageURLs:
		return r.urls(ctx, targets)
	case StageJS:
		return r.js(ctx, targets)
	case StageFuzz:
		return r.fuzz(ctx, targets)
	case StageTemplates:
		return r.templates(ctx, targets)
	default:
		return StageOutput{}, fmt.Errorf("recon_run: unknown stage %q", stage)
	}
}

// subdomains enumerates hosts under the seed domain.
func (r *scannerRunner) subdomains(ctx context.Context) (StageOutput, error) {
	result, err := RunSubdomains(ctx, SubdomainsArgs{
		Target:     r.args.Domain,
		AllSources: true,
		Authorized: r.args.Authorized,
		Scope:      r.scope(),
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StageSubdomains),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: subdomains: %w", err)
	}
	hosts := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if entry.Host != "" {
			hosts = append(hosts, entry.Host)
		}
	}
	hosts = dedupe(hosts)
	return StageOutput{
		Targets:   hosts,
		Count:     len(hosts),
		Truncated: result.Truncated,
		Dropped:   result.Dropped,
	}, nil
}

// dns resolves the host artifact to hosts with DNS records.
func (r *scannerRunner) dns(ctx context.Context, targets []string) (StageOutput, error) {
	result, err := RunDNS(ctx, DNSOptions{
		Targets:    targets,
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StageDNS),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: dns: %w", err)
	}
	hosts := hostsOfEntries(result.Entries)
	return StageOutput{
		Targets:   hosts,
		Count:     len(hosts),
		Truncated: result.Truncated,
		Dropped:   result.Dropped,
	}, nil
}

// http probes the hosts and yields the live URLs the later stages consume.
func (r *scannerRunner) http(ctx context.Context, targets []string) (StageOutput, error) {
	result, err := RunHTTP(ctx, HTTPOptions{
		Targets:    targets,
		StatusCode: true,
		Title:      true,
		Tech:       true,
		Scope:      r.scope(),
		RateLimit:  r.rate(),
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StageHTTP),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: http: %w", err)
	}
	urls := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if entry.URL != "" {
			urls = append(urls, entry.URL)
		}
	}
	urls = dedupe(urls)
	return StageOutput{
		Targets:   urls,
		Count:     len(urls),
		Truncated: result.Truncated,
		Dropped:   result.Dropped,
	}, nil
}

// ports scans the host artifact for open ports.
func (r *scannerRunner) ports(ctx context.Context, targets []string) (StageOutput, error) {
	scan := PortsScan{Targets: hostList(targets)}
	argv, err := scan.Args()
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: ports: %w", err)
	}
	spec, ok := Get("recon_ports")
	if !ok {
		return StageOutput{}, fmt.Errorf("recon_run: ports: the naabu scanner is not registered")
	}
	result, err := Run(ctx, spec, argv, Options{
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StagePorts),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: ports: %w", err)
	}
	return StageOutput{
		Targets:   hostsOfEntries(result.Entries),
		Count:     len(result.Entries),
		Truncated: result.Truncated,
		Dropped:   result.Dropped,
	}, nil
}

// urls crawls the live URLs with katana and yields the discovered endpoints.
func (r *scannerRunner) urls(ctx context.Context, targets []string) (StageOutput, error) {
	result, err := RunURLs(ctx, URLsArgs{
		Targets:    targets,
		Authorized: r.args.Authorized,
		Scope:      r.scope(),
		RateLimit:  r.rate(),
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StageURLs),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: urls: %w", err)
	}
	urls := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if entry.URL != "" {
			urls = append(urls, entry.URL)
		}
	}
	urls = dedupe(urls)
	return StageOutput{
		Targets:   urls,
		Count:     len(urls),
		Truncated: result.Truncated,
		Dropped:   result.Dropped,
	}, nil
}

// js analyzes the JavaScript URLs among the live URLs.
func (r *scannerRunner) js(ctx context.Context, targets []string) (StageOutput, error) {
	result, err := RunJSLuice(ctx, JSLuiceArgs{
		Mode:  "urls",
		URLs:  targets,
		Scope: r.scope(),
		RPS:   r.args.RPS,
		Burst: r.args.Burst,
	}, Options{
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StageJS),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: js: %w", err)
	}
	return StageOutput{
		Count:     len(result.Entries),
		Truncated: result.Truncated,
		Dropped:   result.Dropped,
	}, nil
}

// fuzz fuzzes one URL per host and aggregates the hit counts.
func (r *scannerRunner) fuzz(ctx context.Context, targets []string) (StageOutput, error) {
	out := StageOutput{}
	for _, target := range targets {
		result, err := RunFuzz(ctx, FuzzArgs{
			URL:        target,
			Scope:      r.scope(),
			Rate:       r.rate(),
			MaxEntries: r.maxEntries(),
			ExtraArgs:  r.extra(StageFuzz),
		})
		if err != nil {
			return StageOutput{}, fmt.Errorf("recon_run: fuzz %q: %w", target, err)
		}
		out.Count += result.Count
		out.Dropped += result.Dropped
		if result.Truncated > 0 {
			out.Truncated = 1
		}
	}
	return out, nil
}

// templates scans the live URLs with the pinned nuclei templates.
func (r *scannerRunner) templates(
	ctx context.Context,
	targets []string,
) (StageOutput, error) {
	request := NucleiRequest{
		Targets:   targets,
		Templates: r.args.Templates,
		RateLimit: r.rate(),
	}
	if err := request.Validate(); err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: templates: %w", err)
	}
	result, err := Run(ctx, NucleiSpec, request.Argv(), Options{
		MaxEntries: r.maxEntries(),
		ExtraArgs:  r.extra(StageTemplates),
	})
	if err != nil {
		return StageOutput{}, fmt.Errorf("recon_run: templates: %w", err)
	}
	compact := CompactNuclei(result)
	return StageOutput{
		Count:     compact.Count,
		Truncated: compact.Truncated,
		Dropped:   compact.Dropped,
	}, nil
}

// scope returns the single allowlist every stage shares.
func (r *scannerRunner) scope() []string {
	if r.guard == nil {
		return r.args.Scope
	}
	return r.guard.Scope
}

// maxEntries returns the per-stage entry budget.
func (r *scannerRunner) maxEntries() int {
	return pipelineMaxEntries(r.args.MaxEntries)
}

// extra returns the per-stage passthrough arguments.
func (r *scannerRunner) extra(stage string) []string {
	if r.args.ExtraArgs == nil {
		return nil
	}
	return r.args.ExtraArgs[stage]
}

// rate returns the shared per-host rate as an integer scanner flag.
func (r *scannerRunner) rate() int {
	if r.guard == nil || r.guard.RPS <= 0 {
		return 0
	}
	return int(r.guard.RPS)
}
