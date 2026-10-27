package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
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

// TestListingToolsNeverLaunchBrowsers pins the read contract: session_list and
// page_list answer from the live manager and never spawn Chromium.
func TestListingToolsNeverLaunchBrowsers(t *testing.T) {
	mgr := browsers.NewManager(browsers.WithTTL(0), browsers.WithBrowserTTL(0))
	cases := []struct {
		name string
		tool Tool
		args string
	}{
		{"session_list", NewSessionList(mgr), `{}`},
		{"page_list", NewPageList(mgr), `{"browser_id":"browser-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(mgr.Browsers())
			if _, err := tc.tool.Execute(context.Background(), json.RawMessage(tc.args)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if after := len(mgr.Browsers()); after != before {
				t.Fatalf("%s browsers after listing = %d, want %d", tc.name, after, before)
			}
		})
	}
}

// TestSessionListEmptyPayload pins the success envelope on a cold manager: an
// empty array, never null, with no browser created.
func TestSessionListEmptyPayload(t *testing.T) {
	mgr := browsers.NewManager(browsers.WithTTL(0), browsers.WithBrowserTTL(0))

	out, err := NewSessionList(mgr).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("session_list: %v", err)
	}
	var env envelopeProbe
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", out, err)
	}
	if !env.OK {
		t.Fatalf("envelope = %s, want ok", out)
	}
	if string(env.Data) != "[]" {
		t.Fatalf("data = %s, want []", env.Data)
	}
	if got := len(mgr.Browsers()); got != 0 {
		t.Fatalf("browsers after session_list = %d, want 0", got)
	}
}

// TestSessionListUnknownIdentityIsInvalidArgs pins the envelope contract: an
// unknown identity filter is an argument error naming every live identity.
func TestSessionListUnknownIdentityIsInvalidArgs(t *testing.T) {
	mgr := browsers.NewManager(browsers.WithTTL(0), browsers.WithBrowserTTL(0))

	out, err := NewSessionList(mgr).Execute(context.Background(), json.RawMessage(`{"identity":"ghost"}`))
	if err != nil {
		t.Fatalf("session_list: %v", err)
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
	want := `unknown identity "ghost" (known: none)`
	if !strings.Contains(env.Error.Message, want) {
		t.Fatalf("error message = %q, want it to contain %q", env.Error.Message, want)
	}
}
