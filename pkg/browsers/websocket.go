package browsers

import (
	"math"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

const (
	// websocketRingCap bounds the retained WebSocket frames per page.
	websocketRingCap = 200
	// websocketPayloadLimit bounds a stored frame payload so one frame cannot
	// dominate a response.
	websocketPayloadLimit = 2000
	// websocketSocketCap bounds the live socket metadata so a page that opens
	// sockets without closing them cannot grow the map without limit.
	websocketSocketCap = 256
	// websocketTextOpcode is the CDP opcode whose payload is UTF-8 text; every
	// other opcode carries a base64 encoded binary frame.
	websocketTextOpcode = 1
)

// WebSocketEntry is one observed WebSocket frame, tagged with the socket it
// belongs to and the handshake status and age known when it was captured.
type WebSocketEntry struct {
	// Seq is the capture sequence number, shared with the network ring's shape.
	Seq int64 `json:"seq"`
	// SocketID is the CDP request id of the WebSocket connection.
	SocketID string `json:"socket_id"`
	// URL is the socket's request URL, clipped and empty when the open event
	// was missed.
	URL string `json:"url"`
	// Direction is "sent" (page to server) or "received" (server to page).
	Direction string `json:"direction"`
	// Opcode is the WebSocket message opcode (1 text, 2 binary, others control).
	Opcode int `json:"opcode"`
	// Payload is the UTF-8 text payload with secrets redacted and clipped at
	// websocketPayloadLimit. A non-text frame carries an empty payload.
	Payload string `json:"payload"`
	// PayloadBytes is the frame's true payload size in bytes.
	PayloadBytes int `json:"payload_bytes"`
	// PayloadCut reports a text payload clipped at the limit.
	PayloadCut bool `json:"payload_cut"`
	// Status is the handshake HTTP status from the open event, zero when the
	// handshake was not observed.
	Status int `json:"status"`
	// MS is the elapsed milliseconds since the socket opened, zero when the
	// open event was missed.
	MS int64 `json:"ms"`
}

// webSocketSocket is the metadata captured for one live WebSocket connection.
type webSocketSocket struct {
	url      string
	status   int
	openedAt proto.MonotonicTime
}

// recordWebSocketCreated remembers the socket's URL so its frames can name the
// connection. The metadata map is bounded; frames of an untracked socket are
// still captured with an empty URL.
func (o *pageObserver) recordWebSocketCreated(e *proto.NetworkWebSocketCreated) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.sockets[e.RequestID]; ok {
		o.sockets[e.RequestID].url = cutString(e.URL, networkURLLimit)
		return
	}
	if len(o.sockets) >= websocketSocketCap {
		return
	}
	o.sockets[e.RequestID] = &webSocketSocket{url: cutString(e.URL, networkURLLimit)}
}

// recordWebSocketHandshake captures the handshake status and open timestamp
// that the following frames report as their status and age.
func (o *pageObserver) recordWebSocketHandshake(e *proto.NetworkWebSocketHandshakeResponseReceived) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.sockets[e.RequestID]
	if s == nil {
		if len(o.sockets) >= websocketSocketCap {
			return
		}
		s = &webSocketSocket{}
		o.sockets[e.RequestID] = s
	}
	s.openedAt = e.Timestamp
	if e.Response != nil {
		s.status = e.Response.Status
	}
}

// recordWebSocketClosed forgets the socket metadata. Its frames stay in the
// ring as observed truth.
func (o *pageObserver) recordWebSocketClosed(e *proto.NetworkWebSocketClosed) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sockets, e.RequestID)
}

// recordWebSocketFrameSent appends one page-to-server frame.
func (o *pageObserver) recordWebSocketFrameSent(e *proto.NetworkWebSocketFrameSent) {
	o.recordWebSocketFrame(e.RequestID, e.Timestamp, "sent", e.Response)
}

// recordWebSocketFrameReceived appends one server-to-page frame.
func (o *pageObserver) recordWebSocketFrameReceived(e *proto.NetworkWebSocketFrameReceived) {
	o.recordWebSocketFrame(e.RequestID, e.Timestamp, "received", e.Response)
}

// recordWebSocketFrame builds one frame entry under the observer mutex. The
// socket lookup supplies the URL, handshake status and open time; text payloads
// pass the shared body redactor so a token in a frame is never leaked.
func (o *pageObserver) recordWebSocketFrame(
	id proto.NetworkRequestID,
	at proto.MonotonicTime,
	direction string,
	f *proto.NetworkWebSocketFrame,
) {
	if f == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := WebSocketEntry{
		SocketID:  string(id),
		Direction: direction,
		Opcode:    int(f.Opcode),
	}
	if s := o.sockets[id]; s != nil {
		entry.URL = s.url
		entry.Status = s.status
		if s.openedAt > 0 && at >= s.openedAt {
			entry.MS = int64(math.Round(float64(at-s.openedAt) * 1000))
		}
	}
	if int(f.Opcode) == websocketTextOpcode {
		entry.PayloadBytes = len(f.PayloadData)
		payload, _ := redactBody(f.PayloadData, o.secrets)
		entry.Payload, entry.PayloadCut = cutWebSocketPayload(payload)
	} else {
		entry.PayloadBytes = websocketBinarySize(f.PayloadData)
	}
	o.wsSeq++
	entry.Seq = o.wsSeq
	o.ws.push(entry)
}

// cutWebSocketPayload clips s to websocketPayloadLimit runes, counting the
// ellipsis, and reports whether it had to cut.
func cutWebSocketPayload(s string) (string, bool) {
	const keep = websocketPayloadLimit - 1
	n := 0
	for i := range s {
		if n == keep {
			return s[:i] + ellipsis, true
		}
		n++
	}
	return s, false
}

// websocketBinarySize returns the decoded byte count of a non-text frame
// without decoding it. A payload whose length is not a multiple of four is
// counted as received, matching the previous undecodable fallback.
func websocketBinarySize(data string) int {
	n := len(data)
	if n%4 != 0 {
		return n
	}
	size := n / 4 * 3
	switch {
	case strings.HasSuffix(data, "=="):
		return size - 2
	case strings.HasSuffix(data, "="):
		return size - 1
	}
	return size
}

// WebSocketEntries returns a copy of the observed WebSocket frames, oldest
// first, plus the number dropped by the ring bound. It takes only the
// observer's mutex, never the page lock, so no CDP call runs under a lock.
func (p *Page) WebSocketEntries() ([]WebSocketEntry, int) {
	o := p.observer
	if o == nil {
		return []WebSocketEntry{}, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ws.take(), int(o.ws.dropped)
}
