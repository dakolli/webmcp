package recon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunURLsValidation(t *testing.T) {
	t.Parallel()

	seeds := make([]string, 0, urlsMaxSeeds+1)
	for range urlsMaxSeeds + 1 {
		seeds = append(seeds, "https://example.com/seed")
	}

	tests := []struct {
		name string
		args URLsArgs
		want string
	}{
		{
			name: "authorized is required",
			args: URLsArgs{Targets: []string{"https://example.com"}},
			want: "authorized:true is required",
		},
		{
			name: "targets are required",
			args: URLsArgs{Authorized: true},
			want: "targets is required",
		},
		{
			name: "seed cap",
			args: URLsArgs{Targets: seeds, Authorized: true},
			want: "20-seed cap",
		},
		{
			name: "depth ceiling",
			args: URLsArgs{
				Targets:    []string{"https://example.com"},
				Depth:      urlsMaxDepth + 1,
				Authorized: true,
			},
			want: "exceeds the ceiling 10",
		},
		{
			name: "negative depth",
			args: URLsArgs{
				Targets:    []string{"https://example.com"},
				Depth:      -1,
				Authorized: true,
			},
			want: "must be >= 0",
		},
		{
			name: "known files enum",
			args: URLsArgs{
				Targets:    []string{"https://example.com"},
				KnownFiles: "everything",
				Authorized: true,
			},
			want: "known_files",
		},
		{
			name: "seed outside scope",
			args: URLsArgs{
				Targets:    []string{"https://example.com"},
				Scope:      []string{"other.test"},
				Authorized: true,
			},
			want: "outside the scope allowlist",
		},
		{
			name: "relative output file",
			args: URLsArgs{
				Targets:    []string{"https://example.com"},
				OutputFile: "urls.jsonl",
				Authorized: true,
			},
			want: "must be absolute",
		},
		{
			name: "negative max entries",
			args: URLsArgs{
				Targets:    []string{"https://example.com"},
				MaxEntries: -1,
				Authorized: true,
			},
			want: "max_entries must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := RunURLs(context.Background(), tt.args)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestURLsArgvUsesInputFlag(t *testing.T) {
	t.Parallel()

	argv, err := urlsArgv(
		URLsArgs{Depth: 2, JSCrawl: true, Concurrency: 4, RateLimit: 2},
		[]string{"https://example.com"},
		[]string{"example.com"},
	)
	if err != nil {
		t.Fatalf("urlsArgv: %v", err)
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-u" && argv[i+1] == "https://example.com" {
			return
		}
	}
	t.Fatalf("argv %v does not pass the target with -u", argv)
}

func TestURLsArgvDepth(t *testing.T) {
	t.Parallel()

	targets := []string{"https://example.com"}
	scope := []string{"example.com"}

	argv, err := urlsArgv(URLsArgs{}, targets, scope)
	if err != nil {
		t.Fatalf("urlsArgv zero depth: %v", err)
	}
	got := ""
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-d" {
			got = argv[i+1]
			break
		}
	}
	if want := strconv.Itoa(urlsDefaultDepth); got != want {
		t.Errorf("default depth = %q, want %q", got, want)
	}

	if _, err := urlsArgv(URLsArgs{Depth: -1}, targets, scope); err == nil ||
		!strings.Contains(err.Error(), "must be >= 0") {
		t.Fatalf("negative depth = %v, want a must-be->=0 error", err)
	}
}

func TestRunURLsDecodesJSONL(t *testing.T) {
	writeKatanaStub(t,
		`{"request":{"method":"GET","endpoint":"https://example.com/a","tag":"a","source":"https://example.com/"},"response":{"status_code":200}}`,
		`{"request":{"method":"POST","endpoint":"https://example.com/login","tag":"form","source":"https://example.com/"},"response":{"status_code":302}}`,
		`{"request":{"method":"GET","endpoint":"https://api.example.com/v1","tag":"script","source":"https://example.com/app.js"},"response":{"status_code":200}}`,
	)

	raw, err := Run(
		context.Background(),
		katanaSpec,
		[]string{"https://example.com"},
		Options{MaxEntries: 10},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(raw.Entries) != 3 {
		t.Fatalf("decoded %d entries, want 3", len(raw.Entries))
	}
	want := []struct {
		method string
		tag    string
	}{
		{method: "GET", tag: "a"},
		{method: "POST", tag: "form"},
		{method: "GET", tag: "script"},
	}
	for i, entry := range raw.Entries {
		request, ok := entry["request"].(map[string]any)
		if !ok {
			t.Fatalf("entry %d has no request object", i)
		}
		if got := request["method"]; got != want[i].method {
			t.Errorf("entry %d method = %v, want %q", i, got, want[i].method)
		}
		if got := request["tag"]; got != want[i].tag {
			t.Errorf("entry %d tag = %v, want %q", i, got, want[i].tag)
		}
	}

	result, err := RunURLs(context.Background(), URLsArgs{
		Targets:    []string{"https://example.com"},
		JSCrawl:    true,
		MaxEntries: 10,
		Authorized: true,
	})
	if err != nil {
		t.Fatalf("RunURLs: %v", err)
	}
	if result.Count != 3 {
		t.Fatalf("got %d entries, want 3", result.Count)
	}
	if result.OutOfScope != 0 {
		t.Errorf("out_of_scope = %d, want 0", result.OutOfScope)
	}
	if result.Entries[1].URL != "https://example.com/login" {
		t.Errorf("entry 1 url = %q", result.Entries[1].URL)
	}
	if result.Entries[1].Method != "POST" || result.Entries[1].Status != 302 {
		t.Errorf(
			"entry 1 = %+v, want POST 302",
			result.Entries[1],
		)
	}
}

func TestURLsReportDeduplicatesAndCaps(t *testing.T) {
	t.Parallel()

	raw := []map[string]any{
		{"request": map[string]any{
			"method":   "GET",
			"endpoint": "https://example.com/a",
		}},
		{
			"request": map[string]any{
				"method":   "",
				"endpoint": "https://example.com/a",
			},
			"response": map[string]any{"status_code": float64(204)},
		},
		{"request": map[string]any{
			"method":   "GET",
			"endpoint": "https://other.test/x",
		}},
		{"url": "https://example.com/b"},
		{"request": map[string]any{"endpoint": "/relative"}},
	}

	tests := []struct {
		name       string
		max        int
		wantCount  int
		wantDrop   int
		wantOut    int
		wantTrunc  int
		wantMethod string
		wantStatus int
	}{
		{
			name:       "dedupe and cap",
			max:        1,
			wantCount:  1,
			wantDrop:   1,
			wantOut:    1,
			wantTrunc:  1,
			wantMethod: "GET",
			wantStatus: 204,
		},
		{
			name:       "dedupe without cap",
			max:        10,
			wantCount:  2,
			wantDrop:   0,
			wantOut:    1,
			wantTrunc:  0,
			wantMethod: "GET",
			wantStatus: 204,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			report := urlsReport(raw, []string{"example.com"}, tt.max)
			if report.Count != tt.wantCount {
				t.Fatalf("count = %d, want %d", report.Count, tt.wantCount)
			}
			if report.Dropped != tt.wantDrop {
				t.Errorf("dropped = %d, want %d", report.Dropped, tt.wantDrop)
			}
			if report.OutOfScope != tt.wantOut {
				t.Errorf("out_of_scope = %d, want %d", report.OutOfScope, tt.wantOut)
			}
			if report.Truncated != tt.wantTrunc {
				t.Errorf("truncated = %d, want %d", report.Truncated, tt.wantTrunc)
			}
			if report.Entries[0].Method != tt.wantMethod {
				t.Errorf("method = %q, want %q", report.Entries[0].Method, tt.wantMethod)
			}
			if report.Entries[0].Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", report.Entries[0].Status, tt.wantStatus)
			}
		})
	}
}

// writeKatanaStub installs an offline fake katana under a temporary config dir,
// so Run resolves it through the private GOBIN and never touches the network.
func writeKatanaStub(t *testing.T, lines ...string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	dir := filepath.Join(base, "browser-mcp", "recon", "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create stub dir: %v", err)
	}

	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("if [ \"$1\" = \"-version\" ]; then echo 'katana v1.8.0'; exit 0; fi\n")
	script.WriteString("printf '%s\\n'")
	for _, line := range lines {
		script.WriteString(" '" + line + "'")
	}
	script.WriteString("\n")

	path := filepath.Join(dir, "katana")
	if err := os.WriteFile(path, []byte(script.String()), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
}
