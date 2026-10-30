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
// network, console, WebSocket, dialog/auth and CSP violation entries emptied
// from the rings.
type captureCleared struct {
	// Network is the number of network entries cleared.
	Network int `json:"network"`
	// Console is the number of console entries cleared.
	Console int `json:"console"`
	// WebSocket is the number of WebSocket frames cleared.
	WebSocket int `json:"websocket"`
	// Dialogs is the number of dialog/auth entries cleared.
	Dialogs int `json:"dialogs"`
	// Violations is the number of CSP violation events cleared.
	Violations int `json:"violations"`
}

// PageClearCapture empties the network, console, WebSocket, dialog/auth and CSP
// violation rings, resets their dropped counters and returns the cleared counts
// as {"network":N,"console":M,"websocket":K,"dialogs":D,"violations":V}. It
// needs no context: the call touches only in-memory state.
func PageClearCapture(p *browsers.Page) (string, error) {
	c := p.ClearCapture()
	dialogs := p.ClearDialogs()
	violations := p.ClearCSPViolations()
	b, err := json.Marshal(captureCleared{
		Network:    c.Network,
		Console:    c.Console,
		WebSocket:  c.WebSocket,
		Dialogs:    dialogs,
		Violations: violations,
	})
	if err != nil {
		return "", fmt.Errorf("encode cleared logs: %w", err)
	}
	return string(b), nil
}
