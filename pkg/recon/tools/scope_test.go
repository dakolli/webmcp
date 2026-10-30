package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	recontools "github.com/dakolli/webmcp/pkg/recon/tools"
)

// TestScopeRefusalNamesCallingTool asserts a scope refusal starts with the tool
// that made the call on the tool surface itself, so MCP and the CLI agree.
func TestScopeRefusalNamesCallingTool(t *testing.T) {
	cases := []struct {
		name string
		tool toolenv.Tool
		raw  string
		want string
	}{
		{
			name: "scanner",
			tool: recontools.NewSubdomains(),
			raw:  `{"target":"example.com","scope":["scanme.sh"],"authorized":true}`,
			want: "recon_subdomains: invalid arguments:",
		},
		{
			name: "pipeline",
			tool: recontools.NewRun(),
			raw:  `{"domain":"example.com","scope":["scanme.sh"],"authorized":true}`,
			want: "recon_run: invalid arguments:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := tc.tool.Execute(context.Background(), json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var env toolenv.ResultEnvelope
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.OK || env.Error == nil {
				t.Fatalf("envelope = %s, want a scope failure", out)
			}
			if !strings.HasPrefix(env.Error.Message, tc.want) {
				t.Errorf("message = %q, want prefix %q", env.Error.Message, tc.want)
			}
			if strings.Contains(env.Error.Message, "recon: invalid arguments") {
				t.Errorf("message = %q, still carries the generic recon prefix", env.Error.Message)
			}
			if !strings.Contains(env.Error.Message, "add it to scope") {
				t.Errorf("message = %q, dropped the add it to scope hint", env.Error.Message)
			}
		})
	}
}
