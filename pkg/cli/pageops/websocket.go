package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// PageWebsocket returns the newest buffered WebSocket frames, capped at
// MaxLogEmit and ordered oldest first, as {"entries","count","dropped","truncated"}.
// Deliberately does NOT lock: the engine read touches only the observer's own
// mutex, so a closed page still answers from its in-memory buffer.
func PageWebsocket(_ context.Context, p *browsers.Page) (string, error) {
	entries, dropped := p.WebSocketEntries()
	return encodeLogs(entries, dropped, "websocket")
}

// captureCleared is the JSON shape returned by PageClearCapture: the number of
// network, console and WebSocket entries emptied from the capture rings.
type captureCleared struct {
	// Network is the number of network entries cleared.
	Network int `json:"network"`
	// Console is the number of console entries cleared.
	Console int `json:"console"`
	// WebSocket is the number of WebSocket frames cleared.
	WebSocket int `json:"websocket"`
}

// PageClearCapture empties the network, console and WebSocket rings, resets
// their dropped counters and returns the cleared counts as
// {"network":N,"console":M,"websocket":K}. It needs no context: the call
// touches only the observer's in-memory state.
func PageClearCapture(p *browsers.Page) (string, error) {
	c := p.ClearCapture()
	b, err := json.Marshal(captureCleared{
		Network:   c.Network,
		Console:   c.Console,
		WebSocket: c.WebSocket,
	})
	if err != nil {
		return "", fmt.Errorf("encode cleared logs: %w", err)
	}
	return string(b), nil
}

// init registers the WebSocket frame aspect beside the other self-capping log
// aspects. The ring bounds the output, so max_bytes truncation is not applied.
func init() {
	aspectEntries = append(aspectEntries, aspectEntry{
		name:       "websocket",
		selfCapped: true,
		read: func(ctx context.Context, p *browsers.Page, _ AspectQuery) (string, error) {
			return PageWebsocket(ctx, p)
		},
	})
}
