package recon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/config"
)

func TestRunSubdomainsRequiresAuthorized(t *testing.T) {
	_, err := RunSubdomains(context.Background(), SubdomainsArgs{Target: "example.com"})
	requireInvalidArgs(t, err, "authorized:true is required")
}

func TestRunSubdomainsRequiresTarget(t *testing.T) {
	_, err := RunSubdomains(context.Background(), SubdomainsArgs{Authorized: true})
	requireInvalidArgs(t, err, "target is required")
}

func TestRunSubdomainsRefusesRelativeOutputFile(t *testing.T) {
	_, err := RunSubdomains(context.Background(), SubdomainsArgs{
		Target:     "example.com",
		Authorized: true,
		OutputFile: "report.jsonl",
	})
	requireInvalidArgs(t, err, "must be absolute")
}

func TestRunSubdomainsDecodesStub(t *testing.T) {
	installSubfinderStub(
		t,
		"printf '%s\\n' '{\"host\":\"api.example.com\",\"input\":\"example.com\",\"source\":\"crtsh\"}' "+
			"'{\"host\":\"api.example.com\",\"input\":\"example.com\",\"source\":\"github\"}'",
	)

	result, err := RunSubdomains(context.Background(), SubdomainsArgs{
		Target:     "example.com",
		AllSources: true,
		Authorized: true,
	})
	if err != nil {
		t.Fatalf("RunSubdomains: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d, want 1 grouped host", result.Count)
	}
	if result.OutOfScope != 0 {
		t.Errorf("out_of_scope = %d, want 0", result.OutOfScope)
	}
	want := []string{"crtsh", "github"}
	if got := result.Entries[0].Sources; !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

func TestBuildSubdomainsGroupsFiltersAndCaps(t *testing.T) {
	raw := []byte(
		"{\"host\":\"api.example.com\",\"source\":\"crtsh\"}\n" +
			"{\"host\":\"api.example.com\",\"source\":\"github\"}\n" +
			"{\"host\":\"outside.test\",\"source\":\"crtsh\"}\n" +
			"{\"host\":\"www.example.com\",\"sources\":[\"crtsh\",\"virustotal\"]}\n",
	)

	result := buildSubdomains(raw, []string{"example.com"}, 1, "")
	if result.Count != 1 || result.Truncated != 1 || result.Dropped != 1 {
		t.Fatalf(
			"count/truncated/dropped = %d/%d/%d, want 1/1/1",
			result.Count,
			result.Truncated,
			result.Dropped,
		)
	}
	if result.OutOfScope != 1 {
		t.Errorf("out_of_scope = %d, want 1", result.OutOfScope)
	}
	first := result.Entries[0]
	if first.Host != "api.example.com" {
		t.Errorf("first host = %q, want api.example.com", first.Host)
	}
	if want := []string{"crtsh", "github"}; !reflect.DeepEqual(first.Sources, want) {
		t.Errorf("first sources = %v, want %v", first.Sources, want)
	}
}

func installSubfinderStub(t *testing.T, body string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := filepath.Join(cfg, config.DirName, "recon", "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create stub bin dir: %v", err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-version\" ]; then\n" +
		"  echo 'subfinder v2.17.0'\n" +
		"  exit 0\n" +
		"fi\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, subfinderSpec.BinName), []byte(script), 0o755); err != nil {
		t.Fatalf("write subfinder stub: %v", err)
	}
}

func requireInvalidArgs(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("error %q does not say invalid arguments", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}
