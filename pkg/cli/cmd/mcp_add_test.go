package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMcpAdd pins the harness-config merge: unrelated keys survive at every
// level, the original is backed up, a second run refuses, --force replaces,
// --remove deletes only the entry, and an unparsable file is refused by name.
func TestMcpAdd(t *testing.T) {
	t.Run("merge keeps every existing key and backs the file up", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "opencode.json")
		original := `{
  "theme": "dark",
  "mcp": {
    "other": {"type": "local", "command": ["other-server"]},
    "web-mcp": {"type": "remote", "url": "http://old/sse", "enabled": true}
  }
}`
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := runMcpAdd(t, "opencode", "--dir", path, "--url", "http://127.0.0.1:4099/sse", "--force"); err != nil {
			t.Fatalf("mcp add opencode: %v", err)
		}

		body := readFile(t, path)
		var got map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("merged file is not JSON: %v", err)
		}
		if got["theme"] != "dark" {
			t.Errorf("theme = %v, want dark", got["theme"])
		}
		mcp, _ := got["mcp"].(map[string]any)
		if _, ok := mcp["other"].(map[string]any); !ok {
			t.Errorf("unrelated mcp entry was dropped: %v", mcp)
		}
		entry, _ := mcp["web-mcp"].(map[string]any)
		if entry["url"] != "http://127.0.0.1:4099/sse" {
			t.Errorf("web-mcp url = %v", entry["url"])
		}
		if entry["enabled"] != true || entry["type"] != "remote" {
			t.Errorf("web-mcp entry = %v", entry)
		}
		if backup := readFile(t, path+".bak"); backup != original {
			t.Errorf("backup = %q, want the original file", backup)
		}
	})

	t.Run("second run refuses without force", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[other]\nkey = \"value\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := runMcpAdd(t, "codex", "--dir", path); err != nil {
			t.Fatalf("first mcp add codex: %v", err)
		}
		_, err := runMcpAdd(t, "codex", "--dir", path)
		if err == nil {
			t.Fatal("second mcp add codex = nil error, want refusal")
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "--force") {
			t.Errorf("refusal = %q, want the file path and --force", err)
		}
		if _, err := runMcpAdd(t, "codex", "--dir", path, "--force", "--url", "http://127.0.0.1:4089/sse"); err != nil {
			t.Fatalf("codex add --force: %v", err)
		}
		body := readFile(t, path)
		if !strings.Contains(body, "[other]") || !strings.Contains(body, `key = "value"`) {
			t.Errorf("unrelated TOML table was not kept:\n%s", body)
		}
		if strings.Count(body, "[mcp_servers.web-mcp]") != 1 {
			t.Errorf("want exactly one mcp table:\n%s", body)
		}
		if !strings.Contains(body, `url = "http://127.0.0.1:4089/sse"`) {
			t.Errorf("forced url missing from TOML:\n%s", body)
		}
	})

	t.Run("remove deletes only the entry", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "opencode.json")
		original := `{"theme":"dark","mcp":{"other":{"url":"http://other"},"web-mcp":{"type":"remote","url":"http://old","enabled":true}}}`
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := runMcpAdd(t, "opencode", "--dir", path, "--remove"); err != nil {
			t.Fatalf("mcp add --remove: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(readFile(t, path)), &got); err != nil {
			t.Fatal(err)
		}
		mcp, _ := got["mcp"].(map[string]any)
		if _, ok := mcp["web-mcp"]; ok {
			t.Errorf("web-mcp entry still present: %v", mcp)
		}
		if _, ok := mcp["other"]; !ok {
			t.Errorf("unrelated entry was removed: %v", mcp)
		}
		if got["theme"] != "dark" {
			t.Errorf("theme = %v, want dark", got["theme"])
		}
	})

	t.Run("unparsable config is refused by path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "opencode.json")
		if err := os.WriteFile(path, []byte(`{"theme":`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := runMcpAdd(t, "opencode", "--dir", path)
		if err == nil {
			t.Fatal("mcp add on an unparsable file = nil error, want refusal")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("refusal = %q, want it to name %s", err, path)
		}
		if body := readFile(t, path); body != `{"theme":` {
			t.Errorf("unparsable file was modified: %q", body)
		}
	})
}

// runMcpAdd executes `browser mcp add` with args and returns its output.
func runMcpAdd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewMcpCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"add"}, args...))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), err
}

// readFile reads path as a string, failing the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
