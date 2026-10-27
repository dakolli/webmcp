package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

	ipcActions := ipc.PageActActions()
	t.Run("actions", func(t *testing.T) {
		for _, action := range mcpActions {
			cliOK, ipcOK := actionVocab[action], slices.Contains(ipcActions, action)
			if cliOK != ipcOK {
				t.Errorf("MCP action %q is wired on one surface only (CLI=%v IPC=%v)", action, cliOK, ipcOK)
			}
			if !cliOK {
				t.Errorf("MCP action %q is missing from the CLI and IPC vocabularies", action)
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

// TestParseActArgsReplayFuzz pins the replay_fuzz argument mapping: the
// replay-fuzz alias and the underscore action both resolve, --points accepts
// the param:location and JSON shapes, and a positional target wins over --target.
func TestParseActArgsReplayFuzz(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		flags actArgFlags
		want  actArgs
	}{
		{
			name: "hyphen alias with flags",
			args: []string{"replay-fuzz", "/api/search"},
			flags: actArgFlags{
				points:   []string{"q:query", "id:body"},
				payloads: []string{"a", "b"},
				strategy: "sniper",
			},
			want: actArgs{
				action: "replay_fuzz",
				target: "/api/search",
				points: []browsers.FuzzPoint{
					{Param: "q", Location: browsers.FuzzQuery},
					{Param: "id", Location: browsers.FuzzBody},
				},
				payloads: []string{"a", "b"},
				strategy: "sniper",
			},
		},
		{
			name: "id plus underscore action and JSON point",
			args: []string{"12", "replay_fuzz", "/api/login"},
			flags: actArgFlags{
				points:   []string{`{"param":"X-Env","location":"header"}`},
				payloads: []string{"staging"},
				strategy: "pitchfork",
			},
			want: actArgs{
				id:     "12",
				action: "replay_fuzz",
				target: "/api/login",
				points: []browsers.FuzzPoint{
					{Param: "X-Env", Location: browsers.FuzzHeader},
				},
				payloads: []string{"staging"},
				strategy: "pitchfork",
			},
		},
		{
			name: "flag-only arguments",
			flags: actArgFlags{
				id:       "page-1",
				action:   "replay_fuzz",
				target:   "/api/user",
				points:   []string{"session:cookie"},
				payloads: []string{"a"},
				strategy: "clusterbomb",
			},
			want: actArgs{
				id:     "page-1",
				action: "replay_fuzz",
				target: "/api/user",
				points: []browsers.FuzzPoint{
					{Param: "session", Location: browsers.FuzzCookie},
				},
				payloads: []string{"a"},
				strategy: "clusterbomb",
			},
		},
		{
			name:  "positional target wins over the flag",
			args:  []string{"replay-fuzz", "/api/search"},
			flags: actArgFlags{target: "/other", points: []string{"q:query"}, payloads: []string{"a"}},
			want: actArgs{
				action: "replay_fuzz",
				target: "/api/search",
				points: []browsers.FuzzPoint{
					{Param: "q", Location: browsers.FuzzQuery},
				},
				payloads: []string{"a"},
			},
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

// TestActArgsFuzzSpec pins the CLI-to-engine mapping: the merged arguments
// become the engine's FuzzSpec with the strategy cast to its typed enum.
func TestActArgsFuzzSpec(t *testing.T) {
	a := actArgs{
		target:   "/api/search",
		strategy: "clusterbomb",
		points: []browsers.FuzzPoint{
			{Param: "q", Location: browsers.FuzzQuery},
		},
		payloads: []string{"a", "b"},
		maxBytes: 4096,
	}
	got := a.fuzzSpec(nil)
	want := browsers.FuzzSpec{
		Target:   "/api/search",
		Strategy: browsers.FuzzClusterbomb,
		Points:   []browsers.FuzzPoint{{Param: "q", Location: browsers.FuzzQuery}},
		Payloads: []string{"a", "b"},
		MaxBytes: 4096,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fuzzSpec() = %+v, want %+v", got, want)
	}
}

// TestPageActReplayFuzzFlags pins the CLI surface: the fuzz matrix rides
// --points/--payloads/--strategy and the strategy defaults to sniper.
func TestPageActReplayFuzzFlags(t *testing.T) {
	cmd := newPageActCmd(nil)
	for _, name := range []string{"points", "payloads", "strategy"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("page act is missing --%s for replay_fuzz", name)
		}
	}
	if flag := cmd.Flags().Lookup("strategy"); flag != nil && flag.DefValue != "sniper" {
		t.Errorf("--strategy default = %q, want sniper", flag.DefValue)
	}
}

// TestParseActArgsInterception pins the interception argument mapping: a bare
// action word is never read as a page id, and intercept_add parses its rules
// payload into the same typed rules the MCP action decodes.
func TestParseActArgsInterception(t *testing.T) {
	rules := `[{"url_pattern":"*api*","action":"block"}]`
	tests := []struct {
		name  string
		args  []string
		flags actArgFlags
		want  actArgs
	}{
		{
			name: "bare intercept_list is the action, not an id",
			args: []string{"intercept_list"},
			want: actArgs{action: "intercept_list"},
		},
		{
			name: "bare intercept_clear is the action, not an id",
			args: []string{"intercept_clear"},
			want: actArgs{action: "intercept_clear"},
		},
		{
			name: "id plus intercept_list",
			args: []string{"12", "intercept_list"},
			want: actArgs{id: "12", action: "intercept_list"},
		},
		{
			name: "intercept_add parses its rules payload",
			args: []string{"intercept_add", rules},
			want: actArgs{
				action: "intercept_add",
				rules:  []browsers.InterceptRule{{URLPattern: "*api*", Action: "block"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseActArgs(tt.args, tt.flags)
			if err != nil {
				t.Fatalf("parseActArgs(%v) error = %v, want nil", tt.args, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseActArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

// TestBrowserCreateRateFlagsReachDaemon pins the daemon path for the per-host
// rate budget: --rps and --burst ride browser.open as explicit values, and an
// absent flag stays absent so the engine defaults apply.
func TestBrowserCreateRateFlagsReachDaemon(t *testing.T) {
	path, captured := startCapturingBrowserOpenServer(t)
	cmd := NewBrowserCreateCmd(nil)
	cmd.Flags().String("socket", "", "")
	cmd.SetArgs([]string{"--socket", path, "--rps", "2.5", "--burst", "4"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("browser create: %v", err)
	}

	select {
	case params := <-captured:
		if params.RPS == nil || *params.RPS != 2.5 {
			t.Errorf("wire rps = %v, want 2.5", params.RPS)
		}
		if params.Burst == nil || *params.Burst != 4 {
			t.Errorf("wire burst = %v, want 4", params.Burst)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fake server saw no browser.open request")
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

// TestPageActForwardsRawIntent pins the daemon path: --raw and the CLI's own
// WEB_MCP_RAW_ACTIONS hatch both ride page.act as raw, while an unset client
// leaves raw absent so the daemon's default applies.
func TestPageActForwardsRawIntent(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		rawFlag bool
		want    bool
	}{
		{name: "raw flag", rawFlag: true, want: true},
		{name: "env hatch", env: "1", want: true},
		{name: "default leaves raw absent", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(browsers.RawActionsEnv, tt.env)
			path, captured := startPageActDaemon(t)
			cmd := newPageActCmd(nil)
			cmd.Flags().String("socket", path, "")
			args := []string{"click", "#submit"}
			if tt.rawFlag {
				args = append(args, "--raw")
			}
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("page act: %v", err)
			}

			select {
			case params := <-captured:
				if params.Raw != tt.want {
					t.Errorf("wire raw = %v, want %v", params.Raw, tt.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the fake daemon saw no page.act request")
			}
		})
	}
}

// TestPageActForwardsShortTimeoutUnchanged pins the daemon path: --timeout-ms
// reaches page.act as the caller's value, with no client-side grace added.
func TestPageActForwardsShortTimeoutUnchanged(t *testing.T) {
	path, captured := startPageActDaemon(t)
	cmd := newPageActCmd(nil)
	cmd.Flags().String("socket", path, "")
	cmd.SetArgs([]string{"click", "#submit", "--timeout-ms", "1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("page act: %v", err)
	}

	select {
	case params := <-captured:
		if params.TimeoutMS != 1 {
			t.Errorf("wire timeout_ms = %d, want 1", params.TimeoutMS)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fake daemon saw no page.act request")
	}
}

// TestPageActTimeoutHelp pins the documented meaning of --timeout-ms: it bounds
// the action, while the read grace exists only to prevent false transport
// errors.
func TestPageActTimeoutHelp(t *testing.T) {
	cmd := newPageActCmd(nil)
	flag := cmd.Flags().Lookup("timeout-ms")
	if flag == nil {
		t.Fatal("page act is missing --timeout-ms")
	}
	for _, want := range []string{"bounds the action", "grace"} {
		if !strings.Contains(flag.Usage, want) {
			t.Errorf("--timeout-ms help = %q, want it to mention %q", flag.Usage, want)
		}
	}
}

// startPageActDaemon answers the calls the CLI makes on the daemon path (ping,
// browser.list and page.act) and reports the page.act params it received.
func startPageActDaemon(t *testing.T) (string, <-chan ipc.PageActParams) {
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
				if json.Unmarshal(line, &req) != nil {
					return
				}
				var result any
				switch req.Method {
				case ipc.MethodBrowserList:
					result = ipc.BrowserListResult{Browsers: []ipc.BrowserEntry{{
						BrowserID: "browser-1",
						Pages:     []browsers.PageInfo{{ID: "page-1", URL: "https://a.test/"}},
					}}}
				case ipc.MethodPageAct:
					var params ipc.PageActParams
					if json.Unmarshal(req.Params, &params) == nil {
						select {
						case captured <- params:
						default:
						}
					}
					result = ipc.PageActResult{Data: "ok"}
				default:
					result = ipc.PingResult{OK: true}
				}
				resp, err := json.Marshal(ipc.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
				if err != nil {
					return
				}
				_, _ = c.Write(append(resp, '\n'))
			}(conn)
		}
	}()
	return path, captured
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

// startCapturingBrowserOpenServer listens on a temp unix socket and reports the
// browser.open params of the first real request. It answers the client's ping
// probe and the create call so browser create stays on the daemon path.
func startCapturingBrowserOpenServer(t *testing.T) (string, <-chan ipc.BrowserOpenParams) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })

	captured := make(chan ipc.BrowserOpenParams, 1)
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
				if json.Unmarshal(line, &req) != nil {
					return
				}
				var result any
				switch req.Method {
				case ipc.MethodBrowserOpen:
					var params ipc.BrowserOpenParams
					if json.Unmarshal(req.Params, &params) == nil {
						select {
						case captured <- params:
						default:
						}
					}
					result = ipc.BrowserOpenResult{
						BrowserID: "browser-1",
						Limits:    browsers.LimitReport{RPS: 2.5, Burst: 4},
					}
				default:
					result = ipc.PingResult{OK: true}
				}
				resp, err := json.Marshal(ipc.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
				if err != nil {
					return
				}
				_, _ = c.Write(append(resp, '\n'))
			}(conn)
		}
	}()
	return path, captured
}
