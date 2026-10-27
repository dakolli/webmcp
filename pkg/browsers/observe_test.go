package browsers

import (
	"fmt"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

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
