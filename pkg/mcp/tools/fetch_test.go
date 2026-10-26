package tools

import (
	"strings"
	"testing"
)

func TestFetchArgsOwnerValidation(t *testing.T) {
	valid := fetchArgs{URL: "https://example.com", Owner: "  scraper-a  "}
	if err := valid.validate(); err != nil {
		t.Fatalf("validate valid owner: %v", err)
	}
	if valid.Owner != "scraper-a" {
		t.Fatalf("Owner = %q, want the trimmed scraper-a", valid.Owner)
	}

	invalid := fetchArgs{URL: "https://example.com", Owner: "scraper/a"}
	err := invalid.validate()
	if err == nil {
		t.Fatal("validate invalid owner = nil, want an actionable refusal")
	}
	if !strings.Contains(err.Error(), `"scraper/a"`) || !strings.Contains(err.Error(), "1 to 64 characters") {
		t.Fatalf("error %q does not name the bad owner and the rule", err)
	}
}

func TestFetchArgsWaitForDefault(t *testing.T) {
	tests := []struct {
		name    string
		waitFor string
		want    string
		wantErr bool
	}{
		{name: "omitted defaults to idle", waitFor: "", want: "idle"},
		{name: "load preserved", waitFor: "load", want: "load"},
		{name: "idle preserved", waitFor: "idle", want: "idle"},
		{name: "stable preserved", waitFor: "stable", want: "stable"},
		{name: "invalid rejected", waitFor: "networkidle", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := fetchArgs{URL: "https://example.com", WaitFor: tt.waitFor}
			err := a.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("validate() = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() error = %v", err)
			}
			if a.WaitFor != tt.want {
				t.Fatalf("WaitFor = %q, want %q", a.WaitFor, tt.want)
			}
		})
	}
}

func TestFetchArgsIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		want     string
		wantErr  bool
	}{
		{name: "omitted defaults to default", identity: "", want: "default"},
		{name: "trimmed and preserved", identity: "  victim  ", want: "victim"},
		{name: "explicit default", identity: "default", want: "default"},
		{name: "invalid label rejected", identity: "bad name", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := fetchArgs{URL: "https://example.com", Identity: tt.identity}
			err := a.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("validate() = nil, want an invalid-identity refusal")
				}
				if !strings.Contains(err.Error(), "invalid identity") {
					t.Fatalf("error = %q, want the invalid-identity rule", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() error = %v", err)
			}
			if a.Identity != tt.want {
				t.Fatalf("Identity = %q, want %q", a.Identity, tt.want)
			}
		})
	}

	prop, ok := (&Fetch{}).InputSchema().Properties["identity"].(map[string]any)
	if !ok {
		t.Fatal("identity schema property is missing")
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "default") || !strings.Contains(desc, "context") {
		t.Errorf("identity description = %q, want the default and incognito-context contract", desc)
	}
}
