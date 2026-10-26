package browsers

import (
	"strings"
	"testing"
)

func TestParseProxy(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		server  string
		host    string
		wantErr string
	}{
		{
			name:   "host and port",
			raw:    "127.0.0.1:8080",
			server: "127.0.0.1:8080",
			host:   "127.0.0.1:8080",
		},
		{
			name:   "http scheme",
			raw:    "http://127.0.0.1:8080",
			server: "http://127.0.0.1:8080",
			host:   "127.0.0.1:8080",
		},
		{
			name:   "socks5 scheme lowercases the key",
			raw:    "socks5://Proxy.Example.com:1080",
			server: "socks5://Proxy.Example.com:1080",
			host:   "proxy.example.com:1080",
		},
		{
			name:   "ipv6 loopback",
			raw:    "[::1]:8080",
			server: "[::1]:8080",
			host:   "[::1]:8080",
		},
		{name: "empty", raw: "", wantErr: "empty"},
		{name: "missing port", raw: "127.0.0.1", wantErr: "missing port"},
		{name: "missing host", raw: ":8080", wantErr: "missing host"},
		{name: "port out of range", raw: "127.0.0.1:70000", wantErr: "65535"},
		{name: "non-numeric port", raw: "http://127.0.0.1:abc", wantErr: "invalid proxy"},
		{name: "unsupported scheme", raw: "ftp://127.0.0.1:21", wantErr: "unsupported scheme"},
		{name: "embedded credentials", raw: "http://u:p@127.0.0.1:8080", wantErr: "credentials"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, host, err := parseProxy(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseProxy(%q) error = nil, want %q", tt.raw, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseProxy(%q) error = %q, want %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProxy(%q) error = %v", tt.raw, err)
			}
			if server != tt.server || host != tt.host {
				t.Fatalf("parseProxy(%q) = (%q, %q), want (%q, %q)", tt.raw, server, host, tt.server, tt.host)
			}
		})
	}
}
