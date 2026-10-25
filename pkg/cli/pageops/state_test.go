package pageops

import "testing"

// TestActStateDelta pins the exact suffix bytes: only changed fields are
// emitted, url stays bare, title is quoted, and an unchanged page adds nothing.
func TestActStateDelta(t *testing.T) {
	tests := []struct {
		name          string
		before, after PageState
		want          string
	}{
		{
			name:   "unchanged adds nothing",
			before: PageState{URL: "https://example.com", Title: "Home"},
			after:  PageState{URL: "https://example.com", Title: "Home"},
			want:   "",
		},
		{
			name:   "url only",
			before: PageState{URL: "https://example.com", Title: "Home"},
			after:  PageState{URL: "https://example.com/next", Title: "Home"},
			want:   "\n→ url=https://example.com/next",
		},
		{
			name:   "title only is quoted",
			before: PageState{URL: "https://example.com", Title: "Home"},
			after:  PageState{URL: "https://example.com", Title: "Next page"},
			want:   "\n→ title=\"Next page\"",
		},
		{
			name:   "both changed url first",
			before: PageState{URL: "https://example.com", Title: "Home"},
			after:  PageState{URL: "https://example.com/next", Title: "Next page"},
			want:   "\n→ url=https://example.com/next title=\"Next page\"",
		},
		{
			name:   "trailing slash is not normalised",
			before: PageState{URL: "https://example.com", Title: ""},
			after:  PageState{URL: "https://example.com/", Title: ""},
			want:   "\n→ url=https://example.com/",
		},
		{
			name:   "title escaping",
			before: PageState{URL: "https://example.com", Title: ""},
			after:  PageState{URL: "https://example.com", Title: `say "hi"`},
			want:   "\n→ title=\"say \\\"hi\\\"\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActStateDelta(tt.before, tt.after); got != tt.want {
				t.Fatalf("ActStateDelta() = %q, want %q", got, tt.want)
			}
		})
	}
}
