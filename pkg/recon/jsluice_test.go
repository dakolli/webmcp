package recon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testAWSKey = "AKIAIOSFODNN7EXAMPLE"

func TestRunJSLuiceInputValidation(t *testing.T) {
	tests := []struct {
		name string
		args JSLuiceArgs
		want string
	}{
		{
			name: "urls and js_files together fail",
			args: JSLuiceArgs{
				URLs:    []string{"https://example.com/app.js"},
				JSFiles: []string{"/tmp/app.js"},
			},
			want: "mutually exclusive",
		},
		{
			name: "neither input fails",
			args: JSLuiceArgs{},
			want: "one input is required",
		},
		{
			name: "relative js_files path fails",
			args: JSLuiceArgs{JSFiles: []string{"app.js"}},
			want: "must be an absolute path",
		},
		{
			name: "unknown mode fails",
			args: JSLuiceArgs{Mode: "tree", JSFiles: []string{"/tmp/app.js"}},
			want: "must be urls, secrets, or both",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RunJSLuice(t.Context(), tc.args, Options{})
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeJSLuiceURL(t *testing.T) {
	local := jsInput{file: "/tmp/app.js", local: true}
	entries := []map[string]any{
		{"url": "/rel/path", "method": "GET", "type": "fetch"},
		{"url": "https://api.example.com/v1/x", "method": "", "type": "stringLiteral"},
		{"note": "not a url"},
	}

	got := normalizeJSLuice("urls", entries, local)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0]["kind"] != "relative" || got[0]["relative"] != true {
		t.Fatalf("local relative entry = %+v, want kind relative with relative:true", got[0])
	}
	if got[1]["kind"] != "absolute" {
		t.Fatalf("absolute entry kind = %v, want absolute", got[1]["kind"])
	}
	if got[1]["context"] != "stringLiteral" {
		t.Fatalf("context = %v, want stringLiteral", got[1]["context"])
	}

	fetched := normalizeJSLuice("urls", entries[:1], jsInput{
		file:      "/tmp/fetched.js",
		sourceURL: "https://example.com/app.js",
	})
	if _, ok := fetched[0]["relative"]; ok {
		t.Fatalf("fetched entry must not carry relative:true: %+v", fetched[0])
	}
}

func TestNormalizeJSLuiceSecretRedacts(t *testing.T) {
	local := jsInput{file: "/tmp/app.js", local: true}
	entries := []map[string]any{
		{
			"kind":     "AWSAccessKey",
			"data":     map[string]any{"key": testAWSKey},
			"severity": "low",
		},
		{"kind": "Empty", "data": map[string]any{}},
	}

	got := normalizeJSLuice("secrets", entries, local)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	secret := got[0]
	if secret["redacted"] != true {
		t.Fatalf("redacted = %v, want true", secret["redacted"])
	}
	if secret["value_prefix"] != "AKIAIOSF" {
		t.Fatalf("value_prefix = %v, want AKIAIOSF", secret["value_prefix"])
	}
	if secret["value_length"] != len(testAWSKey) {
		t.Fatalf("value_length = %v, want %d", secret["value_length"], len(testAWSKey))
	}
	if strings.Contains(secret["value"].(string), testAWSKey) {
		t.Fatalf("value %q leaks the full secret", secret["value"])
	}
}

func TestRedactSecretNeverReturnsFullValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "short", value: "abc"},
		{name: "medium", value: "abcdefghij"},
		{name: "long", value: "abcdefghijklmnopqrstuvwxyz0123456789"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			display, prefix, length := redactSecret(tc.value)
			if length != len([]rune(tc.value)) {
				t.Fatalf("length = %d, want %d", length, len([]rune(tc.value)))
			}
			if display == tc.value {
				t.Fatalf("display %q equals the full value", display)
			}
			if !strings.Contains(display, "…") {
				t.Fatalf("display %q has no redaction marker", display)
			}
			if !strings.HasPrefix(tc.value, prefix) {
				t.Fatalf("prefix %q is not a prefix of %q", prefix, tc.value)
			}
		})
	}
}

func TestRunJSLuiceLocalFileWithStub(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	writeStub(t, filepath.Join(cfg, "browser-mcp", "recon", "bin", "jsluice"))
	fixture := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(fixture, []byte("const x='/rel';"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result, err := RunJSLuice(t.Context(), JSLuiceArgs{
		Mode:    "both",
		JSFiles: []string{fixture},
	}, Options{})
	if err != nil {
		t.Fatalf("RunJSLuice: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("count = %d, want 2", result.Count)
	}
	if result.Truncated != 0 || result.Dropped != 0 {
		t.Fatalf("truncated/dropped = %d/%d, want 0/0", result.Truncated, result.Dropped)
	}
	if strings.Contains(string(result.Raw), testAWSKey) {
		t.Fatal("raw report leaks the full secret")
	}
}

func writeStub(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then echo "jsluice stub"; exit 0; fi
case "$1" in
  urls) printf '{"url":"/rel/path","method":"GET","type":"fetch","filename":"%s"}\n' "$2" ;;
  secrets) printf '{"kind":"AWSAccessKey","data":{"key":"AKIAIOSFODNN7EXAMPLE"},"filename":"%s","severity":"low","context":null}\n' "$2" ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
}
