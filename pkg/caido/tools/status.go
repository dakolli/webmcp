package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
)

// caidoStatusDescription is the LLM-facing description of caido_status.
const caidoStatusDescription = "Reports which Caido project, scopes, capture mode and history size " +
	"caido_search reads. Call this FIRST when caido_search returns nothing: an " +
	"empty result usually means the wrong project, a scope that does not match, " +
	"or a capture filter, not an empty history. Read scopes[].matches before you " +
	"trust a row set: an empty allowlist renders as allowlist: [] with " +
	"matches \"all traffic\", and that scope records every host, while " +
	"matches \"allowlist\" records only its allowlist. project.selected is the " +
	"instance's project and project.configured is the [caido] project in " +
	"webmcp.toml, so a mismatch is visible. capture is the passthroughOptions " +
	"object, or {\"unfiltered\": true} when Caido filters nothing. Takes no " +
	"arguments. Example: {} returns " +
	`{"ok":true,"data":{"auth":"guest","url":"http://127.0.0.1:8080/graphql",` +
	`"project":{"id":"063ae3b3-...","name":"webmcp","path":"/tmp/webmcp",` +
	`"status":"READY","temporary":false,"read_only":false,` +
	`"selected":"webmcp","configured":"webmcp"},` +
	`"scopes":[{"id":"1","name":"target","allowlist":[],"denylist":[],` +
	`"matches":"all traffic","focus":true}],` +
	`"capture":{"unfiltered":true},"history":1879}}` + toolenv.ResultEnvelopeDoc

// CaidoStatus implements the "caido_status" tool: it reports the project,
// scopes and capture settings that shape what caido_search can see.
type CaidoStatus struct {
	client *caido.Client
}

// NewCaidoStatus constructs the caido_status tool for the configured endpoint.
func NewCaidoStatus() *CaidoStatus {
	return &CaidoStatus{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_status".
func (t *CaidoStatus) Name() string {
	return "caido_status"
}

// Description returns the LLM-facing description of caido_status.
func (t *CaidoStatus) Description() string {
	return caidoStatusDescription
}

// InputSchema returns the empty JSON Schema for caido_status's arguments.
func (t *CaidoStatus) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:       "object",
		Properties: map[string]any{},
	}
}

// Execute reads the configured instance's status.
func (t *CaidoStatus) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return toolenv.FailResult(err)
	}
	project, scope := caido.Selection()
	status, err := t.client.Status(ctx, project, scope)
	if err != nil {
		return toolenv.FailResult(err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("caido_status: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure CaidoStatus satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*CaidoStatus)(nil)
