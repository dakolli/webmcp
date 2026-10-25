package tools

import "testing"

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
