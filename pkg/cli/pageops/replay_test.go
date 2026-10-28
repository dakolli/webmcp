package pageops

import (
	"context"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
)

func TestPageReplayDelegatesToEngine(t *testing.T) {
	p := &browsers.Page{}
	_, err := PageReplay(context.Background(), p, ReplayRequest{Target: "  "})
	if err == nil || !strings.Contains(err.Error(), "replay requires target") {
		t.Fatalf("PageReplay() error = %v, want the engine's target error", err)
	}
}

// TestReplayQueryURL pins the query contract: a non-empty value replaces the
// parameter, an empty value removes it, and every other parameter survives.
func TestReplayQueryURL(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		edits map[string]string
		want  string
	}{
		{
			name:  "add a parameter beside the captured one",
			raw:   "https://host.test/search?q=test",
			edits: map[string]string{"lang": "en"},
			want:  "https://host.test/search?lang=en&q=test",
		},
		{
			name:  "replace a parameter",
			raw:   "https://host.test/search?q=test",
			edits: map[string]string{"q": "other"},
			want:  "https://host.test/search?q=other",
		},
		{
			name:  "remove a parameter with an empty value",
			raw:   "https://host.test/search?q=test&lang=en",
			edits: map[string]string{"q": ""},
			want:  "https://host.test/search?lang=en",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := replayQueryURL(tt.raw, tt.edits)
			if err != nil {
				t.Fatalf("replayQueryURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("replayQueryURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
