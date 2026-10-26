package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"webmcp/pkg/mcp/models"
)

type argCaptureTool struct {
	name string
	args json.RawMessage
}

func (c *argCaptureTool) Name() string        { return c.name }
func (c *argCaptureTool) Description() string { return "" }
func (c *argCaptureTool) InputSchema() models.InputSchema {
	return models.InputSchema{}
}

func (c *argCaptureTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	c.args = args
	return "", nil
}

func TestExecuteCoercesBooleanStrings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercase true", `"true"`, "true"},
		{"mixed case with spaces", `" True "`, "true"},
		{"lowercase false", `"false"`, "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &argCaptureTool{name: "browser_create"}
			r := NewEmptyRegistry()
			r.Register(tool)

			args := json.RawMessage(`{"ignore_certs":` + tt.input + `}`)
			if _, err := r.Execute(context.Background(), "browser_create", args); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			var got map[string]json.RawMessage
			if err := json.Unmarshal(tool.args, &got); err != nil {
				t.Fatalf("tool args = %q: %v", tool.args, err)
			}
			if string(got["ignore_certs"]) != tt.want {
				t.Fatalf("ignore_certs = %s, want %s", got["ignore_certs"], tt.want)
			}
		})
	}
}

func TestExecuteExplainsStringForBool(t *testing.T) {
	r := NewEmptyRegistry()
	r.Register(NewBrowserCreate(nil))

	_, err := r.Execute(
		context.Background(),
		"browser_create",
		json.RawMessage(`{"ignore_certs":"yes"}`),
	)
	if err == nil {
		t.Fatal("Execute() = nil, want argument type error")
	}

	want := `browser_create: "ignore_certs" must be a boolean (true or false), but the request sent the string "yes".`
	if got := firstLine(err.Error()); got != want {
		t.Fatalf("Execute() first line = %q, want %q", got, want)
	}
	if !strings.Contains(err.Error(), "\nRetry with: ") {
		t.Fatalf("Execute() = %q, want a Retry with hint", err)
	}
}

func TestExecuteExplainsNumberForString(t *testing.T) {
	r := NewEmptyRegistry()
	r.Register(NewPageList(nil))

	_, err := r.Execute(
		context.Background(),
		"page_list",
		json.RawMessage(`{"browser_id":123}`),
	)
	if err == nil {
		t.Fatal("Execute() = nil, want argument type error")
	}

	want := `page_list: "browser_id" must be a string, but the request sent the number 123.`
	if got := firstLine(err.Error()); got != want {
		t.Fatalf("Execute() first line = %q, want %q", got, want)
	}
	if !strings.Contains(err.Error(), "\nRetry with: ") {
		t.Fatalf("Execute() = %q, want a Retry with hint", err)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
