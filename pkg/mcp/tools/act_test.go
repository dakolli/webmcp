package tools

import (
	"encoding/json"
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
		{name: "back needs nothing", args: `{"action":"back"}`},
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
