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

// caidoInterceptDescription is the LLM-facing description of caido_intercept.
const caidoInterceptDescription = "DANGER: a rule created here outlives this process and keeps rewriting " +
	"response traffic from every browser that uses the Caido proxy — including browsers you do not " +
	"own — and it enables response interception that pauses matching traffic. Clear it with " +
	"action:clear as soon as you are done; caido_status reports intercept.rules so a stale rule is " +
	"visible. " +
	"action:add REQUIRES url_filter, an HTTPQL expression such as req.host.eq:\"example.com\": it is " +
	"written to each new rule's own condition AND to the response intercept filter, so only matching " +
	"traffic is paused and rewritten. A blank, \"*\", a res. expression or a malformed expression is " +
	"refused, and add refuses when intercept is already enabled with a different filter — clear that first. " +
	"action:list is read-only: it returns status (RUNNING or DISABLED), each direction with enabled " +
	"and filter, the rules (id, name, enabled, sources), the ids and the queued count. " +
	"action:add enables only the response direction with url_filter and creates one enabled rule per " +
	"requested rewrite: status sets the response code (200..599), body replaces the whole response " +
	"body, and headers replaces each named response header; at least one of the three is required. " +
	"Caido allows one section per rule, so an add with all three creates three rules and ids holds " +
	"all three; the result reports the filter, the rules and the rules armed before the write. " +
	"action:clear with an id deletes that rule; with no id it is the kill switch — it deletes only the " +
	"rules this tool created (their names carry webmcp-intercept), leaves any other rule in place, " +
	"disables every direction and clears the filters — and needs confirm:true. " +
	"A rule is project-global: clear takes no scope, and scope_id only narrows a new rule. " +
	"scope_id (\"\" for the focus scope, \"*\" for every scope) narrows a new rule to that scope's " +
	"allowlist hosts through its HTTPQL condition. " +
	"Example: {\"action\":\"add\",\"url_filter\":\"req.host.eq:\\\"example.com\\\"\"," +
	"\"status\":599,\"body\":\"wm-intercept\"}. Example: {\"action\":\"clear\",\"confirm\":true}. " +
	"If add fails, call action:list and delete the stale ids with action:clear before retrying." +
	toolenv.ResultEnvelopeDoc

// CaidoIntercept implements the "caido_intercept" tool: it lists, arms and
// clears the persistent response rewrite rules Caido applies to intercepted
// proxy traffic.
type CaidoIntercept struct {
	client *caido.Client
}

// NewCaidoIntercept constructs the caido_intercept tool for the configured
// endpoint.
func NewCaidoIntercept() *CaidoIntercept {
	return &CaidoIntercept{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_intercept".
func (t *CaidoIntercept) Name() string {
	return "caido_intercept"
}

// Description returns the LLM-facing description of caido_intercept.
func (t *CaidoIntercept) Description() string {
	return caidoInterceptDescription
}

// InputSchema returns the JSON Schema for caido_intercept's arguments.
func (t *CaidoIntercept) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "add", "clear"},
				"description": "list reports the rules and the live intercept state, add arms rewrite rules, clear deletes them. Required.",
			},
			"url_filter": map[string]any{
				"type":        "string",
				"description": "add: required HTTPQL expression that selects the traffic, e.g. req.host.eq:\"example.com\". It is written to the response intercept filter; blank, \"*\" or an expression without req. is refused, because an add must never rewrite every response.",
			},
			"status": map[string]any{
				"type":        "integer",
				"description": "add: the response status code to write, 200..599. Omit to leave the status alone.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "add: replaces the whole response body. An empty string writes an empty body. Omit to leave the body alone.",
			},
			"headers": map[string]any{
				"type":        "object",
				"description": "add: header name to value; each named response header is replaced with that value.",
			},
			"scope_id": map[string]any{
				"type":        "string",
				"description": "add and list only: scope id from caido_status. \"\" uses the focus scope and \"*\" every scope; a new rule is narrowed to a scope's allowlist hosts. A rule is project-global, so clear takes no scope.",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "clear: one rule id from action:list. Empty deletes every webmcp-intercept rule and needs confirm:true; a rule this tool did not create is left in place.",
			},
			"confirm": map[string]any{
				"type":        "boolean",
				"description": "clear: required true when id is empty, because the kill switch deletes every rule.",
			},
		},
		Required: []string{"action"},
	}
}

// caidoInterceptArgs is the strict decoding target for caido_intercept
// arguments.
type caidoInterceptArgs struct {
	Action    string            `json:"action"`
	URLFilter string            `json:"url_filter"`
	Status    int               `json:"status"`
	Body      *string           `json:"body"`
	Headers   map[string]string `json:"headers"`
	ScopeID   string            `json:"scope_id"`
	ID        string            `json:"id"`
	Confirm   bool              `json:"confirm"`
}

// Execute runs one intercept action against the configured Caido instance.
func (t *CaidoIntercept) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoInterceptArgs
	if err := toolenv.DecodeArgs("caido_intercept", args, &a); err != nil {
		return toolenv.FailResult(err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "list", "add", "clear":
	default:
		return toolenv.FailResult(fmt.Errorf(
			"caido_intercept: invalid arguments: action must be list, add, or clear",
		))
	}
	id := strings.TrimSpace(a.ID)
	scopeID := strings.TrimSpace(a.ScopeID)
	if action == "clear" && id == "" && !a.Confirm {
		return toolenv.FailResult(fmt.Errorf(
			"caido_intercept: invalid arguments: clear needs confirm:true or an id",
		))
	}
	if action == "add" {
		if err := caido.ValidateInterceptFilter(a.URLFilter); err != nil {
			return toolenv.FailResult(err)
		}
		if a.Status != 0 && (a.Status < 200 || a.Status > 599) {
			return toolenv.FailResult(fmt.Errorf(
				"caido_intercept: invalid arguments: status must be 200..599",
			))
		}
		if a.Status == 0 && a.Body == nil && len(a.Headers) == 0 {
			return toolenv.FailResult(fmt.Errorf(
				"caido_intercept: invalid arguments: add needs status, body, or headers",
			))
		}
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
		state, err := t.client.ReadIntercept(ctx, scopeID)
		if err != nil {
			return toolenv.FailResult(err)
		}
		return okPayload("caido_intercept", state)

	case "add":
		write, err := t.client.AddIntercept(ctx, caido.InterceptRequest{
			Status:    a.Status,
			Body:      a.Body,
			Headers:   a.Headers,
			ScopeID:   scopeID,
			URLFilter: a.URLFilter,
		})
		if err != nil {
			return toolenv.FailResult(err)
		}
		return okPayload("caido_intercept", write)

	default:
		write, err := t.client.ClearIntercept(ctx, id)
		if err != nil {
			return toolenv.FailResult(err)
		}
		return okPayload("caido_intercept", write)
	}
}

// okPayload marshals one intercept result into the shared success envelope.
func okPayload(tool string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("%s: %w", tool, err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure CaidoIntercept satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*CaidoIntercept)(nil)
