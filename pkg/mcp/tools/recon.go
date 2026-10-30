package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
	recontools "github.com/dakolli/webmcp/pkg/recon/tools"
)

// reconToolSuffix documents the shared guard, cap and output contract that every
// scanner tool carries.
const reconToolSuffix = " Runs the pinned scanner from the private recon GOBIN " +
	"(<config dir>/recon/bin), bootstrapping it with go install on first use; PATH is never consulted. " +
	"Active scanning requires authorized:true — a call without it fails and names the reason. " +
	"scope is the host allowlist (same vocabulary as browser_create: a bare host covers its subdomains, " +
	"a leading \"*.\" is accepted, \"*\" allows all); a target outside it fails and the error names the target and the allowlist. " +
	"targets are passed to the scanner as positional arguments after its JSON flags; extra_args are appended " +
	"verbatim, so any CLI flag the scanner offers is reachable. " +
	"Caps are 500 hosts, 5000 URLs and 200 ports per call; an over-cap call fails naming the count — use " +
	"output_file (an absolute path, written mode 0600) for a larger report. " +
	"max_entries defaults to 200; truncated is 1 when the entry cap cut the result and dropped is the number of omitted entries."

// reconTool exposes one registered scanner spec as an MCP tool. Every surface
// behaviour lives in pkg/recon; this type validates consent, applies the guard
// and renders the envelope.
type reconTool struct {
	spec recon.Spec
}

// newReconTool wraps one scanner spec as a tool.
func newReconTool(spec recon.Spec) *reconTool {
	return &reconTool{spec: spec}
}

// ReconTools returns one MCP tool per registered scanner. A typed tool wins
// over the generic wrapper for its spec, and a typed tool without a spec is
// appended too, so no wired tool is dropped. An empty registry returns an empty
// slice, so the server starts without a recon tool.
func ReconTools() []toolenv.Tool {
	specs := recon.All()
	out := make([]toolenv.Tool, 0, len(specs))
	for _, spec := range specs {
		if typed, ok := recontools.Get(spec.ID); ok {
			out = append(out, typed)
			continue
		}
		out = append(out, newReconTool(spec))
	}
	for _, typed := range recontools.All() {
		if _, ok := recon.Get(typed.Name()); !ok {
			out = append(out, typed)
		}
	}
	return out
}

// Name returns the scanner's registered ID as the MCP tool name.
func (t *reconTool) Name() string { return t.spec.ID }

// Description returns the scanner's documentation plus the shared guard text.
func (t *reconTool) Description() string {
	return t.spec.Doc + reconToolSuffix + toolenv.ResultEnvelopeDoc
}

// InputSchema returns the shared recon argument schema.
func (t *reconTool) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"authorized"},
		Properties: map[string]any{
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for targets you are permitted to scan; a call without it fails.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. A bare host covers its subdomains, a leading \"*.\" is accepted, \"*\" allows all. An empty list allows every target unless scope_strict is true.",
			},
			"scope_strict": map[string]any{
				"type":        "boolean",
				"description": "When true, an empty scope denies every target.",
			},
			"targets": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Targets passed to the scanner as positional arguments. A URL is scope-checked by host, a bare number is treated as a port, anything else as a host.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments appended verbatim after the typed targets, reaching any scanner flag.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the raw stdout report, written mode 0600. A relative path is refused.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum decoded entries returned. Default 200.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
			"rps": map[string]any{
				"type":        "number",
				"description": "Sustained per-host request rate. Default 5.",
			},
			"burst": map[string]any{
				"type":        "integer",
				"description": "Per-host token bucket depth. Default 10.",
			},
		},
	}
}

// reconArgs is the strict decoding target for recon tool arguments.
type reconArgs struct {
	Authorized  bool     `json:"authorized"`
	Scope       []string `json:"scope"`
	ScopeStrict bool     `json:"scope_strict"`
	Targets     []string `json:"targets"`
	ExtraArgs   []string `json:"extra_args"`
	OutputFile  string   `json:"output_file"`
	MaxEntries  *int     `json:"max_entries"`
	TimeoutMS   *int     `json:"timeout_ms"`
	RPS         *float64 `json:"rps"`
	Burst       *int     `json:"burst"`
}

// Execute checks consent and scope, then runs the scanner.
func (t *reconTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a reconArgs
	if err := toolenv.DecodeArgs(t.spec.ID, raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"%s: invalid arguments: authorized:true is required — active scanning touches hosts you may not own; set it only for targets you are permitted to scan",
			t.spec.ID,
		))
	}

	guard := recon.Guard{
		Scope:       a.Scope,
		ScopeStrict: a.ScopeStrict,
	}
	if a.RPS != nil {
		guard.RPS = *a.RPS
	}
	if a.Burst != nil {
		guard.Burst = *a.Burst
	}
	if err := guard.CheckTargets(a.Targets); err != nil {
		return toolenv.FailResult(err)
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

	result, err := recon.Run(ctx, t.spec, a.Targets, opts)
	if err != nil {
		return toolenv.FailResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: %w", t.spec.ID, err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure reconTool satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*reconTool)(nil)
