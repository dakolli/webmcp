package server

import (
	"context"
	"encoding/json"
	"testing"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
	"webmcp/pkg/mcp/tools"
)

func TestProcessRequestInitialize(t *testing.T) {
	s := &Server{registry: tools.NewEmptyRegistry()}
	req := models.JSONRPCRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "initialize",
	}

	resp := s.processRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	initRes, ok := resp.Result.(models.InitializeResult)
	if !ok {
		t.Fatalf("expected InitializeResult type, got %T", resp.Result)
	}

	if initRes.Instructions == "" {
		t.Error("expected non-empty instructions in initialize result")
	}
	if initRes.Capabilities.Prompts == nil {
		t.Error("expected prompts capability map in initialize result")
	}
}

func TestProcessRequestPromptsListAndGet(t *testing.T) {
	r := tools.NewEmptyRegistry()
	r.RegisterPrompt(tools.NewFetchAndSummarizePrompt())
	r.RegisterPrompt(tools.NewFormSearchAndSubmitPrompt())

	s := &Server{registry: r}

	t.Run("prompts/list", func(t *testing.T) {
		req := models.JSONRPCRequest{
			Jsonrpc: "2.0",
			ID:      2,
			Method:  "prompts/list",
		}
		resp := s.processRequest(context.Background(), req)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %v", resp.Error)
		}

		listRes, ok := resp.Result.(models.ListPromptsResult)
		if !ok {
			t.Fatalf("expected ListPromptsResult, got %T", resp.Result)
		}

		if len(listRes.Prompts) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(listRes.Prompts))
		}
		if listRes.Prompts[0].Name != "fetch_and_summarize" {
			t.Errorf("expected first prompt fetch_and_summarize, got %s", listRes.Prompts[0].Name)
		}
		if listRes.Prompts[1].Name != "form_search_and_submit" {
			t.Errorf("expected second prompt form_search_and_submit, got %s", listRes.Prompts[1].Name)
		}
	})

	t.Run("prompts/get valid", func(t *testing.T) {
		params, _ := json.Marshal(models.GetPromptRequest{
			Name: "fetch_and_summarize",
			Arguments: map[string]string{
				"url": "https://news.google.com",
			},
		})
		req := models.JSONRPCRequest{
			Jsonrpc: "2.0",
			ID:      3,
			Method:  "prompts/get",
			Params:  params,
		}
		resp := s.processRequest(context.Background(), req)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %v", resp.Error)
		}

		getRes, ok := resp.Result.(*models.GetPromptResult)
		if !ok {
			t.Fatalf("expected *GetPromptResult, got %T", resp.Result)
		}

		if len(getRes.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(getRes.Messages))
		}
		if getRes.Messages[0].Role != "user" {
			t.Errorf("expected role 'user', got %s", getRes.Messages[0].Role)
		}
	})

	t.Run("prompts/get unknown prompt", func(t *testing.T) {
		params, _ := json.Marshal(models.GetPromptRequest{
			Name: "non_existent_prompt",
		})
		req := models.JSONRPCRequest{
			Jsonrpc: "2.0",
			ID:      4,
			Method:  "prompts/get",
			Params:  params,
		}
		resp := s.processRequest(context.Background(), req)
		if resp.Error == nil {
			t.Fatalf("expected error for non existent prompt, got nil")
		}
	})
}

type ownerCaptureTool struct {
	owner string
	label string
}

func (t *ownerCaptureTool) Name() string        { return "capture_owner" }
func (t *ownerCaptureTool) Description() string { return "test owner capture" }

func (t *ownerCaptureTool) InputSchema() models.InputSchema {
	return models.InputSchema{Type: "object", Properties: map[string]any{}}
}

func (t *ownerCaptureTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.owner = browsers.OwnerFrom(ctx)
	t.label = browsers.OwnerLabelFrom(ctx)
	return "ok", nil
}

func callCaptureTool(t *testing.T, s *Server, ctx context.Context) {
	t.Helper()
	params, _ := json.Marshal(models.CallToolRequest{
		Name:      "capture_owner",
		Arguments: json.RawMessage("{}"),
	})
	resp := s.processRequest(ctx, models.JSONRPCRequest{
		Jsonrpc: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params:  params,
	})
	if resp.Error != nil {
		t.Fatalf("tools/call error: %v", resp.Error)
	}
}

func TestProcessRequestInjectsSSEOwner(t *testing.T) {
	capture := &ownerCaptureTool{}
	r := tools.NewEmptyRegistry()
	r.Register(capture)
	s := &Server{registry: r, labels: map[string]string{}}

	const sessionID = "1700000000000000001"
	initParams, _ := json.Marshal(map[string]any{
		"clientInfo": map[string]string{"name": "Claude"},
	})
	initResp := s.processRequest(withSSESession(context.Background(), sessionID), models.JSONRPCRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  initParams,
	})
	if initResp.Error != nil {
		t.Fatalf("initialize error: %v", initResp.Error)
	}

	callCaptureTool(t, s, withSSESession(context.Background(), sessionID))

	if capture.owner != sessionID {
		t.Fatalf("owner = %q, want the SSE session id %q", capture.owner, sessionID)
	}
	if capture.label != "Claude" {
		t.Fatalf("label = %q, want the client name Claude", capture.label)
	}
}

func TestProcessRequestOwnerFallbacks(t *testing.T) {
	capture := &ownerCaptureTool{}
	r := tools.NewEmptyRegistry()
	r.Register(capture)
	s := &Server{registry: r}

	callCaptureTool(t, s, context.Background())
	if capture.owner != browsers.UnownedOwner {
		t.Fatalf("owner without a session = %q, want %q", capture.owner, browsers.UnownedOwner)
	}
	if capture.label != "" {
		t.Fatalf("label without a session = %q, want empty", capture.label)
	}

	const sessionID = "1700000000000999999"
	callCaptureTool(t, s, withSSESession(context.Background(), sessionID))
	if capture.owner != sessionID {
		t.Fatalf("owner = %q, want the SSE session id", capture.owner)
	}
	if capture.label != "client-999999" {
		t.Fatalf("label = %q, want the synthetic client-999999", capture.label)
	}
}
