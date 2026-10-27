package har

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"webmcp/pkg/browsers"
)

// TestEncodeEntryShape pins the HAR 1.2 keys on one entry and the redaction
// rule: Authorization and X-CSRF-Token values are replaced while their names
// survive.
func TestEncodeEntryShape(t *testing.T) {
	entries := []browsers.NetworkEntry{{
		Method: "POST",
		URL:    "https://example.com/login?next=%2Fhome",
		Status: 302,
		MIME:   "text/html; charset=utf-8",
		Bytes:  1234,
		MS:     42,
		Seq:    1,
		ReqHeaders: map[string]string{
			"X-CSRF-Token":  "csrf123",
			"Authorization": "Bearer token123",
			"Accept":        "*/*",
		},
		RespHeaders: map[string]string{
			"Location":     "/home",
			"Content-Type": "text/html; charset=utf-8",
		},
	}}
	raw, err := Encode(
		PageInfo{Title: "Example"},
		entries,
		Options{},
	)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	log := object(t, doc, "log")
	for _, key := range []string{"version", "creator", "pages", "entries"} {
		requireKey(t, log, key)
	}
	if log["version"] != "1.2" {
		t.Errorf("log.version = %v, want 1.2", log["version"])
	}

	entry := firstObject(t, log, "entries")
	for _, key := range []string{
		"startedDateTime", "time", "request", "response", "cache", "timings",
	} {
		requireKey(t, entry, key)
	}
	request := object(t, entry, "request")
	for _, key := range []string{
		"method", "url", "httpVersion", "headers", "queryString", "cookies",
		"headersSize", "bodySize",
	} {
		requireKey(t, request, key)
	}
	response := object(t, entry, "response")
	for _, key := range []string{
		"status", "statusText", "httpVersion", "headers", "cookies", "content",
		"redirectURL", "headersSize", "bodySize",
	} {
		requireKey(t, response, key)
	}
	content := object(t, response, "content")
	for _, key := range []string{"size", "mimeType"} {
		requireKey(t, content, key)
	}
	timings := object(t, entry, "timings")
	for _, key := range []string{
		"blocked", "dns", "connect", "send", "wait", "receive", "ssl",
	} {
		requireKey(t, timings, key)
	}

	if got := response["statusText"]; got != "Found" {
		t.Errorf("response.statusText = %v, want Found", got)
	}
	if got := response["redirectURL"]; got != "/home" {
		t.Errorf("response.redirectURL = %v, want /home", got)
	}
	query := array(t, request, "queryString")
	if len(query) != 1 || query[0].(map[string]any)["name"] != "next" {
		t.Errorf("queryString = %v, want one next parameter", query)
	}

	reqHeaders := array(t, request, "headers")
	assertRedacted(t, reqHeaders, "Authorization")
	assertRedacted(t, reqHeaders, "X-CSRF-Token")
	if got := headerValue(reqHeaders, "Accept"); got != "*/*" {
		t.Errorf("Accept header = %q, want */*", got)
	}
	respHeaders := array(t, response, "headers")
	if got := headerValue(respHeaders, "Location"); got != "/home" {
		t.Errorf("Location header = %q, want /home", got)
	}

	truncated, ok := log["truncated"].(float64)
	if !ok || truncated != 0 {
		t.Errorf("log.truncated = %v, want an explicit 0 when nothing was dropped", log["truncated"])
	}
}

// TestEncodeDocumentIdentity pins the custom CDP identity fields and proves an
// entry recorded before them still exports: an empty frame or loader id is
// simply omitted, never an encode failure.
func TestEncodeDocumentIdentity(t *testing.T) {
	t.Run("identity and current-document marker are exported", func(t *testing.T) {
		entries := []browsers.NetworkEntry{{
			Method:      "GET",
			URL:         "https://example.test/",
			Status:      200,
			Seq:         1,
			FrameID:     "frame-top",
			LoaderID:    "loader-1",
			TopDocument: true,
		}}
		raw, err := Encode(PageInfo{Title: "Identity"}, entries, Options{})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		entry := firstObject(t, object(t, doc, "log"), "entries")
		if entry["_frameId"] != "frame-top" {
			t.Errorf("_frameId = %v, want frame-top", entry["_frameId"])
		}
		if entry["_loaderId"] != "loader-1" {
			t.Errorf("_loaderId = %v, want loader-1", entry["_loaderId"])
		}
		if entry["_topDocument"] != true {
			t.Errorf("_topDocument = %v, want true", entry["_topDocument"])
		}
	})

	t.Run("empty identity is omitted", func(t *testing.T) {
		entries := []browsers.NetworkEntry{{
			Method: "GET",
			URL:    "https://example.test/old",
			Status: 200,
			Seq:    1,
		}}
		raw, err := Encode(PageInfo{Title: "Legacy"}, entries, Options{})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		entry := firstObject(t, object(t, doc, "log"), "entries")
		for _, key := range []string{"_frameId", "_loaderId", "_topDocument"} {
			if _, ok := entry[key]; ok {
				t.Errorf("%s is present on an entry with no identity", key)
			}
		}
	})
}

// TestEncodeCommentStatesRedactionTruth pins the provenance comment: Cookie and
// Set-Cookie are absent because Chrome omits them from CDP events, while the
// two headers that do appear are redacted.
func TestEncodeCommentStatesRedactionTruth(t *testing.T) {
	raw, err := Encode(
		PageInfo{Title: "Comment"},
		nil,
		Options{},
	)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	log := object(t, doc, "log")
	comment, ok := log["comment"].(string)
	if !ok {
		t.Fatalf("log.comment is not a string: %T", log["comment"])
	}
	for _, want := range []string{
		"Cookie and Set-Cookie are absent",
		"Chrome omits them from CDP events",
		"Authorization and X-CSRF-Token header values are <redacted>",
		"Cap " + strconv.Itoa(DefaultMaxBytes) + " bytes",
	} {
		if !strings.Contains(comment, want) {
			t.Errorf("log.comment = %q, missing %q", comment, want)
		}
	}
	if strings.Contains(comment, "Set-Cookie header values are") {
		t.Errorf("log.comment claims a Set-Cookie value rewrite: %q", comment)
	}
	if truncated, ok := log["truncated"].(float64); !ok || truncated != 0 {
		t.Errorf("log.truncated = %v, want an explicit 0", log["truncated"])
	}
}

// TestEncodeCapDropsOldest pins the 512 KB-style cap: the output stays valid
// JSON under the limit, the oldest entries are dropped, and truncated counts
// them.
func TestEncodeCapDropsOldest(t *testing.T) {
	entries := make([]browsers.NetworkEntry, 40)
	for i := range entries {
		entries[i] = browsers.NetworkEntry{
			Method: "GET",
			URL:    fmt.Sprintf("https://example.com/%02d?q=%s", i, strings.Repeat("x", 120)),
			Status: 200,
			MIME:   "text/html",
			Bytes:  2048,
			MS:     10,
			Seq:    int64(i + 1),
		}
	}
	const limit = 4096
	raw, err := Encode(
		PageInfo{Title: "Heavy"},
		entries,
		Options{MaxBytes: limit},
	)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(raw) > limit {
		t.Fatalf("encoded HAR is %d bytes, want <= %d", len(raw), limit)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	log := object(t, doc, "log")
	kept := array(t, log, "entries")
	if len(kept) == 0 || len(kept) >= len(entries) {
		t.Fatalf("kept %d entries, want between 1 and %d", len(kept), len(entries)-1)
	}
	truncated, ok := log["truncated"].(float64)
	if !ok || int(truncated) != len(entries)-len(kept) {
		t.Fatalf("log.truncated = %v, want %d", log["truncated"], len(entries)-len(kept))
	}
	if !strings.Contains(log["comment"].(string), "Truncated") {
		t.Error("log.comment does not state the truncation")
	}
}

// TestEncodeRedirectChain pins one HAR entry per redirect hop: the 302 hop
// keeps its Location, and only the final hop has an empty redirectURL.
func TestEncodeRedirectChain(t *testing.T) {
	entries := []browsers.NetworkEntry{
		{
			Method: "GET",
			URL:    "https://example.com/start",
			Status: 302,
			MIME:   "text/html",
			Bytes:  0,
			Seq:    1,
			RespHeaders: map[string]string{
				"Location": "/final",
			},
		},
		{
			Method: "GET",
			URL:    "https://example.com/final",
			Status: 200,
			MIME:   "text/html",
			Bytes:  512,
			MS:     20,
			Seq:    2,
		},
	}
	raw, err := Encode(
		PageInfo{Title: "Redirect"},
		entries,
		Options{},
	)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	kept := array(t, object(t, doc, "log"), "entries")
	if len(kept) != 2 {
		t.Fatalf("entries = %d, want one per redirect hop", len(kept))
	}
	first := kept[0].(map[string]any)
	second := kept[1].(map[string]any)
	firstResp := object(t, first, "response")
	secondResp := object(t, second, "response")
	if got := firstResp["status"]; got != float64(302) {
		t.Errorf("first status = %v, want 302", got)
	}
	if got := firstResp["redirectURL"]; got != "/final" {
		t.Errorf("first redirectURL = %v, want /final", got)
	}
	if got := secondResp["redirectURL"]; got != "" {
		t.Errorf("second redirectURL = %v, want empty", got)
	}
	firstHeaders := array(t, firstResp, "headers")
	if got := headerValue(firstHeaders, "Location"); got != "/final" {
		t.Errorf("Location header = %q, want /final", got)
	}
}

// TestEncodeRingLoss pins the ring-loss rule: evicted entries are added to
// log.truncated and stated in log.comment, on top of the size-cap drops.
func TestEncodeRingLoss(t *testing.T) {
	entries := make([]browsers.NetworkEntry, 40)
	for i := range entries {
		entries[i] = browsers.NetworkEntry{
			Method: "GET",
			URL:    fmt.Sprintf("https://example.com/%02d?q=%s", i, strings.Repeat("x", 120)),
			Status: 200,
			MIME:   "text/html",
			Bytes:  2048,
			MS:     10,
			Seq:    int64(i + 1),
		}
	}
	const (
		limit   = 4096
		dropped = 9
	)
	raw, err := Encode(
		PageInfo{Title: "Lossy"},
		entries,
		Options{MaxBytes: limit, Dropped: dropped},
	)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(raw) > limit {
		t.Fatalf("encoded HAR is %d bytes, want <= %d", len(raw), limit)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	log := object(t, doc, "log")
	kept := array(t, log, "entries")
	want := len(entries) - len(kept) + dropped
	truncated, ok := log["truncated"].(float64)
	if !ok || int(truncated) != want {
		t.Fatalf("log.truncated = %v, want %d", log["truncated"], want)
	}
	comment, _ := log["comment"].(string)
	if !strings.Contains(comment, "Ring loss") || !strings.Contains(comment, strconv.Itoa(dropped)) {
		t.Fatalf("log.comment does not state the ring loss: %q", comment)
	}
	if !strings.Contains(comment, "Truncated") {
		t.Fatalf("log.comment does not state the size-cap truncation: %q", comment)
	}
}

// TestEncodeUsesSharedRedactionRule pins that the HAR export reads the one
// sensitive-key set the network ring and replay use, so the three surfaces
// cannot drift: every canonical sensitive header is replaced in both
// directions, and a neighbouring name is not.
func TestEncodeUsesSharedRedactionRule(t *testing.T) {
	respHeaders := func(name string) map[string]string {
		return map[string]string{name: "secret-value", "Content-Type": "text/plain"}
	}
	for _, name := range browsers.SensitiveHeaderNames() {
		t.Run(name, func(t *testing.T) {
			entries := []browsers.NetworkEntry{{
				Method:      "GET",
				URL:         "https://example.test/",
				Status:      200,
				ReqHeaders:  map[string]string{name: "secret-value"},
				RespHeaders: respHeaders(name),
			}}
			raw, err := Encode(PageInfo{Title: "Rule"}, entries, Options{})
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("HAR is not valid JSON: %v", err)
			}
			entry := firstObject(t, object(t, doc, "log"), "entries")
			request := object(t, entry, "request")
			response := object(t, entry, "response")
			assertRedacted(t, array(t, request, "headers"), name)
			assertRedacted(t, array(t, response, "headers"), name)
			if got := headerValue(array(t, response, "headers"), "Content-Type"); got != "text/plain" {
				t.Fatalf("Content-Type = %q, want text/plain", got)
			}
		})
	}
	if browsers.IsSensitiveHeader("content-type") {
		t.Fatal("content-type must not be sensitive")
	}
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %T", key, parent[key])
	}
	return v
}

func array(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	v, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array: %T", key, parent[key])
	}
	return v
}

func firstObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	items := array(t, parent, key)
	if len(items) == 0 {
		t.Fatalf("%s is empty", key)
	}
	return items[0].(map[string]any)
}

func requireKey(t *testing.T, obj map[string]any, key string) {
	t.Helper()
	if _, ok := obj[key]; !ok {
		t.Errorf("required key %q is missing", key)
	}
}

func assertRedacted(t *testing.T, headers []any, name string) {
	t.Helper()
	got := headerValue(headers, name)
	if got != browsers.RedactedValue {
		t.Errorf("%s header = %q, want %q", name, got, browsers.RedactedValue)
	}
}

func headerValue(headers []any, name string) string {
	for _, item := range headers {
		h := item.(map[string]any)
		if h["name"] == name {
			value, _ := h["value"].(string)
			return value
		}
	}
	return ""
}
