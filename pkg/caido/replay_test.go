package caido

import (
	"encoding/base64"
	"strings"
	"testing"
)

// replayRawNode wraps a captured message as the base64 raw node the replay
// rewrites.
func replayRawNode(t *testing.T, message string) replaySourceNode {
	t.Helper()
	return replaySourceNode{
		ID:    "1",
		Host:  "example.test",
		Port:  443,
		IsTLS: true,
		Raw:   base64.StdEncoding.EncodeToString([]byte(message)),
	}
}

// decodeReplayRaw unwraps the base64 message a rewrite returns.
func decodeReplayRaw(t *testing.T, encoded string) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode rewritten raw: %v", err)
	}
	return string(decoded)
}

// TestRewriteReplayRawBodyFraming pins the body framing the rewrite emits after
// a body override.
func TestRewriteReplayRawBodyFraming(t *testing.T) {
	body := func(s string) *string { return &s }
	cases := []struct {
		name         string
		raw          string
		body         *string
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:         "body change recomputes content-length",
			raw:          "POST /items HTTP/1.1\r\nHost: example.test\r\nContent-Length: 3\r\n\r\nold",
			body:         body("replacement"),
			wantContains: []string{"Content-Length: 11\r\n", "\r\n\r\nreplacement"},
			wantAbsent:   []string{"Content-Length: 3\r\n", "old"},
		},
		{
			name:         "body change drops transfer-encoding",
			raw:          "POST /items HTTP/1.1\r\nHost: example.test\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
			body:         body("new"),
			wantContains: []string{"Content-Length: 3\r\n", "\r\n\r\nnew"},
			wantAbsent:   []string{"Transfer-Encoding"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := rewriteReplayRaw(replayRawNode(t, tc.raw), ReplayRequest{
				RequestID: "1",
				Body:      tc.body,
			})
			if err != nil {
				t.Fatalf("rewriteReplayRaw() error = %v", err)
			}
			got := decodeReplayRaw(t, out)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("rewritten message %q does not contain %q", got, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("rewritten message %q still contains %q", got, absent)
				}
			}
		})
	}
}

// TestRewriteReplayRawLFOnly pins that an LF-only capture separates the head from
// the body instead of folding the body into the head.
func TestRewriteReplayRawLFOnly(t *testing.T) {
	body := "replacement"
	out, err := rewriteReplayRaw(
		replayRawNode(t, "POST /items HTTP/1.1\nHost: example.test\nContent-Length: 3\n\nold"),
		ReplayRequest{RequestID: "1", Body: &body},
	)
	if err != nil {
		t.Fatalf("rewriteReplayRaw() error = %v", err)
	}
	got := decodeReplayRaw(t, out)
	if !strings.HasPrefix(got, "POST /items HTTP/1.1\r\nHost: example.test\r\n") {
		t.Errorf("rewritten message %q has a mangled head", got)
	}
	if !strings.HasSuffix(got, "\r\n\r\nreplacement") {
		t.Errorf("rewritten message %q has a mangled body", got)
	}
	if !strings.Contains(got, "Content-Length: 11\r\n") {
		t.Errorf("rewritten message %q does not recompute Content-Length", got)
	}
	if strings.Contains(got, "old") {
		t.Errorf("rewritten message %q keeps the captured body", got)
	}
}

// TestReplayTargetQuery pins how query edits splice into the raw query.
func TestReplayTargetQuery(t *testing.T) {
	cases := []struct {
		name   string
		target string
		edits  map[string]string
		want   string
	}{
		{
			name:   "set replaces duplicates in place",
			target: "/items?a=1&b=2&b=3&c=4",
			edits:  map[string]string{"b": "9"},
			want:   "/items?a=1&b=9&c=4",
		},
		{
			name:   "del removes every duplicate",
			target: "/items?a=1&b=2&b=3",
			edits:  map[string]string{"b": ""},
			want:   "/items?a=1",
		},
		{
			name:   "untouched keeps order and encoding",
			target: "/items?z=%20x&a=1&z2=a%2Bb",
			edits:  map[string]string{"a": "2"},
			want:   "/items?z=%20x&a=2&z2=a%2Bb",
		},
		{
			name:   "new parameter appended encoded",
			target: "/items?a=1",
			edits:  map[string]string{"b": "hello world"},
			want:   "/items?a=1&b=hello+world",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := replayTargetQuery(tc.target, tc.edits)
			if err != nil {
				t.Fatalf("replayTargetQuery() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("replayTargetQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}
