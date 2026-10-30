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

// portsDescription is the LLM-facing description of recon_ports. It carries the
// argument table, one example, the error branch, the consent rule and the SYN
// limit so the tool is callable without the skill.
const portsDescription = `Port-scans hosts with naabu and returns one compact entry per open port (host, ip, port).

Runs the pinned naabu from the private recon GOBIN (<config dir>/recon/bin), installing it with go install on first use; PATH is never consulted.

Arguments:
| arg | type | meaning |
| --- | --- | --- |
| targets | []string, required | hosts, CIDRs or ASNs; 500 expanded hosts per call, a CIDR wider than /24 is refused |
| ports | string | comma-separated ports and ranges, e.g. 80,443,8000-8100; max 200 ports; default top-100 |
| mode | string | connect (default), udp, or syn (unavailable on this build) |
| exclude_ports | string | ports or ranges to skip, passed as -exclude-ports |
| exclude_cdn | bool | skip hosts behind a CDN |
| passive | bool | shodan-backed lookup that sends no packets |
| rate | int | packets per second, naabu -rate |
| threads | int | worker concurrency, naabu -c |
| scope | []string | host allowlist; defaults to the literal target hosts; a target outside it fails |
| authorized | bool, required | true only for hosts you are permitted to scan |
| max_entries | int | entries returned, default 200 |
| timeout_ms | int | run budget in milliseconds, default 120000, ceiling 1800000 |
| output_file | string | absolute path for the raw JSONL report, written mode 0600 |
| extra_args | []string | appended verbatim, reaching any naabu flag |

Example: {"targets":["scanme.nmap.org"],"authorized":true}

Errors: invalid_args (bad arguments, an over-cap call, an out-of-scope target, or mode syn), timeout (the run budget expired), unknown (the scanner binary failed).
Consent: authorized:true is required and never defaulted; pass it only for hosts you may scan.
SYN: mode syn is unavailable because naabu needs a libpcap-capable build and this binary is installed with CGO_ENABLED=0; use connect (default) or udp.` + toolenv.ResultEnvelopeDoc

// Ports implements the "recon_ports" tool: a naabu port scan with CONNECT by
// default, an explicit port cap and a consent gate.
type Ports struct{}

// NewPorts constructs the recon_ports tool.
func NewPorts() *Ports {
	return &Ports{}
}

// Name returns the MCP tool name "recon_ports".
func (t *Ports) Name() string {
	return "recon_ports"
}

// Description returns the LLM-facing description of recon_ports.
func (t *Ports) Description() string {
	return portsDescription
}

// InputSchema returns the JSON Schema for recon_ports arguments.
func (t *Ports) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"targets", "authorized"},
		Properties: map[string]any{
			"targets": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Hosts, CIDRs or ASNs to scan. Required. 500 expanded hosts per call; a CIDR wider than /24 is refused before any packet.",
			},
			"ports": map[string]any{
				"type":        "string",
				"description": "Comma-separated ports and ranges, e.g. 80,443,8000-8100. Max 200 ports. Empty selects the top 100.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"connect", "udp", "syn"},
				"description": "Scan type. Default connect, which works on this CGO-disabled build; udp also works; syn fails with the libpcap explanation.",
			},
			"exclude_ports": map[string]any{
				"type":        "string",
				"description": "Ports or ranges to skip, e.g. 80,443.",
			},
			"exclude_cdn": map[string]any{
				"type":        "boolean",
				"description": "Skip hosts that resolve to a CDN. Default false.",
			},
			"passive": map[string]any{
				"type":        "boolean",
				"description": "Shodan-backed passive lookup that sends no packets. Default false.",
			},
			"rate": map[string]any{
				"type":        "integer",
				"description": "Packets per second, passed as -rate. 0 leaves naabu's default.",
			},
			"threads": map[string]any{
				"type":        "integer",
				"description": "Worker concurrency, passed as -c. 0 leaves naabu's default.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. A bare host covers its subdomains, a leading \"*.\" is accepted, \"*\" allows all. Empty defaults to the literal target hosts.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for hosts you are permitted to scan; it is never defaulted.",
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
				"description": "Absolute path for the raw stdout report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments appended verbatim after the typed flags, reaching any naabu flag.",
			},
		},
	}
}

// portsArgs is the strict decoding target for recon_ports arguments.
type portsArgs struct {
	Targets      []string `json:"targets"`
	Ports        string   `json:"ports"`
	Mode         string   `json:"mode"`
	ExcludePorts string   `json:"exclude_ports"`
	ExcludeCDN   bool     `json:"exclude_cdn"`
	Passive      bool     `json:"passive"`
	Rate         *int     `json:"rate"`
	Threads      *int     `json:"threads"`
	Scope        []string `json:"scope"`
	Authorized   bool     `json:"authorized"`
	MaxEntries   *int     `json:"max_entries"`
	TimeoutMS    *int     `json:"timeout_ms"`
	OutputFile   string   `json:"output_file"`
	ExtraArgs    []string `json:"extra_args"`
}

// Execute checks consent and scope, validates the naabu scan, then runs it.
func (t *Ports) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a portsArgs
	if err := toolenv.DecodeArgs("recon_ports", raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"recon_ports: invalid arguments: authorized:true is required — port scanning touches hosts you may not own; set it only for hosts you are permitted to scan",
		))
	}

	scan := recon.PortsScan{
		Targets:      a.Targets,
		Ports:        a.Ports,
		Mode:         a.Mode,
		ExcludePorts: a.ExcludePorts,
		ExcludeCDN:   a.ExcludeCDN,
		Passive:      a.Passive,
	}
	if a.Rate != nil {
		scan.Rate = *a.Rate
	}
	if a.Threads != nil {
		scan.Threads = *a.Threads
	}
	args, err := scan.Args()
	if err != nil {
		return toolenv.FailResult(err)
	}

	scope := a.Scope
	if len(scope) == 0 {
		scope = a.Targets
	}
	guard := recon.Guard{Scope: scope}
	if err := guard.CheckHosts(a.Targets); err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}

	spec, ok := recon.Get("recon_ports")
	if !ok {
		return toolenv.FailResult(fmt.Errorf("recon_ports: the naabu scanner is not registered"))
	}
	opts := recon.Options{
		OutputFile: strings.TrimSpace(a.OutputFile),
		DedupeKey:  recon.PortEntryKey,
		ExtraArgs:  a.ExtraArgs,
	}
	if a.MaxEntries != nil {
		opts.MaxEntries = *a.MaxEntries
	}
	if a.TimeoutMS != nil {
		opts.TimeoutMS = *a.TimeoutMS
	}

	result, err := recon.Run(ctx, spec, args, opts)
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	result.Entries = compactPorts(result.Entries)
	result.Count = len(result.Entries)
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("recon_ports: encode result: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// compactPorts keeps only host, ip and port from each naabu entry. An entry
// with none of them (a raw line) is preserved so a decode failure stays visible.
func compactPorts(entries []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		compact := make(map[string]any, 3)
		for _, key := range []string{"host", "ip", "port"} {
			if value, ok := entry[key]; ok {
				compact[key] = value
			}
		}
		if len(compact) == 0 {
			out = append(out, entry)
			continue
		}
		out = append(out, compact)
	}
	return out
}

// Ensure Ports satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*Ports)(nil)
