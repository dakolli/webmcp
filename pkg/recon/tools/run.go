package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
)

// runDescription is the LLM-facing description of recon_run. It names the order,
// the auto-add rule, the per-stage shape, the consent gate and the error types so
// the tool is callable without the skill.
const runDescription = `recon_run chains the eight recon stages over one target set and returns one record per stage.

Stages always run in this fixed dependency order:
subdomains → dns → http → ports → urls → js → fuzz → templates

A selection runs in that order whatever order you pass. Missing dependencies are added automatically and reported: stages:["urls"] adds http, which adds dns; the causing stage carries an added list and the run carries warnings.

Each stage record is {stage, status, count, truncated, dropped, added, error, ms}; status is ran, skipped or failed, and added and error are omitempty so they appear only when that stage caused an auto-add or failed. The run result is {domain, stages:[<stage record>], truncated, dropped, warnings, output_file}; warnings and output_file are omitempty, so a clean, file-less run omits them. A stage with no input is skipped with a reason. A failed stage records its error and the run continues, so a failed ports scan never loses the http results; only stop_on_empty halts the chain, marking every remaining stage skipped when a stage returns nothing.

Artifacts flow forward: subdomains and dns yield hosts, http and urls yield live URLs. urls, js, fuzz and templates need http's live URLs.

Arguments:
| arg | type | meaning |
| --- | --- | --- |
| domain | string, required | the registrable seed domain, e.g. example.com |
| stages | string[] | subset of subdomains,dns,http,ports,urls,js,fuzz,templates; default subdomains,dns,http |
| templates | string[] | absolute nuclei template directory or file, passed as -t to the templates stage; accepted but ignored with a warning when that stage is not selected |
| authorized | bool, required | consent gate; never defaulted |
| scope | string[] | host allowlist, default [domain] plus its subdomains |
| stop_on_empty | bool | default false; true ends the run when a stage returns nothing |
| max_hosts | int | host cap per stage input; default 100 |
| max_urls | int | URL cap per stage input; default 500 |
| max_entries | int | total entries across all stages; default 200 |
| timeout_ms | int | whole-run budget; default 600000, ceiling 3600000 |
| output_file | string | absolute path, mode 0600, one JSONL line per stage |
| extra_args | object | per-stage scanner flags, e.g. {"http":["-threads","20"]} |
| rps | number | shared per-host request rate; default 5 |
| burst | int | shared per-host token bucket; default 10 |

Example: {"domain":"example.com","stages":["subdomains","dns","http"],"authorized":true,"max_entries":100}

Errors: invalid_args for missing authorized, a bad domain, an unknown stage, a target outside scope, a relative output_file, or a relative or unreadable templates path; timeout when the run budget expires; unknown for a scanner failure.
Consent: authorized:true is required and is never defaulted.`

// Run implements the recon_run MCP tool: it chains the selected stages in the
// fixed dependency order and reports one record per stage.
type Run struct{}

// NewRun constructs the recon_run tool.
func NewRun() *Run {
	return &Run{}
}

// Name returns the MCP tool name "recon_run".
func (t *Run) Name() string {
	return "recon_run"
}

// Description returns the LLM-facing description of recon_run.
func (t *Run) Description() string {
	return runDescription + toolenv.ResultEnvelopeDoc
}

// InputSchema returns the JSON Schema for recon_run's arguments.
func (t *Run) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"domain", "authorized"},
		Properties: map[string]any{
			"domain": map[string]any{
				"type":        "string",
				"description": "Registrable seed domain, e.g. example.com. A URL, host:port or wildcard is refused.",
			},
			"stages": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
					"enum": []string{
						recon.StageSubdomains,
						recon.StageDNS,
						recon.StageHTTP,
						recon.StagePorts,
						recon.StageURLs,
						recon.StageJS,
						recon.StageFuzz,
						recon.StageTemplates,
					},
				},
				"description": "Subset of the eight stages. They always run in the fixed order subdomains,dns,http,ports,urls,js,fuzz,templates. Default subdomains,dns,http. Missing dependencies are added automatically and reported.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for domains you are permitted to assess; it is never defaulted.",
			},
			"templates": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Absolute nuclei template directory or file, passed as -t to the templates stage. A relative or unreadable path is refused. Accepted but ignored with a warning when the templates stage is not selected.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. A bare host covers its subdomains, a leading \"*.\" is accepted, \"*\" allows all. Empty defaults to the seed domain and its subdomains.",
			},
			"stop_on_empty": map[string]any{
				"type":        "boolean",
				"description": "When true, a stage that returns nothing ends the run and marks every remaining selected stage skipped. Default false.",
			},
			"max_hosts": map[string]any{
				"type":        "integer",
				"description": "Maximum hosts a stage input is trimmed to. Default 100.",
			},
			"max_urls": map[string]any{
				"type":        "integer",
				"description": "Maximum URLs a stage input is trimmed to. Default 500.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Total entries across all stages. Default 200; truncated and dropped report the cut per stage and for the run.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Whole-run budget in milliseconds. Default 600000, ceiling 3600000.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for a JSONL report, one line per stage, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"description":          "Per-stage scanner flags, keyed by stage id, e.g. {\"http\":[\"-threads\",\"20\"]}.",
			},
			"rps": map[string]any{
				"type":        "number",
				"description": "Shared per-host request rate for the whole run. Default 5.",
			},
			"burst": map[string]any{
				"type":        "integer",
				"description": "Per-host token bucket depth for the whole run. Default 10.",
			},
		},
	}
}

// runArgs is the strict decoding target for recon_run arguments.
type runArgs struct {
	Domain      string              `json:"domain"`
	Stages      []string            `json:"stages"`
	Templates   []string            `json:"templates"`
	Authorized  bool                `json:"authorized"`
	Scope       []string            `json:"scope"`
	StopOnEmpty bool                `json:"stop_on_empty"`
	MaxHosts    int                 `json:"max_hosts"`
	MaxURLs     int                 `json:"max_urls"`
	MaxEntries  int                 `json:"max_entries"`
	TimeoutMS   int                 `json:"timeout_ms"`
	OutputFile  string              `json:"output_file"`
	ExtraArgs   map[string][]string `json:"extra_args"`
	RPS         float64             `json:"rps"`
	Burst       int                 `json:"burst"`
}

// Execute refuses an undeclared argument, checks consent, then runs the pipeline.
func (t *Run) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a runArgs
	if err := t.rejectUndeclaredArgs(raw); err != nil {
		return toolenv.FailResult(err)
	}
	if err := toolenv.DecodeArgs(t.Name(), raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"%s: invalid arguments: authorized:true is required — the pipeline sends active traffic to hosts you must be permitted to assess; set it explicitly",
			t.Name(),
		))
	}

	result, err := recon.RunPipeline(ctx, recon.PipelineArgs{
		Domain:      a.Domain,
		Stages:      a.Stages,
		Templates:   a.Templates,
		Authorized:  a.Authorized,
		Scope:       a.Scope,
		StopOnEmpty: a.StopOnEmpty,
		MaxHosts:    a.MaxHosts,
		MaxURLs:     a.MaxURLs,
		MaxEntries:  a.MaxEntries,
		TimeoutMS:   a.TimeoutMS,
		OutputFile:  a.OutputFile,
		ExtraArgs:   a.ExtraArgs,
		RPS:         a.RPS,
		Burst:       a.Burst,
	})
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: encode result: %w", t.Name(), err))
	}
	return toolenv.OKResult(string(data))
}

// rejectUndeclaredArgs refuses the first supplied argument the schema does not
// declare and names the sorted valid ones. The sentence matches the MCP dispatch
// gate, so the same call reports the same message on MCP and on the CLI, which
// calls the tool directly and would otherwise decode an unknown key away.
func (t *Run) rejectUndeclaredArgs(raw json.RawMessage) error {
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(raw, &supplied); err != nil || len(supplied) == 0 {
		return nil
	}

	props := t.InputSchema().Properties
	valid := make([]string, 0, len(props))
	for key := range props {
		valid = append(valid, key)
	}
	sort.Strings(valid)

	keys := make([]string, 0, len(supplied))
	for key := range supplied {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if !slices.Contains(valid, key) {
			return fmt.Errorf(
				"%s: unknown argument %q — valid arguments are %s",
				t.Name(),
				key,
				strings.Join(valid, ", "),
			)
		}
	}
	return nil
}

// Ensure Run satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*Run)(nil)
