package pageops

import (
	"slices"
	"testing"
)

// TestSplitStyleDeclarations pins the declaration splitter: it cuts only at
// bracket depth zero and outside quotes, keeps !important, drops empty
// segments, and survives nested url() and rgb() values.
func TestSplitStyleDeclarations(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty value",
			in:   "",
			want: nil,
		},
		{
			name: "single declaration",
			in:   "background-image:url('/a.png')",
			want: []string{"background-image:url('/a.png')"},
		},
		{
			name: "semicolon inside url survives",
			in:   "background-image:url('a;b');color:red",
			want: []string{"background-image:url('a;b')", "color:red"},
		},
		{
			name: "semicolon inside double quotes survives",
			in:   `content:"a;b";color:red`,
			want: []string{`content:"a;b"`, "color:red"},
		},
		{
			name: "parentheses survive",
			in:   "color:rgb(1,2,3);padding:4px",
			want: []string{"color:rgb(1,2,3)", "padding:4px"},
		},
		{
			name: "nested url inside parentheses",
			in:   `mask:url("data:image/svg+xml;utf8,<svg></svg>");color:red`,
			want: []string{`mask:url("data:image/svg+xml;utf8,<svg></svg>")`, "color:red"},
		},
		{
			name: "trailing semicolon",
			in:   "color:red;",
			want: []string{"color:red"},
		},
		{
			name: "important survives",
			in:   "color:red !important",
			want: []string{"color:red !important"},
		},
		{
			name: "empty segments dropped and whitespace trimmed",
			in:   "  color : red ; ; padding: 0 ",
			want: []string{"color : red", "padding: 0"},
		},
		{
			name: "braces and brackets hold depth",
			in:   "grid-template:{a;b};x:[1;2]",
			want: []string{"grid-template:{a;b}", "x:[1;2]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitStyleDeclarations(tt.in)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("splitStyleDeclarations(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
