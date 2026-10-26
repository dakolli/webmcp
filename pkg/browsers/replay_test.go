package browsers

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatchReplayEntry(t *testing.T) {
	entries := []NetworkEntry{
		{Seq: 1, Method: "GET", URL: "https://api.example.com/users/1"},
		{Seq: 2, Method: "PUT", URL: "https://api.example.com/users/2"},
		{Seq: 3, Method: "GET", URL: "https://api.example.com/orders"},
	}
	tests := []struct {
		name    string
		target  string
		wantSeq int64
		wantErr []string
	}{
		{name: "seq picks one entry", target: "2", wantSeq: 2},
		{name: "unique substring picks one entry", target: "orders", wantSeq: 3},
		{name: "ambiguous substring is refused", target: "/users/", wantErr: []string{"matches 2 network entries", "seq=1", "seq=2"}},
		{name: "unknown seq", target: "9", wantErr: []string{"no network entry with seq 9"}},
		{name: "unknown substring", target: "missing", wantErr: []string{"no network entry matches"}},
		{name: "blank target", target: "  ", wantErr: []string{"replay requires target"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := matchReplayEntry(entries, tt.target)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("matchReplayEntry() error = %v, want nil", err)
				}
				if entry.Seq != tt.wantSeq {
					t.Fatalf("matchReplayEntry() seq = %d, want %d", entry.Seq, tt.wantSeq)
				}
				return
			}
			if err == nil {
				t.Fatalf("matchReplayEntry() = nil error, want %q", tt.wantErr)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("matchReplayEntry() error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func TestReplayScope(t *testing.T) {
	var checked string
	refused := ReplaySpec{CheckScope: func(rawURL string) error {
		checked = rawURL
		return errors.New("host is outside the scope allowlist")
	}}
	err := replayScope(refused, "https://evil.example/")
	if err == nil || !strings.Contains(err.Error(), "https://evil.example/") {
		t.Fatalf("replayScope() error = %v, want a refusal naming the target", err)
	}
	if checked != "https://evil.example/" {
		t.Fatalf("replayScope() checked = %q, want the target URL", checked)
	}
	if err := replayScope(ReplaySpec{}, "https://any.example/"); err != nil {
		t.Fatalf("replayScope() without a check = %v, want nil", err)
	}
}

func TestReplayHeaderShaping(t *testing.T) {
	captured := map[string]string{
		"Host":            "api.example.com",
		"Cookie":          "session=abc",
		"Content-Length":  "42",
		"Accept-Encoding": "gzip, br",
		"Connection":      "keep-alive",
		"Authorization":   redactedValue,
		"Content-Type":    "application/json",
		"X-Trace":         "abc",
	}
	override := map[string]string{
		"authorization": "Bearer token",
		"x-test":        "1",
	}

	t.Run("drops client-owned and redacted captured values", func(t *testing.T) {
		h := replayHeaders(captured, nil)
		for _, name := range []string{"Host", "Cookie", "Content-Length", "Accept-Encoding", "Connection", "Authorization"} {
			if got := h.Get(name); got != "" {
				t.Fatalf("replayHeaders(): %s = %q, want dropped", name, got)
			}
		}
		if got := h.Get("Content-Type"); got != "application/json" {
			t.Fatalf("replayHeaders(): Content-Type = %q, want captured", got)
		}
		if got := h.Get("X-Trace"); got != "abc" {
			t.Fatalf("replayHeaders(): X-Trace = %q, want captured", got)
		}
	})

	t.Run("overrides win over captured values", func(t *testing.T) {
		h := replayHeaders(captured, override)
		if got := h.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("replayHeaders(): Authorization = %q, want override", got)
		}
		if got := h.Get("X-Test"); got != "1" {
			t.Fatalf("replayHeaders(): X-Test = %q, want override", got)
		}
	})

	t.Run("echo and response redact sensitive names unless capture keeps them", func(t *testing.T) {
		sent := http.Header{"Authorization": {"Bearer token"}, "X-Trace": {"abc"}}
		echo := replayEchoHeaders(sent, false)
		if got := echo["Authorization"]; got != redactedValue {
			t.Fatalf("replayEchoHeaders(): Authorization = %q, want %q", got, redactedValue)
		}
		if got := echo["X-Trace"]; got != "abc" {
			t.Fatalf("replayEchoHeaders(): X-Trace = %q, want abc", got)
		}
		if got := replayEchoHeaders(sent, true)["Authorization"]; got != "Bearer token" {
			t.Fatalf("replayEchoHeaders(keep): Authorization = %q, want kept", got)
		}

		resp := http.Header{"Set-Cookie": {"session=abc"}, "Content-Type": {"text/plain"}}
		redacted := replayResponseHeaders(resp, false)
		if got := redacted["Set-Cookie"]; len(got) != 1 || got[0] != redactedValue {
			t.Fatalf("replayResponseHeaders(): Set-Cookie = %v, want [%s]", got, redactedValue)
		}
		if got := redacted["Content-Type"]; len(got) != 1 || got[0] != "text/plain" {
			t.Fatalf("replayResponseHeaders(): Content-Type = %v, want [text/plain]", got)
		}
		if got := replayResponseHeaders(resp, true)["Set-Cookie"]; len(got) != 1 || got[0] != "session=abc" {
			t.Fatalf("replayResponseHeaders(keep): Set-Cookie = %v, want kept", got)
		}
	})
}

func TestReadReplayBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantBody  string
		wantBytes int64
		wantCut   bool
		wantB64   bool
	}{
		{name: "plain text", body: "hello", wantBody: "hello", wantBytes: 5},
		{name: "binary is base64", body: "\xff\xfe", wantBytes: 2, wantB64: true},
		{name: "oversized body is capped", body: strings.Repeat("a", replayBodyCap+10), wantBody: strings.Repeat("a", replayBodyCap), wantBytes: replayBodyCap, wantCut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tt.body))}
			got, err := readReplayBody(resp)
			if err != nil {
				t.Fatalf("readReplayBody() error = %v", err)
			}
			if got.Body != tt.wantBody {
				t.Fatalf("readReplayBody() body length = %d, want %d", len(got.Body), len(tt.wantBody))
			}
			if got.Bytes != tt.wantBytes {
				t.Fatalf("readReplayBody() bytes = %d, want %d", got.Bytes, tt.wantBytes)
			}
			if got.Truncated != tt.wantCut {
				t.Fatalf("readReplayBody() truncated = %t, want %t", got.Truncated, tt.wantCut)
			}
			if (got.Base64 != "") != tt.wantB64 {
				t.Fatalf("readReplayBody() base64 = %q, want base64=%t", got.Base64, tt.wantB64)
			}
		})
	}
}

func TestCapReplayBodyRuneBoundary(t *testing.T) {
	raw := append([]byte(strings.Repeat("a", replayBodyCap-1)), []byte("é")...)
	cut, truncated := capReplayBody(raw)
	if !truncated {
		t.Fatal("capReplayBody() truncated = false, want true")
	}
	if len(cut) != replayBodyCap-1 {
		t.Fatalf("capReplayBody() length = %d, want %d", len(cut), replayBodyCap-1)
	}
	if !utf8.Valid(cut) {
		t.Fatal("capReplayBody() cut a rune in half")
	}
}
