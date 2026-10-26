package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestActValidateRequiredArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{name: "click missing selector", args: `{"action":"click"}`, wantErr: "click requires selector"},
		{name: "click with handle", args: `{"action":"click","handle":"g1-b2"}`},
		{name: "input missing selector", args: `{"action":"input"}`, wantErr: "input requires selector"},
		{name: "type missing selector", args: `{"action":"type"}`, wantErr: "type requires selector"},
		{name: "hover missing selector", args: `{"action":"hover"}`, wantErr: "hover requires selector"},
		{name: "select missing selector", args: `{"action":"select"}`, wantErr: "select requires selector"},
		{name: "upload missing selector", args: `{"action":"upload_file"}`, wantErr: "upload_file requires selector"},
		{name: "click_at missing y", args: `{"action":"click_at","x":1}`, wantErr: "click_at requires x and y"},
		{name: "hover_at missing x", args: `{"action":"hover_at","y":1}`, wantErr: "hover_at requires x and y"},
		{name: "click_at explicit zero coords", args: `{"action":"click_at","x":0,"y":0}`},
		{name: "eval missing value", args: `{"action":"eval"}`, wantErr: "eval requires value"},
		{name: "navigate missing value", args: `{"action":"navigate"}`, wantErr: "navigate requires value"},
		{name: "replay missing target", args: `{"action":"replay"}`, wantErr: "replay requires target"},
		{name: "replay with URL substring", args: `{"action":"replay","target":"/api/user"}`},
		{name: "replay with seq and overrides", args: `{"action":"replay","target":"12","method":"PUT","headers":{"X-Test":"1"},"value":"{}"}`},
		{name: "back needs nothing", args: `{"action":"back"}`},
		{name: "sinks_install needs nothing", args: `{"action":"sinks_install"}`},
		{name: "sinks_clear needs nothing", args: `{"action":"sinks_clear"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a actArgs
			if err := json.Unmarshal([]byte(tt.args), &a); err != nil {
				t.Fatalf("unmarshal args: %v", err)
			}
			var provided map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tt.args), &provided); err != nil {
				t.Fatalf("unmarshal provided: %v", err)
			}
			err := a.validate(provided)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate() = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestActSinksActions pins that both tracer actions are advertised in the
// schema enum and described on both surfaces, and that they need no argument
// beyond session_id and action.
func TestActSinksActions(t *testing.T) {
	action, ok := (&Act{}).InputSchema().Properties["action"].(map[string]any)
	if !ok {
		t.Fatal("action schema property is missing")
	}
	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action schema property has no enum")
	}
	propDesc, _ := action["description"].(string)
	toolDesc := (&Act{}).Description()

	for _, name := range []string{"sinks_install", "sinks_clear"} {
		if !slices.Contains(enum, name) {
			t.Errorf("action enum = %v, want %s included", enum, name)
		}
		if !strings.Contains(propDesc, name) {
			t.Errorf("action schema description does not name %s", name)
		}
		if !strings.Contains(toolDesc, name) {
			t.Errorf("page_act description does not name %s", name)
		}
	}

	for _, want := range []string{"never starts by itself", "what=sinks", "200-hit ring", "per document"} {
		if !strings.Contains(toolDesc, want) {
			t.Errorf("page_act description is missing %q", want)
		}
	}

	if got := actActionList(); !strings.Contains(got, "sinks_install, sinks_clear") {
		t.Errorf("actActionList() = %q, want both sinks actions", got)
	}
}
