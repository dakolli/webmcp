package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// TestCaidoReplayRequiresRequestID pins that a replay with no request_id is
// refused as invalid_args before the client is contacted.
func TestCaidoReplayRequiresRequestID(t *testing.T) {
	out, err := NewCaidoReplay().Execute(context.Background(), json.RawMessage(
		`{"query":{}}`,
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
