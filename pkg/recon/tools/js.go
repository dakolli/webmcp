// Package tools exposes the recon scanners as MCP tools with the shared
// toolenv envelope. Each file owns one tool and adds nothing to a shared table.
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

// JS implements the "recon_js" tool: jsluice AST analysis of JavaScript for
// URLs, paths and secrets.
type JS struct{}

// NewJS constructs the recon_js tool.
func NewJS() *JS { return &JS{} }

// Name returns the MCP tool name "recon_js".
func (t *JS) Name() string { return "recon_js" }

// Description returns the LLM-facing description of recon_js.
func (t *JS) Description() string { return jsDescription }

// jsDescription is the LLM-facing description of recon_js.
const jsDescription = "recon_js parses JavaScript with jsluice's Tree-sitter AST and returns the URLs, paths " +
	"and secrets a regex misses. It analyzes a script body: pass urls (absolute http(s) script URLs, " +
	"fetched with a plain HTTP client under a 20 s budget and a 5 MB ceiling) or js_files (absolute " +
	"local .js paths) — never both and never neither. Relative paths in a fetched script resolve " +
	"against its URL; a local file has no base, so relative results stay raw and carry relative:true. " +
	"Arguments: authorized (boolean, required) is the consent gate; mode is urls (default), secrets, " +
	"or both (both runs each and merges); urls is a string array of scripts to fetch; js_files is a " +
	"string array of absolute local scripts; max_bytes is the per-file ceiling, default and hard cap " +
	"5242880; max_entries caps returned entries at 200 by default; output_file is an absolute path for " +
	"the report, written 0600; extra_args are appended verbatim after [mode, file]; scope is the host " +
	"allowlist and defaults to the literal target hosts; scope_strict makes an empty scope deny every " +
	"host; rps and burst set the per-host budget (defaults 5 and 10); timeout_ms is the run budget " +
	"(default 120000, ceiling 1800000). " +
	"urls entries carry url, method, context (the match type) and kind (absolute or relative). " +
	"secrets entries carry kind, a redacted value (value_prefix plus value_length, redacted:true), " +
	"filename, severity and context — a full secret value is never returned. " +
	"Error types: invalid_args for missing consent, both or neither input, a relative js_files path, " +
	"or an out-of-scope host; timeout when the run budget expires; unknown for a scanner failure. " +
	"Consent: set authorized:true only for hosts you are permitted to analyze. " +
	`Example urls: {"authorized":true,"mode":"urls","urls":["https://example.com/app.js"]}. ` +
	`Example secrets: {"authorized":true,"mode":"secrets","js_files":["/tmp/app.js"]}.` +
	toolenv.ResultEnvelopeDoc

// InputSchema returns the JSON Schema for recon_js's arguments.
func (t *JS) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"authorized"},
		Properties: map[string]any{
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for targets you are permitted to analyze; a call without it fails.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"urls", "secrets", "both"},
				"description": "What to extract: urls (default), secrets, or both. Both runs each and merges.",
			},
			"urls": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Absolute http(s) script URLs to fetch and analyze. Mutually exclusive with js_files. Scope is checked before any fetch and on every redirect.",
			},
			"js_files": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Absolute paths to local .js files. Mutually exclusive with urls; a relative path is refused.",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"description": "Per-file byte ceiling. Default and hard cap 5242880 (5 MB).",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum decoded entries returned. Default 200.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the normalized report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments appended verbatim after [mode, file], reaching any jsluice flag.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. A bare host covers its subdomains, a leading \"*.\" is accepted, \"*\" allows all. Defaults to the literal hosts of urls.",
			},
			"scope_strict": map[string]any{
				"type":        "boolean",
				"description": "When true, an empty scope denies every target.",
			},
			"rps": map[string]any{
				"type":        "number",
				"description": "Sustained per-host request rate. Default 5.",
			},
			"burst": map[string]any{
				"type":        "integer",
				"description": "Per-host token bucket depth. Default 10.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
		},
	}
}

// jsArgs is the strict decoding target for recon_js arguments.
type jsArgs struct {
	Authorized  bool     `json:"authorized"`
	Mode        string   `json:"mode"`
	URLs        []string `json:"urls"`
	JSFiles     []string `json:"js_files"`
	MaxBytes    *int     `json:"max_bytes"`
	MaxEntries  *int     `json:"max_entries"`
	OutputFile  string   `json:"output_file"`
	ExtraArgs   []string `json:"extra_args"`
	Scope       []string `json:"scope"`
	ScopeStrict bool     `json:"scope_strict"`
	RPS         *float64 `json:"rps"`
	Burst       *int     `json:"burst"`
	TimeoutMS   *int     `json:"timeout_ms"`
}

// Execute checks consent, then runs jsluice over the supplied JavaScript bodies.
func (t *JS) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a jsArgs
	if err := toolenv.DecodeArgs("recon_js", raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"recon_js: invalid arguments: authorized:true is required — analyzing a target you do not own is not permitted; set it only for hosts you may analyze",
		))
	}

	args := recon.JSLuiceArgs{
		Mode:        strings.TrimSpace(a.Mode),
		URLs:        a.URLs,
		JSFiles:     a.JSFiles,
		Scope:       a.Scope,
		ScopeStrict: a.ScopeStrict,
	}
	if a.MaxBytes != nil {
		args.MaxBytes = *a.MaxBytes
	}
	if a.RPS != nil {
		args.RPS = *a.RPS
	}
	if a.Burst != nil {
		args.Burst = *a.Burst
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

	result, err := recon.RunJSLuice(ctx, args, opts)
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("recon_js: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure JS satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*JS)(nil)
