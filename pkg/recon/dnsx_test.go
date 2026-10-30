package recon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDNSRecordTypes(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{name: "default", in: nil, want: []string{"a", "cname"}},
		{name: "dedup and lowercase", in: []string{"A", "aaaa", "a"}, want: []string{"a", "aaaa"}},
		{name: "canonical order", in: []string{"txt", "mx"}, want: []string{"mx", "txt"}},
		{name: "unknown names the fix", in: []string{"zzz"}, wantErr: "zzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DNSRecordTypes(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("DNSRecordTypes(%v) = %v, %v; want an error naming %q", tc.in, got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DNSRecordTypes(%v): %v", tc.in, err)
			}
			if joined, want := strings.Join(got, ","), strings.Join(tc.want, ","); joined != want {
				t.Errorf("DNSRecordTypes(%v) = %q, want %q", tc.in, joined, want)
			}
		})
	}
}

func TestDNSWordlistPath(t *testing.T) {
	if _, err := dnsWordlistPath("words.txt"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative wordlist = %v, want an absolute-path error", err)
	}
	if _, err := dnsWordlistPath(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Fatal("missing wordlist was accepted")
	}

	dir := t.TempDir()
	if _, err := dnsWordlistPath(dir); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory wordlist = %v, want a directory error", err)
	}

	words := filepath.Join(dir, "words.txt")
	if err := os.WriteFile(words, []byte("www\napi\n"), 0o600); err != nil {
		t.Fatalf("write wordlist: %v", err)
	}
	got, err := dnsWordlistPath(words)
	if err != nil {
		t.Fatalf("valid wordlist: %v", err)
	}
	if got != words {
		t.Errorf("wordlist = %q, want %q", got, words)
	}
}

func TestBuildDNSArgsResolve(t *testing.T) {
	args, cleanup, err := buildDNSArgs(DNSOptions{
		Targets:   []string{"example.com", "api.example.com"},
		Resolvers: []string{"1.1.1.1", "8.8.8.8"},
		Threads:   25,
	}, []string{"a", "mx"})
	if err != nil {
		t.Fatalf("buildDNSArgs: %v", err)
	}

	joined := strings.Join(args, " ")
	for _, want := range []string{"-l ", "-a", "-mx", "-r 1.1.1.1,8.8.8.8", "-t 25"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q do not contain %q", joined, want)
		}
	}

	listPath := dnsFlagValue(args, "-l")
	if listPath == "" {
		t.Fatal("resolve args carry no -l list path")
	}
	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatalf("read target list: %v", err)
	}
	if !strings.Contains(string(data), "api.example.com") {
		t.Errorf("target list %q does not hold api.example.com", data)
	}

	cleanup()
	if _, err := os.Stat(listPath); !os.IsNotExist(err) {
		t.Errorf("cleanup left target list %s", listPath)
	}
}

func TestBuildDNSArgsBrute(t *testing.T) {
	words := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(words, []byte("www\n"), 0o600); err != nil {
		t.Fatalf("write wordlist: %v", err)
	}

	args, cleanup, err := buildDNSArgs(DNSOptions{
		Domain:   "example.com",
		Wordlist: words,
	}, []string{"a", "cname"})
	if err != nil {
		t.Fatalf("buildDNSArgs: %v", err)
	}
	defer cleanup()

	joined := strings.Join(args, " ")
	for _, want := range []string{"-d example.com", "-w " + words, "-a", "-cname"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q do not contain %q", joined, want)
		}
	}
}

func TestBuildDNSArgsRejectsMixedModes(t *testing.T) {
	words := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(words, []byte("www\n"), 0o600); err != nil {
		t.Fatalf("write wordlist: %v", err)
	}

	_, _, err := buildDNSArgs(DNSOptions{
		Domain:   "example.com",
		Wordlist: words,
		Targets:  []string{"api.example.com"},
	}, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("mixed modes = %v, want a cannot-be-combined error", err)
	}
}

func TestBuildDNSArgsOverCap(t *testing.T) {
	targets := make([]string, MaxHosts+1)
	for i := range targets {
		targets[i] = "host" + strconv.Itoa(i) + ".example.com"
	}

	_, _, err := buildDNSArgs(DNSOptions{Targets: targets}, []string{"a"})
	if err == nil {
		t.Fatal("over-cap target list was accepted")
	}
	for _, want := range []string{"501", "output_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestCompactDNS(t *testing.T) {
	entries := []map[string]any{
		{
			"host":        "www.example.com",
			"a":           []any{},
			"cname":       []any{"example.net."},
			"status_code": "NOERROR",
			"resolver":    []any{"1.1.1.1:53"},
			"raw":         "a large DNS dump",
		},
		{
			"host":        "nope.example.com",
			"a":           []any{},
			"status_code": "NXDOMAIN",
			"resolver":    []any{"1.1.1.1:53"},
		},
		{"raw": "not json at all"},
	}

	got := compactDNS(entries, []string{"a", "cname"})
	if len(got) != len(entries) {
		t.Fatalf("compactDNS returned %d entries, want %d", len(got), len(entries))
	}

	first := got[0]
	if _, ok := first["raw"]; ok {
		t.Error("compactDNS kept dnsx's raw dump")
	}
	if _, ok := first["a"]; ok {
		t.Error("compactDNS kept an empty record array")
	}
	if first["cname"] == nil {
		t.Error("compactDNS dropped the non-empty cname")
	}

	if got[1]["status_code"] != "NXDOMAIN" {
		t.Errorf("NXDOMAIN line status_code = %v, want NXDOMAIN", got[1]["status_code"])
	}
	if got[2]["raw"] != "not json at all" {
		t.Errorf("non-JSON line raw = %v, want the preserved line", got[2]["raw"])
	}
}

func TestCompactDNSNullMX(t *testing.T) {
	t.Parallel()

	got := compactDNS([]map[string]any{
		{"host": "null.example.com", "mx": []any{"", ""}, "status_code": "NOERROR"},
		{"host": "mail.example.com", "mx": []any{"mail.example.com."}, "status_code": "NOERROR"},
	}, []string{"mx"})
	if len(got) != 2 {
		t.Fatalf("compactDNS returned %d entries, want 2", len(got))
	}

	if got[0]["mx_null"] != true {
		t.Errorf("null MX marker = %v, want true", got[0]["mx_null"])
	}
	if _, ok := got[0]["mx"]; ok {
		t.Error("null MX kept the blank mx value")
	}

	targets, ok := got[1]["mx"].([]any)
	if !ok || len(targets) != 1 || targets[0] != "mail.example.com." {
		t.Errorf("non-null MX = %v, want the mail target", got[1]["mx"])
	}
}

func TestRunDNSWithStub(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := BinDir()
	if err != nil {
		t.Fatalf("BinDir: %v", err)
	}
	script := `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "dnsx v1.3.1"
  exit 0
fi
printf '%s\n' '{"host":"a.example.com","a":["1.2.3.4"],"cname":[],"status_code":"NOERROR","resolver":["8.8.8.8:53"],"raw":"dump"}'
printf '%s\n' '{"host":"b.example.com","a":[],"cname":["target.example.net."],"status_code":"NOERROR","resolver":["8.8.8.8:53"],"raw":"dump"}'
`
	if err := os.WriteFile(filepath.Join(dir, "dnsx"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	result, err := RunDNS(context.Background(), DNSOptions{
		Targets:     []string{"a.example.com", "b.example.com"},
		RecordTypes: []string{"a", "cname"},
	})
	if err != nil {
		t.Fatalf("RunDNS: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("count = %d, want 2", result.Count)
	}
	if _, ok := result.Entries[0]["a"]; !ok {
		t.Error("first entry has no a record")
	}
	if _, ok := result.Entries[0]["cname"]; ok {
		t.Error("first entry kept an empty cname")
	}
	if _, ok := result.Entries[1]["cname"]; !ok {
		t.Error("second entry has no cname record")
	}
	if _, ok := result.Entries[0]["raw"]; ok {
		t.Error("compaction kept the raw dump")
	}
}

func dnsFlagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
