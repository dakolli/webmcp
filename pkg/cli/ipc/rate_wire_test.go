package ipc

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBrowserOpenRateLimitWire pins that the per-host rate budget survives the
// daemon round trip: browser.open accepts rps/burst and rejects an invalid
// value, which proves the handler reads the fields rather than dropping them.
func TestBrowserOpenRateLimitWire(t *testing.T) {
	tests := []struct {
		name    string
		params  string
		wantErr string
	}{
		{name: "valid budget is accepted", params: `{"rps":2.5,"burst":4}`},
		{name: "invalid rps is refused", params: `{"rps":0,"burst":4}`, wantErr: "rps must be"},
		{name: "invalid burst is refused", params: `{"rps":1,"burst":0}`, wantErr: "burst must be at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, newStubServer())
			conn := dialRaw(t, path)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

			line := `{"jsonrpc":"2.0","id":1,"method":"browser.open","params":` + tt.params + "}\n"
			if _, err := conn.Write([]byte(line)); err != nil {
				t.Fatalf("write browser.open: %v", err)
			}
			resp := readLineFrom(t, bufio.NewReader(conn))

			var parsed Response
			if err := json.Unmarshal(resp, &parsed); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if tt.wantErr == "" {
				if parsed.Error != nil {
					t.Fatalf("browser.open error = %+v, want success", parsed.Error)
				}
				return
			}
			if parsed.Error == nil {
				t.Fatalf("browser.open with %s succeeded, want an invalid-params error", tt.params)
			}
			if parsed.Error.Code != CodeInvalidParams {
				t.Errorf("error code = %d, want %d (-32602)", parsed.Error.Code, CodeInvalidParams)
			}
			if !strings.Contains(parsed.Error.Message, tt.wantErr) {
				t.Errorf("error message = %q, want it to contain %q", parsed.Error.Message, tt.wantErr)
			}
		})
	}
}
