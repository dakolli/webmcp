package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"webmcp/pkg/browsers"
)

func TestBrowserCreateResultCarriesLifecycleFields(t *testing.T) {
	created := browsers.Created{
		ID:      "browser-2",
		Browser: browsers.NewBrowser("browser-2", context.Background(), false, false, "", ""),
		Reused:  true,
	}

	data, err := json.Marshal(newBrowserCreateResult(created))
	if err != nil {
		t.Fatalf("marshal create result: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	for _, key := range []string{"browser_id", "owner", "persist", "ttl", "reused"} {
		if _, ok := got[key]; !ok {
			t.Errorf("browser_create result missing key %q", key)
		}
	}
	if got["owner"] != "unowned" {
		t.Errorf("owner = %v, want unowned", got["owner"])
	}
	if got["reused"] != true {
		t.Errorf("reused = %v, want true", got["reused"])
	}
	if strings.Contains(string(data), "\n") {
		t.Errorf("browser_create result is not one line: %q", data)
	}
}

func TestBrowserListEntryCarriesLifecycleFields(t *testing.T) {
	entry := browserListEntry(
		browsers.BrowserInfo{
			BrowserID:  "browser-2",
			Owner:      "Claude",
			Bin:        "/usr/bin/brave-browser",
			ProfileDir: "/home/u/.config/browser-mcp/profiles/.ephemeral/browser-2-1",
			Persist:    false,
			Ephemeral:  true,
			CreatedAt:  time.Unix(1700000000, 0).UTC(),
			IdleFor:    "4m2s",
			TTL:        "30m0s",
		},
		[]browsers.PageInfo{{ID: "page-1", URL: "https://example.com"}},
	)

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	for _, key := range []string{
		"browser_id", "owner", "bin", "profile_dir", "persist",
		"ephemeral", "created_at", "idle_for", "ttl", "pages",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("browser_list entry missing key %q", key)
		}
	}
	if got["owner"] != "Claude" {
		t.Errorf("owner = %v, want Claude", got["owner"])
	}
}

func TestOwnerValidation(t *testing.T) {
	tests := []struct {
		name    string
		owner   string
		want    string
		wantErr bool
	}{
		{name: "omitted", owner: "", want: ""},
		{name: "trimmed", owner: "  scraper-a  ", want: "scraper-a"},
		{name: "all allowed characters", owner: "a.b_c-9", want: "a.b_c-9"},
		{name: "64 characters accepted", owner: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
		{name: "65 characters rejected", owner: strings.Repeat("a", 65), wantErr: true},
		{name: "slash rejected", owner: "scraper/a", wantErr: true},
		{name: "inner space rejected", owner: "scraper a", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOwner(tt.owner)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeOwner(%q) = %q, nil; want an error", tt.owner, got)
				}
				if !strings.Contains(err.Error(), tt.owner) {
					t.Errorf("error %q does not name the bad value %q", err, tt.owner)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeOwner(%q) error = %v", tt.owner, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeOwner(%q) = %q, want %q", tt.owner, got, tt.want)
			}
		})
	}
}

func TestBrowserCreateInvalidOwnerIsActionable(t *testing.T) {
	tool := NewBrowserCreate(nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"scraper/a"}`))
	if err == nil {
		t.Fatal("Execute with an invalid owner = nil error, want an actionable refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"scraper/a"`) {
		t.Errorf("error %q does not name the bad owner", msg)
	}
	if !strings.Contains(msg, "1 to 64 characters") {
		t.Errorf("error %q does not state the rule", msg)
	}
}

func TestBrowserCloseCleanupArgValidation(t *testing.T) {
	tool := NewBrowserClose(nil)
	tests := []struct {
		name string
		args string
	}{
		{"purge and prune are exclusive", `{"purge":true,"prune":true}`},
		{"purge needs a browser_id", `{"purge":true}`},
		{"prune takes no browser_id", `{"prune":true,"browser_id":"browser-1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), json.RawMessage(tt.args)); err == nil {
				t.Fatalf("Execute(%s) = nil error, want an actionable refusal", tt.args)
			}
		})
	}
}
