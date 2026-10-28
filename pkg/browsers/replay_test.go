package browsers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

// TestMatchReplayEntryBoundsAmbiguousCandidates pins the byte ceiling on the
// ambiguous-target error when a common substring matches sixty entries.
func TestMatchReplayEntryBoundsAmbiguousCandidates(t *testing.T) {
	entries := make([]NetworkEntry, 60)
	for i := range entries {
		entries[i] = NetworkEntry{
			Seq:    int64(i + 1),
			Method: "GET",
			URL:    fmt.Sprintf("https://bun.sh/docs/runtime/script-%02d.js", i+1),
		}
	}
	_, err := matchReplayEntry(entries, "bun.sh")
	if err == nil {
		t.Fatal("matchReplayEntry() error = nil, want the ambiguous target")
	}
	if !errors.Is(err, ErrReplayTargetAmbiguous) {
		t.Fatalf("matchReplayEntry() error = %v, want it to wrap ErrReplayTargetAmbiguous", err)
	}
	full := "page_act: replay_fuzz: " + err.Error()
	if len(full) >= 400 {
		t.Fatalf("ambiguous error is %d bytes, want under 400: %s", len(full), full)
	}
	if got := strings.Count(err.Error(), "seq="); got != replayCandidateLimit {
		t.Fatalf("candidate lines = %d, want %d", got, replayCandidateLimit)
	}
	for _, want := range []string{
		"matches 60 network entries",
		"pass a seq or a longer substring",
		"first 5",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
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
		limit     int64
		wantBody  string
		wantBytes int64
		wantCut   bool
		wantB64   bool
	}{
		{name: "plain text", body: "hello", limit: replayBodyCap, wantBody: "hello", wantBytes: 5},
		{name: "binary is base64", body: "\xff\xfe", limit: replayBodyCap, wantBytes: 2, wantB64: true},
		{name: "oversized body is capped at the ceiling", body: strings.Repeat("a", replayBodyCap+10), limit: replayBodyCap, wantBody: strings.Repeat("a", replayBodyCap), wantBytes: replayBodyCap, wantCut: true},
		{name: "requested cap is honoured", body: "0123456789", limit: 4, wantBody: "0123", wantBytes: 4, wantCut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tt.body))}
			got, err := readReplayBody(resp, tt.limit)
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
	cut, truncated := capReplayBody(raw, replayBodyCap)
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

func TestReplayBodyLimit(t *testing.T) {
	tests := []struct {
		name      string
		requested int64
		want      int64
	}{
		{name: "zero uses the ceiling", requested: 0, want: replayBodyCap},
		{name: "negative uses the ceiling", requested: -1, want: replayBodyCap},
		{name: "above the ceiling is clamped", requested: replayBodyCap + 1, want: replayBodyCap},
		{name: "below the ceiling is honoured", requested: 4096, want: 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replayBodyLimit(tt.requested); got != tt.want {
				t.Fatalf("replayBodyLimit(%d) = %d, want %d", tt.requested, got, tt.want)
			}
		})
	}
}

// TestReplayResultAlwaysEmitsEnvelopeFields pins that a clean replay always
// carries truncated, chain and body_redactions, and that an echoed cookie is
// redacted while bytes stays readable.
func TestReplayResultAlwaysEmitsEnvelopeFields(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"cookie":"wmsession=abc123def456","ok":true}`)),
	}
	got, err := replayResult(
		resp,
		[]ReplayHop{{Status: 200, URL: "https://example.test/"}},
		ReplaySent{Method: "GET", URL: "https://example.test/"},
		false,
		time.Now(),
		replayBodyCap,
	)
	if err != nil {
		t.Fatalf("replayResult() error = %v", err)
	}
	if strings.Contains(got.Body, "abc123def456") {
		t.Fatalf("replayResult() body leaks the cookie: %q", got.Body)
	}
	if !strings.Contains(got.Body, redactedValue) {
		t.Fatalf("replayResult() body = %q, want the redaction marker", got.Body)
	}
	if got.BodyRedactions != 1 {
		t.Fatalf("replayResult() body_redactions = %d, want 1", got.BodyRedactions)
	}
	if got.Bytes <= 0 {
		t.Fatalf("replayResult() bytes = %d, want readable", got.Bytes)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal replay result: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal replay result: %v", err)
	}
	for _, key := range []string{"truncated", "chain", "body_redactions", "bytes"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("replay result is missing %q: %s", key, raw)
		}
	}
	if truncated, _ := doc["truncated"].(bool); truncated {
		t.Fatalf("clean replay truncated = %v, want false", doc["truncated"])
	}
	chain, ok := doc["chain"].([]any)
	if !ok {
		t.Fatalf("clean replay chain = %v, want an array", doc["chain"])
	}
	if len(chain) != 0 {
		t.Fatalf("clean replay chain = %v, want []", chain)
	}
}

// TestRedactBody pins the body redactor against the shapes a replay echoes back:
// a JSON cookie field, a session-id object key, a bearer token and a form pair.
func TestRedactBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      string
		wantCount int
		keep      bool
	}{
		{
			name:      "JSON cookie value becomes redacted",
			body:      `{"cookie":"wmsession=abc123def456; sails.sid=s%3Asecret"}`,
			want:      `{"cookie":"<redacted>"}`,
			wantCount: 1,
		},
		{
			name:      "JSON session key becomes redacted",
			body:      `{"cookies":{"wmsession":"abc123def456"},"ok":true}`,
			want:      `{"cookies":{"wmsession":"<redacted>"},"ok":true}`,
			wantCount: 1,
		},
		{
			name:      "bearer token is redacted",
			body:      `{"note":"sent Authorization: Bearer abc123xyz"}`,
			want:      `{"note":"sent Authorization: Bearer <redacted>"}`,
			wantCount: 1,
		},
		{
			name:      "form session pair is redacted and ordinary pairs survive",
			body:      `wmsession=abc123def456&page=2`,
			want:      `wmsession=<redacted>&page=2`,
			wantCount: 1,
		},
		{
			name:      "keep leaves the body alone",
			body:      `{"cookie":"wmsession=abc123def456"}`,
			want:      `{"cookie":"wmsession=abc123def456"}`,
			wantCount: 0,
			keep:      true,
		},
		{
			name:      "ordinary JSON is untouched",
			body:      `{"consideration":"none","url":"https://example.com/a?x=1"}`,
			want:      `{"consideration":"none","url":"https://example.com/a?x=1"}`,
			wantCount: 0,
		},
		{
			name:      "repeated top-level key is redacted at every occurrence",
			body:      `{"token":"first-token","token":"second-token"}`,
			want:      `{"token":"<redacted>","token":"<redacted>"}`,
			wantCount: 2,
		},
		{
			name:      "escaped nested copy is redacted too",
			body:      `{"a":1,"token":"tok-cli-999","data":"{\"a\":1,\"token\":\"tok-cli-999\"}"}`,
			want:      `{"a":1,"token":"<redacted>","data":"{\"a\":1,\"token\":\"<redacted>\"}"}`,
			wantCount: 2,
		},
		{
			name:      "double-escaped nested copy is redacted",
			body:      `{"outer":"{\"inner\":\"{\\\"token\\\":\\\"tok-cli-999\\\"}\"}"}`,
			want:      `{"outer":"{\"inner\":\"{\\\"token\\\":\\\"<redacted>\\\"}\"}"}`,
			wantCount: 1,
		},
		{
			name:      "escaped non-secret JSON is untouched",
			body:      `{"data":"{\"a\":1,\"ok\":true}"}`,
			want:      `{"data":"{\"a\":1,\"ok\":true}"}`,
			wantCount: 0,
		},
		{
			name:      "an already-marked value is not counted twice",
			body:      `{"token":"<redacted>"}`,
			want:      `{"token":"<redacted>"}`,
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, count := redactBody(tt.body, tt.keep)
			if got != tt.want {
				t.Fatalf("redactBody() = %q, want %q", got, tt.want)
			}
			if count != tt.wantCount {
				t.Fatalf("redactBody() count = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

// TestRedactBodyPreservesJSONValidity pins that reaching an escaped nested
// secret rewrites only the secret: the result still parses and no proof token
// survives at any depth.
func TestRedactBodyPreservesJSONValidity(t *testing.T) {
	bodies := []string{
		`{"a":1,"token":"tok-cli-999","data":"{\"a\":1,\"token\":\"tok-cli-999\"}"}`,
		`{"token":"first-token","token":"second-token"}`,
		`{"outer":"{\"inner\":\"{\\\"token\\\":\\\"tok-cli-999\\\"}\"}"}`,
		`{"data":"{\"a\":1}"}`,
	}
	for _, body := range bodies {
		got, _ := redactBody(body, false)
		if !json.Valid([]byte(got)) {
			t.Errorf("redactBody(%q) produced invalid JSON: %q", body, got)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(got), &doc); err != nil {
			t.Errorf("redactBody(%q) result does not decode: %v", body, err)
		}
		if strings.Contains(got, "tok-cli-999") || strings.Contains(got, "first-token") || strings.Contains(got, "second-token") {
			t.Errorf("redactBody(%q) leaked a token: %q", body, got)
		}
	}
}

// TestReplayResultRedactsEscapedNestedBody pins the live proof case at the
// envelope: the top-level and escaped copies are both replaced and the counter
// reports both, so a clean count never hides a skipped occurrence.
func TestReplayResultRedactsEscapedNestedBody(t *testing.T) {
	const body = `{"a":1,"token":"tok-cli-999","data":"{\"a\":1,\"token\":\"tok-cli-999\"}"}`
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	got, err := replayResult(
		resp,
		[]ReplayHop{{Status: 200, URL: "https://example.test/"}},
		ReplaySent{Method: "POST", URL: "https://example.test/"},
		false,
		time.Now(),
		replayBodyCap,
	)
	if err != nil {
		t.Fatalf("replayResult() error = %v", err)
	}
	if strings.Contains(got.Body, "tok-cli-999") {
		t.Fatalf("replayResult() body leaks the escaped copy: %q", got.Body)
	}
	if got.BodyRedactions != 2 {
		t.Fatalf("replayResult() body_redactions = %d, want 2", got.BodyRedactions)
	}
	if len(got.BodyRedactionUnreached) != 0 {
		t.Fatalf("replayResult() unreached = %v, want none", got.BodyRedactionUnreached)
	}
	if !json.Valid([]byte(got.Body)) {
		t.Fatalf("replayResult() body is not valid JSON: %q", got.Body)
	}
}

// TestReplayResultCapsRedactionGrowth pins that replacing short secrets with
// the marker never returns more bytes than the requested cap.
func TestReplayResultCapsRedactionGrowth(t *testing.T) {
	const limit = 48
	const body = `{"token":"a","token":"b","token":"c","token":"d","token":"e"}`
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	got, err := replayResult(
		resp,
		nil,
		ReplaySent{Method: "GET", URL: "https://example.test/"},
		false,
		time.Now(),
		limit,
	)
	if err != nil {
		t.Fatalf("replayResult() error = %v", err)
	}
	if int64(len(got.Body)) > limit {
		t.Fatalf("replayResult() body is %d bytes, want <= %d", len(got.Body), limit)
	}
	if !got.Truncated {
		t.Fatal("replayResult() truncated = false, want true after the cap")
	}
	if got.BodyRedactions == 0 {
		t.Fatal("replayResult() body_redactions = 0, want the repeated secrets replaced")
	}
}

func TestReplayAuthorizeScope(t *testing.T) {
	p := &Page{browser: &Browser{scope: []string{"example.com"}}}
	if err := replayAuthorize(context.Background(), p, ReplaySpec{}, "https://api.example.com/x"); err != nil {
		t.Fatalf("in-scope authorize error = %v, want nil", err)
	}
	err := replayAuthorize(context.Background(), p, ReplaySpec{}, "https://evil.test/x")
	if err == nil {
		t.Fatal("out-of-scope replay authorize = nil, want a refusal")
	}
	for _, want := range []string{"evil.test", "outside the scope allowlist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
	if err := replayAuthorize(context.Background(), nil, ReplaySpec{}, "https://evil.test/x"); err != nil {
		t.Fatalf("authorize with no page = %v, want nil", err)
	}
}

func TestReplayHopAuthorizeStopsRedirect(t *testing.T) {
	p := &Page{browser: &Browser{scope: []string{"example.com"}}}
	err := replayHopAuthorize(context.Background(), p, ReplaySpec{}, "https://evil.test/next", 2)
	if err == nil {
		t.Fatal("out-of-scope redirect = nil, want a refusal")
	}
	for _, want := range []string{"redirect hop 2", "evil.test", "stopped"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("redirect refusal %q missing %q", err.Error(), want)
		}
	}
}

func TestReplayAuthorizeRateLimit(t *testing.T) {
	b := &Browser{limits: newHostLimiter(RateLimit{RPS: 1, Burst: 1})}
	p := &Page{browser: b}
	if err := replayAuthorize(context.Background(), p, ReplaySpec{}, "https://example.test/a"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := replayAuthorize(ctx, p, ReplaySpec{}, "https://example.test/b")
	if err == nil {
		t.Fatal("second authorize = nil, want a rate-limit refusal")
	}
	var limited *RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("authorize error = %v, want it to wrap *RateLimitError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("authorize error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}
