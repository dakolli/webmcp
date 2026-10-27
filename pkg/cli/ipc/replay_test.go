package ipc

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// TestPageActReplayVocabulary pins replay as a first-class page.act action on
// the wire: the allow-list gates validation, so an omission here rejects a
// request the CLI can send.
func TestPageActReplayVocabulary(t *testing.T) {
	if !ValidPageActAction("replay") {
		t.Fatal("replay is not in the page.act action allow-list")
	}
	if !strings.Contains(PageActActionList(), "replay") {
		t.Errorf("PageActActionList() = %q, want it to include replay", PageActActionList())
	}
}

// TestServerPageActReplayValidation pins the replay argument mapping at the
// wire boundary: target is required, max_bytes is non-negative, and a valid
// replay (including an explicitly empty body via value_set) passes validation
// and fails later on the stub's missing page, never as -32602.
func TestServerPageActReplayValidation(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantCode int
		want     string
	}{
		{
			name:     "missing target",
			line:     `{"jsonrpc":"2.0","id":1,"method":"page.act","params":{"session_id":"page-1","action":"replay"}}`,
			wantCode: CodeInvalidParams,
			want:     "page.act: replay requires target=<seq> or target=<url substring> from page_fetch what=network",
		},
		{
			name:     "negative max_bytes",
			line:     `{"jsonrpc":"2.0","id":1,"method":"page.act","params":{"session_id":"page-1","action":"replay","target":"/api","max_bytes":-1}}`,
			wantCode: CodeInvalidParams,
			want:     "page.act: replay max_bytes must be >= 0",
		},
		{
			name:     "valid replay passes validation",
			line:     `{"jsonrpc":"2.0","id":1,"method":"page.act","params":{"session_id":"page-1","action":"replay","target":"/api","method":"PUT","headers":{"X-Env":"staging"},"value":"","value_set":true,"max_bytes":4096}}`,
			wantCode: CodeServerError,
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, newStubServer())
			conn := dialRaw(t, path)
			r := bufio.NewReader(conn)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

			if _, err := conn.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("write request: %v", err)
			}
			line := readLineFrom(t, r)
			var resp Response
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != tt.wantCode {
				t.Fatalf("error = %+v, want code %d", resp.Error, tt.wantCode)
			}
			if tt.want != "" && !strings.Contains(resp.Error.Message, tt.want) {
				t.Errorf("error message = %q, want it to contain %q", resp.Error.Message, tt.want)
			}
			if tt.wantCode == CodeServerError && strings.Contains(resp.Error.Message, "unknown action") {
				t.Errorf("message = %q, replay must pass action validation", resp.Error.Message)
			}
		})
	}
}

// TestPageActReplayFuzzVocabulary pins replay_fuzz as a first-class page.act
// action on the wire: the allow-list gates validation, so an omission here
// rejects a request the CLI can send.
func TestPageActReplayFuzzVocabulary(t *testing.T) {
	if !ValidPageActAction("replay_fuzz") {
		t.Fatal("replay_fuzz is not in the page.act action allow-list")
	}
	if !strings.Contains(PageActActionList(), "replay_fuzz") {
		t.Errorf("PageActActionList() = %q, want it to include replay_fuzz", PageActActionList())
	}
}

// TestPageActParamsFuzzSpec pins the IPC-to-engine mapping and the wire field
// names: a page.act replay_fuzz line decodes its points, payloads and strategy
// into the engine's FuzzSpec.
func TestPageActParamsFuzzSpec(t *testing.T) {
	line := `{"session_id":"page-1","action":"replay_fuzz","target":"/api/search",` +
		`"strategy":"clusterbomb","points":[{"param":"q","location":"query"}],` +
		`"payloads":["a","b"],"max_bytes":4096}`
	var p PageActParams
	if err := json.Unmarshal([]byte(line), &p); err != nil {
		t.Fatalf("decode page.act params: %v", err)
	}
	got := p.FuzzSpec(nil)
	want := browsers.FuzzSpec{
		Target:   "/api/search",
		Strategy: browsers.FuzzClusterbomb,
		Points:   []browsers.FuzzPoint{{Param: "q", Location: browsers.FuzzQuery}},
		Payloads: []string{"a", "b"},
		MaxBytes: 4096,
	}
	if got.Target != want.Target || got.Strategy != want.Strategy || got.MaxBytes != want.MaxBytes {
		t.Errorf("FuzzSpec() = %+v, want %+v", got, want)
	}
	if len(got.Points) != 1 || got.Points[0] != want.Points[0] {
		t.Errorf("FuzzSpec points = %+v, want %+v", got.Points, want.Points)
	}
	if len(got.Payloads) != 2 || got.Payloads[0] != "a" || got.Payloads[1] != "b" {
		t.Errorf("FuzzSpec payloads = %v, want [a b]", got.Payloads)
	}
}

// TestServerPageActReplayFuzzValidation pins the replay_fuzz argument mapping at
// the wire boundary: target is required, max_bytes is non-negative, and a valid
// matrix passes validation and fails later on the stub's missing page, never as
// -32602.
func TestServerPageActReplayFuzzValidation(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantCode int
		want     string
	}{
		{
			name:     "missing target",
			line:     `{"jsonrpc":"2.0","id":1,"method":"page.act","params":{"session_id":"page-1","action":"replay_fuzz"}}`,
			wantCode: CodeInvalidParams,
			want:     "page.act: replay_fuzz requires target=<seq> or target=<url substring> from page_fetch what=network",
		},
		{
			name:     "negative max_bytes",
			line:     `{"jsonrpc":"2.0","id":1,"method":"page.act","params":{"session_id":"page-1","action":"replay_fuzz","target":"/api","max_bytes":-1}}`,
			wantCode: CodeInvalidParams,
			want:     "page.act: replay_fuzz max_bytes must be >= 0",
		},
		{
			name:     "valid matrix passes validation",
			line:     `{"jsonrpc":"2.0","id":1,"method":"page.act","params":{"session_id":"page-1","action":"replay_fuzz","target":"/api","points":[{"param":"q","location":"query"}],"payloads":["a"],"strategy":"sniper"}}`,
			wantCode: CodeServerError,
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, newStubServer())
			conn := dialRaw(t, path)
			r := bufio.NewReader(conn)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

			if _, err := conn.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("write request: %v", err)
			}
			line := readLineFrom(t, r)
			var resp Response
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != tt.wantCode {
				t.Fatalf("error = %+v, want code %d", resp.Error, tt.wantCode)
			}
			if tt.want != "" && !strings.Contains(resp.Error.Message, tt.want) {
				t.Errorf("error message = %q, want it to contain %q", resp.Error.Message, tt.want)
			}
			if tt.wantCode == CodeServerError && strings.Contains(resp.Error.Message, "unknown action") {
				t.Errorf("message = %q, replay_fuzz must pass action validation", resp.Error.Message)
			}
		})
	}
}
