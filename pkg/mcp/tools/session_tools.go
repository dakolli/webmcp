package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/mcp/models"
)

// SessionClose implements the "session_close" tool: it closes a live browser
// session opened by fetch_page, releasing its browser tab and freeing memory.
// The session can no longer be used by page_fetch/page_act afterwards.
type SessionClose struct {
	// sessions is the shared SessionManager owning the live browser sessions.
	sessions *SessionManager
}

// NewSessionClose constructs the session_close tool wired to the given
// SessionManager.
func NewSessionClose(sessions *SessionManager) *SessionClose {
	return &SessionClose{sessions: sessions}
}

// Name returns the MCP tool name "session_close".
func (t *SessionClose) Name() string {
	return "session_close"
}

// Description returns a customer/LLM-facing description of session_close.
func (t *SessionClose) Description() string {
	return "Closes a live browser session opened by fetch_page, releasing its " +
		"browser tab. Call this when you are done with a page to free memory; " +
		"the session_id becomes invalid afterwards. Use session_list to see " +
		"which sessions are still open."
}

// InputSchema returns the JSON Schema for session_close's arguments: a required
// session_id.
func (t *SessionClose) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by fetch_page for the session to close.",
			},
		},
		Required: []string{"session_id"},
	}
}

// sessionCloseArgs is the strict decoding target for session_close arguments.
type sessionCloseArgs struct {
	// SessionID identifies the session to close (required).
	SessionID SessionID `json:"session_id"`
}

// Execute parses the session_id and closes the session via the SessionManager.
// This is thin glue around SessionManager.Close and is implemented fully (no
// browser work lives here).
func (t *SessionClose) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a sessionCloseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("session_close: invalid arguments: %w", err)
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("session_close: missing required argument %q", "session_id")
	}
	if err := t.sessions.Close(ctx, a.SessionID); err != nil {
		return "", fmt.Errorf("session_close: %w", err)
	}
	return fmt.Sprintf("session %s closed", a.SessionID), nil
}

// SessionList implements the "session_list" tool: it lists every live browser
// session (id, url, title, created/last-used timestamps) as a JSON array so the
// agent can see what tabs are still open across its session lifecycle.
type SessionList struct {
	// sessions is the shared SessionManager owning the live browser sessions.
	sessions *SessionManager
}

// NewSessionList constructs the session_list tool wired to the given
// SessionManager.
func NewSessionList(sessions *SessionManager) *SessionList {
	return &SessionList{sessions: sessions}
}

// Name returns the MCP tool name "session_list".
func (t *SessionList) Name() string {
	return "session_list"
}

// Description returns a customer/LLM-facing description of session_list.
func (t *SessionList) Description() string {
	return "Lists all live browser sessions (id, url, title, timestamps) as a " +
		"JSON array. Use it to see which fetch_page sessions are still open " +
		"before closing them with session_close."
}

// InputSchema returns the JSON Schema for session_list's arguments: none.
func (t *SessionList) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:       "object",
		Properties: map[string]any{},
	}
}

// Execute returns the current sessions as a JSON array. This is thin glue
// around SessionManager.List and is implemented fully (no browser work lives
// here).
func (t *SessionList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	infos := t.sessions.List(ctx)
	b, err := json.MarshalIndent(infos, "", "  ")
	if err != nil {
		return "", fmt.Errorf("session_list: %w", err)
	}
	return string(b), nil
}

// Ensure both session tools satisfy the Tool interface at compile time.
var _ Tool = (*SessionClose)(nil)
var _ Tool = (*SessionList)(nil)
