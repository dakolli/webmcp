package recon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFuzzTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		args     FuzzArgs
		want     string
		wantMode fuzzMode
		wantErr  string
	}{
		{
			name:     "dir valid",
			args:     FuzzArgs{URL: "https://example.com/FUZZ"},
			want:     "https://example.com/FUZZ",
			wantMode: fuzzDir,
		},
		{
			name:    "dir missing marker",
			args:    FuzzArgs{URL: "https://example.com/"},
			wantErr: "dir mode requires",
		},
		{
			name:    "dir marker in host",
			args:    FuzzArgs{Mode: "dir", URL: "https://FUZZ.example.com/"},
			wantErr: "dir mode requires",
		},
		{
			name:     "dir marker in query",
			args:     FuzzArgs{URL: "https://example.com/?q=FUZZ"},
			want:     "https://example.com/?q=FUZZ",
			wantMode: fuzzDir,
		},
		{
			name:     "vhost valid",
			args:     FuzzArgs{URL: "https://FUZZ.example.com"},
			want:     "https://FUZZ.example.com",
			wantMode: fuzzVhost,
		},
		{
			name:    "vhost marker in path",
			args:    FuzzArgs{Mode: "vhost", URL: "https://example.com/FUZZ"},
			wantErr: "vhost mode requires",
		},
		{
			name:     "param via params",
			args:     FuzzArgs{URL: "https://example.com/search", Params: "q"},
			want:     "https://example.com/search?q=FUZZ",
			wantMode: fuzzParam,
		},
		{
			name:     "param merges existing query",
			args:     FuzzArgs{URL: "https://example.com/search?x=1", Params: "q"},
			want:     "https://example.com/search?q=FUZZ&x=1",
			wantMode: fuzzParam,
		},
		{
			name:    "param without params or marker",
			args:    FuzzArgs{Mode: "param", URL: "https://example.com/search"},
			wantErr: "param mode requires",
		},
		{
			name:    "params outside param mode",
			args:    FuzzArgs{Mode: "dir", URL: "https://example.com/FUZZ", Params: "q"},
			wantErr: "params is only valid in param mode",
		},
		{
			name:    "unknown mode",
			args:    FuzzArgs{Mode: "scan", URL: "https://example.com/FUZZ"},
			wantErr: `mode "scan" is not dir, vhost, or param`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, mode, err := resolveFuzzTarget(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got error %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveFuzzTarget: %v", err)
			}
			if got != tc.want {
				t.Fatalf("target = %q, want %q", got, tc.want)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tc.wantMode)
			}
		})
	}
}

func TestFuzzHost(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"https://example.com/FUZZ":      "example.com",
		"https://FUZZ.example.com":      "example.com",
		"https://api.FUZZ.example.com/": "api.example.com",
		"https://EXAMPLE.com/FUZZ":      "example.com",
	}
	for raw, want := range cases {
		if got := fuzzHost(raw); got != want {
			t.Errorf("fuzzHost(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestFuzzArgv(t *testing.T) {
	t.Parallel()

	argv := fuzzArgv(
		FuzzArgs{
			Headers:    []string{"X-Test: 1", " "},
			FilterSize: "0",
			Rate:       500,
			Threads:    0,
			ExtraArgs:  []string{"-ac"},
		},
		"https://example.com/FUZZ",
		"/tmp/words.txt",
		"/tmp/report.json",
	)
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"-u https://example.com/FUZZ",
		"-w /tmp/words.txt",
		"-of json",
		"-o /tmp/report.json",
		"-X GET",
		"-H X-Test: 1",
		"-mc " + fuzzDefaultMatchCodes,
		"-fs 0",
		"-rate 100",
		"-t 40",
		"-ac",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q does not contain %q", joined, want)
		}
	}
}

func TestParseFFUFReport(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"results":[
		{"input":{"FFUFHASH":"768201","FUZZ":"admin"},"status":200,"length":42,"words":7,"lines":2,"content-type":"text/html","redirectlocation":"/login","duration":1500000,"url":"https://example.com/admin"},
		{"input":{"FUZZ":"api"},"status":403,"length":9,"words":1,"url":"https://example.com/api"},
		{"input":{"FUZZ":"skip"},"status":404,"length":0,"words":0,"url":""}
	]}`)

	entries, err := parseFFUFReport(raw)
	if err != nil {
		t.Fatalf("parseFFUFReport: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (the blank row is filtered)", len(entries))
	}

	first := entries[0]
	if first["url"] != "https://example.com/admin" {
		t.Errorf("url = %v", first["url"])
	}
	if first["status"] != 200 {
		t.Errorf("status = %v", first["status"])
	}
	if first["length"] != 42 {
		t.Errorf("length = %v", first["length"])
	}
	if first["words"] != 7 {
		t.Errorf("words = %v", first["words"])
	}
	if first["content_type"] != "text/html" {
		t.Errorf("content_type = %v", first["content_type"])
	}
	if first["redirect"] != "/login" {
		t.Errorf("redirect = %v", first["redirect"])
	}
	if first["duration_ms"] != int64(1) {
		t.Errorf("duration_ms = %v", first["duration_ms"])
	}
	if first["input"] != "admin" {
		t.Errorf("input = %v", first["input"])
	}

	second := entries[1]
	if second["status"] != 403 {
		t.Errorf("second status = %v", second["status"])
	}
	if _, ok := second["lines"]; ok {
		t.Errorf("lines should be omitted when zero")
	}
}

func TestFirstFFUFInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input map[string]string
		want  string
	}{
		{name: "empty map", input: nil, want: ""},
		{
			name:  "prefers FUZZ over FFUFHASH",
			input: map[string]string{"FFUFHASH": "768201", "FUZZ": "admin"},
			want:  "admin",
		},
		{name: "ignores FFUFHASH fallback", input: map[string]string{"FFUFHASH": "768201"}, want: ""},
		{name: "custom keyword", input: map[string]string{"KEYWORD": "value"}, want: "value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := firstFFUFInput(tc.input); got != tc.want {
				t.Fatalf("firstFFUFInput(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseFFUFReportRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	if _, err := parseFFUFReport([]byte(`{"results":`)); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestExecuteFFUFStub(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stub := filepath.Join(dir, "ffuf")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    cat > "$1" <<'JSON'
{"results":[
  {"input":{"FUZZ":"admin"},"status":200,"length":42,"words":7,"url":"https://example.com/admin"},
  {"input":{"FUZZ":"api"},"status":403,"length":9,"words":1,"url":"https://example.com/api"}
]}
JSON
    exit 0
  fi
  shift
done
exit 1
`
	if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	report := filepath.Join(dir, "report.json")
	result, err := executeFFUF(
		context.Background(),
		stub,
		FuzzArgs{MaxEntries: 1},
		"https://example.com/FUZZ",
		"/tmp/words.txt",
		report,
	)
	if err != nil {
		t.Fatalf("executeFFUF: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d, want 1", result.Count)
	}
	if result.Truncated != 1 || result.Dropped != 1 {
		t.Fatalf("truncated/dropped = %d/%d, want 1/1", result.Truncated, result.Dropped)
	}
}

func TestResolveWordlist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if _, _, err := resolveWordlist("relative.txt"); err == nil {
		t.Fatal("expected a relative wordlist to be refused")
	}
	if _, _, err := resolveWordlist(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Fatal("expected a missing wordlist to be refused")
	}

	path, cleanup, err := resolveWordlist("")
	if err != nil {
		t.Fatalf("resolveWordlist: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated wordlist: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("generated wordlist mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated wordlist: %v", err)
	}
	if !bytes.Contains(data, []byte("admin\n")) || !bytes.Contains(data, []byte(".git\n")) {
		t.Errorf("generated wordlist is missing expected entries")
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("generated wordlist still exists after cleanup: %v", err)
	}
}
