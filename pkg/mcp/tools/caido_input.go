package tools

import "github.com/dakolli/webmcp/pkg/mcp/models"

// newCaidoSearchInputSchema returns the input schema of the caido_search tool.
func newCaidoSearchInputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"httpql": map[string]any{
				"type":        "string",
				"description": "Raw HTTPQL expression sent to the instance unchanged, e.g. req.host.eq:\"www.gnu.org\". When non-empty the typed keys are ignored. An instance error returns as an error.",
			},
			"scope_id": map[string]any{
				"type":        "string",
				"description": "Scope to search: \"\" uses the configured focus scope, \"*\" searches every scope, any other value is a scope id from caido_status.",
			},
			"host": map[string]any{
				"type":        "string",
				"description": "Typed host filter, compiled to req.host.eq:\"H\". Empty means every host.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "Substring matched case-insensitively against the composed URL, applied over the rows the instance returns. Empty means every URL.",
			},
			"method": map[string]any{
				"type":        "string",
				"enum":        []string{"GET", "POST", ""},
				"description": "HTTP method filter: GET, POST, or empty for all methods.",
			},
			"status": map[string]any{
				"type":        "integer",
				"description": "Exact status code, compiled to resp.code.eq:N. 0 for all codes.",
			},
			"status_gte": map[string]any{
				"type":        "integer",
				"description": "Minimum status code, compiled to resp.code.gte:N. 0 disables it.",
			},
			"path_contains": map[string]any{
				"type":        "string",
				"description": "Path substring, compiled to req.path.cont:\"P\". Empty disables it.",
			},
			"since": map[string]any{
				"type":        "integer",
				"description": "Lookback window in seconds applied to the returned rows. Default 900. 0 disables the time filter. total ignores it, so count 0 beside a non-zero total means pass since:0 to read the older rows.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Rows returned. Default 20, clamps at 100.",
			},
			"body_bytes": map[string]any{
				"type":        "integer",
				"description": "Cap per body in bytes. Default 2000, clamps at 65536. 0 returns metadata and headers with empty bodies.",
			},
			"part": map[string]any{
				"type":        "string",
				"enum":        []string{"request", "response", "both"},
				"description": "Which sides to return bodies for. Default both.",
			},
			"raw": map[string]any{
				"type":        "boolean",
				"description": "Disable credential redaction. Default false. The body_bytes cut still applies.",
			},
		},
	}
}

// newCaidoStatusInputSchema returns the input schema of the caido_status tool.
func newCaidoStatusInputSchema() models.InputSchema {
	return models.InputSchema{
		Type:       "object",
		Properties: map[string]any{},
	}
}

// newCaidoSitemapInputSchema returns the input schema of the caido_sitemap tool.
func newCaidoSitemapInputSchema() models.InputSchema {
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

// newCaidoFindingInputSchema returns the input schema of the caido_finding tool.
func newCaidoFindingInputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "get", "create"},
				"description": "list returns every finding with a count, get reads one by id, create links a title and notes to a request_id. Required.",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Finding id, required by get. Ignored by list and create.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Finding title, required by create.",
			},
			"request_id": map[string]any{
				"type":        "string",
				"description": "Captured request id from caido_search, required by create. A finding with no linked request is useless to the evidence lane.",
			},
			"notes": map[string]any{
				"type":        "string",
				"description": "Finding description used by create, cut at 4096 bytes.",
			},
		},
		Required: []string{"action"},
	}
}
