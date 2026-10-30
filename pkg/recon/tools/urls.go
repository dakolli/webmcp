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

// URLs implements the recon_urls MCP tool: it crawls seeds with katana and
// returns deduplicated in-scope URLs as compact entries.
type URLs struct{}

// NewURLs constructs the recon_urls tool.
func NewURLs() *URLs {
	return &URLs{}
}

// Name returns the MCP tool name "recon_urls".
func (t *URLs) Name() string {
	return recon.URLsID
}

// Description returns the LLM-facing description of recon_urls.
func (t *URLs) Description() string {
	return recon.URLsDoc + toolenv.ResultEnvelopeDoc
}

// InputSchema returns the JSON Schema for recon_urls' arguments.
func (t *URLs) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:     "object",
		Required: []string{"targets", "authorized"},
		Properties: map[string]any{
			"targets": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Seed URLs or hosts, at most 20. Each seed is scope-checked before katana starts.",
			},
			"depth": map[string]any{
				"type":        "integer",
				"description": "Crawl depth (-d). Default 3, ceiling 10. Every extra level multiplies requests and runtime.",
			},
			"js_crawl": map[string]any{
				"type":        "boolean",
				"description": "Parse JavaScript for endpoints (-jc). Default true.",
			},
			"form_extraction": map[string]any{
				"type":        "boolean",
				"description": "Emit form, input, textarea and select elements (-fx). Default false.",
			},
			"known_files": map[string]any{
				"type":        "string",
				"enum":        []string{"", "all", "robotstxt", "sitemapxml"},
				"description": "Crawl known files (-kf): all, robotstxt or sitemapxml. Needs depth >= 3. Empty disables it.",
			},
			"headless": map[string]any{
				"type":        "boolean",
				"description": "Run katana's headless Chromium crawler (-hl). Default false; it starts its own browser on PATH and is slower.",
			},
			"concurrency": map[string]any{
				"type":        "integer",
				"description": "Concurrent fetchers (-c). Default 10.",
			},
			"rate_limit": map[string]any{
				"type":        "integer",
				"description": "Per-host requests per second (-hrl). Default 5.",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "Maximum compact entries returned. Default 200; the crawl parses at most 5000.",
			},
			"output_file": map[string]any{
				"type":        "string",
				"description": "Absolute path for the raw JSONL report, written mode 0600. A relative path is refused.",
			},
			"extra_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "katana flags appended verbatim after the typed arguments, reaching any CLI flag.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Host allowlist. Empty defaults to the literal seed hosts. A returned URL outside scope is dropped and counted in out_of_scope.",
			},
			"authorized": map[string]any{
				"type":        "boolean",
				"description": "Required. Set true only for targets you are permitted to scan; a call without it fails as invalid_args.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Run budget in milliseconds. Default 120000, ceiling 1800000.",
			},
		},
	}
}

// urlsArgs is the strict decoding target for recon_urls arguments.
type urlsArgs struct {
	Targets        []string `json:"targets"`
	Depth          *int     `json:"depth"`
	JSCrawl        *bool    `json:"js_crawl"`
	FormExtraction bool     `json:"form_extraction"`
	KnownFiles     string   `json:"known_files"`
	Headless       bool     `json:"headless"`
	Concurrency    *int     `json:"concurrency"`
	RateLimit      *int     `json:"rate_limit"`
	MaxEntries     *int     `json:"max_entries"`
	OutputFile     string   `json:"output_file"`
	ExtraArgs      []string `json:"extra_args"`
	Scope          []string `json:"scope"`
	Authorized     bool     `json:"authorized"`
	TimeoutMS      *int     `json:"timeout_ms"`
}

// Execute validates consent and scope in pkg/recon, then runs katana.
func (t *URLs) Execute(
	ctx context.Context,
	raw json.RawMessage,
) (string, error) {
	var a urlsArgs
	if err := toolenv.DecodeArgs(recon.URLsID, raw, &a); err != nil {
		return toolenv.FailResult(err)
	}
	jsCrawl := true
	if a.JSCrawl != nil {
		jsCrawl = *a.JSCrawl
	}

	result, err := recon.RunURLs(ctx, recon.URLsArgs{
		Targets:        a.Targets,
		Depth:          urlsInt(a.Depth),
		JSCrawl:        jsCrawl,
		FormExtraction: a.FormExtraction,
		KnownFiles:     a.KnownFiles,
		Headless:       a.Headless,
		Concurrency:    urlsInt(a.Concurrency),
		RateLimit:      urlsInt(a.RateLimit),
		TimeoutMS:      urlsInt(a.TimeoutMS),
		MaxEntries:     urlsInt(a.MaxEntries),
		OutputFile:     a.OutputFile,
		ExtraArgs:      a.ExtraArgs,
		Scope:          a.Scope,
		Authorized:     a.Authorized,
	})
	if err != nil {
		return toolenv.FailResult(namedError(t.Name(), err))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: encode result: %w", recon.URLsID, err))
	}
	return toolenv.OKResult(string(data))
}

// urlsInt returns an optional integer's value, or 0 when it was absent.
func urlsInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// Ensure URLs satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*URLs)(nil)
