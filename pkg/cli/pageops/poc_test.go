package pageops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
)

type fakePocPage struct {
	url            string
	title          string
	shot           []byte
	htmlDoc        string
	storageReport  string
	sinkReport     string
	sinksInstalled bool
	entries        []browsers.NetworkEntry
	reqBody        browsers.NetworkBody
	respBody       browsers.NetworkBody
	secrets        bool
}

func (f *fakePocPage) sessionID() string { return "page-9" }

func (f *fakePocPage) secretsCaptured() bool { return f.secrets }

func (f *fakePocPage) state(context.Context) (string, string, error) {
	return f.url, f.title, nil
}

func (f *fakePocPage) screenshot(context.Context) ([]byte, error) { return f.shot, nil }

func (f *fakePocPage) html(context.Context) (string, error) { return f.htmlDoc, nil }

func (f *fakePocPage) storage(context.Context) (string, error) { return f.storageReport, nil }

func (f *fakePocPage) sinks(context.Context) (string, bool, error) {
	return f.sinkReport, f.sinksInstalled, nil
}

func (f *fakePocPage) networkEntries() []browsers.NetworkEntry { return f.entries }

func (f *fakePocPage) responseBody(context.Context, int64) (browsers.NetworkBody, error) {
	return f.respBody, nil
}

func (f *fakePocPage) requestBody(context.Context, int64) (browsers.NetworkBody, error) {
	return f.reqBody, nil
}

func newFakePocPage() *fakePocPage {
	return &fakePocPage{
		url:            "https://example.test/search?q=x",
		title:          "Search",
		shot:           []byte("\x89PNG\r\n\x1a\n"),
		htmlDoc:        "<html><body><h1>ok</h1></body></html>",
		storageReport:  `{"cookies":[],"local_storage":[]}`,
		sinkReport:     `{"installed":true,"hits":[]}`,
		sinksInstalled: true,
		entries: []browsers.NetworkEntry{{
			Method:      "POST",
			URL:         "https://example.test/search?q=x",
			Type:        "Document",
			Status:      200,
			Protocol:    "http/1.1",
			Seq:         1,
			TopDocument: true,
			ReqHeaders: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
			},
			RespHeaders: map[string]string{
				"Content-Type": "text/html; charset=utf-8",
			},
		}},
		reqBody:  browsers.NetworkBody{Seq: 1, Body: "q=x"},
		respBody: browsers.NetworkBody{Seq: 1, Body: "<html>result</html>"},
	}
}

func TestPocBundleRefusesRelativePath(t *testing.T) {
	t.Parallel()
	_, err := PagePocBundle(context.Background(), nil, "finding", []string{"poc-1"}, "")
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("got err %v, want absolute-path refusal", err)
	}
}

func TestPocBundleRefusesNonEmptyDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := bundlePoc(context.Background(), newFakePocPage(), "finding", dir, "")
	if err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("got err %v, want non-empty-directory refusal", err)
	}
}

func TestPocBundleWritesEveryFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "poc")
	res, err := bundlePoc(context.Background(), newFakePocPage(), "Reflected XSS", dir, "")
	if err != nil {
		t.Fatalf("bundlePoc: %v", err)
	}

	want := []string{
		"meta.json", "request.http", "response.http", "screenshot.png",
		"dom.html", "storage.json", "sinks.json", "README.md",
	}
	if len(res.Files) != len(want) {
		t.Fatalf("files = %v, want %v", res.Files, want)
	}
	for i, name := range want {
		if res.Files[i].Name != name {
			t.Errorf("files[%d] = %q, want %q", i, res.Files[i].Name, name)
		}
		if res.Files[i].Bytes == 0 {
			t.Errorf("%s has zero bytes", name)
		}
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			t.Errorf("stat %s: %v", name, statErr)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}

	request, err := os.ReadFile(filepath.Join(dir, "request.http"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(request), "POST /search?q=x HTTP/1.1\n") {
		t.Errorf("request line = %q", strings.SplitN(string(request), "\n", 2)[0])
	}
	if !strings.Contains(string(request), "q=x") {
		t.Error("request body missing from request.http")
	}

	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "# Reflected XSS") {
		t.Errorf("README missing finding title:\n%s", readme)
	}
}

func TestPocBundleOmitsSinksWhenNotInstalled(t *testing.T) {
	t.Parallel()
	fake := newFakePocPage()
	fake.sinksInstalled = false
	fake.sinkReport = `{"installed":false,"hits":[]}`

	dir := filepath.Join(t.TempDir(), "poc")
	res, err := bundlePoc(context.Background(), fake, "finding", dir, "")
	if err != nil {
		t.Fatalf("bundlePoc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sinks.json")); !os.IsNotExist(err) {
		t.Fatalf("sinks.json exists, want omitted (stat err %v)", err)
	}
	found := false
	for _, o := range res.Omitted {
		if o.Name == "sinks.json" && o.Reason == "not installed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("omitted = %v, want sinks.json/not installed", res.Omitted)
	}
}

func TestPocBundleRedactsSensitiveHeaders(t *testing.T) {
	t.Parallel()
	fake := newFakePocPage()
	fake.entries[0].ReqHeaders["Cookie"] = "session=live-token"
	fake.entries[0].ReqHeaders["Authorization"] = "Bearer live-token"

	dir := filepath.Join(t.TempDir(), "poc")
	res, err := bundlePoc(context.Background(), fake, "finding", dir, "")
	if err != nil {
		t.Fatalf("bundlePoc: %v", err)
	}
	request, err := os.ReadFile(filepath.Join(dir, "request.http"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(request), "live-token") {
		t.Fatalf("request.http leaked a secret:\n%s", request)
	}
	if !strings.Contains(string(request), "Cookie: <redacted>") {
		t.Errorf("request.http did not redact Cookie:\n%s", request)
	}
	found := false
	for _, o := range res.Omitted {
		if o.Reason == "secrets hidden" {
			found = true
		}
	}
	if !found {
		t.Errorf("omitted = %v, want a secrets-hidden row", res.Omitted)
	}
}

func TestPocBundleCapsDOM(t *testing.T) {
	t.Parallel()
	fake := newFakePocPage()
	fake.htmlDoc = strings.Repeat("a", pocFileMaxBytes+16)

	dir := filepath.Join(t.TempDir(), "poc")
	res, err := bundlePoc(context.Background(), fake, "finding", dir, "")
	if err != nil {
		t.Fatalf("bundlePoc: %v", err)
	}
	dom, err := os.ReadFile(filepath.Join(dir, "dom.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(dom), "<!-- truncated -->\n") {
		t.Errorf("dom.html lacks the truncation marker (len %d)", len(dom))
	}
	found := false
	for _, o := range res.Omitted {
		if o.Name == "dom.html body" && o.Reason == "cap reached" {
			found = true
		}
	}
	if !found {
		t.Errorf("omitted = %v, want a cap-reached row", res.Omitted)
	}
}
