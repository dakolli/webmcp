package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
)

// HTTP implements the recon_http MCP tool: HTTP probing with status, title,
// technology and TLS detection through the pinned projectdiscovery httpx.
type HTTP struct{}

// NewHTTP constructs the recon_http tool.
func NewHTTP() *HTTP {
	return &HTTP{}
}

// Name returns the MCP tool name "recon_http".
func (t *HTTP) Name() string {
	return recon.HTTPID
}

// Description returns the LLM-facing description of recon_http.
func (t *HTTP) Description() string {
	return recon.HTTPDoc + toolenv.ResultEnvelopeDoc
}

// InputSchema returns the JSON Schema for recon_http's arguments.
func (t *HTTP) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"targets", "authorized"},
		Properties: map[string]any{
			"targets": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Hosts or URLs to probe. Required, at most 500.",
			},
			"ports": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Ports to try, e.g. [\"80\",\"443\",\"8080\"]. Empty uses httpx's default ports.",
			},
			"status_code": map[string]any{
				"type":        "boolean",
				"description": "Return the HTTP status of each probe.",
			},
			"title": map[string]any{
				"type":        "boolean",
				"description": "Return the page title, clipped at 200 characters.",
			},
			"tech": map[string]any{
				"type":        "boolean",
				"description": "Return detected technologies. Default true.",
			},
			"tls": map[string]any{
				"type":        "boolean",
				"description": "Return the certificate subject, issuer and expiry.",
			},
			"follow_redirects": map[string]any{
				"type":        "boolean",
				"description": "Follow HTTP redirects.",
			},
			"threads": map[string]any{
				"type":        "integer",
				"description": "Worker threads. Zero leaves httpx's default.",
			},
			"rate_limit": map[string]any{
				"type":        "integer",
				"description": "Requests per second. Zero leaves httpx's default.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. Empty defaults to the literal target hosts; a returned URL outside it is dropped and counted in out_of_scope.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for hosts you are permitted to scan; a call without it fails as invalid_args.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Decoded entries returned. Default 200.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the raw JSONL report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "httpx flags appended verbatim after the typed arguments, reaching any CLI flag.",
			},
		},
	}
}

// httpArgs is the strict decoding target for recon_http arguments.
type httpArgs struct {
	Targets         []string `json:"targets"`
	Ports           []string `json:"ports"`
	StatusCode      bool     `json:"status_code"`
	Title           bool     `json:"title"`
	Tech            *bool    `json:"tech"`
	TLS             bool     `json:"tls"`
	FollowRedirects bool     `json:"follow_redirects"`
	Threads         *int     `json:"threads"`
	RateLimit       *int     `json:"rate_limit"`
	Scope           []string `json:"scope"`
	Authorized      bool     `json:"authorized"`
	TimeoutMS       *int     `json:"timeout_ms"`
	MaxEntries      *int     `json:"max_entries"`
	OutputFile      string   `json:"output_file"`
	ExtraArgs       []string `json:"extra_args"`
}

// Execute checks consent and the count arguments, then runs httpx.
func (t *HTTP) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a httpArgs
	if err := toolenv.DecodeArgs(recon.HTTPID, raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	if !a.Authorized {
		return toolenv.FailResult(fmt.Errorf(
			"%s: invalid arguments: authorized:true is required — probing touches hosts you may not own; set it only for targets you are permitted to scan",
			recon.HTTPID,
		))
	}

	tech := true
	if a.Tech != nil {
		tech = *a.Tech
	}
	opts := recon.HTTPOptions{
		Targets:         a.Targets,
		Ports:           a.Ports,
		StatusCode:      a.StatusCode,
		Title:           a.Title,
		Tech:            tech,
		TLS:             a.TLS,
		FollowRedirects: a.FollowRedirects,
		Scope:           a.Scope,
		OutputFile:      a.OutputFile,
		ExtraArgs:       a.ExtraArgs,
	}

	threads, err := httpCount("threads", a.Threads)
	if err != nil {
		return toolenv.FailResult(err)
	}
	rateLimit, err := httpCount("rate_limit", a.RateLimit)
	if err != nil {
		return toolenv.FailResult(err)
	}
	timeoutMS, err := httpCount("timeout_ms", a.TimeoutMS)
	if err != nil {
		return toolenv.FailResult(err)
	}
	maxEntries, err := httpCount("max_entries", a.MaxEntries)
	if err != nil {
		return toolenv.FailResult(err)
	}
	opts.Threads = threads
	opts.RateLimit = rateLimit
	opts.TimeoutMS = timeoutMS
	opts.MaxEntries = maxEntries

	result, err := recon.RunHTTP(ctx, opts)
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: encode result: %w", recon.HTTPID, err))
	}
	return toolenv.OKResult(string(data))
}

// httpCount resolves an optional non-negative integer argument.
func httpCount(name string, value *int) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf(
			"%s: invalid arguments: %s must be >= 0",
			recon.HTTPID,
			name,
		)
	}
	return *value, nil
}

// Ensure HTTP satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*HTTP)(nil)
