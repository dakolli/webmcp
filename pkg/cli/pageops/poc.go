package pageops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"webmcp/pkg/browsers"
	"webmcp/pkg/version"
)

const (
	// pocFileMaxBytes caps every text artifact at 2 MB; the screenshot is the
	// documented exception because image size scales with the viewport.
	pocFileMaxBytes = 2 << 20
	// pocDirMode and pocFileMode keep the evidence directory private.
	pocDirMode  = 0o700
	pocFileMode = 0o600
	// pocTruncatedMarker closes a text file cut at pocFileMaxBytes.
	pocTruncatedMarker = "\n[poc_bundle: truncated at 2 MB]\n"
)

// PocFile is one artifact written into a PoC bundle.
type PocFile struct {
	// Name is the file's base name.
	Name string `json:"name"`
	// Bytes is the written size.
	Bytes int64 `json:"bytes"`
}

// PocOmission records an artifact or value the bundle could not write.
type PocOmission struct {
	// Name is the missing file or file part.
	Name string `json:"name"`
	// Reason is why it was left out.
	Reason string `json:"reason"`
}

// PocBundleResult is the poc_bundle result: the directory, its files and every
// artifact left out.
type PocBundleResult struct {
	// Directory is the absolute output directory.
	Directory string `json:"directory"`
	// Files are the written artifacts with their byte counts.
	Files []PocFile `json:"files"`
	// Omitted are the artifacts or values left out.
	Omitted []PocOmission `json:"omitted"`
}

// pocVersion is the build identity written into a bundle: the stamped release
// version, or version.String() for a dev build so evidence names the build
// instead of claiming a release.
func pocVersion() string {
	if version.Version == "dev" {
		return version.String()
	}
	return version.Version
}

// pocMeta is the meta.json document.
type pocMeta struct {
	URL           string `json:"url"`
	Title         string `json:"title"`
	Timestamp     string `json:"timestamp"`
	Finding       string `json:"finding"`
	WebMCPVersion string `json:"web_mcp_version"`
	SessionID     string `json:"session_id"`
}

// pocPage is the page surface a bundle gathers from. *browsers.Page is the
// only production implementation; tests supply a fake.
type pocPage interface {
	sessionID() string
	secretsCaptured() bool
	state(ctx context.Context) (url, title string, err error)
	screenshot(ctx context.Context) ([]byte, error)
	html(ctx context.Context) (string, error)
	storage(ctx context.Context) (string, error)
	sinks(ctx context.Context) (report string, installed bool, err error)
	networkEntries() []browsers.NetworkEntry
	responseBody(ctx context.Context, seq int64) (browsers.NetworkBody, error)
	requestBody(ctx context.Context, seq int64) (browsers.NetworkBody, error)
}

// pageBundleSource adapts *browsers.Page to the bundle surface, calling the
// shared pageops seams so every read keeps its own page-lock boundary.
type pageBundleSource struct {
	page *browsers.Page
}

func (s *pageBundleSource) sessionID() string {
	return string(s.page.ID)
}

func (s *pageBundleSource) secretsCaptured() bool {
	return s.page.SecretsCaptured()
}

func (s *pageBundleSource) state(ctx context.Context) (string, string, error) {
	st, err := PageStateOf(ctx, s.page)
	return st.URL, st.Title, err
}

func (s *pageBundleSource) screenshot(ctx context.Context) ([]byte, error) {
	uri, err := PageScreenshot(ctx, s.page, false)
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}
	return b, nil
}

func (s *pageBundleSource) html(ctx context.Context) (string, error) {
	return PageHTML(ctx, s.page, "")
}

func (s *pageBundleSource) storage(ctx context.Context) (string, error) {
	return PageStorage(ctx, s.page, s.page.SecretsCaptured())
}

func (s *pageBundleSource) sinks(ctx context.Context) (string, bool, error) {
	report, err := PageSinks(ctx, s.page)
	if err != nil {
		return "", false, err
	}
	var probe struct {
		Installed bool `json:"installed"`
	}
	if err := json.Unmarshal([]byte(report), &probe); err != nil {
		return "", false, fmt.Errorf("decode sink report: %w", err)
	}
	return report, probe.Installed, nil
}

func (s *pageBundleSource) networkEntries() []browsers.NetworkEntry {
	entries, _ := s.page.NetworkEntries()
	return entries
}

func (s *pageBundleSource) responseBody(
	ctx context.Context,
	seq int64,
) (browsers.NetworkBody, error) {
	return s.page.NetworkBody(ctx, seq)
}

func (s *pageBundleSource) requestBody(
	ctx context.Context,
	seq int64,
) (browsers.NetworkBody, error) {
	return s.page.NetworkRequestBody(ctx, seq)
}

// PagePocBundle turns the live page into a packaged evidence directory. value
// is the finding title, paths[0] is the absolute output directory, and target
// optionally selects the captured request by seq or URL substring. The result
// is the PocBundleResult JSON envelope.
func PagePocBundle(
	ctx context.Context,
	p *browsers.Page,
	finding string,
	paths []string,
	target string,
) (string, error) {
	if strings.TrimSpace(finding) == "" {
		return "", fmt.Errorf("poc_bundle: finding title is required (pass value=<title>)")
	}
	if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
		return "", fmt.Errorf("poc_bundle: output directory is required (pass paths=[<absolute directory>])")
	}
	result, err := bundlePoc(ctx, &pageBundleSource{page: p}, finding, paths[0], target)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("poc_bundle: encode result: %w", err)
	}
	return string(b), nil
}

// bundlePoc validates the destination, gathers every artifact into memory, then
// writes the directory. No page lock is held across a file write.
func bundlePoc(
	ctx context.Context,
	src pocPage,
	finding string,
	dir string,
	target string,
) (PocBundleResult, error) {
	if !filepath.IsAbs(dir) {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: %q must be absolute", dir)
	}
	if err := preparePocDir(dir); err != nil {
		return PocBundleResult{}, err
	}

	url, title, err := src.state(ctx)
	if err != nil {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: read page state: %w", err)
	}
	secrets := src.secretsCaptured()

	shot, err := src.screenshot(ctx)
	if err != nil {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: capture screenshot: %w", err)
	}
	html, err := src.html(ctx)
	if err != nil {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: read DOM: %w", err)
	}
	storage, err := src.storage(ctx)
	if err != nil {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: read storage: %w", err)
	}
	sinks, sinksInstalled, err := src.sinks(ctx)
	if err != nil {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: read sinks: %w", err)
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	files := make([]PocFile, 0, 8)
	omitted := make([]PocOmission, 0, 4)

	meta, err := json.MarshalIndent(pocMeta{
		URL:           url,
		Title:         title,
		Timestamp:     stamp,
		Finding:       finding,
		WebMCPVersion: pocVersion(),
		SessionID:     src.sessionID(),
	}, "", "  ")
	if err != nil {
		return PocBundleResult{}, fmt.Errorf("poc_bundle: encode meta: %w", err)
	}
	if err := appendPocFile(dir, "meta.json", append(meta, '\n'), &files); err != nil {
		return PocBundleResult{}, err
	}

	files, omitted, err = writePocExchange(ctx, src, dir, target, url, secrets, files, omitted)
	if err != nil {
		return PocBundleResult{}, err
	}

	if err := appendPocFile(dir, "screenshot.png", shot, &files); err != nil {
		return PocBundleResult{}, err
	}

	dom, truncated := capPocText([]byte(html))
	if err := appendPocFile(dir, "dom.html", dom, &files); err != nil {
		return PocBundleResult{}, err
	}
	if truncated {
		omitted = append(omitted, PocOmission{Name: "dom.html body", Reason: "cap reached"})
	}

	if err := appendPocFile(dir, "storage.json", []byte(storage), &files); err != nil {
		return PocBundleResult{}, err
	}

	if sinksInstalled {
		if err := appendPocFile(dir, "sinks.json", []byte(sinks), &files); err != nil {
			return PocBundleResult{}, err
		}
	} else {
		omitted = append(omitted, PocOmission{Name: "sinks.json", Reason: "not installed"})
	}

	readme := pocReadme(finding, url, stamp, files)
	if err := appendPocFile(dir, "README.md", readme, &files); err != nil {
		return PocBundleResult{}, err
	}

	return PocBundleResult{Directory: dir, Files: files, Omitted: omitted}, nil
}

// writePocExchange writes request.http and response.http for target, or the
// page's main document when target is empty.
func writePocExchange(
	ctx context.Context,
	src pocPage,
	dir string,
	target string,
	pageURL string,
	secrets bool,
	files []PocFile,
	omitted []PocOmission,
) ([]PocFile, []PocOmission, error) {
	entry, ok := selectPocEntry(src.networkEntries(), target, pageURL)
	if !ok {
		omitted = append(omitted,
			PocOmission{Name: "request.http", Reason: "no network entry"},
			PocOmission{Name: "response.http", Reason: "no network entry"},
		)
		return files, omitted, nil
	}

	reqBody := browsers.NetworkBody{}
	var reqMissing bool
	if body, err := src.requestBody(ctx, entry.Seq); err == nil {
		reqBody = body
	} else {
		reqMissing = true
	}

	respBody := browsers.NetworkBody{}
	var respMissing bool
	if body, err := src.responseBody(ctx, entry.Seq); err == nil {
		respBody = body
	} else {
		respMissing = true
	}

	redacted := false
	request := buildPocRequest(entry, reqBody, secrets, &redacted)
	if err := appendPocFile(dir, "request.http", capPocRaw(request), &files); err != nil {
		return files, omitted, err
	}
	if reqMissing || pocBodyEmpty(reqBody) {
		omitted = append(omitted, PocOmission{Name: "request.http body", Reason: "no body"})
	}

	response := buildPocResponse(entry, respBody, secrets, &redacted)
	if err := appendPocFile(dir, "response.http", capPocRaw(response), &files); err != nil {
		return files, omitted, err
	}
	if respMissing || pocBodyEmpty(respBody) {
		omitted = append(omitted, PocOmission{Name: "response.http body", Reason: "no body"})
	}

	if redacted {
		omitted = append(omitted, PocOmission{Name: "secret values", Reason: "secrets hidden"})
	}
	return files, omitted, nil
}

// preparePocDir creates an absolute destination that must be absent or empty.
func preparePocDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("poc_bundle: %s is not a directory — pass a new directory or remove that file", dir)
	case err == nil:
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return fmt.Errorf("poc_bundle: read %s: %w", dir, readErr)
		}
		if len(entries) > 0 {
			return fmt.Errorf("poc_bundle: %s is not empty — pass a new directory or remove its contents", dir)
		}
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("poc_bundle: inspect %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, pocDirMode); err != nil {
		return fmt.Errorf("poc_bundle: create %s: %w", dir, err)
	}
	return nil
}

// appendPocFile writes one artifact with O_EXCL so an existing file is never
// overwritten, and records its size.
func appendPocFile(
	dir string,
	name string,
	data []byte,
	files *[]PocFile,
) error {
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, pocFileMode)
	if err != nil {
		return fmt.Errorf("poc_bundle: write %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("poc_bundle: write %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("poc_bundle: close %s: %w", name, err)
	}
	*files = append(*files, PocFile{Name: name, Bytes: int64(len(data))})
	return nil
}

// selectPocEntry resolves target to one captured entry: a seq number, or the
// newest URL substring match. An empty target selects the main document.
func selectPocEntry(
	entries []browsers.NetworkEntry,
	target string,
	pageURL string,
) (browsers.NetworkEntry, bool) {
	if len(entries) == 0 {
		return browsers.NetworkEntry{}, false
	}
	if target != "" {
		if seq, err := strconv.ParseInt(target, 10, 64); err == nil {
			for _, e := range entries {
				if e.Seq == seq {
					return e, true
				}
			}
			return browsers.NetworkEntry{}, false
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if strings.Contains(entries[i].URL, target) {
				return entries[i], true
			}
		}
		return browsers.NetworkEntry{}, false
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].TopDocument {
			return entries[i], true
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].URL == pageURL {
			return entries[i], true
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "Document" {
			return entries[i], true
		}
	}
	return entries[len(entries)-1], true
}

// buildPocRequest renders the raw request line, headers and body.
func buildPocRequest(
	entry browsers.NetworkEntry,
	body browsers.NetworkBody,
	secrets bool,
	redacted *bool,
) []byte {
	var b strings.Builder
	method := entry.Method
	if method == "" {
		method = http.MethodGet
	}
	fmt.Fprintf(&b, "%s %s %s\n", method, pocRequestPath(entry.URL), pocHTTPVersion(entry.Protocol))
	writePocHeaders(&b, entry.ReqHeaders, secrets, redacted)
	b.WriteString("\n")
	b.Write(pocBodyBytes(body))
	return []byte(b.String())
}

// buildPocResponse renders the raw status line, headers and body.
func buildPocResponse(
	entry browsers.NetworkEntry,
	body browsers.NetworkBody,
	secrets bool,
	redacted *bool,
) []byte {
	var b strings.Builder
	status := entry.Status
	text := http.StatusText(status)
	if text == "" {
		fmt.Fprintf(&b, "%s %d\n", pocHTTPVersion(entry.Protocol), status)
	} else {
		fmt.Fprintf(&b, "%s %d %s\n", pocHTTPVersion(entry.Protocol), status, text)
	}
	writePocHeaders(&b, entry.RespHeaders, secrets, redacted)
	b.WriteString("\n")
	b.Write(pocBodyBytes(body))
	return []byte(b.String())
}

// writePocHeaders writes sorted headers, replacing sensitive values unless the
// page opted into secret capture.
func writePocHeaders(
	b *strings.Builder,
	headers map[string]string,
	secrets bool,
	redacted *bool,
) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := headers[name]
		if !secrets && browsers.IsSensitiveHeader(name) {
			value = browsers.RedactedValue
			*redacted = true
		}
		fmt.Fprintf(b, "%s: %s\n", name, value)
	}
}

// pocRequestPath returns the request-target: path plus query, or "/".
func pocRequestPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := u.RequestURI()
	if path == "" {
		return "/"
	}
	return path
}

// pocHTTPVersion normalizes CDP's protocol onto an HTTP/1.1-style token.
func pocHTTPVersion(protocol string) string {
	switch strings.ToLower(protocol) {
	case "h2", "http/2", "http/2.0":
		return "HTTP/2"
	case "h3", "http/3", "http/3.0":
		return "HTTP/3"
	case "http/1.0":
		return "HTTP/1.0"
	default:
		return "HTTP/1.1"
	}
}

// pocBodyBytes decodes a captured body, preferring raw base64 content.
func pocBodyBytes(body browsers.NetworkBody) []byte {
	if body.Base64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(body.Base64); err == nil {
			return raw
		}
	}
	return []byte(body.Body)
}

// pocBodyEmpty reports whether a captured body carries no payload.
func pocBodyEmpty(body browsers.NetworkBody) bool {
	return body.Body == "" && body.Base64 == ""
}

// capPocText truncates text at pocFileMaxBytes on a rune boundary and appends
// the trailing truncation comment.
func capPocText(data []byte) ([]byte, bool) {
	marker := []byte("\n<!-- truncated -->\n")
	limit := pocFileMaxBytes - len(marker)
	if len(data) <= limit {
		return data, false
	}
	cut := data[:limit]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return append(cut, marker...), true
}

// capPocRaw truncates a raw HTTP file at pocFileMaxBytes with a text marker.
func capPocRaw(data []byte) []byte {
	marker := []byte(pocTruncatedMarker)
	limit := pocFileMaxBytes - len(marker)
	if len(data) <= limit {
		return data
	}
	cut := data[:limit]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return append(cut, marker...)
}

// pocReadme renders the bundle index listing every earlier artifact.
func pocReadme(finding, pageURL, stamp string, files []PocFile) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", finding)
	fmt.Fprintf(&b, "- URL: %s\n", pageURL)
	fmt.Fprintf(&b, "- Captured: %s\n", stamp)
	fmt.Fprintf(&b, "- web-mcp: %s\n\n", pocVersion())
	b.WriteString("| File | Bytes |\n")
	b.WriteString("| :--- | ---: |\n")
	for _, f := range files {
		fmt.Fprintf(&b, "| %s | %d |\n", f.Name, f.Bytes)
	}
	return []byte(b.String())
}
