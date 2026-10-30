package browsers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func TestObserverCSPCaptureBypassesRingClip(t *testing.T) {
	const topFrame = proto.PageFrameID("frame-top")

	directives := []string{
		"default-src 'none'",
		"script-src 'self' 'nonce-AbC123' 'sha256-AbCdEf'",
		"style-src 'self'",
		"img-src 'self' data:",
		"connect-src 'self' https://api-one.test https://api-two.test https://api-three.test https://api-four.test https://api-five.test",
		"font-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"object-src 'none'",
		"report-uri https://site.test/csp-report",
		"report-to default",
		"sandbox allow-scripts allow-same-origin",
		"require-trusted-types-for 'script'",
		"trusted-types default",
		"worker-src 'self'",
		"manifest-src 'self'",
		"media-src 'self'",
		"prefetch-src 'self'",
		"child-src 'self'",
		"frame-src 'self'",
		"upgrade-insecure-requests",
	}
	long := strings.Join(directives, "; ")
	if len(long) <= networkHeaderValueLimit {
		t.Fatalf("policy is %d bytes, needs to exceed the %d-byte clip", len(long), networkHeaderValueLimit)
	}

	page := &Page{observer: newPageObserver()}
	page.observer.topFrameID = topFrame
	const id = proto.NetworkRequestID("doc-1")
	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		FrameID:   topFrame,
		LoaderID:  "loader-1",
		Type:      proto.NetworkResourceTypeDocument,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://site.test/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status:   200,
			MIMEType: "text/html",
			Headers: proto.NetworkHeaders{
				"content-security-policy": gson.New(long),
			},
		},
	})

	enforced, _ := page.DocumentCSP()
	if enforced != long {
		t.Fatalf("DocumentCSP = %d bytes, want the full %d-byte policy", len(enforced), len(long))
	}

	entries, _ := page.NetworkEntries()
	clipped := entries[0].RespHeaders["content-security-policy"]
	if len(clipped) > networkHeaderValueLimit {
		t.Fatalf("ring header = %d bytes, want the %d-byte clip kept for display", len(clipped), networkHeaderValueLimit)
	}
	if clipped == long {
		t.Fatal("ring header kept the full policy, want the display clip unchanged")
	}

	page.ClearCapture()
	if enforced, _ = page.DocumentCSP(); enforced != long {
		t.Fatal("the policy was lost when the ring was cleared")
	}
}

func TestObserverCSPJoinsRepeatedHeaders(t *testing.T) {
	const topFrame = proto.PageFrameID("frame-top")

	page := &Page{observer: newPageObserver()}
	page.observer.topFrameID = topFrame
	const id = proto.NetworkRequestID("doc-1")
	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		FrameID:   topFrame,
		Type:      proto.NetworkResourceTypeDocument,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://site.test/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status: 200,
			Headers: proto.NetworkHeaders{
				"content-security-policy":             gson.New("default-src 'none'\nscript-src 'self'"),
				"content-security-policy-report-only": gson.New("img-src 'self'"),
			},
		},
	})

	enforced, reportOnly := page.DocumentCSP()
	if enforced != "default-src 'none', script-src 'self'" {
		t.Fatalf("enforced = %q, want the repeated fields joined with a comma", enforced)
	}
	if reportOnly != "img-src 'self'" {
		t.Fatalf("reportOnly = %q, want the report-only variant captured too", reportOnly)
	}

	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response:  &proto.NetworkResponse{Status: 200},
	})
	if enforced, reportOnly = page.DocumentCSP(); enforced != "" || reportOnly != "" {
		t.Fatalf("csp = %q/%q, want a navigation without CSP to replace the previous policy", enforced, reportOnly)
	}
}

func TestObserverCSPIgnoresChildFrames(t *testing.T) {
	const (
		topFrame   = proto.PageFrameID("frame-top")
		childFrame = proto.PageFrameID("frame-child")
	)

	page := &Page{observer: newPageObserver()}
	page.observer.topFrameID = topFrame
	const id = proto.NetworkRequestID("frame-doc")
	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		FrameID:   childFrame,
		Type:      proto.NetworkResourceTypeDocument,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://child.test/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status:  200,
			Headers: proto.NetworkHeaders{"content-security-policy": gson.New("default-src 'none'")},
		},
	})

	if enforced, reportOnly := page.DocumentCSP(); enforced != "" || reportOnly != "" {
		t.Fatalf("csp = %q/%q, want a child frame document to leave the policy alone", enforced, reportOnly)
	}
}

func TestObserverDocumentSecurityHeadersSurviveClear(t *testing.T) {
	const topFrame = proto.PageFrameID("frame-top")
	const hsts = "max-age=31536000; includeSubDomains; preload"
	longPermissions := "camera=(), microphone=(), geolocation=(), " + strings.Repeat("x", 600)
	if len(longPermissions) <= networkHeaderValueLimit {
		t.Fatalf("permissions policy is %d bytes, needs to exceed the %d-byte clip", len(longPermissions), networkHeaderValueLimit)
	}

	page := &Page{observer: newPageObserver()}
	page.observer.topFrameID = topFrame
	const id = proto.NetworkRequestID("doc-sec")
	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		FrameID:   topFrame,
		LoaderID:  "loader-1",
		Type:      proto.NetworkResourceTypeDocument,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://site.test/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status:   200,
			MIMEType: "text/html",
			Headers: proto.NetworkHeaders{
				"strict-transport-security": gson.New(hsts),
				"x-content-type-options":    gson.New("nosniff"),
				"permissions-policy":        gson.New(longPermissions),
				"x-irrelevant":              gson.New("not stored"),
			},
		},
	})

	got := page.DocumentSecurityHeaders()
	if got["strict-transport-security"] != hsts {
		t.Errorf("hsts = %q, want the uncapped header", got["strict-transport-security"])
	}
	if got["x-content-type-options"] != "nosniff" {
		t.Errorf("xcto = %q, want nosniff", got["x-content-type-options"])
	}
	if got["permissions-policy"] != longPermissions {
		t.Error("the stored permissions-policy was clipped, want the full value")
	}
	if _, ok := got["x-irrelevant"]; ok {
		t.Error("a header outside the closed set was stored")
	}

	entries, _ := page.NetworkEntries()
	clipped := entries[0].RespHeaders["permissions-policy"]
	if len(clipped) > networkHeaderValueLimit || clipped == longPermissions {
		t.Fatalf("ring value = %d bytes, want the %d-byte clip kept for other consumers", len(clipped), networkHeaderValueLimit)
	}

	page.ClearCapture()
	after := page.DocumentSecurityHeaders()
	if after["strict-transport-security"] != hsts {
		t.Error("the stored hsts was lost when the ring was cleared")
	}
	if after["x-content-type-options"] != "nosniff" {
		t.Error("the stored x-content-type-options was lost when the ring was cleared")
	}
}

// TestObserverDocumentSecurityHeadersIncludeCORS proves the closed set carries
// the five CORS response headers the audit judges: a value longer than the ring
// clip survives intact, a header outside the set is not stored, and the exported
// read returns a copy that survives ClearCapture.
func TestObserverDocumentSecurityHeadersIncludeCORS(t *testing.T) {
	const topFrame = proto.PageFrameID("frame-top")
	longMethods := "GET, POST, PUT, PATCH, DELETE, " + strings.Repeat("x", 600)
	if len(longMethods) <= networkHeaderValueLimit {
		t.Fatalf("allow-methods is %d bytes, needs to exceed the %d-byte clip", len(longMethods), networkHeaderValueLimit)
	}

	page := &Page{observer: newPageObserver()}
	page.observer.topFrameID = topFrame
	const id = proto.NetworkRequestID("doc-cors")
	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		FrameID:   topFrame,
		LoaderID:  "loader-1",
		Type:      proto.NetworkResourceTypeDocument,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://site.test/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status: 200,
			Headers: proto.NetworkHeaders{
				"access-control-allow-origin":      gson.New("*"),
				"access-control-allow-credentials": gson.New("true"),
				"access-control-allow-methods":     gson.New(longMethods),
				"access-control-allow-headers":     gson.New("X-Requested-With"),
				"access-control-expose-headers":    gson.New("X-Total-Count"),
				"access-control-max-age":           gson.New("600"),
			},
		},
	})

	want := map[string]string{
		"access-control-allow-origin":      "*",
		"access-control-allow-credentials": "true",
		"access-control-allow-methods":     longMethods,
		"access-control-allow-headers":     "X-Requested-With",
		"access-control-expose-headers":    "X-Total-Count",
	}
	got := page.DocumentSecurityHeaders()
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
	if _, ok := got["access-control-max-age"]; ok {
		t.Error("a header outside the closed set was stored")
	}

	got["access-control-allow-origin"] = "mutated"
	if after := page.DocumentSecurityHeaders(); after["access-control-allow-origin"] != "*" {
		t.Error("DocumentSecurityHeaders returned the live map, not a copy")
	}

	page.ClearCapture()
	after := page.DocumentSecurityHeaders()
	for name, value := range want {
		if after[name] != value {
			t.Errorf("%s lost when the ring was cleared: got %q", name, after[name])
		}
	}
}

func TestObserverDocumentSecurityHeadersReplaceOnNavigation(t *testing.T) {
	const topFrame = proto.PageFrameID("frame-top")

	page := &Page{observer: newPageObserver()}
	page.observer.topFrameID = topFrame
	const id = proto.NetworkRequestID("doc-nav")
	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		FrameID:   topFrame,
		LoaderID:  "loader-1",
		Type:      proto.NetworkResourceTypeDocument,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://site.test/a"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status: 200,
			Headers: proto.NetworkHeaders{
				"strict-transport-security": gson.New("max-age=63072000"),
				"x-frame-options":           gson.New("DENY"),
			},
		},
	})
	if got := page.DocumentSecurityHeaders(); got["x-frame-options"] != "DENY" {
		t.Fatalf("x-frame-options = %q, want DENY before the navigation", got["x-frame-options"])
	}

	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response:  &proto.NetworkResponse{Status: 200},
	})
	if got := page.DocumentSecurityHeaders(); len(got) != 0 {
		t.Fatalf("headers = %v, want the next document response to replace the record", got)
	}
}

func TestObserverDocumentIdentity(t *testing.T) {
	const (
		topFrame   = proto.PageFrameID("frame-top")
		childFrame = proto.PageFrameID("frame-child")
	)

	request := func(
		id proto.NetworkRequestID,
		frame proto.PageFrameID,
		loader proto.NetworkLoaderID,
		kind proto.NetworkResourceType,
		url string,
	) *proto.NetworkRequestWillBeSent {
		return &proto.NetworkRequestWillBeSent{
			RequestID: id,
			FrameID:   frame,
			LoaderID:  loader,
			Type:      kind,
			Request:   &proto.NetworkRequest{Method: "GET", URL: url},
			Timestamp: 1,
		}
	}
	marked := func(t *testing.T, entries []NetworkEntry) []string {
		t.Helper()
		urls := make([]string, 0, 1)
		for _, e := range entries {
			if e.TopDocument {
				urls = append(urls, e.URL)
			}
		}
		return urls
	}

	t.Run("two navigations mark only the newest document", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.topFrameID = topFrame
		page.observer.recordRequest(request("doc-1", topFrame, "loader-1", proto.NetworkResourceTypeDocument, "https://site.test/a"))
		page.observer.recordRequest(request("xhr-1", topFrame, "loader-1", proto.NetworkResourceTypeXHR, "https://site.test/a/api"))
		page.observer.recordRequest(request("doc-2", topFrame, "loader-2", proto.NetworkResourceTypeDocument, "https://site.test/b"))
		page.observer.recordRequest(request("xhr-2", topFrame, "loader-2", proto.NetworkResourceTypeXHR, "https://site.test/b/api"))

		entries, _ := page.NetworkEntries()
		got := marked(t, entries)
		if len(got) != 1 || got[0] != "https://site.test/b" {
			t.Fatalf("top document entries = %v, want only the second navigation", got)
		}
		if entries[2].FrameID != string(topFrame) || entries[2].LoaderID != "loader-2" {
			t.Fatalf("entry identity = frame %q loader %q, want frame-top/loader-2", entries[2].FrameID, entries[2].LoaderID)
		}
	})

	t.Run("same-origin route change keeps the loaded document", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.topFrameID = topFrame
		page.observer.recordRequest(request("doc-1", topFrame, "loader-1", proto.NetworkResourceTypeDocument, "https://spa.test/"))
		page.observer.recordRequest(request("xhr-1", topFrame, "loader-1", proto.NetworkResourceTypeXHR, "https://spa.test/api/me"))

		entries, _ := page.NetworkEntries()
		got := marked(t, entries)
		if len(got) != 1 || got[0] != "https://spa.test/" {
			t.Fatalf("top document entries = %v, want the single loaded document", got)
		}
	})

	t.Run("same-origin iframe loading the same URL does not move the boundary", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.topFrameID = topFrame
		page.observer.recordRequest(request("doc-1", topFrame, "loader-1", proto.NetworkResourceTypeDocument, "https://app.test/"))
		page.observer.recordFrameNavigated(&proto.PageFrameNavigated{
			Frame: &proto.PageFrame{ID: childFrame, ParentID: topFrame, LoaderID: "loader-child", URL: "https://app.test/"},
			Type:  proto.PageNavigationTypeNavigation,
		})
		page.observer.recordRequest(request("doc-2", childFrame, "loader-child", proto.NetworkResourceTypeDocument, "https://app.test/"))

		entries, _ := page.NetworkEntries()
		got := marked(t, entries)
		if len(got) != 1 {
			t.Fatalf("top document entries = %v, want one", got)
		}
		if entries[0].FrameID != string(topFrame) || entries[0].LoaderID != "loader-1" {
			t.Fatalf("marked entry = frame %q loader %q, want the top document", entries[0].FrameID, entries[0].LoaderID)
		}
	})

	t.Run("bfcache restore resolves to the restored document", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.topFrameID = topFrame
		page.observer.recordRequest(request("doc-1", topFrame, "loader-1", proto.NetworkResourceTypeDocument, "https://site.test/a"))
		page.observer.recordRequest(request("doc-2", topFrame, "loader-2", proto.NetworkResourceTypeDocument, "https://site.test/b"))
		page.observer.recordFrameNavigated(&proto.PageFrameNavigated{
			Frame: &proto.PageFrame{ID: topFrame, LoaderID: "loader-1", URL: "https://site.test/a"},
			Type:  proto.PageNavigationTypeBackForwardCacheRestore,
		})

		entries, _ := page.NetworkEntries()
		got := marked(t, entries)
		if len(got) != 1 || got[0] != "https://site.test/a" {
			t.Fatalf("top document entries = %v, want the restored document", got)
		}
	})

	t.Run("empty identity marks nothing", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.recordRequest(request("doc-1", "", "", proto.NetworkResourceTypeDocument, "https://site.test/"))
		entries, _ := page.NetworkEntries()
		if got := marked(t, entries); len(got) != 0 {
			t.Fatalf("top document entries = %v, want none without frame identity", got)
		}
	})
}

func TestObserverRemoteIP(t *testing.T) {
	page := &Page{observer: newPageObserver()}
	id := proto.NetworkRequestID("req-ip")

	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://example.com/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response: &proto.NetworkResponse{
			Status:          200,
			MIMEType:        "text/html",
			RemoteIPAddress: "203.0.113.7",
		},
	})

	entries, _ := page.NetworkEntries()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].RemoteIP != "203.0.113.7" {
		t.Fatalf("RemoteIP = %q, want the CDP remoteIPAddress", entries[0].RemoteIP)
	}

	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response:  &proto.NetworkResponse{Status: 200, MIMEType: "text/html"},
	})
	entries, _ = page.NetworkEntries()
	if entries[0].RemoteIP != "203.0.113.7" {
		t.Fatalf("RemoteIP = %q, want the last reported IP kept when CDP omits it", entries[0].RemoteIP)
	}
}

func TestObserverProtocol(t *testing.T) {
	page := &Page{observer: newPageObserver()}
	id := proto.NetworkRequestID("req-proto")

	page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
		RequestID: id,
		Request:   &proto.NetworkRequest{Method: "GET", URL: "https://example.com/"},
		Timestamp: 1,
	})
	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response:  &proto.NetworkResponse{Status: 200, MIMEType: "text/html", Protocol: "h2"},
	})

	entries, _ := page.NetworkEntries()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Protocol != "h2" {
		t.Fatalf("Protocol = %q, want the CDP protocol", entries[0].Protocol)
	}

	page.observer.recordResponse(&proto.NetworkResponseReceived{
		RequestID: id,
		Type:      proto.NetworkResourceTypeDocument,
		Response:  &proto.NetworkResponse{Status: 200, MIMEType: "text/html"},
	})
	entries, _ = page.NetworkEntries()
	if entries[0].Protocol != "h2" {
		t.Fatalf("Protocol = %q, want the last reported protocol kept when CDP omits it", entries[0].Protocol)
	}
}

func TestObserverRingAndRequestJoin(t *testing.T) {
	t.Run("redirect hops keep one entry and join the final response", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		id := proto.NetworkRequestID("req-1")

		page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
			RequestID: id,
			Request:   &proto.NetworkRequest{Method: "GET", URL: "https://example.com/start"},
			Timestamp: 10,
		})
		page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
			RequestID: id,
			Request:   &proto.NetworkRequest{Method: "GET", URL: "https://example.com/final"},
			Timestamp: 10.5,
			RedirectResponse: &proto.NetworkResponse{
				URL:      "https://example.com/start",
				Status:   302,
				MIMEType: "text/html",
				Headers: proto.NetworkHeaders{
					"Location":   gson.New("/final"),
					"Set-Cookie": gson.New("sid=abc; Path=/"),
				},
			},
		})

		entries, dropped := page.NetworkEntries()
		if len(entries) != 2 {
			t.Fatalf("redirect hops: got %d entries, want 2", len(entries))
		}
		if dropped != 0 {
			t.Fatalf("dropped: got %d, want 0", dropped)
		}
		if entries[0].URL != "https://example.com/start" || entries[0].Seq != 1 {
			t.Fatalf("first hop: got url=%q seq=%d", entries[0].URL, entries[0].Seq)
		}
		if entries[1].URL != "https://example.com/final" || entries[1].Seq != 2 {
			t.Fatalf("second hop: got url=%q seq=%d", entries[1].URL, entries[1].Seq)
		}
		if entries[0].Status != 302 || entries[0].RespHeaders["Location"] != "/final" {
			t.Fatalf("redirect response: got status=%d headers=%v", entries[0].Status, entries[0].RespHeaders)
		}
		if entries[0].RespHeaders["Set-Cookie"] == "" {
			t.Fatal("redirect hop lost its Set-Cookie")
		}

		page.observer.recordResponse(&proto.NetworkResponseReceived{
			RequestID: id,
			Type:      proto.NetworkResourceTypeDocument,
			Response:  &proto.NetworkResponse{Status: 200, MIMEType: "text/html"},
		})
		page.observer.recordFinished(&proto.NetworkLoadingFinished{
			RequestID:         id,
			EncodedDataLength: 512,
			Timestamp:         12,
		})

		entries, _ = page.NetworkEntries()
		if entries[0].Status != 302 {
			t.Fatalf("first hop status: got %d, want it untouched by the final response", entries[0].Status)
		}
		got := entries[1]
		if got.Status != 200 || got.MIME != "text/html" || got.Type != "Document" {
			t.Fatalf("response join: got %+v", got)
		}
		if got.Bytes != 512 || got.MS != 1500 {
			t.Fatalf("finish join: bytes=%d ms=%d, want 512/1500", got.Bytes, got.MS)
		}
	})

	t.Run("ring bound and copy semantics", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		for i := range 300 {
			page.observer.recordLogEntry(&proto.LogEntryAdded{
				Entry: &proto.LogLogEntry{
					Level: proto.LogLogEntryLevelVerbose,
					Text:  fmt.Sprintf("line-%d", i),
				},
			})
		}

		entries, dropped := page.ConsoleEntries()
		if len(entries) != 200 {
			t.Fatalf("ring bound: got %d entries, want 200", len(entries))
		}
		if dropped != 100 {
			t.Fatalf("dropped: got %d, want 100", dropped)
		}
		if entries[0].Text != "line-100" || entries[len(entries)-1].Text != "line-299" {
			t.Fatalf("ring content: got %q..%q", entries[0].Text, entries[len(entries)-1].Text)
		}

		entries[0].Text = "mutated"
		again, _ := page.ConsoleEntries()
		if again[0].Text == "mutated" {
			t.Fatal("ConsoleEntries returned the internal buffer, not a copy")
		}

		netCleared, conCleared := page.ClearLogs()
		if netCleared != 0 || conCleared != 200 {
			t.Fatalf("clear: network=%d console=%d, want 0/200", netCleared, conCleared)
		}
		if after, dropped := page.ConsoleEntries(); len(after) != 0 || dropped != 0 {
			t.Fatalf("after clear: len=%d dropped=%d, want 0/0", len(after), dropped)
		}
	})
}
