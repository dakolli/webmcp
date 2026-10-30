package tools_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	recontools "github.com/dakolli/webmcp/pkg/recon/tools"
)

// TestRunRequiresAuthorization asserts recon_run refuses a call without the
// consent gate and classifies it as invalid_args.
func TestRunRequiresAuthorization(t *testing.T) {
	tool := recontools.NewRun()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"domain":"example.com"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var envelope toolenv.ResultEnvelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.OK {
		t.Fatal("recon_run accepted a call without authorized:true")
	}
	if envelope.Error == nil {
		t.Fatal("failure envelope has no error")
	}
	if envelope.Error.Type != "invalid_args" {
		t.Errorf("error type = %q, want invalid_args", envelope.Error.Type)
	}
	if !strings.Contains(envelope.Error.Message, "authorized") {
		t.Errorf("error %q does not name the consent gate", envelope.Error.Message)
	}
}

// TestRunContract asserts the tool name, the fixed stage order in the
// description and the required schema fields an LLM needs.
func TestRunContract(t *testing.T) {
	tool := recontools.NewRun()
	if name := tool.Name(); name != "recon_run" {
		t.Fatalf("Name() = %q, want recon_run", name)
	}

	description := tool.Description()
	order := "subdomains → dns → http → ports → urls → js → fuzz → templates"
	if !strings.Contains(description, order) {
		t.Errorf("description does not state the stage order %q", order)
	}
	for _, want := range []string{"added", "stop_on_empty", "authorized", "invalid_args"} {
		if !strings.Contains(description, want) {
			t.Errorf("description does not mention %q", want)
		}
	}

	required := tool.InputSchema().Required
	for _, field := range []string{"domain", "authorized"} {
		if !slices.Contains(required, field) {
			t.Errorf("schema required = %v, want %q", required, field)
		}
	}

	if _, ok := tool.InputSchema().Properties["templates"]; !ok {
		t.Error("schema does not declare the templates argument")
	}
}
