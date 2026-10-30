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

// DNS implements the recon_dns tool: it resolves DNS records for a list of
// hostnames, or brute-forces names under one domain with the pinned dnsx binary.
type DNS struct{}

// NewDNS constructs the recon_dns tool.
func NewDNS() *DNS { return &DNS{} }

// Name returns the MCP tool name "recon_dns".
func (t *DNS) Name() string { return "recon_dns" }

// Description returns the LLM-facing description of recon_dns.
func (t *DNS) Description() string { return dnsDescription + toolenv.ResultEnvelopeDoc }

// dnsDescription is the LLM-facing description of recon_dns.
const dnsDescription = `Resolves DNS records for a list of hostnames, or brute-forces names under one domain, with the pinned dnsx binary.

Two modes, one tool:
- Resolve: pass targets (hostnames) to query their records; at most 500 per call.
- Brute-force: pass domain plus an absolute-path wordlist to expand names under that domain.

Arguments:
| arg | type | meaning |
| :-- | :-- | :-- |
| targets | string[] | Resolve mode: hostnames to query, max 500. Omit when brute-forcing. |
| domain | string | Brute-force mode: the parent domain to expand. Requires wordlist. |
| wordlist | string | Absolute path to a wordlist file; its presence selects brute-force mode. A relative or missing path fails. |
| record_types | string[] | a, aaaa, cname, mx, ns, txt, srv, ptr, caa. Default ["a","cname"]. |
| resolver | string[] | Resolver addresses joined into dnsx -r. Empty uses dnsx defaults. |
| threads | integer | Concurrent lookups; dnsx default 100. |
| max_entries | integer | Entry cap returned; default 200. |
| timeout_ms | integer | Run budget in milliseconds; default 120000, ceiling 1800000. |
| output_file | string | Absolute path for the raw JSONL report, written mode 0600. |
| extra_args | string[] | Extra dnsx arguments appended verbatim. |
| scope | string[] | Host allowlist. Defaults to the literal targets, or the domain in brute-force mode. A bare host covers its subdomains. |
| authorized | boolean | Required true. Active scanning consent; never defaulted. |

Resolve example: {"authorized":true,"targets":["example.com","api.example.com"],"record_types":["a","aaaa","mx"],"scope":["example.com"]}
Brute-force example: {"authorized":true,"domain":"example.com","wordlist":"/abs/path/words.txt","record_types":["a","cname"],"scope":["example.com"],"threads":50}

Output is one compact entry per host carrying only the record values that came back non-empty. A host with no record of a requested type returns count 0, not an error.

Errors: invalid_args for a missing authorized, an over-cap target list, a relative or missing wordlist, an unknown record_type, or a target outside scope; timeout when the run exceeds its budget; unknown otherwise.

Consent: authorized:true is required and never defaulted — set it only for hosts you are permitted to scan.`

// dnsRecordEnums is the accepted record-type vocabulary for the schema.
var dnsRecordEnums = []string{"a", "aaaa", "cname", "mx", "ns", "txt", "srv", "ptr", "caa"}

// InputSchema returns the JSON Schema for recon_dns's arguments.
func (t *DNS) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"authorized"},
		Properties: map[string]any{
			"targets": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Resolve mode: hostnames to query, max 500. Omit when brute-forcing with domain and wordlist.",
			},
			"domain": map[string]any{
				"type":        "string",
				"description": "Brute-force mode: the parent domain to expand. Requires wordlist.",
			},
			"wordlist": map[string]any{
				"type":        "string",
				"description": "Absolute path to a wordlist file. Its presence selects brute-force mode; a relative or missing path fails.",
			},
			"record_types": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": dnsRecordEnums},
				"description": "Record types to query. Default [\"a\",\"cname\"].",
			},
			"resolver": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Resolver addresses joined into dnsx -r. Empty uses dnsx defaults.",
			},
			"threads": map[string]any{
				"type":        "integer",
				"description": "Concurrent lookups. dnsx default 100.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum decoded entries returned. Default 200.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the raw JSONL report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Extra dnsx arguments appended verbatim after the typed arguments.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. Defaults to the literal targets, or the domain in brute-force mode. A bare host covers its subdomains; \"*\" allows all.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for hosts you are permitted to scan; a call without it fails.",
			},
		},
	}
}

// dnsArgs is the strict decoding target for recon_dns arguments.
type dnsArgs struct {
	Targets     []string `json:"targets"`
	Domain      string   `json:"domain"`
	Wordlist    string   `json:"wordlist"`
	RecordTypes []string `json:"record_types"`
	Resolver    []string `json:"resolver"`
	Threads     *int     `json:"threads"`
	MaxEntries  *int     `json:"max_entries"`
	TimeoutMS   *int     `json:"timeout_ms"`
	OutputFile  string   `json:"output_file"`
	ExtraArgs   []string `json:"extra_args"`
	Scope       []string `json:"scope"`
	Authorized  bool     `json:"authorized"`
}

// Execute checks consent and scope, then runs dnsx for one mode.
func (t *DNS) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a dnsArgs
	if err := toolenv.DecodeArgs("recon_dns", raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"recon_dns: invalid arguments: authorized:true is required — active DNS scanning touches hosts you may not own; set it only for targets you are permitted to scan",
		))
	}

	scope := a.Scope
	if len(scope) == 0 {
		scope = defaultDNSScope(a)
	}
	guard := recon.Guard{Scope: scope}
	if err := guard.CheckHosts(dnsScopeTargets(a)); err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}

	opts := recon.DNSOptions{
		Targets:     a.Targets,
		Domain:      a.Domain,
		Wordlist:    a.Wordlist,
		RecordTypes: a.RecordTypes,
		Resolvers:   a.Resolver,
		ExtraArgs:   a.ExtraArgs,
		OutputFile:  strings.TrimSpace(a.OutputFile),
	}
	if a.Threads != nil {
		opts.Threads = *a.Threads
	}
	if a.MaxEntries != nil {
		opts.MaxEntries = *a.MaxEntries
	}
	if a.TimeoutMS != nil {
		opts.TimeoutMS = *a.TimeoutMS
	}

	result, err := recon.RunDNS(ctx, opts)
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("recon_dns: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// defaultDNSScope is the implicit allowlist: the literal targets in resolve
// mode, or the parent domain in brute-force mode.
func defaultDNSScope(a dnsArgs) []string {
	if strings.TrimSpace(a.Wordlist) == "" {
		return a.Targets
	}
	if domain := strings.TrimSpace(a.Domain); domain != "" {
		return []string{domain}
	}
	return nil
}

// dnsScopeTargets is what the scope check evaluates: the targets in resolve
// mode, or the domain in brute-force mode.
func dnsScopeTargets(a dnsArgs) []string {
	if strings.TrimSpace(a.Wordlist) == "" {
		return a.Targets
	}
	return []string{a.Domain}
}

// Ensure DNS satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*DNS)(nil)
