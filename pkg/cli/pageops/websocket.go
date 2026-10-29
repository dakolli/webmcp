package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// PageWebSocket returns the newest buffered WebSocket frames, capped at
// MaxLogEmit and ordered oldest first, as {entries,count,dropped,truncated}.
// It rejects a page the watchdog marked unusable before reading the ring.
func PageWebSocket(_ context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
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
