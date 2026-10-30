package tools

import (
	"context"
	"encoding/json"
	"testing"
)

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
	if env.Error.Type != errInvalidArgs {
		t.Fatalf("error type = %q, want %q", env.Error.Type, errInvalidArgs)
	}
}
