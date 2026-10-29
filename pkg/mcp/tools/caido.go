package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// CaidoSearch implements the "caido_search" tool: it reads recorded HTTP
// history from a Caido instance and returns compact transactions.
type CaidoSearch struct {
	client *caido.Client
}

// NewCaidoSearch constructs the caido_search tool for the configured endpoint.
func NewCaidoSearch() *CaidoSearch {
	return &CaidoSearch{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_search".
func (t *CaidoSearch) Name() string {
	return "caido_search"
}

// Description returns the LLM-facing description of caido_search.
func (t *CaidoSearch) Description() string {
	return models.CaidoSearchDescription
}

// InputSchema returns the JSON Schema for caido_search's arguments.
func (t *CaidoSearch) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Substring matched case-insensitively against the composed URL, applied over the newest 500 rows. Empty means every URL.",
			},
			"method": map[string]any{
				"type":        "string",
				"enum":        []string{"GET", "POST", ""},
				"description": "HTTP method filter: GET, POST, or empty for all methods.",
			},
			"status": map[string]any{
				"type":        "integer",
				"description": "Exact response status code, or 0 for all codes.",
			},
			"since": map[string]any{
				"type":        "integer",
				"description": "Lookback window in seconds. Default 900. 0 disables the time filter and still scans only the newest 500 rows.",
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

// caidoSearchArgs is the strict decoding target for caido_search arguments.
type caidoSearchArgs struct {
	URL       string `json:"url"`
	Method    string `json:"method"`
	Status    *int   `json:"status"`
	Since     *int   `json:"since"`
	Limit     *int   `json:"limit"`
	BodyBytes *int   `json:"body_bytes"`
	Part      string `json:"part"`
	Raw       bool   `json:"raw"`
}

// Execute runs one history search against the configured Caido instance.
func (t *CaidoSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoSearchArgs
	if err := decodeArgs("caido_search", args, &a); err != nil {
		return failResult(err)
	}

	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method != "" && method != "GET" && method != "POST" {
		return failResult(fmt.Errorf("caido_search: invalid arguments: method must be GET, POST, or empty"))
	}

	status := 0
	if a.Status != nil {
		status = *a.Status
		if status < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: status must be >= 0"))
		}
	}

	since := 900
	if a.Since != nil {
		since = *a.Since
		if since < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: since must be >= 0"))
		}
	}

	limit := 20
	if a.Limit != nil {
		limit = *a.Limit
		if limit < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: limit must be >= 0"))
		}
	}
	if limit == 0 {
		limit = 20
	}

	bodyBytes := 2000
	if a.BodyBytes != nil {
		bodyBytes = *a.BodyBytes
		if bodyBytes < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: body_bytes must be >= 0"))
		}
	}

	part := strings.ToLower(strings.TrimSpace(a.Part))
	switch part {
	case "", "both":
		part = "both"
	case "request", "response":
	default:
		return failResult(fmt.Errorf("caido_search: invalid arguments: part must be request, response, or both"))
	}

	if err := t.client.Login(ctx, config.ActiveCaido().PAT); err != nil {
		return failResult(err)
	}
	result, err := t.client.Search(ctx, caido.Query{
		URL:       strings.TrimSpace(a.URL),
		Method:    method,
		Status:    status,
		Limit:     limit,
		BodyBytes: bodyBytes,
		Since:     time.Duration(since) * time.Second,
		Part:      part,
		Raw:       a.Raw,
	})
	if err != nil {
		return failResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return failResult(fmt.Errorf("caido_search: %w", err))
	}
	return okResult(string(data))
}

// Ensure CaidoSearch satisfies the Tool interface at compile time.
var _ Tool = (*CaidoSearch)(nil)
