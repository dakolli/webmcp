package tools

import "github.com/dakolli/webmcp/pkg/mcp/models"

// SessionCloseDescription is the LLM-facing description of the SessionClose tool.
const SessionCloseDescription = "Closes a live browser session opened by fetch_page, releasing its " +
	"browser tab. When more than one browser is live, pass browser_id or owner " +
	"to declare which browser holds the session; with one live browser both may " +
	"be omitted, and a session held outside the declared owner is refused as " +
	"not_found with the holding browser named and its content untouched. The id " +
	"is resolved from the live browser registry without probing the tab, then closed through the " +
	"browser that owns it, without launching a browser. A page an operation " +
	"still holds is evicted from the session registry after a short bound, " +
	"and its tab is left to the browser teardown. Closing a page that opened " +
	"popups also closes those popups. The session_id becomes " +
	"invalid afterwards. Use session_list to see which sessions are still " +
	"open; an unknown id fails and names the live ids. This never launches a " +
	"browser and never touches a browser that does not own the session. " +
	"Example: { \"session_id\": \"page-1\" }." + ResultEnvelopeDoc

// SessionListDescription is the LLM-facing description of the SessionList tool.
const SessionListDescription = "Lists every live browser session across every live browser and " +
	"identity as a JSON array. Each entry is {id, url, opener?, context, " +
	"title?, stale?}: url and title are read from the live tab, so they " +
	"follow navigations and redirects; opener is the id of the page that " +
	"opened this popup (a target=\"_blank\" link or window.open), absent for " +
	"a top-level page; context is the incognito identity " +
	"('default' for the browser's own cookie jar); stale marks an entry " +
	"whose tab was busy or unreadable, in which case url is the last known " +
	"value. Use it to see which fetch_page sessions are still open before " +
	"closing them with session_close. Pass identity to list one identity " +
	"only; an unknown identity reports the live names and creates nothing. " +
	"It never launches a browser: with none running it returns an empty " +
	"array. Example: {}." + ResultEnvelopeDoc

// ManageSessionLifecyclePromptDescription is the LLM-facing description of the ManageSessionLifecyclePrompt tool.
const ManageSessionLifecyclePromptDescription = "Provides guidance for inspecting active sessions and releasing unused browser tabs."

// newSessionCloseInputSchema returns the input schema of the session_close tool.
func newSessionCloseInputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by fetch_page for the session to close.",
			},
			"browser_id": map[string]any{
				"type":        "string",
				"description": "Optional browser_id to resolve the session inside that browser only (e.g. 'browser-2'). Use it when more than one browser is live; with exactly one live browser it may be omitted.",
			},
			"owner": map[string]any{
				"type":        "string",
				"description": "Optional owner label (1-64 chars: A-Z, a-z, 0-9, '.', '_', '-') restricting the lookup to browsers with that owner. A session held by another owner is not_found, never closed.",
			},
		},
		Required: []string{"session_id"},
	}
}

// newSessionListInputSchema returns the input schema of the session_list tool.
func newSessionListInputSchema() models.InputSchema {
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
