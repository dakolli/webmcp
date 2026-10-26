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
		"which sessions are still open. If no browser is running this may launch " +
		"one with default options; call browser_create first when you want to " +
		"control launch options. Example: { \"session_id\": \"page-1\" }." +
		resultEnvelopeDoc
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
	if err := decodeArgs("session_close", args, &a); err != nil {
		return failResult(err)
	}
	if a.SessionID == "" {
		return failResult(fmt.Errorf("session_close: missing required argument %q", "session_id"))
	}
	b, err := t.mgr.Open(ctx) // lazily launches (or returns) the single Browser
	if err != nil {
		return failResult(fmt.Errorf("session_close: %w", err))
	}
	// Resolve the session through the manager so a pre-restart session id
	// surfaces the explicit "browser was restarted" error instead of a bare
	// "not found" (or a raw CDP error).
	if _, err := t.mgr.Page(ctx, a.SessionID); err != nil {
		return failResult(fmt.Errorf("session_close: %w", err))
	}
	if err := b.ClosePage(ctx, a.SessionID); err != nil {
		return failResult(fmt.Errorf("session_close: %w", err))
	}
	return okResult(fmt.Sprintf("session %s closed", a.SessionID))
}

// SessionList implements the "session_list" tool: it lists every live browser
// page (id, live url, title and context) of every live browser and identity as
// a JSON array so the agent can see what tabs are still open across its
// session lifecycle.
type SessionList struct {
	// mgr is the shared browsers.Manager owning the live browsers, their
	// identities and their live pages (sessions).
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
	return "Lists every live browser session across every live browser and " +
		"identity as a JSON array. Each entry is {id, url, context, title?, " +
		"stale?}: url and title are read from the live tab, so they follow " +
		"navigations and redirects; context is the incognito identity " +
		"('default' for the browser's own cookie jar); stale marks an entry " +
		"whose tab was busy or unreadable, in which case url is the last known " +
		"value. Use it to see which fetch_page sessions are still open before " +
		"closing them with session_close. Pass identity to list one identity " +
		"only; an unknown identity reports the live names and creates nothing. " +
		"It never launches a browser: with none running it returns an empty " +
		"array. Example: {}." + resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for session_list's arguments: an optional
// identity filter.
func (t *SessionList) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"identity": map[string]any{
				"type":        "string",
				"description": "Optional incognito identity to list (e.g. 'victim', or 'default'). Must already exist; an unknown name reports the live identities and is not created.",
			},
		},
	}
}

// sessionListArgs is the strict decoding target for session_list arguments.
type sessionListArgs struct {
	// Identity optionally narrows the listing to one incognito identity.
	Identity string `json:"identity"`
}

// Execute returns every live page of every live browser and identity as a JSON
// array, narrowed to one identity when requested. It reads the manager only:
// listing never launches a browser.
func (t *SessionList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a sessionListArgs
	if err := decodeArgs("session_list", args, &a); err != nil {
		return failResult(err)
	}
	pages, err := t.mgr.Sessions(a.Identity)
	if err != nil {
		return failResult(fmt.Errorf("session_list: %w", err))
	}
	data, err := json.MarshalIndent(pages, "", "  ")
	if err != nil {
		return failResult(fmt.Errorf("session_list: %w", err))
	}
	return okResult(string(data))
}

// Ensure both session tools satisfy the Tool interface at compile time.
var _ Tool = (*SessionClose)(nil)
var _ Tool = (*SessionList)(nil)

// ManageSessionLifecyclePrompt is a co-located prompt definition for session management tools.
type ManageSessionLifecyclePrompt struct{}

// NewManageSessionLifecyclePrompt constructs the manage_session_lifecycle prompt template.
func NewManageSessionLifecyclePrompt() *ManageSessionLifecyclePrompt {
	return &ManageSessionLifecyclePrompt{}
}

// Name returns the prompt template name "manage_session_lifecycle".
func (p *ManageSessionLifecyclePrompt) Name() string { return "manage_session_lifecycle" }

// Description returns a description of the prompt template.
func (p *ManageSessionLifecyclePrompt) Description() string {
	return "Provides guidance for inspecting active sessions and releasing unused browser tabs."
}

// Arguments returns the parameter list accepted by the prompt template.
func (p *ManageSessionLifecyclePrompt) Arguments() []models.PromptArgument {
	return []models.PromptArgument{}
}

// GetMessages builds the prompt messages payload for the given argument map.
func (p *ManageSessionLifecyclePrompt) GetMessages(args map[string]string) ([]models.PromptMessage, error) {
	text := "Please audit open browser sessions:\n" +
		"1. Call session_list to obtain active tab session_ids and URLs.\n" +
		"2. Identify completed or inactive sessions.\n" +
		"3. Call session_close with session_id for each stale tab to free browser resources."

	return []models.PromptMessage{
		{
			Role:    "user",
			Content: models.TextContent{Type: "text", Text: text},
		},
	}, nil
}

// Ensure ManageSessionLifecyclePrompt satisfies the Prompt interface at compile time.
var _ Prompt = (*ManageSessionLifecyclePrompt)(nil)
