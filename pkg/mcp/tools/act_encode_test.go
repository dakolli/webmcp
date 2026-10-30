package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/cli/pageops"
)

// TestActEncodeNeedsNoPage proves encode resolves before the session lookup and
// that an unknown context classifies as invalid_args listing every context.
func TestActEncodeNeedsNoPage(t *testing.T) {
	t.Parallel()
	tool := NewAct(nil)

	out, err := tool.Execute(context.Background(), json.RawMessage(
		`{"action":"encode","context":"attr-double","value":"<script>alert(1)</script>"}`,
	))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	env := mustEnvelope(t, out)
	if !env.OK {
		t.Fatalf("Execute = %s, want an ok:true envelope", out)
	}
	var res pageops.EncodeResult
	if err := json.Unmarshal(env.Data, &res); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if res.Context != "attr-double" || len(res.Variants) != 4 {
		t.Errorf("result = %+v, want context attr-double with 4 variants", res)
	}

	out, err = tool.Execute(context.Background(), json.RawMessage(
		`{"action":"encode","context":"bogus","value":"x"}`,
	))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	env = mustEnvelope(t, out)
	if env.OK || env.Error == nil {
		t.Fatalf("Execute = %s, want an ok:false envelope", out)
	}
	if env.Error.Type != errInvalidArgs {
		t.Errorf("error.type = %q, want %q", env.Error.Type, errInvalidArgs)
	}
	for _, context := range pageops.EncodeContexts() {
		if !strings.Contains(env.Error.Message, context) {
			t.Errorf("error message %q does not name context %q", env.Error.Message, context)
		}
	}
}
