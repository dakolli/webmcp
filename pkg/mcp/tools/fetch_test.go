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
