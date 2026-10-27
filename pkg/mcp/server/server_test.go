package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/mcp/models"
	"github.com/dakolli/webmcp/pkg/mcp/tools"
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
	owner   string
	session string
	label   string
}

func (t *ownerCaptureTool) Name() string        { return "capture_owner" }
func (t *ownerCaptureTool) Description() string { return "test owner capture" }

func (t *ownerCaptureTool) InputSchema() models.InputSchema {
	return models.InputSchema{Type: "object", Properties: map[string]any{}}
}

func (t *ownerCaptureTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.owner = browsers.OwnerFrom(ctx)
	t.session = browsers.SessionOwnerFrom(ctx)
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

func TestContentBlocks(t *testing.T) {
	const payload = "cG5n"
	const carrier = `{"ok":true,"data":{"marks":[{"index":1}],"viewport_width":10,"viewport_height":20,"truncated":0},"image":{"type":"image","data":"` + payload + `","mimeType":"image/png"},"fallback":{"field":"screenshot"}}`

	t.Run("image capable client gets text and image", func(t *testing.T) {
		blocks := contentBlocks(carrier, true)
		if len(blocks) != 2 {
			t.Fatalf("got %d blocks, want 2", len(blocks))
		}
		if blocks[0].Type != "text" || blocks[1].Type != "image" {
			t.Fatalf("block types = %q, %q, want text, image", blocks[0].Type, blocks[1].Type)
		}
		if strings.Contains(blocks[0].Text, payload) {
			t.Error("text block repeats the image base64 payload")
		}
		if blocks[1].Data != payload || blocks[1].MimeType != "image/png" {
			t.Errorf("image block = %+v, want the PNG payload and mimeType", blocks[1])
		}
	})

	t.Run("text only client gets the data URI", func(t *testing.T) {
		blocks := contentBlocks(carrier, false)
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		want := `"screenshot":"data:image/png;base64,` + payload + `"`
		if !strings.Contains(blocks[0].Text, want) {
			t.Errorf("text block does not expose the data URI under screenshot:\n%s", blocks[0].Text)
		}
	})

	t.Run("plain envelope is unchanged", func(t *testing.T) {
		const plain = `{"ok":true,"data":{"x":1}}`
		blocks := contentBlocks(plain, true)
		if len(blocks) != 1 || blocks[0].Text != plain {
			t.Fatalf("plain result changed: %+v", blocks)
		}
	})
}

func TestImageSupport(t *testing.T) {
	cases := []struct {
		name string
		caps string
		want bool
	}{
		{"absent defaults to images", `{}`, true},
		{"top level opt-out", `{"imageContent":false}`, false},
		{"experimental opt-out", `{"experimental":{"imageContent":false}}`, false},
		{"experimental opt-in", `{"experimental":{"imageContent":true}}`, true},
		{"non boolean defaults to images", `{"imageContent":"no"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var caps map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.caps), &caps); err != nil {
				t.Fatalf("decode capabilities: %v", err)
			}
			if got := imageSupport(caps); got != tc.want {
				t.Errorf("imageSupport() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		port    int
		want    string
		wantErr bool
	}{
		{"loopback default", DefaultBind, 4099, "127.0.0.1:4099", false},
		{"wildcard opt out", "0.0.0.0", 4099, "0.0.0.0:4099", false},
		{"ipv6 loopback", "::1", 4099, "[::1]:4099", false},
		{"hostname", "localhost", 4099, "localhost:4099", false},
		{"empty host", "", 4099, "", true},
		{"url instead of host", "http://0.0.0.0", 4099, "", true},
		{"host carrying a port", "0.0.0.0:8080", 4099, "", true},
		{"port out of range", DefaultBind, 70000, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListenAddress(tc.host, tc.port)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ListenAddress(%q, %d) = %q, want an error", tc.host, tc.port, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListenAddress(%q, %d): %v", tc.host, tc.port, err)
			}
			if got != tc.want {
				t.Errorf("ListenAddress(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

// TestListenAddressErrorsNameBindFlag pins WEB-169: every bad --bind value
// produces an error that names the flag instead of the vague "bind host".
func TestListenAddressErrorsNameBindFlag(t *testing.T) {
	for _, host := range []string{"", "http://0.0.0.0", "0.0.0.0:8080"} {
		t.Run(host, func(t *testing.T) {
			_, err := ListenAddress(host, 4099)
			if err == nil {
				t.Fatalf("ListenAddress(%q) = nil error", host)
			}
			if !strings.Contains(err.Error(), "--bind") {
				t.Errorf("ListenAddress(%q) error %q does not name --bind", host, err)
			}
		})
	}
}

func TestIsLoopbackAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:4099", true},
		{"[::1]:4099", true},
		{"localhost:4099", true},
		{"0.0.0.0:4099", false},
		{"[::]:4099", false},
		{"192.168.1.10:4099", false},
		{"not-an-address", false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackAddress(tc.addr); got != tc.want {
				t.Errorf("isLoopbackAddress(%q) = %t, want %t", tc.addr, got, tc.want)
			}
		})
	}
}

// TestNoAuthWarning pins that only a non-loopback bind produces the operator
// warning, and that the warning names the address and the safe default.
func TestNoAuthWarning(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"ipv4 loopback", "127.0.0.1:4099", false},
		{"ipv6 loopback", "[::1]:4099", false},
		{"hostname loopback", "localhost:4099", false},
		{"ipv4 wildcard", "0.0.0.0:4099", true},
		{"ipv6 wildcard", "[::]:4099", true},
		{"specific interface", "192.168.1.10:4099", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NoAuthWarning(tc.addr)
			if !tc.want {
				if got != "" {
					t.Fatalf("NoAuthWarning(%q) = %q, want empty", tc.addr, got)
				}
				return
			}
			if !strings.Contains(got, "no authentication") {
				t.Errorf("NoAuthWarning(%q) = %q, missing the no-auth warning", tc.addr, got)
			}
			if !strings.Contains(got, tc.addr) {
				t.Errorf("NoAuthWarning(%q) = %q, missing the bound address", tc.addr, got)
			}
			if !strings.Contains(got, DefaultBind) {
				t.Errorf("NoAuthWarning(%q) = %q, missing the safe default %q", tc.addr, got, DefaultBind)
			}
		})
	}
}

func TestProcessRequestLabelsSSESessionWithoutOwnerToken(t *testing.T) {
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

	if capture.owner != browsers.UnownedOwner {
		t.Fatalf("owner = %q, want %q (no transport token)", capture.owner, browsers.UnownedOwner)
	}
	if capture.session != browsers.UnownedOwner {
		t.Fatalf("session owner = %q, want %q (no transport token)", capture.session, browsers.UnownedOwner)
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
	if capture.owner != browsers.UnownedOwner {
		t.Fatalf("owner = %q, want %q (no transport token)", capture.owner, browsers.UnownedOwner)
	}
	if capture.session != browsers.UnownedOwner {
		t.Fatalf("session owner = %q, want %q (no transport token)", capture.session, browsers.UnownedOwner)
	}
	if capture.label != "client-999999" {
		t.Fatalf("label = %q, want the synthetic client-999999", capture.label)
	}
}

// postMessage drives one JSON-RPC request through the real HTTP dispatch entry
// that the SSE and Streamable HTTP paths share.
func postMessage(
	t *testing.T,
	s *Server,
	target,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestHandleMessageQuerySessionReachesTool pins that the SSE endpoint query
// session id reaches the tool as a display label, with no owner token derived
// from it.
func TestHandleMessageQuerySessionReachesTool(t *testing.T) {
	capture := &ownerCaptureTool{}
	r := tools.NewEmptyRegistry()
	r.Register(capture)
	s := &Server{registry: r, labels: map[string]string{}}

	const sessionID = "1700000000000000123"
	rec := postMessage(
		t,
		s,
		"/message?session_id="+sessionID,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"capture_owner","arguments":{}}}`,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if capture.session != browsers.UnownedOwner {
		t.Fatalf("session owner = %q, want %q (no transport token)", capture.session, browsers.UnownedOwner)
	}
	if capture.label != "client-000123" {
		t.Fatalf("label = %q, want client-000123", capture.label)
	}
}
