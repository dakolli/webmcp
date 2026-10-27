package browsers

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-rod/rod/lib/proto"
)

func TestWebSocketCapture(t *testing.T) {
	id := proto.NetworkRequestID("ws-1")
	open := func(page *Page) {
		page.observer.recordWebSocketCreated(&proto.NetworkWebSocketCreated{
			RequestID: id,
			URL:       "wss://example.com/socket",
		})
		page.observer.recordWebSocketHandshake(&proto.NetworkWebSocketHandshakeResponseReceived{
			RequestID: id,
			Timestamp: 10,
			Response:  &proto.NetworkWebSocketResponse{Status: 101},
		})
	}

	t.Run("both directions carry the socket url, status, and age", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		open(page)

		page.observer.recordWebSocketFrameSent(&proto.NetworkWebSocketFrameSent{
			RequestID: id,
			Timestamp: 10.5,
			Response:  &proto.NetworkWebSocketFrame{Opcode: 1, PayloadData: `{"token":"abc"}`},
		})
		page.observer.recordWebSocketFrameReceived(&proto.NetworkWebSocketFrameReceived{
			RequestID: id,
			Timestamp: 11,
			Response:  &proto.NetworkWebSocketFrame{Opcode: 1, PayloadData: `{"token":"abc"}`},
		})

		entries, dropped := page.WebSocketEntries()
		if len(entries) != 2 || dropped != 0 {
			t.Fatalf("entries=%d dropped=%d, want 2/0", len(entries), dropped)
		}
		sent, received := entries[0], entries[1]
		if sent.Direction != "sent" || received.Direction != "received" {
			t.Fatalf("directions: got %q/%q", sent.Direction, received.Direction)
		}
		if sent.URL != "wss://example.com/socket" || sent.Status != 101 {
			t.Fatalf("socket metadata: got url=%q status=%d", sent.URL, sent.Status)
		}
		if sent.MS != 500 || received.MS != 1000 {
			t.Fatalf("frame age: got %d/%d ms, want 500/1000", sent.MS, received.MS)
		}
		if strings.Contains(sent.Payload, "abc") {
			t.Fatalf("payload leaked a token: %q", sent.Payload)
		}
		if !strings.Contains(sent.Payload, "<redacted>") {
			t.Fatalf("payload was not redacted: %q", sent.Payload)
		}
	})

	t.Run("text payload clips at the limit", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.recordWebSocketFrameSent(&proto.NetworkWebSocketFrameSent{
			RequestID: id,
			Response:  &proto.NetworkWebSocketFrame{Opcode: 1, PayloadData: strings.Repeat("é", 3000)},
		})

		entries, _ := page.WebSocketEntries()
		if len(entries) != 1 {
			t.Fatalf("entries=%d, want 1", len(entries))
		}
		if !entries[0].PayloadCut {
			t.Fatal("PayloadCut: got false, want true")
		}
		if entries[0].PayloadBytes != 6000 {
			t.Fatalf("PayloadBytes=%d, want 6000", entries[0].PayloadBytes)
		}
		if got := utf8.RuneCountInString(entries[0].Payload); got != websocketPayloadLimit {
			t.Fatalf("payload runes=%d, want %d", got, websocketPayloadLimit)
		}
	})

	t.Run("binary frame reports its size with no payload", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		page.observer.recordWebSocketFrameReceived(&proto.NetworkWebSocketFrameReceived{
			RequestID: id,
			Response:  &proto.NetworkWebSocketFrame{Opcode: 2, PayloadData: "AQID"},
		})

		entries, _ := page.WebSocketEntries()
		if len(entries) != 1 {
			t.Fatalf("entries=%d, want 1", len(entries))
		}
		if entries[0].Payload != "" {
			t.Fatalf("binary payload: got %q, want empty", entries[0].Payload)
		}
		if entries[0].PayloadBytes != 3 {
			t.Fatalf("binary bytes: got %d, want 3", entries[0].PayloadBytes)
		}
	})

	t.Run("binary size arithmetic handles padding", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			in   string
			want int
		}{
			{"no padding", "AQID", 3},
			{"one pad", "AQI=", 2},
			{"two pad", "AQ==", 1},
			{"empty", "", 0},
			{"not a multiple of four", "AQI", 3},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := websocketBinarySize(tc.in); got != tc.want {
					t.Fatalf("websocketBinarySize(%q)=%d, want %d", tc.in, got, tc.want)
				}
			})
		}
	})

	t.Run("ring bound and clear", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		for range 300 {
			page.observer.recordWebSocketFrameSent(&proto.NetworkWebSocketFrameSent{
				RequestID: id,
				Response:  &proto.NetworkWebSocketFrame{Opcode: 1, PayloadData: "x"},
			})
		}

		entries, dropped := page.WebSocketEntries()
		if len(entries) != websocketRingCap || dropped != 100 {
			t.Fatalf("ring: len=%d dropped=%d, want %d/100", len(entries), dropped, websocketRingCap)
		}
		if entries[0].Seq != 101 || entries[len(entries)-1].Seq != 300 {
			t.Fatalf("seq window: got %d..%d, want 101..300", entries[0].Seq, entries[len(entries)-1].Seq)
		}

		counts := page.ClearCapture()
		if counts.WebSocket != websocketRingCap {
			t.Fatalf("cleared websocket=%d, want %d", counts.WebSocket, websocketRingCap)
		}
		if after, dropped := page.WebSocketEntries(); len(after) != 0 || dropped != 0 {
			t.Fatalf("after clear: len=%d dropped=%d, want 0/0", len(after), dropped)
		}
	})
}
