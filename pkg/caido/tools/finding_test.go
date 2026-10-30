package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// envelopeProbe is the subset of the result envelope these tests assert on.
type envelopeProbe struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// TestCaidoFindingCreateRequiresRequestID pins that a create missing request_id
// is refused as invalid_args before the client is contacted.
func TestCaidoFindingCreateRequiresRequestID(t *testing.T) {
	out, err := NewCaidoFinding().Execute(context.Background(), json.RawMessage(
		`{"action":"create","title":"x"}`,
	))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var env envelopeProbe
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", out, err)
	}
	if env.OK || env.Error == nil {
		t.Fatalf("envelope = %s, want a failure branch", out)
	}
	if env.Error.Type != "invalid_args" {
		t.Fatalf("error type = %q, want %q", env.Error.Type, "invalid_args")
	}
}
