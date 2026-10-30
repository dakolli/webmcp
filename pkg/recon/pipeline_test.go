package recon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stubRunner records the call order and returns canned outputs, so the pipeline
// tests touch no scanner and no network.
type stubRunner struct {
	calls   []string
	outputs map[string]StageOutput
	errs    map[string]error
}

// Run records the stage call and returns its canned output or error.
func (s *stubRunner) Run(_ context.Context, stage string, _ []string) (StageOutput, error) {
	s.calls = append(s.calls, stage)
	if err := s.errs[stage]; err != nil {
		return StageOutput{}, err
	}
	return s.outputs[stage], nil
}

// recordFor returns the stage record named stage.
func recordFor(t *testing.T, stages []StageResult, stage string) StageResult {
	t.Helper()
	for _, record := range stages {
		if record.Stage == stage {
			return record
		}
	}
	t.Fatalf("no record for stage %q in %+v", stage, stages)
	return StageResult{}
}

// TestRunPipelineFixedOrder asserts a subset runs in dependency order, not in
// the order the caller passed.
func TestRunPipelineFixedOrder(t *testing.T) {
	runner := &stubRunner{outputs: map[string]StageOutput{
		StageDNS:  {Targets: []string{"a.example.com"}, Count: 1},
		StageHTTP: {Targets: []string{"https://a.example.com"}, Count: 1},
		StageURLs: {Targets: []string{"https://a.example.com/x"}, Count: 1},
	}}

	result, err := runPipeline(context.Background(), PipelineArgs{
		Domain:     "example.com",
		Stages:     []string{"urls", "http"},
		Authorized: true,
	}, runner)
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	want := []string{StageDNS, StageHTTP, StageURLs}
	if !slices.Equal(runner.calls, want) {
		t.Fatalf("calls = %v, want %v", runner.calls, want)
	}
	if len(result.Stages) != len(want) {
		t.Fatalf("stages = %d, want %d", len(result.Stages), len(want))
	}
}

// TestRunPipelineAddsDependencies asserts a missing dependency is added and
// named on both the causing stage and the run warnings.
func TestRunPipelineAddsDependencies(t *testing.T) {
	runner := &stubRunner{outputs: map[string]StageOutput{
		StageDNS:  {Targets: []string{"a.example.com"}, Count: 1},
		StageHTTP: {Targets: []string{"https://a.example.com"}, Count: 1},
		StageURLs: {Targets: []string{"https://a.example.com/x"}, Count: 1},
	}}

	result, err := runPipeline(context.Background(), PipelineArgs{
		Domain:     "example.com",
		Stages:     []string{"urls"},
		Authorized: true,
	}, runner)
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	if !slices.Contains(result.Warnings, "urls requires http; added it to the run") {
		t.Errorf("warnings %v missing the urls→http note", result.Warnings)
	}
	if !slices.Contains(result.Warnings, "http requires dns; added it to the run") {
		t.Errorf("warnings %v missing the http→dns note", result.Warnings)
	}
	if added := recordFor(t, result.Stages, StageURLs).Added; !slices.Contains(added, StageHTTP) {
		t.Errorf("urls added = %v, want http", added)
	}
	if added := recordFor(t, result.Stages, StageHTTP).Added; !slices.Contains(added, StageDNS) {
		t.Errorf("http added = %v, want dns", added)
	}
}

// TestRunPipelineFailedStageContinues asserts a failed stage records its error
// and the run keeps the earlier results.
func TestRunPipelineFailedStageContinues(t *testing.T) {
	runner := &stubRunner{
		outputs: map[string]StageOutput{
			StageDNS:  {Targets: []string{"a.example.com"}, Count: 1},
			StageHTTP: {Targets: []string{"https://a.example.com"}, Count: 1},
		},
		errs: map[string]error{StagePorts: errors.New("naabu exploded")},
	}

	result, err := runPipeline(context.Background(), PipelineArgs{
		Domain:     "example.com",
		Stages:     []string{"http", "ports"},
		Authorized: true,
	}, runner)
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	httpRecord := recordFor(t, result.Stages, StageHTTP)
	if httpRecord.Status != StageRan || httpRecord.Count != 1 {
		t.Errorf("http record = %+v, want ran with 1 entry", httpRecord)
	}
	portsRecord := recordFor(t, result.Stages, StagePorts)
	if portsRecord.Status != StageFailed {
		t.Errorf("ports status = %q, want failed", portsRecord.Status)
	}
	if !strings.Contains(portsRecord.Error, "naabu exploded") {
		t.Errorf("ports error = %q, want the scanner error", portsRecord.Error)
	}
}

// TestRunPipelineStopOnEmpty asserts an empty stage halts the chain only when
// stop_on_empty is set.
func TestRunPipelineStopOnEmpty(t *testing.T) {
	tests := []struct {
		name       string
		stop       bool
		wantStatus string
	}{
		{name: "stops when set", stop: true, wantStatus: StageSkipped},
		{name: "continues when unset", stop: false, wantStatus: StageRan},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &stubRunner{outputs: map[string]StageOutput{
				StageSubdomains: {Count: 0},
				StageDNS:        {Targets: []string{"example.com"}, Count: 1},
			}}

			result, err := runPipeline(context.Background(), PipelineArgs{
				Domain:      "example.com",
				Stages:      []string{"subdomains", "dns"},
				Authorized:  true,
				StopOnEmpty: tc.stop,
			}, runner)
			if err != nil {
				t.Fatalf("runPipeline: %v", err)
			}
			dns := recordFor(t, result.Stages, StageDNS)
			if dns.Status != tc.wantStatus {
				t.Fatalf("dns status = %q, want %q", dns.Status, tc.wantStatus)
			}
			if tc.stop && !strings.Contains(dns.Error, "stop_on_empty") {
				t.Errorf("dns error = %q, want the stop_on_empty reason", dns.Error)
			}
		})
	}
}

// TestRunPipelineCapsTotalEntries asserts the run-wide cap trims a stage and is
// reported both per stage and for the run.
func TestRunPipelineCapsTotalEntries(t *testing.T) {
	runner := &stubRunner{outputs: map[string]StageOutput{
		StageDNS: {
			Targets: []string{"a.example.com", "b.example.com", "c.example.com"},
			Count:   3,
		},
		StageHTTP: {
			Targets: []string{"https://a.example.com", "https://b.example.com"},
			Count:   2,
		},
	}}

	result, err := runPipeline(context.Background(), PipelineArgs{
		Domain:     "example.com",
		Stages:     []string{"dns", "http"},
		Authorized: true,
		MaxEntries: 2,
	}, runner)
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	dns := recordFor(t, result.Stages, StageDNS)
	if dns.Count != 2 || dns.Truncated != 1 || dns.Dropped != 1 {
		t.Errorf("dns record = %+v, want count 2, truncated 1, dropped 1", dns)
	}
	if result.Dropped == 0 {
		t.Error("run dropped = 0, want the capped remainder")
	}
}

// TestRunPipelineWritesStageJSONL asserts output_file holds one JSON object per
// stage and that every line names its stage.
func TestRunPipelineWritesStageJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	runner := &stubRunner{outputs: map[string]StageOutput{
		StageSubdomains: {Targets: []string{"a.example.com"}, Count: 1},
		StageDNS:        {Targets: []string{"a.example.com"}, Count: 1},
	}}

	result, err := runPipeline(context.Background(), PipelineArgs{
		Domain:     "example.com",
		Stages:     []string{"subdomains", "dns"},
		Authorized: true,
		OutputFile: path,
	}, runner)
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	if result.OutputFile != path {
		t.Errorf("output_file = %q, want %q", result.OutputFile, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output_file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(result.Stages) {
		t.Fatalf("output lines = %d, want %d", len(lines), len(result.Stages))
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
		if _, ok := entry["stage"]; !ok {
			t.Errorf("line %q has no stage field", line)
		}
	}
}

// TestRunPipelineValidatesTemplates asserts a relative or missing template path
// fails before any stage runs, and that an unused path is reported as a warning.
func TestRunPipelineValidatesTemplates(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name      string
		stages    []string
		templates []string
		wantErr   string
		wantWarn  string
	}{
		{
			name:      "relative path refused",
			stages:    []string{StageTemplates},
			templates: []string{"templates"},
			wantErr:   "must be an absolute template directory or file",
		},
		{
			name:      "missing path refused",
			stages:    []string{StageTemplates},
			templates: []string{filepath.Join(dir, "missing")},
			wantErr:   "does not exist or is not readable",
		},
		{
			name:      "unused when stage not selected",
			stages:    []string{StageDNS},
			templates: []string{dir},
			wantWarn:  "the templates stage is not selected",
		},
		{
			name:      "existing path accepted",
			stages:    []string{StageTemplates},
			templates: []string{dir},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &stubRunner{outputs: map[string]StageOutput{}}
			result, err := runPipeline(context.Background(), PipelineArgs{
				Domain:     "example.com",
				Stages:     tc.stages,
				Templates:  tc.templates,
				Authorized: true,
			}, runner)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.templates[0]) {
					t.Errorf("err %q does not name the path %q", err, tc.templates[0])
				}
				if len(runner.calls) != 0 {
					t.Errorf("calls = %v, want none: a bad template path must fail before any stage runs", runner.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("runPipeline: %v", err)
			}
			if tc.wantWarn == "" {
				return
			}
			if !warningsContain(result.Warnings, tc.wantWarn) {
				t.Errorf("warnings %v do not mention %q", result.Warnings, tc.wantWarn)
			}
		})
	}
}

// warningsContain reports whether any warning contains want.
func warningsContain(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return true
		}
	}
	return false
}

// TestRunPipelineRequiresConsent asserts the pipeline refuses a call without
// authorized:true.
func TestRunPipelineRequiresConsent(t *testing.T) {
	runner := &stubRunner{}
	_, err := runPipeline(context.Background(), PipelineArgs{
		Domain: "example.com",
	}, runner)
	if err == nil {
		t.Fatal("runPipeline accepted a call without authorized:true")
	}
	if !strings.Contains(err.Error(), "authorized") {
		t.Errorf("error %q does not name the consent gate", err)
	}
}
