package pageops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// TestPageWebSocketEmptyEnvelope pins the zero-frame envelope: an empty entries
// array rather than null, with zero count and dropped.
func TestPageWebSocketEmptyEnvelope(t *testing.T) {
	out, err := PageWebSocket(context.Background(), &browsers.Page{})
	if err != nil {
		t.Fatalf("PageWebSocket: %v", err)
	}
	if !strings.Contains(out, `"entries":[]`) {
		t.Fatalf("envelope = %q, want an empty entries array not null", out)
	}
	var env logsEnvelope[browsers.WebSocketEntry]
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", out, err)
	}
	if env.Count != 0 || env.Dropped != 0 {
		t.Fatalf("count=%d dropped=%d, want both 0 in %q", env.Count, env.Dropped, out)
	}
}
