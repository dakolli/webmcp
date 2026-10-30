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

// caidoReplayDescription is the LLM-facing description of caido_replay.
const caidoReplayDescription = "Resends one captured request through Caido's replay lane so the new exchange " +
	"is recorded in project history. request_id is a request id from caido_search, never a URL. " +
	"Every other argument is optional: method replaces the captured method (a change to GET, HEAD or " +
	"DELETE drops the body and Content-Length), headers replaces one header value per name, query " +
	"replaces one URL parameter and removes it on an empty value, and body replaces the captured body " +
	"(an empty string sends an empty body). It sends exactly one request per call — no matrix and no retry. " +
	"One replay writes two history rows, a session row with status 0 and the exchange row. The result " +
	"carries the new request_id of the exchange row that caido_search with since:0 finds, plus entry_id " +
	"and session_id for the replay itself, and the status, ms, response_bytes and url of the recorded " +
	"response; response_bytes is the response body size, the same field caido_search reports. " +
	"A non-empty error means the exchange did not complete, and a note explains a row that never appeared. " +
	"Example: {\"request_id\":\"2085\"}. " +
	"Example: {\"request_id\":\"2085\",\"method\":\"PUT\",\"headers\":{\"X-Env\":\"staging\"}," +
	"\"query\":{\"debug\":\"1\"},\"body\":\"{\\\"id\\\":2}\"}. " +
	"If the result has no request_id, read caido_search with since:0 before you take another id; " +
	"if the call fails, confirm request_id with caido_search." + toolenv.ResultEnvelopeDoc

// CaidoReplay implements the "caido_replay" tool: it resends one captured
// request and reports the row the instance recorded for it.
type CaidoReplay struct {
	client *caido.Client
}

// NewCaidoReplay constructs the caido_replay tool for the configured endpoint.
func NewCaidoReplay() *CaidoReplay {
	return &CaidoReplay{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_replay".
func (t *CaidoReplay) Name() string {
	return "caido_replay"
}

// Description returns the LLM-facing description of caido_replay.
func (t *CaidoReplay) Description() string {
	return caidoReplayDescription
}

// InputSchema returns the JSON Schema for caido_replay's arguments.
func (t *CaidoReplay) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"request_id": map[string]any{
				"type":        "string",
				"description": "Captured request id from caido_search, required. Never a URL.",
			},
			"method": map[string]any{
				"type":        "string",
				"description": "HTTP method override for the replay (e.g. PUT, POST); omit to keep the captured method.",
			},
			"headers": map[string]any{
				"type":        "object",
				"description": "Header name to value; each named header replaces its captured value. Omitted headers keep theirs.",
			},
			"query": map[string]any{
				"type":        "object",
				"description": "URL parameter to value; a non-empty value replaces it and an empty value removes it.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Request body override; it replaces the captured body, and an empty string sends an empty body.",
			},
		},
		Required: []string{"request_id"},
	}
}

// caidoReplayArgs is the strict decoding target for caido_replay arguments.
type caidoReplayArgs struct {
	RequestID string            `json:"request_id"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Query     map[string]string `json:"query"`
	Body      *string           `json:"body"`
}

// Execute resends one captured request and returns the recorded row.
func (t *CaidoReplay) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoReplayArgs
	if err := toolenv.DecodeArgs("caido_replay", args, &a); err != nil {
		return toolenv.FailResult(err)
	}
	requestID := strings.TrimSpace(a.RequestID)
	if requestID == "" {
		return toolenv.FailResult(fmt.Errorf(
			"caido_replay: invalid arguments: request_id is required — take a request id from caido_search",
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

	result, err := t.client.Replay(ctx, caido.ReplayRequest{
		RequestID: requestID,
		Method:    a.Method,
		Headers:   a.Headers,
		Query:     a.Query,
		Body:      a.Body,
	})
	if err != nil {
		return toolenv.FailResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("caido_replay: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure CaidoReplay satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*CaidoReplay)(nil)
