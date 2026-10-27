package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
	"webmcp/pkg/cli/pageops"
	"webmcp/pkg/mcp/tools"
)

// TestSurfaceVocabularyParity pins that one name addresses the same aspect or
// action on every surface: the MCP schema enum, the CLI positional token, and
// the IPC allow-list. It reads the MCP enums from the tool schemas and the
// IPC list from its wire contract, so a new aspect or action cannot land on one
// surface alone.
func TestSurfaceVocabularyParity(t *testing.T) {
	mcpAspects := schemaEnum(t, tools.NewPage(nil).InputSchema().Properties, "what")
	mcpActions := schemaEnum(t, tools.NewAct(nil).InputSchema().Properties, "action")

	t.Run("aspects", func(t *testing.T) {
		for _, what := range mcpAspects {
			if !whatVocab[what] {
				t.Errorf("MCP aspect %q is missing from the CLI what vocabulary", what)
			}
			if err := pageops.ValidateAspect(what); err != nil {
				t.Errorf("MCP aspect %q is rejected by the IPC allow-list: %v", what, err)
			}
		}
		for what := range whatVocab {
			if !slices.Contains(mcpAspects, what) {
				t.Errorf("CLI aspect %q has no MCP page_fetch what value", what)
			}
		}
	})

	// intercept_* are landing with WEB-194's observer work: until that ticket
	// wires the CLI and IPC surfaces they may be absent from both, but never
	// from only one. replay (WEB-112) is fully wired and is not exempt.
	pending := map[string]bool{
		"intercept_add":   true,
		"intercept_list":  true,
		"intercept_clear": true,
	}
	ipcActions := ipc.PageActActions()
	t.Run("actions", func(t *testing.T) {
		for _, action := range mcpActions {
			cliOK, ipcOK := actionVocab[action], slices.Contains(ipcActions, action)
			if cliOK != ipcOK {
				t.Errorf("MCP action %q is wired on one surface only (CLI=%v IPC=%v)", action, cliOK, ipcOK)
			}
			if !cliOK {
				if !pending[action] {
					t.Errorf("MCP action %q is missing from the CLI and IPC vocabularies", action)
				}
				continue
			}
			if _, ok := actPayloadSpecs[action]; !ok {
				t.Errorf("CLI action %q has no positional payload spec", action)
			}
		}
		for action := range actionVocab {
			if !slices.Contains(mcpActions, action) {
				t.Errorf("CLI action %q has no MCP page_act action", action)
			}
		}
		for _, action := range ipcActions {
			if !slices.Contains(mcpActions, action) {
				t.Errorf("IPC action %q has no MCP page_act action", action)
			}
		}
	})
}

// schemaEnum reads a string enum out of one schema property.
func schemaEnum(t *testing.T, props map[string]any, field string) []string {
	t.Helper()
	prop, ok := props[field].(map[string]any)
	if !ok {
		t.Fatalf("schema property %q is not an object", field)
	}
	enum, ok := prop["enum"].([]string)
	if !ok {
		t.Fatalf("schema property %q has no []string enum", field)
	}
	return enum
}

// TestParseActArgsReplay pins the replay argument mapping: a positional target
// (the README form) and the --target/--method/--header/--max-bytes/--value
// overrides, with positionals winning over their flag fallbacks.
func TestParseActArgsReplay(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		flags actArgFlags
		want  actArgs
	}{
		{
			name: "action-first positional target",
			args: []string{"replay", "/api/user"},
			want: actArgs{action: "replay", target: "/api/user"},
		},
		{
			name: "id plus positional target",
			args: []string{"12", "replay", "/api/user"},
			want: actArgs{id: "12", action: "replay", target: "/api/user"},
		},
		{
			name:  "flag-only overrides",
			flags: actArgFlags{id: "page-1", action: "replay", target: "/api/user", method: "PUT", headers: []string{"X-Env=staging"}, value: `{"id":2}`, valueSet: true, maxBytes: 4096},
			want:  actArgs{id: "page-1", action: "replay", target: "/api/user", method: "PUT", headers: []string{"X-Env=staging"}, value: `{"id":2}`, valueSet: true, maxBytes: 4096},
		},
		{
			name:  "positional target wins over the flag",
			args:  []string{"replay", "/api/user"},
			flags: actArgFlags{target: "/other", method: "PATCH"},
			want:  actArgs{action: "replay", target: "/api/user", method: "PATCH"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseActArgs(tt.args, tt.flags)
			if err != nil {
				t.Fatalf("parseActArgs(%v, %+v) error = %v, want nil", tt.args, tt.flags, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseActArgs(%v, %+v) = %+v, want %+v", tt.args, tt.flags, got, tt.want)
			}
		})
	}
}

// TestPageActReplayFlags pins the CLI surface the README advertises: replay
// overrides ride dedicated flags, and --max-bytes defaults to the engine cap.
func TestPageActReplayFlags(t *testing.T) {
	cmd := newPageActCmd(nil)
	for _, name := range []string{"target", "method", "header", "max-bytes"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("page act is missing --%s for replay", name)
		}
	}
	if flag := cmd.Flags().Lookup("max-bytes"); flag != nil && flag.DefValue != "0" {
		t.Errorf("--max-bytes default = %q, want 0 (engine 256 KB ceiling)", flag.DefValue)
	}
}

// TestPageActViaIPCForwardsReplay pins the daemon path: every replay override
// reaches the wire intact, including value_set for an explicitly empty body.
func TestPageActViaIPCForwardsReplay(t *testing.T) {
	path, captured := startCapturingActServer(t)
	cl, err := ipc.Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	err = pageActViaIPC(context.Background(), cl, &ipc.PageActParams{
		SessionID: "page-1",
		Action:    "replay",
		Target:    "/api/user",
		Method:    "PUT",
		Headers:   map[string]string{"X-Env": "staging"},
		Value:     `{"id":2}`,
		ValueSet:  true,
		MaxBytes:  4096,
	})
	if err != nil {
		t.Fatalf("pageActViaIPC: %v", err)
	}

	select {
	case params := <-captured:
		if params.Action != "replay" || params.SessionID != "page-1" {
			t.Errorf("wire params = %+v, want session_id page-1 action replay", params)
		}
		if params.Target != "/api/user" || params.Method != "PUT" {
			t.Errorf("wire target/method = %q/%q, want /api/user/PUT", params.Target, params.Method)
		}
		if params.Headers["X-Env"] != "staging" {
			t.Errorf("wire headers = %v, want X-Env=staging", params.Headers)
		}
		if params.Value != `{"id":2}` || !params.ValueSet {
			t.Errorf("wire value/value_set = %q/%v, want the edited body marked set", params.Value, params.ValueSet)
		}
		if params.MaxBytes != 4096 {
			t.Errorf("wire max_bytes = %d, want 4096", params.MaxBytes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fake server saw no page.act request")
	}
}

// TestPageListRowReportsOpener pins the listing format: a top-level page keeps
// its "id<TAB>url" row, and a popup appends its opener id.
func TestPageListRowReportsOpener(t *testing.T) {
	tests := []struct {
		name string
		page browsers.PageInfo
		want string
	}{
		{
			name: "top-level page",
			page: browsers.PageInfo{ID: "page-1", URL: "https://a.test/"},
			want: "page-1\thttps://a.test/",
		},
		{
			name: "popup reports opener",
			page: browsers.PageInfo{ID: "page-2", URL: "https://b.test/", Opener: "page-1"},
			want: "page-2\thttps://b.test/\topener=page-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pageListRow(tt.page); got != tt.want {
				t.Errorf("pageListRow(%+v) = %q, want %q", tt.page, got, tt.want)
			}
		})
	}
}

// TestPageFetchTimeoutFlag pins the CLI contract: --timeout-ms defaults to
// 30000 and resolves through the 1..120000 clamp.
func TestPageFetchTimeoutFlag(t *testing.T) {
	cmd := newPageFetchCmd(nil)
	flag := cmd.Flags().Lookup("timeout-ms")
	if flag == nil {
		t.Fatal("page fetch is missing --timeout-ms")
	}
	if flag.DefValue != "30000" {
		t.Errorf("--timeout-ms default = %q, want 30000", flag.DefValue)
	}

	tests := []struct {
		in   int
		want time.Duration
	}{
		{0, 30 * time.Second},
		{-5, 30 * time.Second},
		{60000, 60 * time.Second},
		{120000, 120 * time.Second},
		{500000, 120 * time.Second},
	}
	for _, tt := range tests {
		if got := pageFetchTimeout(tt.in); got != tt.want {
			t.Errorf("pageFetchTimeout(%d) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestPageFetchViaIPCForwardsTimeout pins the daemon path: pageFetchViaIPC
// puts the resolved --timeout-ms on the wire as timeout_ms.
func TestPageFetchViaIPCForwardsTimeout(t *testing.T) {
	path, captured := startCapturingServer(t)
	cl, err := ipc.Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	err = pageFetchViaIPC(
		&cobra.Command{},
		context.Background(),
		cl,
		"page-1", "", "", "dom", "", "",
		false,
		50000,
		60000,
		false,
	)
	if err != nil {
		t.Fatalf("pageFetchViaIPC: %v", err)
	}

	select {
	case params := <-captured:
		if params.TimeoutMS != 60000 {
			t.Errorf("wire timeout_ms = %d, want 60000", params.TimeoutMS)
		}
		if params.SessionID != "page-1" || params.What != "dom" {
			t.Errorf("wire params = %+v, want session_id page-1 what dom", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fake server saw no page.fetch request")
	}
}

// startCapturingServer listens on a temp unix socket and reports the
// page.fetch params of the first real request. The client's Dial probe opens
// and closes a connection with no line, so it is skipped.
func startCapturingServer(t *testing.T) (string, <-chan ipc.PageFetchParams) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })

	captured := make(chan ipc.PageFetchParams, 1)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return // the Dial probe closes without a line
				}
				var req ipc.Request
				if err := json.Unmarshal(line, &req); err != nil {
					return
				}
				var params ipc.PageFetchParams
				if err := json.Unmarshal(req.Params, &params); err != nil {
					return
				}
				select {
				case captured <- params:
				default:
				}
				_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"data":"ok"}}` + "\n"))
			}(conn)
		}
	}()
	return path, captured
}

// startCapturingActServer listens on a temp unix socket and reports the
// page.act params of the first real request, skipping the Dial probe.
func startCapturingActServer(t *testing.T) (string, <-chan ipc.PageActParams) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })

	captured := make(chan ipc.PageActParams, 1)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req ipc.Request
				if err := json.Unmarshal(line, &req); err != nil {
					return
				}
				var params ipc.PageActParams
				if err := json.Unmarshal(req.Params, &params); err != nil {
					return
				}
				select {
				case captured <- params:
				default:
				}
				_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"data":"ok"}}` + "\n"))
			}(conn)
		}
	}()
	return path, captured
}
