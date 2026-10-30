package pageops

import "testing"

// TestSetHTMLMode pins the set_html mode mapping: empty and inner select
// innerHTML, outer selects outerHTML, append selects insertAdjacentHTML, and an
// unknown mode is refused.
func TestSetHTMLMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "inner", false},
		{"inner", "inner", false},
		{"outer", "outer", false},
		{"append", "append", false},
		{"APPEND", "append", false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := SetHTMLMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SetHTMLMode(%q) returned no error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetHTMLMode(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("SetHTMLMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
