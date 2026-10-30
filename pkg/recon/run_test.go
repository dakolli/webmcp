package recon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeEntries(t *testing.T) {
	raw := []byte(
		"{\"host\":\"a.example.com\",\"port\":443}\n" +
			"\n" +
			"{\"host\":\"b.example.com\"}\n" +
			"not json at all\n",
	)
	entries, truncated, dropped := decodeEntries(raw, 200)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if truncated != 0 || dropped != 0 {
		t.Errorf("truncated=%d dropped=%d, want 0/0", truncated, dropped)
	}
	if entries[0]["host"] != "a.example.com" {
		t.Errorf("first entry host = %v", entries[0]["host"])
	}
	if entries[2]["raw"] != "not json at all" {
		t.Errorf("non-JSON line = %v, want a raw field", entries[2])
	}
}

func TestDecodeEntriesCapsAndDrops(t *testing.T) {
	raw := []byte("1\n2\n3\n4\n5\n")
	entries, truncated, dropped := decodeEntries(raw, 3)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if truncated != 1 {
		t.Errorf("truncated = %d, want 1", truncated)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

func TestExitFailureKeepsStdoutDiagnosticsOutOfEntries(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'flag provided but not defined: -no-such-flag\\n'; exit 2")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatal("stub exited 0, want a non-zero exit")
	}

	err := exitFailure(Spec{ID: "recon_ports"}, stdout.Bytes(), stderr.Bytes(), runErr)
	if err == nil {
		t.Fatal("a non-zero exit with stdout text was treated as success")
	}
	for _, want := range []string{"recon_ports", "failed", "flag provided but not defined"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestExitFailureNilWhenRunSucceeds(t *testing.T) {
	if err := exitFailure(Spec{ID: "recon_ports"}, []byte("line"), nil, nil); err != nil {
		t.Fatalf("exitFailure on a clean exit = %v, want nil", err)
	}
}

func TestEmptyOutputError(t *testing.T) {
	strict := Spec{ID: "recon_http", BinName: "httpx", RequireOutput: true}
	lenient := Spec{ID: "recon_templates", BinName: "nuclei"}
	tests := []struct {
		name       string
		spec       Spec
		args       []string
		raw        string
		runErr     error
		outputFile string
		wantErr    bool
		wantParts  []string
	}{
		{
			name:    "strict scanner names its targets and both causes",
			spec:    strict,
			args:    []string{"-u", "http://example.com:1"},
			wantErr: true,
			wantParts: []string{
				"http://example.com:1",
				"exited 0 with no output",
				"answered nothing on that port",
				"never consumed them",
				"extra_args",
			},
		},
		{
			name:    "strict scanner with whitespace only fails",
			spec:    strict,
			raw:     "  \n\t\n",
			wantErr: true,
		},
		{
			name:    "strict scanner with one entry passes",
			spec:    strict,
			raw:     `{"url":"https://example.com"}`,
			wantErr: false,
		},
		{
			name:       "strict scanner with output_file is exempt",
			spec:       strict,
			outputFile: "/tmp/report.jsonl",
			wantErr:    false,
		},
		{
			name:    "lenient scanner keeps an empty result",
			spec:    lenient,
			wantErr: false,
		},
		{
			name:    "a run error is reported by the caller",
			spec:    strict,
			runErr:  os.ErrPermission,
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := emptyOutputError(tc.spec, tc.args, []byte(tc.raw), tc.runErr, tc.outputFile)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, want := range tc.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestDedupeRunsBeforeCap(t *testing.T) {
	raw := []byte(
		"{\"host\":\"a\",\"port\":80}\n" +
			"{\"host\":\"a\",\"port\":80}\n" +
			"{\"host\":\"b\",\"port\":80}\n" +
			"{\"host\":\"b\",\"port\":80}\n" +
			"{\"host\":\"c\",\"port\":80}\n",
	)
	key := func(entry map[string]any) string {
		host, _ := entry["host"].(string)
		return host
	}
	entries, truncated, dropped := capEntries(
		dedupeEntries(parseEntries(raw), key),
		2,
	)
	if len(entries) != 2 || truncated != 1 || dropped != 1 {
		t.Fatalf("entries=%d truncated=%d dropped=%d, want 2/1/1", len(entries), truncated, dropped)
	}
}

func TestRunRefusesRelativeOutputFile(t *testing.T) {
	_, err := Run(context.Background(), Spec{ID: "recon_test"}, nil, Options{
		OutputFile: "relative.jsonl",
	})
	if err == nil {
		t.Fatal("Run accepted a relative output_file")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error %q does not say the path must be absolute", err)
	}
}

func TestWriteOutputMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.jsonl")
	if err := writeOutput(path, []byte("{\"ok\":true}\n")); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("file mode = %o, want 600", mode)
	}
}
