package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestReconRunRejectsUndeclaredJSONKey asserts the CLI hands --json to the tool
// unfiltered, so an argument the schema does not declare fails with the same
// sentence MCP returns instead of being silently decoded away.
func TestReconRunRejectsUndeclaredJSONKey(t *testing.T) {
	cmd := newReconRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"recon_run",
		"--authorized",
		"--json",
		`{"domain":"example.com","nonsense":1}`,
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("recon run accepted an undeclared argument; output %q", out.String())
	}
	if !strings.Contains(err.Error(), `unknown argument "nonsense"`) {
		t.Errorf("error %q does not name the undeclared key", err)
	}
	if !strings.Contains(err.Error(), "valid arguments are") {
		t.Errorf("error %q does not list the valid arguments", err)
	}
}

// TestReconRunRejectsUndeclaredJSONKeyOnScanner asserts the shared gate covers a
// typed scanner too, not only recon_run, so the CLI never silently drops a key
// the scanner's schema does not declare.
func TestReconRunRejectsUndeclaredJSONKeyOnScanner(t *testing.T) {
	cmd := newReconRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"recon_subdomains",
		"--authorized",
		"--json",
		`{"targets":["example.com"],"nonsense":1}`,
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("recon run accepted an undeclared argument; output %q", out.String())
	}
	if !strings.Contains(err.Error(), `unknown argument "nonsense"`) {
		t.Errorf("error %q does not name the undeclared key", err)
	}
	if !strings.Contains(err.Error(), "valid arguments are") {
		t.Errorf("error %q does not list the valid arguments", err)
	}
}

// TestToolFailureNamesTheCallingTool asserts a guard refusal is retargeted onto
// the tool that made the call while its message body and hint stay intact.
func TestToolFailureNamesTheCallingTool(t *testing.T) {
	body := `recon: invalid arguments: host "example.com" is outside the scope allowlist (allowed: scanme.sh) — add it to scope`
	want := `recon_run: invalid arguments: host "example.com" is outside the scope allowlist (allowed: scanme.sh) — add it to scope`
	if got := toolFailure("recon_run", body); got != want {
		t.Fatalf("toolFailure = %q, want %q", got, want)
	}
}
