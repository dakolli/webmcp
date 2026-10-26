package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProxyCredentials(t *testing.T) {
	t.Run("missing file is empty", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		creds, err := LoadProxyCredentials()
		if err != nil {
			t.Fatalf("LoadProxyCredentials() error = %v", err)
		}
		if len(creds) != 0 {
			t.Fatalf("LoadProxyCredentials() = %v, want empty", creds)
		}
	})

	t.Run("reads credentials keyed by lowercase host", func(t *testing.T) {
		writeProxyFile(t, `{"proxies":{"Proxy.Example.com:8080":{"username":"u","password":"p"}}}`)

		creds, err := LoadProxyCredentials()
		if err != nil {
			t.Fatalf("LoadProxyCredentials() error = %v", err)
		}
		cred, ok := creds["proxy.example.com:8080"]
		if !ok {
			t.Fatalf("LoadProxyCredentials() = %v, want proxy.example.com:8080", creds)
		}
		if cred.Username != "u" || cred.Password != "p" {
			t.Fatalf("credential = %+v, want u/p", cred)
		}
	})

	t.Run("rejects a group-readable file", func(t *testing.T) {
		path := writeProxyFile(t, `{"proxies":{}}`)
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatalf("chmod: %v", err)
		}

		_, err := LoadProxyCredentials()
		if err == nil {
			t.Fatal("LoadProxyCredentials() error = nil, want a mode error")
		}
		if !strings.Contains(err.Error(), "0600") || !strings.Contains(err.Error(), path) {
			t.Fatalf("error = %q, want mode 0600 and path %q", err, path)
		}
	})
}

func writeProxyFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, DirName)
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(cfgDir, "proxy.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write proxy.json: %v", err)
	}
	return path
}
