package cmd

import "testing"

// TestServeLaunchesDefaultBrowser pins the WEB-139 split: only the plain human
// daemon pre-launches browser-1, while `serve mcp` starts no browser at all
// (never a headless fallback, either).
func TestServeLaunchesDefaultBrowser(t *testing.T) {
	tests := []struct {
		name string
		opts serveRunOptions
		want bool
	}{
		{"plain serve launches browser-1", serveRunOptions{}, true},
		{"serve mcp launches nothing", serveRunOptions{mcpPort: defaultServeMcpPort}, false},
		{"serve mcp with --headless still launches nothing", serveRunOptions{mcpPort: defaultServeMcpPort, headless: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serveLaunchesDefaultBrowser(tt.opts); got != tt.want {
				t.Errorf("serveLaunchesDefaultBrowser(%+v) = %v, want %v", tt.opts, got, tt.want)
			}
		})
	}
}
