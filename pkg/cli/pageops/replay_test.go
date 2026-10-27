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
