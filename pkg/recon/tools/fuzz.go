package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
)

// Fuzz implements the "recon_fuzz" tool: content discovery and parameter
// fuzzing with ffuf.
type Fuzz struct{}

// NewFuzz constructs the recon_fuzz tool.
func NewFuzz() *Fuzz { return &Fuzz{} }

// Name returns the MCP tool name "recon_fuzz".
func (t *Fuzz) Name() string { return "recon_fuzz" }

// Description returns the LLM-facing description of recon_fuzz.
func (t *Fuzz) Description() string { return fuzzDescription }

// fuzzDescription is the LLM-facing description of recon_fuzz.
const fuzzDescription = "Content discovery and parameter fuzzing with ffuf. " +
	"Fuzzing is loud: ffuf sends one request per wordlist entry (often thousands) to the target, so use it only on hosts you are authorized to test. " +
	"Consent is required: authorized:true is not defaulted and a call without it fails. " +
	"scope defaults to the target host and refuses a base URL outside it; scope covers the base host only, because ffuf sends the remaining requests itself, so a redirect or vhost that leaves scope is not re-checked. " +
	"Modes: dir fuzzes a path (https://example.com/FUZZ), vhost fuzzes a host (https://FUZZ.example.com), and param fuzzes one query parameter value when params is set (https://example.com/search with params:\"q\" becomes ?q=FUZZ). " +
	"When mode is omitted it is inferred: params means param, FUZZ. in the host means vhost, otherwise dir. " +
	"\n\nArguments:\n" +
	"| arg | type | meaning |\n" +
	"| :-- | :-- | :-- |\n" +
	"| url | string, required | Target URL. dir needs FUZZ in the path or query; vhost needs FUZZ. in the host; param needs params or FUZZ. |\n" +
	"| mode | string | dir, vhost, or param. Omitted means inferred. |\n" +
	"| wordlist | string | Absolute path to the wordlist. Omitted uses the embedded ~40-entry common list, written to a 0600 temp file and removed afterwards. |\n" +
	"| params | string | Query parameter name to fuzz in param mode. |\n" +
	"| method | string | HTTP method, default GET. |\n" +
	"| headers | []string | Request headers, one -H per entry. |\n" +
	"| match_codes | string | Comma list passed to -mc. Default 200,204,301,302,307,401,403. |\n" +
	"| filter_size | string | Comma list passed to -fs. |\n" +
	"| rate | integer | Requests per second. Default 20, clamped to 100. |\n" +
	"| threads | integer | Worker count. Default 40, clamped to 200. |\n" +
	"| max_entries | integer | Entries returned. Default 200; truncated and dropped report the cut. |\n" +
	"| timeout_ms | integer | Run budget in milliseconds. Default 120000, ceiling 1800000. |\n" +
	"| output_file | string | Absolute path for ffuf's raw JSON report, written mode 0600. |\n" +
	"| extra_args | []string | Arguments appended verbatim after the typed flags. |\n" +
	"| scope | []string | Host allowlist; a bare host covers its subdomains, \"*.\" is accepted, \"*\" allows all. Defaults to the target host. |\n" +
	"| authorized | boolean, required | Set true only for targets you are permitted to scan. |\n" +
	"\nEach hit is {url, status, length, words} plus input, lines, content_type, redirect and duration_ms when ffuf reported them. The envelope reports mode, count, truncated, dropped and output_file.\n" +
	"Errors: invalid_args for a bad FUZZ placement, a relative or missing wordlist, missing authorized, or an out-of-scope host; timeout when the run exceeds timeout_ms; unknown for an ffuf failure.\n" +
	"Example: {\"url\":\"https://example.com/FUZZ\",\"authorized\":true} uses the embedded wordlist in dir mode. " +
	"Example: {\"url\":\"https://example.com/search\",\"params\":\"q\",\"authorized\":true} fuzzes the q value. " +
	"Example: {\"url\":\"https://FUZZ.example.com\",\"authorized\":true} fuzzes vhosts." +
	toolenv.ResultEnvelopeDoc

// InputSchema returns the JSON Schema for recon_fuzz's arguments.
func (t *Fuzz) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"url", "authorized"},
		Properties: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Target URL. dir needs FUZZ in the path or query (https://example.com/FUZZ); vhost needs FUZZ. in the host (https://FUZZ.example.com); param needs params or FUZZ.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"dir", "vhost", "param"},
				"description": "Fuzzing placement. Omitted means inferred: params means param, FUZZ. in the host means vhost, otherwise dir.",
			},
			"wordlist": map[string]any{
				"type":        "string",
				"description": "Absolute path to a wordlist file. Omitted uses the embedded common list, written to a 0600 temp file and removed afterwards. A relative path is refused.",
			},
			"params": map[string]any{
				"type":        "string",
				"description": "Query parameter name to fuzz in param mode; its value becomes FUZZ. Only valid in param mode.",
			},
			"method": map[string]any{
				"type":        "string",
				"description": "HTTP method. Default GET.",
			},
			"headers": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Request headers, one -H per entry.",
			},
			"match_codes": map[string]any{
				"type":        "string",
				"description": "Comma list passed to ffuf -mc. Default 200,204,301,302,307,401,403.",
			},
			"filter_size": map[string]any{
				"type":        "string",
				"description": "Comma list passed to ffuf -fs.",
			},
			"rate": map[string]any{
				"type":        "integer",
				"description": "Requests per second. Default 20, clamped to 100.",
			},
			"threads": map[string]any{
				"type":        "integer",
				"description": "Worker count. Default 40, clamped to 200.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum hits returned. Default 200; truncated is 1 when the cap cut the run and dropped is the number omitted.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for ffuf's raw JSON report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments appended verbatim after the typed flags, reaching any ffuf flag.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. A bare host covers its subdomains, a leading \"*.\" is accepted, \"*\" allows all. Empty defaults to the target host.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for targets you are permitted to scan; a call without it fails.",
			},
		},
	}
}

// fuzzArgs is the strict decoding target for recon_fuzz arguments.
type fuzzArgs struct {
	recon.FuzzArgs
	Authorized bool `json:"authorized"`
}

// Execute checks consent, then runs ffuf through the shared recon runner.
func (t *Fuzz) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a fuzzArgs
	if err := toolenv.DecodeArgs("recon_fuzz", raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"recon_fuzz: invalid arguments: authorized:true is required — fuzzing sends many requests to the host; set it only for targets you are permitted to scan",
		))
	}

	result, err := recon.RunFuzz(ctx, a.FuzzArgs)
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("recon_fuzz: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure Fuzz satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*Fuzz)(nil)
