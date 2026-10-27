package cmd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpServer "github.com/dakolli/webmcp/pkg/mcp/server"
)

// TestServeLoggerWritesToServeLog pins the production wiring: the serve loop's
// logger appends the manager's close line to the serve log file under
// $XDG_STATE_HOME/web-cli.
func TestServeLoggerWritesToServeLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	logger, closeLog := serveLogger()
	defer closeLog()

	logger.Info("browsers: browser closed", "reason", "browser_close")

	data, err := os.ReadFile(filepath.Join(dir, "web-cli", "serve.log"))
	if err != nil {
		t.Fatalf("read serve log: %v", err)
	}
	if !strings.Contains(string(data), "browsers: browser closed") {
		t.Fatalf("serve log = %q, want the browser close line", data)
	}
	if !strings.Contains(string(data), "reason=browser_close") {
		t.Fatalf("serve log = %q, want the close reason", data)
	}
}

// TestServeLaunchesDefaultBrowser pins the WEB-139 split: only the plain human
// daemon pre-launches browser-1, while `serve mcp` starts no browser at all
// (never a headless fallback, either).
func TestServeMcpBindDefaultsToLoopback(t *testing.T) {
	f := NewServeMcpCmd().Flags().Lookup("bind")
	if f == nil {
		t.Fatal("serve mcp has no --bind flag")
	}
	if f.DefValue != mcpServer.DefaultBind {
		t.Fatalf("--bind default = %q, want %q", f.DefValue, mcpServer.DefaultBind)
	}
}

func TestMCPProbeAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"loopback", "127.0.0.1:4099", "127.0.0.1:4099"},
		{"ipv4 wildcard", "0.0.0.0:4099", "127.0.0.1:4099"},
		{"ipv6 wildcard", "[::]:4099", "[::1]:4099"},
		{"specific interface", "192.168.1.10:4099", "192.168.1.10:4099"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpProbeAddress(tc.addr); got != tc.want {
				t.Errorf("mcpProbeAddress(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// TestMCPBannerUsesBoundAddress pins WEB-169: the banner advertises the address
// the listener actually bound, never the requested wildcard rewritten to
// loopback.
func TestMCPBannerUsesBoundAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("bind a wildcard listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	got := mcpBanner(mcpEndpointAddress(ln, "0.0.0.0:0"))
	if want := "mcp: http://" + ln.Addr().String(); got != want {
		t.Fatalf("banner = %q, want the bound address %q", got, want)
	}
	if strings.Contains(got, mcpServer.DefaultBind) {
		t.Fatalf("banner %q rewrites the wildcard bind to loopback", got)
	}

	daemon := mcpBanner(mcpEndpointAddress(nil, "0.0.0.0:4101"))
	if want := "mcp: http://0.0.0.0:4101"; daemon != want {
		t.Fatalf("daemon banner = %q, want %q", daemon, want)
	}
}

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
