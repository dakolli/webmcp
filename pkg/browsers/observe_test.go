package browsers

import (
	"fmt"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestObserverRingAndRequestJoin(t *testing.T) {
	t.Run("redirect dedupe and response join", func(t *testing.T) {
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
		})

		entries, dropped := page.NetworkEntries()
		if len(entries) != 1 {
			t.Fatalf("redirect hops: got %d entries, want 1", len(entries))
		}
		if dropped != 0 {
			t.Fatalf("dropped: got %d, want 0", dropped)
		}
		if entries[0].URL != "https://example.com/final" {
			t.Fatalf("url: got %q, want the last redirect hop", entries[0].URL)
		}
		if entries[0].Seq != 1 {
			t.Fatalf("seq: got %d, want 1", entries[0].Seq)
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
		got := entries[0]
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
