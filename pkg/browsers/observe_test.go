package browsers

import (
	"fmt"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

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
