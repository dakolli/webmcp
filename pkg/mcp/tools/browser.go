package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// singleBrowserID is the id reported for the manager's one Browser. The Manager
// owns a single Browser (AGENTS.md "Browser-first paradigm"); a browser_id →
// Browser map is the planned growth path when multi-browser support lands.
const singleBrowserID = "browser-1"

// BrowserCreate implements the "browser_create" tool: it lazily launches the
// manager's single Browser (a live Chromium instance) and returns its
// browser_id so the agent can open pages inside it via fetch_page / page_list.
type BrowserCreate struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewBrowserCreate constructs the browser_create tool wired to the given
// browsers.Manager.
func NewBrowserCreate(mgr *browsers.Manager) *BrowserCreate {
	return &BrowserCreate{mgr: mgr}
}

// Name returns the MCP tool name "browser_create".
func (t *BrowserCreate) Name() string {
	return "browser_create"
}

// Description returns a customer/LLM-facing description of browser_create.
func (t *BrowserCreate) Description() string {
	return "Lazily launches the shared live browser (one Chromium instance) and " +
		"returns its browser_id. Call this before fetch_page to ensure a " +
		"browser is running; pages opened afterwards live inside it. The " +
		"optional headless/stealth flags are reserved for future per-browser " +
		"configuration."
}

// InputSchema returns the JSON Schema for browser_create's arguments: optional
// boolean headless/stealth flags; no required args.
func (t *BrowserCreate) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"headless": map[string]any{
				"type":        "boolean",
				"description": "Reserved: run the browser headless instead of headful (default follows the manager configuration).",
			},
			"stealth": map[string]any{
				"type":        "boolean",
				"description": "Reserved: apply go-rod/stealth to every page (default follows the manager configuration).",
			},
		},
	}
}

// browserCreateArgs is the strict decoding target for browser_create arguments.
type browserCreateArgs struct {
	// Headless, when set, requests a headless browser (reserved; see Execute).
	Headless *bool `json:"headless"`
	// Stealth, when set, toggles per-page stealth (reserved; see Execute).
	Stealth *bool `json:"stealth"`
}

// Execute parses the optional flags, lazily opens the manager's single Browser
// via Manager.Open (stub) and returns its browser_id as a JSON string.
func (t *BrowserCreate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a browserCreateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("browser_create: invalid arguments: %w", err)
	}
	// TODO(web-mcp): map headless/stealth to per-browser functional options
	// once the Manager supports per-open options. The flags are accepted and
	// type-checked here (json.Unmarshal rejects non-boolean values) but not
	// applied: the shared Manager is constructed once in the Registry with
	// browsers.WithHeadless / browsers.WithStealth.
	if _, err := t.mgr.Open(ctx); err != nil { // lazily launches the single Browser
		return "", fmt.Errorf("browser_create: %w", err)
	}
	data, err := json.Marshal(map[string]string{"browser_id": singleBrowserID})
	if err != nil {
		return "", fmt.Errorf("browser_create: %w", err)
	}
	return string(data), nil
}

// Ensure BrowserCreate satisfies the Tool interface at compile time.
var _ Tool = (*BrowserCreate)(nil)

// BrowserList implements the "browser_list" tool: it reports the manager's
// single Browser (browser_id) and its open pages, or an empty JSON array when
// no browser has been launched yet.
type BrowserList struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewBrowserList constructs the browser_list tool wired to the given
// browsers.Manager.
func NewBrowserList(mgr *browsers.Manager) *BrowserList {
	return &BrowserList{mgr: mgr}
}

// Name returns the MCP tool name "browser_list".
func (t *BrowserList) Name() string {
	return "browser_list"
}

// Description returns a customer/LLM-facing description of browser_list.
func (t *BrowserList) Description() string {
	return "Lists the live browser(s) with their open pages (id, url) as JSON. " +
		"Returns an empty array when no browser is running yet. Use it to see " +
		"what is alive before opening or closing pages or the browser."
}

// InputSchema returns the JSON Schema for browser_list's arguments: none.
func (t *BrowserList) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:       "object",
		Properties: map[string]any{},
	}
}

// Execute returns the manager's browser id and its pages via Browser.Pages;
// when no browser is open it returns an empty JSON array instead of failing
// (a list tool should not error on absence). It deliberately does NOT lazily
// launch the browser.
func (t *BrowserList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	b, err := t.mgr.Browser() // no lazy launch: listing must not start a browser
	if err != nil {
		// No browser open yet — nothing to list.
		return "[]", nil
	}
	data, err := json.MarshalIndent(map[string]any{
		"browser_id": singleBrowserID,
		"pages":      b.Pages(),
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("browser_list: %w", err)
	}
	return string(data), nil
}

// Ensure BrowserList satisfies the Tool interface at compile time.
var _ Tool = (*BrowserList)(nil)

// BrowserClose implements the "browser_close" tool: it tears down the manager's
// single Browser and all of its open pages, so a later browser_create (or
// fetch_page) can launch a fresh browser.
type BrowserClose struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewBrowserClose constructs the browser_close tool wired to the given
// browsers.Manager.
func NewBrowserClose(mgr *browsers.Manager) *BrowserClose {
	return &BrowserClose{mgr: mgr}
}

// Name returns the MCP tool name "browser_close".
func (t *BrowserClose) Name() string {
	return "browser_close"
}

// Description returns a customer/LLM-facing description of browser_close.
func (t *BrowserClose) Description() string {
	return "Closes the live browser and all of its open pages, releasing the " +
		"underlying Chromium process. A later browser_create or fetch_page " +
		"will launch a fresh browser. Call this when you are done with the " +
		"browser to free resources."
}

// InputSchema returns the JSON Schema for browser_close's arguments: a required
// browser_id.
func (t *BrowserClose) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"browser_id": map[string]any{
				"type":        "string",
				"description": "The browser_id returned by browser_create for the browser to close.",
			},
		},
		Required: []string{"browser_id"},
	}
}

// browserCloseArgs is the strict decoding target for browser_close arguments.
type browserCloseArgs struct {
	// BrowserID identifies the browser to close (required).
	BrowserID string `json:"browser_id"`
}

// Execute validates the browser_id and closes the manager's Browser via
// Manager.Close (browser + pages torn down; a later Open can launch a fresh
// one). This is thin glue around Manager.Close and is implemented fully (no
// browser work lives here).
func (t *BrowserClose) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a browserCloseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("browser_close: invalid arguments: %w", err)
	}
	if a.BrowserID == "" {
		return "", fmt.Errorf("browser_close: missing required argument %q", "browser_id")
	}
	// TODO(web-mcp): the id is accepted/validated but the Manager owns a single
	// Browser (AGENTS.md browser-first paradigm); multi-browser support will
	// resolve it against a browser_id → Browser map instead.
	if err := t.mgr.Close(ctx); err != nil {
		return "", fmt.Errorf("browser_close: %w", err)
	}
	return fmt.Sprintf("browser %s closed", a.BrowserID), nil
}

// Ensure BrowserClose satisfies the Tool interface at compile time.
var _ Tool = (*BrowserClose)(nil)

// PageList implements the "page_list" tool: it lists every open page (id, url)
// of the manager's single Browser as a JSON array, mirroring session_list but
// scoped to a browser_id.
type PageList struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewPageList constructs the page_list tool wired to the given
// browsers.Manager.
func NewPageList(mgr *browsers.Manager) *PageList {
	return &PageList{mgr: mgr}
}

// Name returns the MCP tool name "page_list".
func (t *PageList) Name() string {
	return "page_list"
}

// Description returns a customer/LLM-facing description of page_list.
func (t *PageList) Description() string {
	return "Lists all open pages (id, url) of the given browser as a JSON " +
		"array. Use it to see which sessions/tabs are still open inside the " +
		"browser before closing them with session_close."
}

// InputSchema returns the JSON Schema for page_list's arguments: a required
// browser_id.
func (t *PageList) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"browser_id": map[string]any{
				"type":        "string",
				"description": "The browser_id returned by browser_create whose open pages to list.",
			},
		},
		Required: []string{"browser_id"},
	}
}

// pageListArgs is the strict decoding target for page_list arguments.
type pageListArgs struct {
	// BrowserID identifies the browser whose pages to list (required).
	BrowserID string `json:"browser_id"`
}

// Execute validates the browser_id, ensures the browser is open via
// Manager.Open, and returns its pages as an indented JSON array via
// Browser.Pages. This is thin glue around Manager.Open -> Browser.Pages and is
// implemented fully (no browser work lives here).
func (t *PageList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a pageListArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("page_list: invalid arguments: %w", err)
	}
	if a.BrowserID == "" {
		return "", fmt.Errorf("page_list: missing required argument %q", "browser_id")
	}
	// TODO(web-mcp): the id is accepted/validated but the Manager owns a single
	// Browser (AGENTS.md browser-first paradigm); multi-browser support will
	// resolve it against a browser_id → Browser map instead.
	b, err := t.mgr.Open(ctx) // lazily launches (or returns) the single Browser
	if err != nil {
		return "", fmt.Errorf("page_list: %w", err)
	}
	data, err := json.MarshalIndent(b.Pages(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("page_list: %w", err)
	}
	return string(data), nil
}

// Ensure PageList satisfies the Tool interface at compile time.
var _ Tool = (*PageList)(nil)

// BrowserSetupAndEmulatePrompt is a co-located prompt definition for browser_create.
type BrowserSetupAndEmulatePrompt struct{}

// NewBrowserSetupAndEmulatePrompt constructs the browser_setup_and_emulate prompt template.
func NewBrowserSetupAndEmulatePrompt() *BrowserSetupAndEmulatePrompt {
	return &BrowserSetupAndEmulatePrompt{}
}

// Name returns the prompt template name "browser_setup_and_emulate".
func (p *BrowserSetupAndEmulatePrompt) Name() string { return "browser_setup_and_emulate" }

// Description returns a description of the prompt template.
func (p *BrowserSetupAndEmulatePrompt) Description() string {
	return "Provides guidance for initializing a browser session and setting up optional device emulation."
}

// Arguments returns the parameter list accepted by the prompt template.
func (p *BrowserSetupAndEmulatePrompt) Arguments() []models.PromptArgument {
	return []models.PromptArgument{
		{Name: "device", Description: "Optional device preset to emulate (e.g. IPhoneX, IPadPro)", Required: false},
	}
}

// GetMessages builds the prompt messages payload for the given argument map.
func (p *BrowserSetupAndEmulatePrompt) GetMessages(args map[string]string) ([]models.PromptMessage, error) {
	device := args["device"]
	text := "Please set up a live browser session:\n" +
		"1. Call browser_create to initialize the Chromium instance.\n" +
		"2. Call fetch_page with target URL to open an initial tab."
	if device != "" {
		text += fmt.Sprintf("\n3. Call page_act with action: \"emulate\" and device: %q to apply device settings.", device)
	}

	return []models.PromptMessage{
		{
			Role:    "user",
			Content: models.TextContent{Type: "text", Text: text},
		},
	}, nil
}

// Ensure BrowserSetupAndEmulatePrompt satisfies the Prompt interface at compile time.
var _ Prompt = (*BrowserSetupAndEmulatePrompt)(nil)
