package caido

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testClient(t *testing.T, fn roundTripFunc) *Client {
	t.Helper()
	c := NewClient("http://127.0.0.1:8080/graphql")
	c.HTTP = &http.Client{Transport: roundTripFunc(fn)}
	c.store("test-token", time.Now().Add(time.Hour), AuthGuest)
	return c
}

func gqlData(t *testing.T, data string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"data":` + data + `}`)),
	}
}

func metadataJSON(now int64) string {
	return `{"requestsByOffset":{"count":{"value":1},"nodes":[{
		"id":"994","host":"example.com","method":"POST","path":"/api","query":"a=1",
		"port":443,"isTls":true,"length":42,"createdAt":` +
		itoa(now) + `,
		"response":{"id":"990","statusCode":200,"length":10,"roundtripTime":7,"createdAt":` +
		itoa(now) + `}}]}}`
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func pageData(t *testing.T, total int, nodes []requestNode) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"requestsByOffset": map[string]any{
			"count": map[string]int{"value": total},
			"nodes": nodes,
		},
	})
	if err != nil {
		t.Fatalf("encoding page: %v", err)
	}
	return string(b)
}

func encodeMessage(message string) string {
	return base64.StdEncoding.EncodeToString([]byte(message))
}

func TestParseRaw(t *testing.T) {
	raw := encodeMessage(
		"POST /x HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n" +
			"X-Test: one\r\nX-Test: two\r\n\r\nhello",
	)
	startLine, headers, body, err := ParseRaw(raw)
	if err != nil {
		t.Fatalf("ParseRaw: %v", err)
	}
	if startLine != "POST /x HTTP/1.1" {
		t.Errorf("start line = %q", startLine)
	}
	if got := headers["content-type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("content-type = %v", got)
	}
	if got := headers["x-test"]; len(got) != 1 || got[0] != "one" {
		t.Errorf("x-test = %v, want first value", got)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q", body)
	}
}

func TestParseRawNotBase64(t *testing.T) {
	_, _, _, err := ParseRaw("!!!not valid base64!!!")
	var decode *RawDecodeError
	if !errors.As(err, &decode) {
		t.Fatalf("error = %v, want *RawDecodeError", err)
	}
}

func TestRedact(t *testing.T) {
	headers := RedactHeaders(map[string][]string{
		"authorization": {"Bearer secret-value"},
		"accept":        {"*/*"},
	})
	if got := headers["authorization"]; len(got) != 1 || got[0] != "<redacted>" {
		t.Errorf("authorization = %v", got)
	}
	if got := headers["accept"]; len(got) != 1 || got[0] != "*/*" {
		t.Errorf("accept = %v", got)
	}
	// A credential-shaped name that is not on any fixed list, and a header echo
	// that arrives as a JSON key with punctuation: both must be redacted.
	keyed := RedactHeaders(map[string][]string{
		"X-Api-Key":    {"wm-secret-4"},
		"content-type": {"application/json"},
	})
	if got := keyed["X-Api-Key"]; len(got) != 1 || got[0] != "<redacted>" {
		t.Errorf("X-Api-Key = %v", got)
	}
	if got := keyed["content-type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("content-type = %v", got)
	}
	echoed, _, err := RedactBody(`{"X-Api-Key":"wm-secret-4","keep":"v"}`)
	if err != nil {
		t.Fatalf("RedactBody: %v", err)
	}
	if strings.Contains(echoed, "wm-secret-4") {
		t.Errorf("echoed api-key value survived: %s", echoed)
	}

	body, count, err := RedactBody(`{"access_token":"abc","nested":{"password":"p"},"keep":"v"}`)
	if err != nil {
		t.Fatalf("RedactBody: %v", err)
	}
	if count != 2 {
		t.Errorf("redaction count = %d, want 2", count)
	}
	if strings.Contains(body, "abc") || strings.Contains(body, `"p"`) {
		t.Errorf("body still holds a credential: %s", body)
	}
	if !strings.Contains(body, "<redacted>") {
		t.Errorf("body missing redaction marker: %s", body)
	}

	plain, n, err := RedactBody("not json")
	if err != nil {
		t.Fatalf("RedactBody: %v", err)
	}
	if plain != "not json" || n != 0 {
		t.Errorf("non-JSON body changed: %q count=%d", plain, n)
	}
}

func TestRedactFormBody(t *testing.T) {
	body, count, err := RedactBody("token=secret&x=1")
	if err != nil {
		t.Fatalf("RedactBody: %v", err)
	}
	if count != 1 {
		t.Errorf("redaction count = %d, want 1", count)
	}
	if strings.Contains(body, "secret") {
		t.Errorf("form body still holds the secret: %s", body)
	}
	if !strings.Contains(body, "token=<redacted>") {
		t.Errorf("form body = %q, want token=<redacted>", body)
	}
	if !strings.Contains(body, "x=1") {
		t.Errorf("form body lost the benign field: %s", body)
	}
}

func TestRedactPlainPatterns(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"colon pair", "access_token: abc", "access_token: <redacted>", 1},
		{"xml element", "<token>abc</token>", "<token><redacted></token>", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, count, err := RedactBody(tc.in)
			if err != nil {
				t.Fatalf("RedactBody: %v", err)
			}
			if count != tc.count {
				t.Errorf("count = %d, want %d", count, tc.count)
			}
			if out != tc.want {
				t.Errorf("out = %q, want %q", out, tc.want)
			}
		})
	}

	out, count, err := RedactBody(`{"apiKey":"xyz"}`)
	if err != nil {
		t.Fatalf("RedactBody: %v", err)
	}
	if count != 1 {
		t.Errorf("JSON key count = %d, want 1", count)
	}
	if strings.Contains(out, "xyz") {
		t.Errorf("JSON credential survived: %s", out)
	}
}

func TestRedactHeaderNames(t *testing.T) {
	headers := RedactHeaders(map[string][]string{
		"braveservicekey": {"secret-value"},
		"content-type":    {"application/json"},
	})
	if got := headers["braveservicekey"]; len(got) != 1 || got[0] != "<redacted>" {
		t.Errorf("braveservicekey = %v, want redacted", got)
	}
	if got := headers["content-type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("content-type = %v, want readable", got)
	}
}

func TestRedactBodyErrorPropagates(t *testing.T) {
	now := time.Now().UnixMilli()
	secret := "super-secret-value"
	requestRaw := encodeMessage(
		"POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n" +
			`{"access_token":"` + secret + `"}`,
	)
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "request(id:") {
			return gqlData(t, `{"r0":{"raw":"`+requestRaw+`"},"s0":null}`), nil
		}
		return gqlData(t, metadataJSON(now)), nil
	})

	original := encodeBody
	encodeBody = func(any) (string, error) { return "", errors.New("encode exploded") }
	t.Cleanup(func() { encodeBody = original })

	result, err := c.Search(t.Context(), Query{Limit: 20, BodyBytes: 2000, Part: "request"})
	if err == nil {
		t.Fatal("expected a redaction error")
	}
	if !strings.Contains(err.Error(), "redacting JSON body") {
		t.Errorf("error = %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("entries = %d, want none on a redaction failure", len(result.Entries))
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked the secret: %v", err)
	}
}

func TestSearchBodyBytesZero(t *testing.T) {
	now := time.Now().UnixMilli()
	requestRaw := encodeMessage(
		"POST /api HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n" +
			`{"payload":1}`,
	)
	responseRaw := encodeMessage(
		"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n" + `{"ok":true}`,
	)
	calls := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "request(id:") {
			return gqlData(t, `{"r0":{"raw":"`+requestRaw+`"},"s0":{"raw":"`+responseRaw+`"}}`), nil
		}
		return gqlData(t, metadataJSON(now)), nil
	})

	result, err := c.Search(t.Context(), Query{Limit: 20, BodyBytes: 0, Part: "both"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want metadata plus one body phase", calls)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.ReqBody != "" || entry.RespBody != "" {
		t.Errorf("bodies = %q/%q, want empty", entry.ReqBody, entry.RespBody)
	}
	if got := entry.ReqHeaders["content-type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("request content-type = %v, want headers kept", got)
	}
	if got := entry.RespHeaders["content-type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("response content-type = %v, want headers kept", got)
	}
	if entry.Mime != "application/json" {
		t.Errorf("mime = %q", entry.Mime)
	}
	if entry.ReqCut || entry.RespCut {
		t.Errorf("cut flags = %v/%v, want none: body_bytes 0 asks for no body", entry.ReqCut, entry.RespCut)
	}
}

func TestSearchLimitAndBodyBytesClamp(t *testing.T) {
	now := time.Now().UnixMilli()
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "request(id:") {
			return gqlData(t, `{"r0":null,"s0":null}`), nil
		}
		return gqlData(t, metadataJSON(now)), nil
	})

	result, err := c.Search(t.Context(), Query{Limit: 500, BodyBytes: 999999, Part: "both"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.LimitApplied != maxLimit {
		t.Errorf("limit_applied = %d, want %d", result.LimitApplied, maxLimit)
	}
	if result.BodyBytesApplied != maxBodyBytes {
		t.Errorf("body_bytes_applied = %d, want %d", result.BodyBytesApplied, maxBodyBytes)
	}
}

func TestSearchPaginatesPastFirstPage(t *testing.T) {
	now := time.Now().UnixMilli()
	mk := func(id, path string) requestNode {
		return requestNode{
			ID:        id,
			Host:      "example.com",
			Method:    "GET",
			Path:      path,
			Port:      443,
			IsTLS:     true,
			Length:    10,
			CreatedAt: now,
			Response: &responseNode{
				ID:            "s" + id,
				StatusCode:    200,
				Length:        5,
				RoundtripTime: 3,
				CreatedAt:     now,
			},
		}
	}
	pages := [][]requestNode{make([]requestNode, 0, maxPageRows), {mk("101", "/licenses/7"), mk("102", "/f")}}
	for i := range maxPageRows {
		pages[0] = append(pages[0], mk("p"+strconv.Itoa(i), "/p"+strconv.Itoa(i)))
	}
	raw := encodeMessage(
		"GET /licenses/7 HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n{}",
	)
	metaCalls := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "request(id:") {
			return gqlData(t, `{"r0":{"raw":"`+raw+`"},"s0":{"raw":"`+raw+`"}}`), nil
		}
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		metaCalls++
		offset := int(payload.Variables["offset"].(float64))
		page := offset / maxPageRows
		if page >= len(pages) {
			page = len(pages) - 1
		}
		return gqlData(t, pageData(t, 102, pages[page])), nil
	})

	result, err := c.Search(t.Context(), Query{
		URL:       "/licenses/",
		Limit:     1,
		BodyBytes: 100,
		Part:      "both",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if metaCalls != 2 {
		t.Errorf("metadata pages = %d, want 2", metaCalls)
	}
	if result.Count != 1 {
		t.Errorf("count = %d, want 1", result.Count)
	}
	if result.Total != 102 {
		t.Errorf("total = %d, want 102", result.Total)
	}
	if result.Scanned != maxPageRows+1 {
		t.Errorf("scanned = %d, want %d", result.Scanned, maxPageRows+1)
	}
	if result.Dropped != maxPageRows {
		t.Errorf("dropped = %d, want %d", result.Dropped, maxPageRows)
	}
	if len(result.Entries) != 1 || result.Entries[0].URL != "https://example.com/licenses/7" {
		t.Errorf("entries = %+v", result.Entries)
	}
}

func TestSearchFetchesBodies(t *testing.T) {
	now := time.Now().UnixMilli()
	requestRaw := encodeMessage(
		"POST /api HTTP/1.1\r\nHost: example.com\r\nAuthorization: Bearer secret-value\r\n" +
			"Content-Type: application/json\r\n\r\n" +
			`{"access_token":"abc","user":"ada"}`,
	)
	responseRaw := encodeMessage(
		"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\n" +
			"Set-Cookie: sid=xyz\r\n\r\n" + `{"ok":true}`,
	)

	var filter string
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request: %v", err)
		}
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if strings.Contains(payload.Query, "request(id:") {
			return gqlData(t, `{"r0":{"raw":"`+requestRaw+`"},"s0":{"raw":"`+responseRaw+`"}}`), nil
		}
		if f, ok := payload.Variables["filter"].(map[string]any); ok {
			filter, _ = f["code"].(string)
		}
		return gqlData(t, metadataJSON(now)), nil
	})

	result, err := c.Search(t.Context(), Query{
		Method:    "POST",
		Status:    200,
		Limit:     20,
		BodyBytes: 2000,
		Part:      "both",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.URL != "https://example.com/api?a=1" {
		t.Errorf("url = %q", entry.URL)
	}
	if entry.Mime != "application/json" {
		t.Errorf("mime = %q", entry.Mime)
	}
	if got := entry.ReqHeaders["authorization"]; len(got) != 1 || got[0] != "<redacted>" {
		t.Errorf("request authorization = %v", got)
	}
	if strings.Contains(entry.ReqBody, "abc") || !strings.Contains(entry.ReqBody, "<redacted>") {
		t.Errorf("request body not redacted: %s", entry.ReqBody)
	}
	if got := entry.RespHeaders["set-cookie"]; len(got) != 1 || got[0] != "<redacted>" {
		t.Errorf("response set-cookie = %v", got)
	}
	if entry.RespBody != `{"ok":true}` {
		t.Errorf("response body = %q", entry.RespBody)
	}
	if entry.ReqCut || entry.RespCut {
		t.Errorf("cut flags set under the body cap")
	}
	want := `req.method.eq:"POST" AND resp.code.eq:200`
	if filter != want {
		t.Errorf("httpql = %q, want %q", filter, want)
	}
}

func TestSearchNullRawAndBadBase64(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("null response body is empty", func(t *testing.T) {
		requestRaw := encodeMessage("POST /api HTTP/1.1\r\nHost: example.com\r\n\r\n")
		c := testClient(t, func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "request(id:") {
				return gqlData(t, `{"r0":{"raw":"`+requestRaw+`"},"s0":null}`), nil
			}
			return gqlData(t, metadataJSON(now)), nil
		})
		result, err := c.Search(t.Context(), Query{Limit: 20, BodyBytes: 2000, Part: "both"})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got := result.Entries[0].RespBody; got != "" {
			t.Errorf("response body = %q, want empty for null raw", got)
		}
	})

	t.Run("bad base64 names the request", func(t *testing.T) {
		c := testClient(t, func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "request(id:") {
				return gqlData(t, `{"r0":{"raw":"%%%"},"s0":null}`), nil
			}
			return gqlData(t, metadataJSON(now)), nil
		})
		_, err := c.Search(t.Context(), Query{Limit: 20, BodyBytes: 2000, Part: "both"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "caido: raw message from request 994 is not base64") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestRedactBeforeCutKeepsSecretsOut(t *testing.T) {
	body := `{"padding":"` + strings.Repeat("x", 200) + `","api_key":"wm-secret-6"}`
	e := &Entry{ID: "7"}
	if err := applyRaw(e, "response", encodeMessage(
		"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"+body), 40, false); err != nil {
		t.Fatalf("applyRaw: %v", err)
	}
	if strings.Contains(e.RespBody, "wm-secret-6") {
		t.Errorf("truncated body leaked the credential: %s", e.RespBody)
	}
	if !e.RespCut {
		t.Error("response_cut = false, want true")
	}
}

func TestRedactURL(t *testing.T) {
	got, n := RedactURL("https://example.com/a?api_key=abc&page=2")
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	if strings.Contains(got, "abc") {
		t.Errorf("url still holds the token: %s", got)
	}
	if !strings.Contains(got, "page=2") {
		t.Errorf("url lost the benign parameter: %s", got)
	}
}

func TestRedactHeadersKeepsPolicyFlags(t *testing.T) {
	h := RedactHeaders(map[string][]string{
		"access-control-allow-credentials": {"true"},
		"x-api-key":                        {"wm-secret-4"},
	})
	if got := h["access-control-allow-credentials"]; len(got) != 1 || got[0] != "true" {
		t.Errorf("policy flag = %v, want true", got)
	}
	if got := h["x-api-key"]; len(got) != 1 || got[0] != "<redacted>" {
		t.Errorf("x-api-key = %v", got)
	}
}

func TestCutBodyZeroMeansNoBody(t *testing.T) {
	text, cut := cutBody([]byte("hello"), 0)
	if text != "" || cut {
		t.Errorf("cutBody(0) = %q, %t; want empty and no cut", text, cut)
	}
}

func TestRedactHeadersScrubsURLValues(t *testing.T) {
	h := RedactHeaders(map[string][]string{
		"referer": {"https://example.com/a?api_key=wm-secret-8&page=2"},
	})
	got := h["referer"][0]
	if strings.Contains(got, "wm-secret-8") {
		t.Errorf("referer leaked the token: %s", got)
	}
	if !strings.Contains(got, "page=2") {
		t.Errorf("referer lost the benign parameter: %s", got)
	}
}

// searchFilter returns a client that records the variables of the metadata
// request, so a test can assert what reached the wire.
func searchFilter(t *testing.T, now int64) (*Client, *map[string]any) {
	t.Helper()
	captured := map[string]any{}
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "request(id:") {
			return gqlData(t, `{}`), nil
		}
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		for k, v := range payload.Variables {
			captured[k] = v
		}
		return gqlData(t, metadataJSON(now)), nil
	})
	return c, &captured
}

func TestSearchHTTPQLPassesThrough(t *testing.T) {
	now := time.Now().UnixMilli()
	expression := `req.host.eq:"www.gnu.org"`
	c, vars := searchFilter(t, now)

	result, err := c.Search(t.Context(), Query{HTTPQL: expression, Limit: 5, Part: "request"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	filter, _ := (*vars)["filter"].(map[string]any)
	if got, _ := filter["code"].(string); got != expression {
		t.Errorf("filter = %q, want %q", got, expression)
	}
	if result.HTTPQL != expression || result.HTTPQLSource != "httpql" {
		t.Errorf("envelope = %q/%q, want the raw expression", result.HTTPQL, result.HTTPQLSource)
	}
}

func TestSearchTypedKeysCompile(t *testing.T) {
	now := time.Now().UnixMilli()
	c, vars := searchFilter(t, now)

	result, err := c.Search(t.Context(), Query{
		Host:         "www.gnu.org",
		Method:       "post",
		Status:       200,
		StatusGTE:    400,
		PathContains: "/api",
		Limit:        5,
		Part:         "request",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := `req.host.eq:"www.gnu.org" AND req.method.eq:"POST" AND ` +
		`resp.code.eq:200 AND resp.code.gte:400 AND req.path.cont:"/api"`
	filter, _ := (*vars)["filter"].(map[string]any)
	if got, _ := filter["code"].(string); got != want {
		t.Errorf("filter = %q, want %q", got, want)
	}
	if result.HTTPQL != want || result.HTTPQLSource != "typed" {
		t.Errorf("envelope = %q/%q, want the typed expression", result.HTTPQL, result.HTTPQLSource)
	}
}

func TestSearchHTTPQLWinsOverTypedKeys(t *testing.T) {
	now := time.Now().UnixMilli()
	expression := `req.host.eq:"www.gnu.org"`
	c, vars := searchFilter(t, now)

	result, err := c.Search(t.Context(), Query{
		HTTPQL: expression,
		Host:   "ignored.example",
		Method: "GET",
		Status: 500,
		Limit:  5,
		Part:   "request",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	filter, _ := (*vars)["filter"].(map[string]any)
	if got, _ := filter["code"].(string); got != expression {
		t.Errorf("filter = %q, want %q", got, expression)
	}
	if result.HTTPQLSource != "httpql" {
		t.Errorf("source = %q, want httpql", result.HTTPQLSource)
	}
}

func TestSearchAllScopesOmitsArgument(t *testing.T) {
	now := time.Now().UnixMilli()
	c, vars := searchFilter(t, now)

	result, err := c.Search(t.Context(), Query{ScopeID: "*", Limit: 5, Part: "request"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, ok := (*vars)["scopeId"]; ok {
		t.Errorf("scopeId sent for \"*\": %v", *vars)
	}
	if !result.AllScopes || result.ScopeID != "*" {
		t.Errorf("envelope all_scopes=%t scope_id=%q", result.AllScopes, result.ScopeID)
	}
}

func TestSearchRejectedScope(t *testing.T) {
	c := testClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"errors":[{"message":"Invalid ID format, should be an i32"}]}`,
			)),
		}, nil
	})

	_, err := c.Search(t.Context(), Query{ScopeID: "not-an-id", Limit: 5})
	var scopeErr *ScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("error = %v, want *ScopeError", err)
	}
	if got := scopeErr.Hint(); got != "call caido_status to list scopes" {
		t.Errorf("hint = %q", got)
	}
	if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("error = %q, want an invalid-arguments sentence", err)
	}
}

func TestResolveScope(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "scopes") {
			t.Errorf("unexpected query: %s", body)
		}
		return gqlData(t, `{"scopes":[{"id":"1","name":"target"},{"id":"2","name":"other"}]}`), nil
	})

	cases := []struct {
		name    string
		id      string
		scope   string
		wantID  string
		wantErr bool
	}{
		{name: "by id", id: "2", wantID: "2"},
		{name: "by name", scope: "target", wantID: "1"},
		{name: "missing id", id: "9", wantErr: true},
		{name: "missing name", scope: "nope", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.ResolveScope(t.Context(), tc.id, tc.scope)
			if tc.wantErr {
				var scopeErr *ScopeError
				if !errors.As(err, &scopeErr) {
					t.Fatalf("error = %v, want *ScopeError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveScope: %v", err)
			}
			if got.ID != tc.wantID {
				t.Errorf("id = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}

func loginClient(t *testing.T, fn roundTripFunc) *Client {
	t.Helper()
	c := NewClient("http://127.0.0.1:8080/graphql")
	c.HTTP = &http.Client{Transport: roundTripFunc(fn)}
	return c
}

func rejectedResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"errors":[{"message":"Operation error","extensions":{"CAIDO":{"reason":"INVALID_TOKEN"}}}]}`,
		)),
	}
}

func guestSuccess() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"data":{"loginAsGuest":{"token":{"accessToken":"guest-token","expiresAt":"2030-01-01T00:00:00Z"}}}}`,
		)),
	}
}

func TestLoginAccessTokenWins(t *testing.T) {
	calls := 0
	c := loginClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "loginAsGuest") {
			t.Error("guest login called with an access token set")
			return guestSuccess(), nil
		}
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("authorization = %q, want the access token", got)
		}
		return gqlData(t, `{}`), nil
	})

	path, err := c.Login(t.Context(), "access-token", "pat-token", true)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if path != AuthAccessToken {
		t.Errorf("path = %q, want %q", path, AuthAccessToken)
	}
	if got := c.currentToken(); got != "access-token" {
		t.Errorf("token = %q, want the access token", got)
	}
	if calls != 1 {
		t.Errorf("requests = %d, want one probe", calls)
	}
}

func TestLoginPATRejectedFallsBackToGuest(t *testing.T) {
	probes, guest := 0, 0
	c := loginClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "loginAsGuest") {
			guest++
			return guestSuccess(), nil
		}
		probes++
		return rejectedResponse(), nil
	})

	path, err := c.Login(t.Context(), "", "bad-pat", true)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if path != AuthPATGuest {
		t.Errorf("path = %q, want %q", path, AuthPATGuest)
	}
	if got := c.currentToken(); got != "guest-token" {
		t.Errorf("token = %q, want the guest token", got)
	}
	if probes != 1 {
		t.Errorf("probes = %d, want 1", probes)
	}
	if guest != 1 {
		t.Errorf("guest logins = %d, want 1", guest)
	}
}

func TestLoginPATRejectedGuestOff(t *testing.T) {
	probes, guest := 0, 0
	c := loginClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "loginAsGuest") {
			guest++
			return guestSuccess(), nil
		}
		probes++
		return rejectedResponse(), nil
	})

	path, err := c.Login(t.Context(), "", "bad-pat", false)
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("error = %v, want ErrNoSession", err)
	}
	want := "caido: no session — the instance rejected the credential and guest access is off"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if path != AuthNone {
		t.Errorf("path = %q, want %q", path, AuthNone)
	}
	if probes != 1 {
		t.Errorf("probes = %d, want 1", probes)
	}
	if guest != 0 {
		t.Errorf("guest logins = %d, want none", guest)
	}
}

func TestLoginGuestWhenNoCredential(t *testing.T) {
	c := loginClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "loginAsGuest") {
			t.Error("unexpected non-guest request")
		}
		return guestSuccess(), nil
	})

	path, err := c.Login(t.Context(), "", "", true)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if path != AuthGuest {
		t.Errorf("path = %q, want %q", path, AuthGuest)
	}
	if got := c.currentToken(); got != "guest-token" {
		t.Errorf("token = %q, want the guest token", got)
	}
}

func TestStatus(t *testing.T) {
	project := `"project":{"project":{"id":"p1","name":"webmcp","path":"/srv/webmcp",` +
		`"status":"READY","temporary":false},"readOnly":false}`
	cases := []struct {
		name  string
		data  string
		check func(t *testing.T, st Status)
	}{
		{
			name: "project present",
			data: `{` + project +
				`,"scopes":[{"id":"1","name":"target","allowlist":["example.com"],` +
				`"denylist":["ads.example.com"]}],` +
				`"capture":{"captureList":["example.com"],"passthroughList":[],` +
				`"outOfScope":true},"history":{"count":{"value":1879}}}`,
			check: func(t *testing.T, st Status) {
				t.Helper()
				if st.Project.ID != "p1" || st.Project.Name != "webmcp" {
					t.Errorf("project = %+v", st.Project)
				}
				if st.Project.Path != "/srv/webmcp" || st.Project.Status != "READY" {
					t.Errorf("project path/status = %+v", st.Project)
				}
				if st.Project.Temporary || st.Project.ReadOnly {
					t.Errorf("project flags = temporary %t, read_only %t", st.Project.Temporary, st.Project.ReadOnly)
				}
				if st.Project.Selected != "webmcp" || st.Project.Configured != "webmcp" {
					t.Errorf("selected/configured = %q/%q", st.Project.Selected, st.Project.Configured)
				}
				if st.History != 1879 {
					t.Errorf("history = %d, want 1879", st.History)
				}
				if st.Capture.Unfiltered || st.Capture.OutOfScope == nil || !*st.Capture.OutOfScope {
					t.Errorf("capture = %+v", st.Capture)
				}
				if len(st.Capture.CaptureList) != 1 || st.Capture.CaptureList[0] != "example.com" {
					t.Errorf("capture_list = %v", st.Capture.CaptureList)
				}
				if len(st.Scopes) != 1 {
					t.Fatalf("scopes = %d, want 1", len(st.Scopes))
				}
				if st.Scopes[0].Matches != "allowlist" || !st.Scopes[0].Focus {
					t.Errorf("scope = %+v", st.Scopes[0])
				}
			},
		},
		{
			name: "passthrough options null",
			data: `{` + project + `,"scopes":[],"capture":null,` +
				`"history":{"count":{"value":3}}}`,
			check: func(t *testing.T, st Status) {
				t.Helper()
				if !st.Capture.Unfiltered {
					t.Errorf("capture = %+v, want unfiltered", st.Capture)
				}
				if st.Capture.OutOfScope != nil || st.Capture.CaptureList != nil {
					t.Errorf("capture fields set on a null object: %+v", st.Capture)
				}
				encoded, err := json.Marshal(st.Capture)
				if err != nil {
					t.Fatalf("encoding capture: %v", err)
				}
				if string(encoded) != `{"unfiltered":true}` {
					t.Errorf("capture JSON = %s, want {\"unfiltered\":true}", encoded)
				}
			},
		},
		{
			name: "empty scope list",
			data: `{` + project + `,"scopes":[],"capture":null,` +
				`"history":{"count":{"value":0}}}`,
			check: func(t *testing.T, st Status) {
				t.Helper()
				if len(st.Scopes) != 0 {
					t.Errorf("scopes = %d, want 0", len(st.Scopes))
				}
			},
		},
		{
			name: "one scope with an empty allowlist",
			data: `{` + project + `,"scopes":[{"id":"2","name":"target",` +
				`"allowlist":[],"denylist":[]}],"capture":null,` +
				`"history":{"count":{"value":5}}}`,
			check: func(t *testing.T, st Status) {
				t.Helper()
				if len(st.Scopes) != 1 {
					t.Fatalf("scopes = %d, want 1", len(st.Scopes))
				}
				scope := st.Scopes[0]
				if scope.Matches != "all traffic" {
					t.Errorf("matches = %q, want all traffic", scope.Matches)
				}
				if scope.Allowlist == nil {
					t.Error("allowlist = nil, want an empty slice")
				}
				if !scope.Focus {
					t.Error("focus = false, want true for the configured scope")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				var payload struct {
					Query string `json:"query"`
				}
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decoding request: %v", err)
				}
				for _, alias := range []string{
					"project: currentProject",
					"scopes: scopes",
					"capture: passthroughOptions",
					"history: requestsByOffset",
				} {
					if !strings.Contains(payload.Query, alias) {
						t.Errorf("query missing alias %q", alias)
					}
				}
				return gqlData(t, tc.data), nil
			})

			st, err := c.Status(t.Context(), "webmcp", "target")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if calls != 1 {
				t.Errorf("requests = %d, want one round trip", calls)
			}
			if st.Auth != "guest" {
				t.Errorf("auth = %q, want guest", st.Auth)
			}
			if st.URL != "http://127.0.0.1:8080/graphql" {
				t.Errorf("url = %q", st.URL)
			}
			tc.check(t, st)
		})
	}
}

func TestSearchSinceEmptiesResult(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	nodes := make([]requestNode, 3)
	for i := range nodes {
		id := strconv.Itoa(500 + i)
		nodes[i] = requestNode{
			ID:        id,
			Host:      "www.gnu.org",
			Method:    "GET",
			Path:      "/licenses/" + id,
			Port:      443,
			IsTLS:     true,
			Length:    10,
			CreatedAt: old,
		}
	}
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		return gqlData(t, pageData(t, 3, nodes)), nil
	})

	result, err := c.Search(t.Context(), Query{
		HTTPQL: `req.host.eq:"www.gnu.org"`,
		Since:  900 * time.Second,
		Limit:  5,
		Part:   "request",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Count != 0 || result.Total != 3 {
		t.Fatalf("count/total = %d/%d, want 0/3", result.Count, result.Total)
	}
	if result.SinceSeconds != 900 {
		t.Errorf("since_seconds = %d, want 900", result.SinceSeconds)
	}
	if result.DroppedSince != 3 {
		t.Errorf("dropped_since = %d, want 3", result.DroppedSince)
	}
	want := "all 3 matching rows are older than since=900s — pass since:0 to read them"
	if result.Note != want {
		t.Errorf("note = %q, want %q", result.Note, want)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encoding result: %v", err)
	}
	if !strings.Contains(string(encoded), `"note":"all 3 matching rows`) {
		t.Errorf("envelope missing note: %s", encoded)
	}
}
