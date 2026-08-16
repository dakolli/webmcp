package server

import (
	"context"
	"encoding/json"
	"testing"

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
