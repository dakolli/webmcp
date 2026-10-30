package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
)

// caidoSitemapDescription is the LLM-facing description of caido_sitemap.
const caidoSitemapDescription = "Lists the endpoints the Caido sitemap recorded for one host as JSON, " +
	"one row per method and path shape, so you can pick a candidate endpoint before " +
	"probing it. Reads the proxy sitemap over GraphQL and never changes, replays or " +
	"blocks traffic. host is a bare host such as www.gnu.org, with no scheme and no " +
	"path. path keeps only endpoints whose path starts with that prefix. max_depth " +
	"bounds the tree walk: default 5, clamped 1..5. scope_id selects the scope: \"\" " +
	"uses the configured focus scope, \"*\" reads every scope, any other value is a " +
	"scope id from caido_status. Each row carries method, path, kind (api, page or " +
	"static), the status of the latest hit, mime guessed from the path extension, the " +
	"requests count, sorted query params (at most 10) and shape. Rows collapse by " +
	"method and shape, where numeric and UUID segments become {id}, so /user/1 and " +
	"/user/2 are one row; requests is summed and the latest status is kept. Rows sort " +
	"by requests descending then path. The result caps at 200 rows and reports " +
	"truncated; scanned is the tree nodes walked and dropped is the rows collapsed by " +
	"shape. An empty result returns rows:[] with note naming the host, not an error. " +
	"When the walk stops at the depth cap, note reports the unvisited child count, so " +
	"a shallow read is never mistaken for the whole host. " +
	"Example: {\"host\":\"www.gnu.org\"} returns every endpoint shape on that host. " +
	"If it returns nothing, call caido_status to confirm the project and scope, " +
	"because a host outside the focus scope records no sitemap entry." + toolenv.ResultEnvelopeDoc

// CaidoSitemap implements the "caido_sitemap" tool: it reads a host's Caido
// sitemap tree and returns the collapsed endpoint list.
type CaidoSitemap struct {
	client *caido.Client
}

// NewCaidoSitemap constructs the caido_sitemap tool for the configured endpoint.
func NewCaidoSitemap() *CaidoSitemap {
	return &CaidoSitemap{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_sitemap".
func (t *CaidoSitemap) Name() string {
	return "caido_sitemap"
}

// Description returns the LLM-facing description of caido_sitemap.
func (t *CaidoSitemap) Description() string {
	return caidoSitemapDescription
}

// InputSchema returns the JSON Schema for caido_sitemap's arguments.
func (t *CaidoSitemap) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"host": map[string]any{
				"type":        "string",
				"description": "Bare host, no scheme and no path, e.g. www.gnu.org. Required.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Keep only endpoints whose path starts with this prefix. Empty keeps every path.",
			},
			"max_depth": map[string]any{
				"type":        "integer",
				"description": "Sitemap tree depth to walk. Default 5, clamped 1..5.",
			},
			"scope_id": map[string]any{
				"type":        "string",
				"description": "Scope to read: \"\" uses the configured focus scope, \"*\" reads every scope, any other value is a scope id from caido_status.",
			},
		},
		Required: []string{"host"},
	}
}

// caidoSitemapArgs is the strict decoding target for caido_sitemap arguments.
type caidoSitemapArgs struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	MaxDepth *int   `json:"max_depth"`
	ScopeID  string `json:"scope_id"`
}

// Execute reads one host's endpoint tree from the configured Caido instance.
func (t *CaidoSitemap) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoSitemapArgs
	if err := toolenv.DecodeArgs("caido_sitemap", args, &a); err != nil {
		return toolenv.FailResult(err)
	}

	host := strings.TrimSpace(a.Host)
	if host == "" {
		return toolenv.FailResult(fmt.Errorf("caido_sitemap: invalid arguments: host is required"))
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") {
		return toolenv.FailResult(fmt.Errorf(
			"caido_sitemap: invalid arguments: host must be a bare host such as www.gnu.org — no scheme and no path",
		))
	}

	depth := 5
	if a.MaxDepth != nil {
		depth = *a.MaxDepth
	}
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}

	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return toolenv.FailResult(err)
	}

	scopeID := strings.TrimSpace(a.ScopeID)
	scopeName := ""
	allScopes := scopeID == "*"
	if !allScopes {
		if scopeID == "" {
			_, scopeName = caido.Selection()
		}
		if scopeID != "" || scopeName != "" {
			scope, err := t.client.ResolveScope(ctx, scopeID, scopeName)
			if err != nil {
				return toolenv.FailResult(err)
			}
			scopeID, scopeName = scope.ID, scope.Name
		}
	}

	result, err := t.client.Sitemap(ctx, caido.SitemapQuery{
		Host:    host,
		Path:    strings.TrimSpace(a.Path),
		Depth:   depth,
		ScopeID: scopeID,
	})
	if err != nil {
		return toolenv.FailResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("caido_sitemap: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure CaidoSitemap satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*CaidoSitemap)(nil)
