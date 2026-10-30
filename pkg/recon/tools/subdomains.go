// Package tools implements the recon lane's MCP tools, one file per scanner.
// Each tool translates MCP arguments into a typed recon request and returns the
// shared toolenv envelope; the scanner machinery stays in pkg/recon.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
)

// subdomainsDescription is the LLM-facing description of recon_subdomains.
const subdomainsDescription = `Passive subdomain enumeration with subfinder. It queries public passive sources, so the target never sees a request, and it needs one registrable domain (for example example.com).

Arguments:
| arg | type | meaning |
| :-- | :-- | :-- |
| target | string, required | Registrable domain to enumerate, e.g. example.com. A URL, host:port or IP is refused. |
| all_sources | boolean | Use every passive source (-all). Default true. |
| recursive | boolean | Also use recursive-capable sources (-recursive). Default false. |
| sources | string[] | Restrict enumeration to these sources (-sources, comma joined). Empty uses subfinder's defaults, or every source when all_sources is true. |
| scope | string[] | Host allowlist. Defaults to the target host, so only its subdomains are kept. A returned host outside scope is dropped and counted in out_of_scope. |
| authorized | boolean, required | Consent gate; must be true. |
| timeout_ms | integer | Run budget in milliseconds. Default 120000, ceiling 600000. |
| max_entries | integer | Maximum hosts returned. Default 500. |
| output_file | string | Absolute path for the raw JSONL report, written mode 0600. |
| extra_args | string[] | subfinder flags appended verbatim after the typed arguments. |

Example: {"target":"example.com","all_sources":true,"authorized":true}

Output is one compact entry per host with the sources that reported it. Entries are capped at max_entries; truncated is 1 when the cap cut the result and dropped is the number of hosts omitted. A run that finds nothing, or has no usable sources, returns count 0, not an error.

Errors: invalid_args names the missing or invalid argument (target, authorized, a relative output_file, a target outside scope, or an over-ceiling timeout_ms); timeout means the run budget expired; unknown otherwise.

Consent: passive, but authorized:true is required and never defaulted — set it only for domains you are permitted to enumerate.`

// Subdomains implements the recon_subdomains MCP tool: passive enumeration with
// subfinder, one compact entry per host plus the sources that reported it.
type Subdomains struct{}

// NewSubdomains constructs the recon_subdomains tool.
func NewSubdomains() *Subdomains {
	return &Subdomains{}
}

// Name returns the MCP tool name "recon_subdomains".
func (t *Subdomains) Name() string {
	return recon.SubdomainsID
}

// Description returns the LLM-facing description of recon_subdomains.
func (t *Subdomains) Description() string {
	return subdomainsDescription + toolenv.ResultEnvelopeDoc
}

// InputSchema returns the JSON Schema for recon_subdomains' arguments.
func (t *Subdomains) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"target", "authorized"},
		Properties: map[string]any{
			"target": map[string]any{
				"type":        "string",
				"description": "Registrable domain to enumerate, e.g. example.com. A URL, host:port or IP is refused.",
			},
			"all_sources": map[string]any{
				"type":        "boolean",
				"description": "Use every passive source (-all). Default true.",
			},
			"recursive": map[string]any{
				"type":        "boolean",
				"description": "Also use recursive-capable sources (-recursive). Default false.",
			},
			"sources": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Restrict enumeration to these passive sources (-sources, comma joined). Empty uses subfinder's defaults, or every source when all_sources is true.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. Empty defaults to the target host, so only its subdomains are kept. A returned host outside scope is dropped and counted in out_of_scope.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for domains you are permitted to enumerate; a call without it fails as invalid_args.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 600000.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum hosts returned. Default 500.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the raw JSONL report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "subfinder flags appended verbatim after the typed arguments, reaching any CLI flag.",
			},
		},
	}
}

// subdomainsArgs is the strict decoding target for recon_subdomains arguments.
type subdomainsArgs struct {
	Target     string   `json:"target"`
	AllSources *bool    `json:"all_sources"`
	Recursive  bool     `json:"recursive"`
	Sources    []string `json:"sources"`
	Scope      []string `json:"scope"`
	Authorized bool     `json:"authorized"`
	TimeoutMS  *int     `json:"timeout_ms"`
	MaxEntries *int     `json:"max_entries"`
	OutputFile string   `json:"output_file"`
	ExtraArgs  []string `json:"extra_args"`
}

// Execute validates the arguments through the recon guard and runs subfinder.
func (t *Subdomains) Execute(
	ctx context.Context,
	raw json.RawMessage,
) (string, error) {
	var a subdomainsArgs
	if err := toolenv.DecodeArgs(recon.SubdomainsID, raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	timeoutMS, err := subdomainCount("timeout_ms", a.TimeoutMS)
	if err != nil {
		return toolenv.FailResult(err)
	}
	maxEntries, err := subdomainCount("max_entries", a.MaxEntries)
	if err != nil {
		return toolenv.FailResult(err)
	}
	allSources := true
	if a.AllSources != nil {
		allSources = *a.AllSources
	}

	result, err := recon.RunSubdomains(ctx, recon.SubdomainsArgs{
		Target:     a.Target,
		AllSources: allSources,
		Recursive:  a.Recursive,
		Sources:    a.Sources,
		TimeoutMS:  timeoutMS,
		MaxEntries: maxEntries,
		OutputFile: a.OutputFile,
		ExtraArgs:  a.ExtraArgs,
		Scope:      a.Scope,
		Authorized: a.Authorized,
	})
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: encode result: %w", recon.SubdomainsID, err))
	}
	return toolenv.OKResult(string(data))
}

// subdomainCount resolves a non-negative optional integer argument.
func subdomainCount(name string, value *int) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf(
			"%s: invalid arguments: %s must be >= 0",
			recon.SubdomainsID,
			name,
		)
	}
	return *value, nil
}

// Ensure Subdomains satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*Subdomains)(nil)
