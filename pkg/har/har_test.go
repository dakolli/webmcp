package har

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/browsers"
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

// TestEncodeCommentStatesRedactionTruth pins the provenance comment: without a
// cookie snapshot both cookie arrays are stated empty, while the headers that do
// appear are redacted.
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
		"request.cookies and response.cookies are empty",
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

// TestEncodeUncappedKeepsEveryEntry pins the negative cap: the export drops no
// entry even when the document exceeds DefaultMaxBytes, and truncated stays 0.
func TestEncodeUncappedKeepsEveryEntry(t *testing.T) {
	const count = 40
	entries := make([]browsers.NetworkEntry, count)
	for i := range entries {
		entries[i] = browsers.NetworkEntry{
			Method: "GET",
			URL:    fmt.Sprintf("https://example.test/pad/%02d", i),
			Status: 200,
			MIME:   "text/html",
			Bytes:  1024,
			MS:     5,
			Seq:    int64(i + 1),
			RespHeaders: map[string]string{
				"X-Pad": strings.Repeat("p", 20<<10),
			},
		}
	}
	raw, err := Encode(PageInfo{Title: "Uncapped"}, entries, Options{MaxBytes: -1})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(raw) <= DefaultMaxBytes {
		t.Fatalf("encoded %d bytes, want an uncapped document above %d", len(raw), DefaultMaxBytes)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	log := object(t, doc, "log")
	if kept := array(t, log, "entries"); len(kept) != count {
		t.Fatalf("entries = %d, want all %d", len(kept), count)
	}
	if truncated, ok := log["truncated"].(float64); !ok || truncated != 0 {
		t.Errorf("log.truncated = %v, want an explicit 0", log["truncated"])
	}
	comment, _ := log["comment"].(string)
	if !strings.Contains(comment, "Cap: none") {
		t.Errorf("log.comment does not state the uncapped export: %q", comment)
	}
}

// TestEncodeResponseBody pins content.text: text bodies stay text, binary bodies
// become base64, and size is the decoded byte count rather than the encoded one.
func TestEncodeResponseBody(t *testing.T) {
	encode := func(t *testing.T, fetch BodyFetcher) (map[string]any, map[string]any) {
		t.Helper()
		entries := []browsers.NetworkEntry{{
			Method: "GET",
			URL:    "https://example.test/app.js",
			Status: 200,
			MIME:   "application/javascript",
			Bytes:  999,
			MS:     8,
			Seq:    7,
		}}
		raw, err := Encode(
			PageInfo{Title: "Body"},
			entries,
			Options{ResponseBody: fetch},
		)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		log := object(t, doc, "log")
		response := object(t, firstObject(t, log, "entries"), "response")
		return log, response
	}

	t.Run("text body lands in content.text with no encoding", func(t *testing.T) {
		_, response := encode(t, func(int64) (Body, error) {
			return Body{Text: "console.log(1)", Bytes: 14}, nil
		})
		content := object(t, response, "content")
		if content["text"] != "console.log(1)" {
			t.Errorf("content.text = %v, want the fetched text", content["text"])
		}
		if _, ok := content["encoding"]; ok {
			t.Errorf("content.encoding = %v, want it omitted for text", content["encoding"])
		}
		if content["size"] != float64(14) {
			t.Errorf("content.size = %v, want the decoded 14", content["size"])
		}
	})

	t.Run("binary body is base64 with encoding and decoded size", func(t *testing.T) {
		payload := []byte{0x00, 0x01, 0xff}
		_, response := encode(t, func(int64) (Body, error) {
			return Body{
				Base64: base64.StdEncoding.EncodeToString(payload),
				Bytes:  int64(len(payload)),
			}, nil
		})
		content := object(t, response, "content")
		if content["text"] != base64.StdEncoding.EncodeToString(payload) {
			t.Errorf("content.text = %v, want the base64 payload", content["text"])
		}
		if content["encoding"] != "base64" {
			t.Errorf("content.encoding = %v, want base64", content["encoding"])
		}
		if content["size"] != float64(len(payload)) {
			t.Errorf("content.size = %v, want the decoded %d", content["size"], len(payload))
		}
	})

	t.Run("invalid UTF-8 text is base64 encoded", func(t *testing.T) {
		_, response := encode(t, func(int64) (Body, error) {
			return Body{Text: "\x00\xff", Bytes: 2}, nil
		})
		content := object(t, response, "content")
		if content["encoding"] != "base64" {
			t.Errorf("content.encoding = %v, want base64 for invalid UTF-8", content["encoding"])
		}
	})

	t.Run("fetch failure leaves text out and names the reason", func(t *testing.T) {
		log, response := encode(t, func(int64) (Body, error) {
			return Body{}, fmt.Errorf("body is no longer available")
		})
		content := object(t, response, "content")
		if _, ok := content["text"]; ok {
			t.Errorf("content.text = %v, want it omitted on a failed fetch", content["text"])
		}
		if content["size"] != float64(999) {
			t.Errorf("content.size = %v, want the measured 999", content["size"])
		}
		comment, _ := log["comment"].(string)
		if !strings.Contains(comment, "body is no longer available") {
			t.Errorf("log.comment does not state the body failure: %q", comment)
		}
	})
}

// TestEncodeRequestPostData pins request.postData: it carries the MIME type and
// text when the request had a body, and is omitted when it had none.
func TestEncodeRequestPostData(t *testing.T) {
	encode := func(
		t *testing.T,
		headers map[string]string,
		fetch BodyFetcher,
	) map[string]any {
		t.Helper()
		entries := []browsers.NetworkEntry{{
			Method:     "POST",
			URL:        "https://example.test/login",
			Status:     302,
			MS:         12,
			Seq:        3,
			ReqHeaders: headers,
		}}
		raw, err := Encode(
			PageInfo{Title: "Post"},
			entries,
			Options{RequestBody: fetch},
		)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		return object(t, firstObject(t, object(t, doc, "log"), "entries"), "request")
	}

	t.Run("request body becomes postData", func(t *testing.T) {
		request := encode(
			t,
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			func(int64) (Body, error) { return Body{Text: "user=a&pass=b", Bytes: 12}, nil },
		)
		post := object(t, request, "postData")
		if post["mimeType"] != "application/x-www-form-urlencoded" {
			t.Errorf("postData.mimeType = %v, want the request content type", post["mimeType"])
		}
		if post["text"] != "user=a&pass=b" {
			t.Errorf("postData.text = %v, want the fetched body", post["text"])
		}
		if request["bodySize"] != float64(12) {
			t.Errorf("request.bodySize = %v, want the decoded 12", request["bodySize"])
		}
	})

	t.Run("request with no body omits postData", func(t *testing.T) {
		request := encode(t, nil, func(int64) (Body, error) {
			t.Fatal("body fetched for a request whose headers announce none")
			return Body{}, nil
		})
		if _, ok := request["postData"]; ok {
			t.Errorf("request.postData = %v, want it omitted", request["postData"])
		}
		if request["bodySize"] != float64(-1) {
			t.Errorf("request.bodySize = %v, want -1", request["bodySize"])
		}
	})

	t.Run("binary request body is base64", func(t *testing.T) {
		request := encode(
			t,
			map[string]string{"Content-Type": "application/octet-stream"},
			func(int64) (Body, error) { return Body{Text: "\x00\xff", Bytes: 2}, nil },
		)
		post := object(t, request, "postData")
		if post["text"] != base64.StdEncoding.EncodeToString([]byte("\x00\xff")) {
			t.Errorf("postData.text = %v, want the base64 payload", post["text"])
		}
	})
}

// TestEncodeServerIPAddress pins the standard serverIPAddress field: it is
// emitted when Chrome reported the peer IP and omitted otherwise.
func TestEncodeServerIPAddress(t *testing.T) {
	t.Run("reported IP is exported", func(t *testing.T) {
		entries := []browsers.NetworkEntry{{
			Method:   "GET",
			URL:      "https://example.test/",
			Status:   200,
			Seq:      1,
			RemoteIP: "203.0.113.7",
		}}
		raw, err := Encode(PageInfo{Title: "IP"}, entries, Options{})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		entry := firstObject(t, object(t, doc, "log"), "entries")
		if entry["serverIPAddress"] != "203.0.113.7" {
			t.Errorf("serverIPAddress = %v, want 203.0.113.7", entry["serverIPAddress"])
		}
	})

	t.Run("unknown IP is omitted", func(t *testing.T) {
		entries := []browsers.NetworkEntry{{
			Method: "GET",
			URL:    "https://example.test/",
			Status: 200,
			Seq:    1,
		}}
		raw, err := Encode(PageInfo{Title: "IP"}, entries, Options{})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		entry := firstObject(t, object(t, doc, "log"), "entries")
		if _, ok := entry["serverIPAddress"]; ok {
			t.Errorf("serverIPAddress = %v, want it omitted", entry["serverIPAddress"])
		}
	})
}

// TestEncodeBodyBudget pins the fetch budget: the newest entries get bodies,
// the oldest survivors are counted in bodies_skipped, and the stop is stated.
func TestEncodeBodyBudget(t *testing.T) {
	survivors := func(count int) []browsers.NetworkEntry {
		entries := make([]browsers.NetworkEntry, count)
		for i := range entries {
			entries[i] = browsers.NetworkEntry{
				Method: "GET",
				URL:    fmt.Sprintf("https://example.test/%02d", i),
				Status: 200,
				MIME:   "text/plain",
				Seq:    int64(i + 1),
			}
		}
		return entries
	}

	t.Run("fetch count stops the export", func(t *testing.T) {
		const count = 60
		fetches := 0
		raw, err := Encode(
			PageInfo{Title: "Budget"},
			survivors(count),
			Options{MaxBytes: -1, ResponseBody: func(int64) (Body, error) {
				fetches++
				return Body{Text: "ok", Bytes: 2}, nil
			}},
		)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		if fetches != maxBodyFetches {
			t.Fatalf("fetches = %d, want the %d-fetch budget", fetches, maxBodyFetches)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		log := object(t, doc, "log")
		if skipped, ok := log["bodies_skipped"].(float64); !ok || int(skipped) != count-maxBodyFetches {
			t.Fatalf("log.bodies_skipped = %v, want %d", log["bodies_skipped"], count-maxBodyFetches)
		}
		kept := array(t, log, "entries")
		oldest := object(t, kept[0].(map[string]any), "response")
		if _, ok := object(t, oldest, "content")["text"]; ok {
			t.Error("the oldest entry got a body after the budget ran out")
		}
		newest := object(t, kept[count-1].(map[string]any), "response")
		if object(t, newest, "content")["text"] != "ok" {
			t.Error("the newest entry did not get its body")
		}
		comment, _ := log["comment"].(string)
		if !strings.Contains(comment, "Body budget") {
			t.Errorf("log.comment does not state the budget stop: %q", comment)
		}
	})

	t.Run("byte bound stops the export", func(t *testing.T) {
		fetches := 0
		raw, err := Encode(
			PageInfo{Title: "Budget"},
			survivors(4),
			Options{MaxBytes: -1, ResponseBody: func(int64) (Body, error) {
				fetches++
				return Body{Text: "ok", Bytes: 5 << 20}, nil
			}},
		)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		if fetches != 2 {
			t.Fatalf("fetches = %d, want 2 before the %d-byte bound", fetches, maxBodyFetchBytes)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		if skipped := object(t, doc, "log")["bodies_skipped"]; skipped != float64(2) {
			t.Fatalf("log.bodies_skipped = %v, want 2", skipped)
		}
	})
}

// TestEncodeHTTPVersion pins the CDP protocol mapping onto both HAR
// httpVersion fields, including the HTTP/1.1 fallback when Chrome reports none.
func TestEncodeHTTPVersion(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		want     string
	}{
		{name: "h2 is carried through", protocol: "h2", want: "h2"},
		{name: "http/1.1 is carried through", protocol: "http/1.1", want: "http/1.1"},
		{name: "missing protocol falls back", protocol: "", want: "HTTP/1.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []browsers.NetworkEntry{{
				Method:   "GET",
				URL:      "https://example.test/",
				Status:   200,
				Seq:      1,
				Protocol: tc.protocol,
			}}
			raw, err := Encode(PageInfo{Title: "Version"}, entries, Options{})
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("HAR is not valid JSON: %v", err)
			}
			entry := firstObject(t, object(t, doc, "log"), "entries")
			if got := object(t, entry, "request")["httpVersion"]; got != tc.want {
				t.Errorf("request.httpVersion = %v, want %q", got, tc.want)
			}
			if got := object(t, entry, "response")["httpVersion"]; got != tc.want {
				t.Errorf("response.httpVersion = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestEncodeRequestCookies pins the jar matching rule: a cookie is attached only
// when its domain and path cover the request URL and it is not Secure on an
// HTTP URL. Values stay redacted unless secret capture is on, and response
// cookies stay empty because CDP does not report Set-Cookie.
func TestEncodeRequestCookies(t *testing.T) {
	jar := []browsers.Cookie{
		{
			Name:     "sid",
			Value:    "session",
			Domain:   "example.test",
			Path:     "/",
			HTTPOnly: true,
		},
		{
			Name:    "secure",
			Value:   "s3cr3t",
			Domain:  "example.test",
			Path:    "/",
			Secure:  true,
			Expires: 1893456000,
		},
		{
			Name:   "admin",
			Value:  "only-admin",
			Domain: ".example.test",
			Path:   "/admin",
			Secure: true,
		},
		{
			Name:   "other",
			Value:  "other",
			Domain: "other.test",
			Path:   "/",
		},
	}
	entries := []browsers.NetworkEntry{
		{
			Method: "GET",
			URL:    "https://example.test/",
			Status: 200,
			Seq:    1,
		},
		{
			Method: "GET",
			URL:    "https://example.test/admin/panel",
			Status: 200,
			Seq:    2,
		},
		{
			Method: "GET",
			URL:    "http://example.test/",
			Status: 200,
			Seq:    3,
		},
		{
			Method: "GET",
			URL:    "https://other.test/",
			Status: 200,
			Seq:    4,
		},
		{
			Method: "GET",
			URL:    "https://unrelated.test/",
			Status: 200,
			Seq:    5,
		},
	}
	encode := func(t *testing.T, secrets bool) []map[string]any {
		t.Helper()
		raw, err := Encode(PageInfo{Title: "Cookies"}, entries, Options{
			Cookies: func() ([]browsers.Cookie, error) { return jar, nil },
			Secrets: secrets,
		})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("HAR is not valid JSON: %v", err)
		}
		log := object(t, doc, "log")
		comment, _ := log["comment"].(string)
		if !strings.Contains(comment, "response.cookies is empty because Chrome does not report Set-Cookie") {
			t.Errorf("log.comment = %q, want the empty response.cookies reason", comment)
		}
		kept := array(t, log, "entries")
		out := make([]map[string]any, 0, len(kept))
		for _, item := range kept {
			out = append(out, item.(map[string]any))
		}
		return out
	}

	t.Run("domain path and secure rules select the jar", func(t *testing.T) {
		got := encode(t, false)
		want := [][]string{
			{"secure", "sid"},
			{"secure", "sid", "admin"},
			{"sid"},
			{"other"},
			nil,
		}
		for i, names := range want {
			request := object(t, got[i], "request")
			if have := cookieNames(t, request); !slices.Equal(have, names) {
				t.Errorf("entry %d request.cookies = %v, want %v", i, have, names)
			}
			if responseCookies := array(t, object(t, got[i], "response"), "cookies"); len(responseCookies) != 0 {
				t.Errorf("entry %d response.cookies = %v, want none", i, responseCookies)
			}
		}
	})

	t.Run("values are redacted by default", func(t *testing.T) {
		for i, entry := range encode(t, false) {
			for _, item := range array(t, object(t, entry, "request"), "cookies") {
				c := item.(map[string]any)
				if c["value"] != browsers.RedactedValue {
					t.Errorf("entry %d cookie %v value = %v, want %q", i, c["name"], c["value"], browsers.RedactedValue)
				}
				for _, key := range []string{"name", "value", "path", "domain", "secure", "httpOnly", "expires"} {
					if _, ok := c[key]; !ok {
						t.Errorf("entry %d cookie %v is missing %q", i, c["name"], key)
					}
				}
			}
		}
	})

	t.Run("secret capture exports values and expiry", func(t *testing.T) {
		entry := encode(t, true)[0]
		if got := cookieValue(t, entry, "sid"); got != "session" {
			t.Errorf("sid value = %q, want session", got)
		}
		if got := cookieValue(t, entry, "secure"); got != "s3cr3t" {
			t.Errorf("secure value = %q, want s3cr3t", got)
		}
		for _, item := range array(t, object(t, entry, "request"), "cookies") {
			c := item.(map[string]any)
			if c["name"] == "secure" && c["expires"] != "2030-01-01T00:00:00Z" {
				t.Errorf("secure expires = %v, want 2030-01-01T00:00:00Z", c["expires"])
			}
		}
	})
}

func cookieNames(t *testing.T, request map[string]any) []string {
	t.Helper()
	items := array(t, request, "cookies")
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	return names
}

func cookieValue(t *testing.T, entry map[string]any, name string) string {
	t.Helper()
	for _, item := range array(t, object(t, entry, "request"), "cookies") {
		c := item.(map[string]any)
		if c["name"] == name {
			value, _ := c["value"].(string)
			return value
		}
	}
	return ""
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
