package recon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubBinary writes a shell script that prints output and exits 0.
func stubBinary(t *testing.T, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub")
	script := "#!/bin/sh\nprintf '%s\\n' " + "'" + output + "'" + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestInstallEnvCGO(t *testing.T) {
	off := installEnv("/opt/bin", false)
	if !hasEnv(off, "CGO_ENABLED=0") {
		t.Fatalf("CGO-disabled install env = %v, want CGO_ENABLED=0", off)
	}
	on := installEnv("/opt/bin", true)
	if !hasEnv(on, "CGO_ENABLED=1") {
		t.Fatalf("CGO-enabled install env = %v, want CGO_ENABLED=1", on)
	}
	if hasEnv(on, "CGO_ENABLED=0") {
		t.Fatalf("CGO-enabled install env still carries CGO_ENABLED=0: %v", on)
	}
}

func hasEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func TestInstallErrorIsOneActionableLine(t *testing.T) {
	spec := Spec{ID: "recon_ports"}
	output := "go: downloading github.com/x/y v1.0.0\n" +
		"go: finding module for package\n" +
		"requires go >= 1.26.10 (running go 1.26.4)\n" +
		"more noise\n"
	err := newInstallError(spec, "github.com/x/y@v2.7.0", output, errors.New("exit status 1"))

	if got := err.Error(); strings.Contains(got, "\n") {
		t.Fatalf("error is multi-line: %q", got)
	}
	for _, want := range []string{"recon_ports", "requires go >= 1.26.10"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "go: downloading") {
		t.Errorf("error kept download noise: %q", err)
	}
	if err.log != output {
		t.Errorf("installError dropped the full log")
	}

	var hinted interface{ Hint() string }
	if !errors.As(err, &hinted) {
		t.Fatal("installError carries no Hint")
	}
	for _, want := range []string{"browser recon list", "go version"} {
		if !strings.Contains(hinted.Hint(), want) {
			t.Errorf("hint %q does not name %q", hinted.Hint(), want)
		}
	}
}

func TestProbeRejectsWrongBinary(t *testing.T) {
	path := stubBinary(t, "imposter 9.9")
	spec := Spec{
		ID:          "recon_http",
		Module:      "github.com/projectdiscovery/httpx/cmd/httpx",
		VersionArgs: []string{"-version"},
	}
	err := probe(context.Background(), spec, path)
	if err == nil {
		t.Fatal("probe accepted a binary that never names the module")
	}
	for _, want := range []string{path, "imposter 9.9"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestProbeAcceptsMatchingBinary(t *testing.T) {
	path := stubBinary(t, "httpx version 1.6.0")
	spec := Spec{
		ID:          "recon_http",
		Module:      "github.com/projectdiscovery/httpx/cmd/httpx",
		VersionArgs: []string{"-version"},
	}
	if err := probe(context.Background(), spec, path); err != nil {
		t.Fatalf("probe rejected the matching binary: %v", err)
	}
}
