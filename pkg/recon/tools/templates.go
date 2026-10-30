package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
)

// Templates implements the recon_templates tool: it scans targets with the
// pinned nuclei v3 binary and returns one compact finding per match.
type Templates struct{}

// NewTemplates constructs the recon_templates tool.
func NewTemplates() *Templates { return &Templates{} }

// Name returns the MCP tool name "recon_templates".
func (t *Templates) Name() string { return recon.NucleiSpec.ID }

// Description returns the LLM-facing contract of recon_templates.
func (t *Templates) Description() string {
	return recon.NucleiDoc + toolenv.ResultEnvelopeDoc
}

// InputSchema returns the typed argument schema for recon_templates.
func (t *Templates) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"targets", "authorized"},
		Properties: map[string]any{
			"targets": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "URLs or hosts to scan, maximum 200. Each is passed to nuclei as a positional argument and scope-checked by host.",
			},
			"severity": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Severities to scan, passed as -severity. Default critical, high and medium.",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Template tags to include, passed as -tags.",
			},
			"templates": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Absolute template directory or file, passed as -t. A relative path is refused. A template that exploits runs only when named here.",
			},
			"exclude_tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Template tags to exclude, passed as -etags.",
			},
			"rate_limit": map[string]any{
				"type":        "integer",
				"description": "Per-second request budget, passed as -rl. Default 20, clamped at 100.",
			},
			"concurrency": map[string]any{
				"type":        "integer",
				"description": "Template concurrency, passed as -c. Default 10.",
			},
			"headless": map[string]any{
				"type":        "boolean",
				"description": "Run headless templates, passed as -headless. Default false.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum decoded findings returned. Default 200.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the raw JSONL report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "nuclei flags appended verbatim after the targets, so any CLI flag is reachable. Interactsh callbacks stay off unless you pass their flags here.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. A bare host covers its subdomains, a leading \"*.\" is accepted, \"*\" allows all. Defaults to the literal target hosts.",
			},
			"scope_strict": map[string]any{
				"type":        "boolean",
				"description": "When true, an empty scope denies every target.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for targets you are permitted to scan; a call without it fails.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
		},
	}
}

// templatesArgs is the strict decoding target for recon_templates arguments.
type templatesArgs struct {
	Targets     []string `json:"targets"`
	Severity    []string `json:"severity"`
	Tags        []string `json:"tags"`
	Templates   []string `json:"templates"`
	ExcludeTags []string `json:"exclude_tags"`
	RateLimit   *int     `json:"rate_limit"`
	Concurrency *int     `json:"concurrency"`
	Headless    bool     `json:"headless"`
	MaxEntries  *int     `json:"max_entries"`
	OutputFile  string   `json:"output_file"`
	ExtraArgs   []string `json:"extra_args"`
	Scope       []string `json:"scope"`
	ScopeStrict bool     `json:"scope_strict"`
	Authorized  bool     `json:"authorized"`
	TimeoutMS   *int     `json:"timeout_ms"`
}

// Execute validates consent and scope, runs nuclei, and returns compact findings.
func (t *Templates) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a templatesArgs
	if err := toolenv.DecodeArgs(t.Name(), raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"%s: invalid arguments: authorized:true is required — active scanning touches hosts you may not own; set it only for targets you are permitted to scan",
			t.Name(),
		))
	}

	req := recon.NucleiRequest{
		Targets:     a.Targets,
		Severity:    a.Severity,
		Tags:        a.Tags,
		Templates:   a.Templates,
		ExcludeTags: a.ExcludeTags,
		Headless:    a.Headless,
	}
	if a.RateLimit != nil {
		req.RateLimit = *a.RateLimit
	}
	if a.Concurrency != nil {
		req.Concurrency = *a.Concurrency
	}
	if err := req.Validate(); err != nil {
		return toolenv.FailResult(err)
	}

	scope := a.Scope
	if len(scope) == 0 {
		scope = recon.HostsOfTargets(a.Targets)
	}
	guard := recon.Guard{
		Scope:       scope,
		ScopeStrict: a.ScopeStrict,
	}
	if err := guard.CheckTargets(a.Targets); err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}

	opts := recon.Options{
		OutputFile: strings.TrimSpace(a.OutputFile),
		ExtraArgs:  a.ExtraArgs,
	}
	if a.MaxEntries != nil {
		opts.MaxEntries = *a.MaxEntries
	}
	if a.TimeoutMS != nil {
		opts.TimeoutMS = *a.TimeoutMS
	}

	result, err := recon.Run(ctx, recon.NucleiSpec, req.Argv(), opts)
	if err != nil {
		if recon.TemplatesMissing(err) {
			return toolenv.FailResult(fmt.Errorf(
				"%s: invalid arguments: no nuclei templates found — pass templates with an absolute template directory or file, or run \"nuclei -update-templates\" once to download the built-in set; this tool never downloads templates itself",
				t.Name(),
			))
		}
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(recon.CompactNuclei(result))
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: %w", t.Name(), err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure Templates satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*Templates)(nil)
