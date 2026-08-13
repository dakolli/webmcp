package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// SessionClose implements the "session_close" tool: it closes a live browser
// page opened by fetch_page, releasing its browser tab and freeing memory.
// The session can no longer be used by page_fetch/page_act afterwards.
type SessionClose struct {
	// mgr is the shared browsers.Manager owning the single Browser and its
	// live pages (sessions).
	mgr *browsers.Manager
}

// NewSessionClose constructs the session_close tool wired to the given
// browsers.Manager.
func NewSessionClose(mgr *browsers.Manager) *SessionClose {
	return &SessionClose{mgr: mgr}
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
	// SessionID identifies the page/session to close (required).
	SessionID browsers.PageID `json:"session_id"`
}

// Execute parses the session_id and closes the page via the browsers.Manager.
// This is thin glue around Manager.Open -> Browser.ClosePage and is implemented
// fully (no browser work lives here).
func (t *SessionClose) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a sessionCloseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("session_close: invalid arguments: %w", err)
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("session_close: missing required argument %q", "session_id")
	}
	b, err := t.mgr.Open(ctx) // lazily launches (or returns) the single Browser
	if err != nil {
		return "", fmt.Errorf("session_close: %w", err)
	}
	if err := b.ClosePage(ctx, a.SessionID); err != nil {
		return "", fmt.Errorf("session_close: %w", err)
	}
	return fmt.Sprintf("session %s closed", a.SessionID), nil
}

// SessionList implements the "session_list" tool: it lists every live browser
// page (id, url) as a JSON array so the agent can see what tabs are still open
// across its session lifecycle.
type SessionList struct {
	// mgr is the shared browsers.Manager owning the single Browser and its
	// live pages (sessions).
	mgr *browsers.Manager
}

// NewSessionList constructs the session_list tool wired to the given
// browsers.Manager.
func NewSessionList(mgr *browsers.Manager) *SessionList {
	return &SessionList{mgr: mgr}
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

// Execute returns the current pages as a JSON array. This is thin glue around
// Manager.Open -> Browser.Pages and is implemented fully (no browser work
// lives here).
func (t *SessionList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	b, err := t.mgr.Open(ctx) // lazily launches (or returns) the single Browser
	if err != nil {
		return "", fmt.Errorf("session_list: %w", err)
	}
	data, err := json.MarshalIndent(b.Pages(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("session_list: %w", err)
	}
	return string(data), nil
}

// Ensure both session tools satisfy the Tool interface at compile time.
var _ Tool = (*SessionClose)(nil)
var _ Tool = (*SessionList)(nil)
