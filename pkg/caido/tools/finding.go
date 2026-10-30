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

// caidoFindingDescription is the LLM-facing description of caido_finding.
const caidoFindingDescription = "Reads or creates Caido findings so a proven bug outlives the chat. " +
	"A finding links a title and notes to one captured request id, and the Caido UI shows it. " +
	"action:list returns the newest findings as a findings array plus count. " +
	"action:get needs id and returns one finding. " +
	"action:create needs title and request_id and returns the new finding; request_id is a " +
	"request id from caido_search, not a URL. If caido_search returns entries:[] with dropped_since>0, " +
	"send it again with since:0 before you take a request_id. notes becomes the description and is cut at 4 KB. " +
	"Bodies in a returned description are redacted; there is no raw switch. " +
	"The tool never deletes or updates a finding. " +
	"Example: {\"action\":\"list\"}. Example: {\"action\":\"get\",\"id\":\"1\"}. " +
	"Example: {\"action\":\"create\",\"title\":\"IDOR on /api/orders\",\"request_id\":\"994\"," +
	"\"notes\":\"User A reads user B's order\"}. " +
	"If create returns an error, take request_id from a caido_search row; if list is empty, " +
	"call caido_status to confirm the project." + toolenv.ResultEnvelopeDoc

// CaidoFinding implements the "caido_finding" tool: it lists, reads and creates
// findings that link a title and notes to one captured request.
type CaidoFinding struct {
	client *caido.Client
}

// NewCaidoFinding constructs the caido_finding tool for the configured endpoint.
func NewCaidoFinding() *CaidoFinding {
	return &CaidoFinding{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_finding".
func (t *CaidoFinding) Name() string {
	return "caido_finding"
}

// Description returns the LLM-facing description of caido_finding.
func (t *CaidoFinding) Description() string {
	return caidoFindingDescription
}

// InputSchema returns the JSON Schema for caido_finding's arguments.
func (t *CaidoFinding) InputSchema() models.InputSchema {
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

// caidoFindingArgs is the strict decoding target for caido_finding arguments.
type caidoFindingArgs struct {
	Action    string `json:"action"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	RequestID string `json:"request_id"`
	Notes     string `json:"notes"`
}

// Execute runs one finding action against the configured Caido instance.
func (t *CaidoFinding) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoFindingArgs
	if err := toolenv.DecodeArgs("caido_finding", args, &a); err != nil {
		return toolenv.FailResult(err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "list", "get", "create":
	default:
		return toolenv.FailResult(fmt.Errorf(
			"caido_finding: invalid arguments: action must be list, get, or create",
		))
	}
	id := strings.TrimSpace(a.ID)
	title := strings.TrimSpace(a.Title)
	requestID := strings.TrimSpace(a.RequestID)
	if action == "get" && id == "" {
		return toolenv.FailResult(fmt.Errorf(
			"caido_finding: invalid arguments: get needs id",
		))
	}
	if action == "create" && (title == "" || requestID == "") {
		return toolenv.FailResult(fmt.Errorf(
			"caido_finding: invalid arguments: create needs title and request_id — take a request id from caido_search",
		))
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

	switch action {
	case "list":
		findings, err := t.client.Findings(ctx)
		if err != nil {
			return toolenv.FailResult(err)
		}
		payload := struct {
			Findings []caido.Finding `json:"findings"`
			Count    int             `json:"count"`
		}{Findings: findings, Count: len(findings)}
		data, err := json.Marshal(payload)
		if err != nil {
			return toolenv.FailResult(fmt.Errorf("caido_finding: %w", err))
		}
		return toolenv.OKResult(string(data))

	case "get":
		finding, err := t.client.Finding(ctx, id)
		if err != nil {
			return toolenv.FailResult(err)
		}
		data, err := json.Marshal(finding)
		if err != nil {
			return toolenv.FailResult(fmt.Errorf("caido_finding: %w", err))
		}
		return toolenv.OKResult(string(data))

	default:
		finding, err := t.client.CreateFinding(ctx, title, requestID, a.Notes)
		if err != nil {
			return toolenv.FailResult(err)
		}
		data, err := json.Marshal(finding)
		if err != nil {
			return toolenv.FailResult(fmt.Errorf("caido_finding: %w", err))
		}
		return toolenv.OKResult(string(data))
	}
}

// Ensure CaidoFinding satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*CaidoFinding)(nil)
