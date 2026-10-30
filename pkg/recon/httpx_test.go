package recon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pythonHTTPXHelp is the real Python httpx CLI help text, which names "httpx"
// but never the projectdiscovery organization.
const pythonHTTPXHelp = `Usage: httpx [OPTIONS] URL

  A next generation HTTP client for Python.

Options:
  -version  Show the version and exit.
`

// projectDiscoveryBanner is the banner projectdiscovery httpx prints on -version.
const projectDiscoveryBanner = `
    __    __  __       _  __
   / /_  / /_/ /_____ | |/ /
  / __ \/ __/ __/ __ \|   /
 / / / / /_/ /_/ /_/ /   |
/_/ /_/\__/ .___/_/|_|

		projectdiscovery.io

Current Version: v1.12.0
`

// stubVersion writes a shell script that prints body regardless of its arguments.
func stubVersion(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "httpx")
	script := "#!/bin/sh\ncat <<'WM_VERSION_EOF'\n" + body + "WM_VERSION_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestVerifyHTTPSpecRejectsPythonHelp(t *testing.T) {
	path := stubVersion(t, pythonHTTPXHelp)
	err := verifyHTTPSpec(context.Background(), path)
	if err == nil {
		t.Fatal("verifyHTTPSpec accepted a binary that never names projectdiscovery")
	}
	for _, want := range []string{
		path,
		"Usage: httpx [OPTIONS] URL",
		httpxSpec.Module,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestVerifyHTTPSpecAcceptsProjectDiscoveryBanner(t *testing.T) {
	path := stubVersion(t, projectDiscoveryBanner)
	if err := verifyHTTPSpec(context.Background(), path); err != nil {
		t.Fatalf("verifyHTTPSpec rejected the projectdiscovery banner: %v", err)
	}
}

func TestHTTPRegistered(t *testing.T) {
	spec, ok := Get(HTTPID)
	if !ok {
		t.Fatal("recon_http is not registered")
	}
	if spec.BinName != "httpx" || spec.Pin != httpxPin {
		t.Errorf("spec = %+v, want httpx %s", spec, httpxPin)
	}
}

const httpSampleJSONL = `{"url":"https://a.example.com","status_code":200,"title":"Alpha","tech":["nginx","HTTP/3"],"tls":{"subject_dn":"CN=a.example.com","issuer_dn":"CN=Test CA","not_after":"2027-01-01T00:00:00Z"}}
{"url":"https://b.example.com","status_code":301,"title":"Beta"}`

func TestHTTPXArgsPassesEveryTargetWithU(t *testing.T) {
	args := httpxArgs(
		[]string{"https://a.example.com", "https://b.example.com"},
		HTTPOptions{StatusCode: true, Title: true},
	)
	targets := map[string]bool{
		"https://a.example.com": false,
		"https://b.example.com": false,
	}
	for i, arg := range args {
		if _, ok := targets[arg]; !ok {
			continue
		}
		if i == 0 || args[i-1] != "-u" {
			t.Fatalf("target %q at %d is not preceded by -u: %v", arg, i, args)
		}
		targets[arg] = true
	}
	for target, seen := range targets {
		if !seen {
			t.Fatalf("target %q never appears after -u: %v", target, args)
		}
	}
}

func TestBuildHTTPResult(t *testing.T) {
	t.Run("compacts and drops out of scope", func(t *testing.T) {
		entries, truncated, dropped := decodeEntries([]byte(httpSampleJSONL), 200)
		if truncated != 0 || dropped != 0 {
			t.Fatalf("truncated=%d dropped=%d, want 0/0", truncated, dropped)
		}
		result := buildHTTPResult(Result{
			Entries:   entries,
			Truncated: truncated,
			Dropped:   dropped,
		}, []string{"a.example.com"}, HTTPOptions{
			StatusCode: true,
			Title:      true,
			Tech:       true,
			TLS:        true,
		})
		if result.Count != 1 || result.OutOfScope != 1 {
			t.Fatalf("count=%d out_of_scope=%d, want 1/1", result.Count, result.OutOfScope)
		}
		got := result.Entries[0]
		if got.URL != "https://a.example.com" || got.Status != 200 || got.Title != "Alpha" {
			t.Errorf("entry = %+v", got)
		}
		if len(got.Tech) != 2 || got.Tech[0] != "nginx" || got.Tech[1] != "HTTP/3" {
			t.Errorf("tech = %v, want [nginx HTTP/3]", got.Tech)
		}
		if got.TLS == nil || got.TLS.Subject != "CN=a.example.com" || got.TLS.Expiry != "2027-01-01T00:00:00Z" {
			t.Errorf("tls = %+v", got.TLS)
		}
	})

	t.Run("omits unrequested fields", func(t *testing.T) {
		entries, _, _ := decodeEntries([]byte(httpSampleJSONL), 200)
		result := buildHTTPResult(Result{Entries: entries}, []string{"example.com"}, HTTPOptions{Title: true})
		if len(result.Entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(result.Entries))
		}
		for _, entry := range result.Entries {
			if entry.Status != 0 || entry.Tech != nil || entry.TLS != nil {
				t.Errorf("entry %q carries an unrequested field: %+v", entry.URL, entry)
			}
		}
	})

	t.Run("clips a long title", func(t *testing.T) {
		long := strings.Repeat("x", httpTitleClip+100)
		entries, _, _ := decodeEntries(
			[]byte(`{"url":"https://a.example.com","title":"`+long+`"}`),
			200,
		)
		result := buildHTTPResult(Result{Entries: entries}, []string{"example.com"}, HTTPOptions{Title: true})
		if got := result.Entries[0].Title; len(got) > httpTitleClip+len("…") {
			t.Errorf("title length = %d, want <= %d", len(got), httpTitleClip+len("…"))
		}
	})
}
