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
	return "Lazily launches the shared live browser (Brave by default, or Chromium) and " +
		"returns its browser_id (browser-1). Reuses browser-1 idempotently when running. " +
		"browser-1 with no profile uses the persistent profile at " +
		"~/.config/browser-mcp/profiles/<browser_name>. A bare profile name resolves under " +
		"~/.config/browser-mcp/profiles; an absolute path is used as given and relative paths " +
		"are rejected. Secondary browsers default to an ephemeral profile that is deleted on close. " +
		"Set persist=true with a profile name to keep that profile (and its logins) after close. " +
		"Passing any option (headless, profile, bin, …) creates a NEW browser id rather than " +
		"reusing browser-1, and fails with a Chromium SingletonLock error if another running " +
		"browser holds the same profile — pass a distinct profile name per browser. " +
		"Example: { \"headless\": true, \"profile\": \"audit\", \"persist\": true }."
}

// InputSchema returns the JSON Schema for browser_create's arguments: optional
// bin, profile, persist, headless, stealth, and position parameters.
func (t *BrowserCreate) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"bin": map[string]any{
				"type":        "string",
				"description": "Optional custom browser binary path or command (e.g. 'brave-browser', 'google-chrome').",
			},
			"profile": map[string]any{
				"type":        "string",
				"description": "Optional profile name ('brave', 'chrome', 'firefox', 'default' or any label) resolved under ~/.config/browser-mcp/profiles, or an absolute directory path. Relative paths are rejected.",
			},
			"persist": map[string]any{
				"type":        "boolean",
				"description": "Keep the profile (and its logins) after the browser closes. Default false: secondary browsers get an ephemeral profile deleted on close, while browser-1 with no explicit profile stays persistent.",
			},
			"headless": map[string]any{
				"type":        "boolean",
				"description": "Run the browser headless instead of headful (default follows manager configuration).",
			},
			"stealth": map[string]any{
				"type":        "boolean",
				"description": "Apply go-rod/stealth to every page (default follows manager configuration).",
			},
			"position": map[string]any{
				"type":        "string",
				"enum":        []string{"left", "right", "max", "normal"},
				"description": "Optional window position split screen preset: 'left' (left screen half), 'right' (right screen half), 'max', 'normal'.",
			},
		},
	}
}

// browserCreateArgs is the strict decoding target for browser_create arguments.
type browserCreateArgs struct {
	Bin      string `json:"bin,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Position string `json:"position,omitempty"`
	Headless *bool  `json:"headless,omitempty"`
	Stealth  *bool  `json:"stealth,omitempty"`
	Persist  *bool  `json:"persist,omitempty"`
}

// Execute parses options and creates a new distinct Browser via Manager.CreateBrowser.
func (t *BrowserCreate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a browserCreateArgs
	if len(args) > 0 && string(args) != "{}" {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("browser_create: invalid arguments: %w", err)
		}
	}

	var opts []browsers.Option
	if a.Bin != "" {
		opts = append(opts, browsers.WithBin(a.Bin))
	}
	if a.Profile != "" {
		opts = append(opts, browsers.WithProfileName(a.Profile))
	}
	if a.Persist != nil {
		opts = append(opts, browsers.WithPersist(*a.Persist))
	}
	if a.Position != "" {
		opts = append(opts, browsers.WithPosition(a.Position))
	}
	if a.Headless != nil {
		opts = append(opts, browsers.WithHeadless(*a.Headless))
	}
	if a.Stealth != nil {
		opts = append(opts, browsers.WithStealth(*a.Stealth))
	}

	id, _, err := t.mgr.CreateBrowser(ctx, opts...)
	if err != nil {
		return "", fmt.Errorf("browser_create: %w", err)
	}

	data, err := json.Marshal(map[string]string{"browser_id": string(id)})
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

// Execute returns the snapshot of all live browsers and their open pages.
func (t *BrowserList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	active := t.mgr.Browsers()
	if len(active) == 0 {
		return "[]", nil
	}

	type browserEntry struct {
		BrowserID  string              `json:"browser_id"`
		Bin        string              `json:"bin,omitempty"`
		ProfileDir string              `json:"profile_dir,omitempty"`
		Pages      []browsers.PageInfo `json:"pages"`
	}

	var result []browserEntry
	for id, b := range active {
		result = append(result, browserEntry{
			BrowserID:  string(id),
			Bin:        b.BinPath(),
			ProfileDir: b.ProfileDir(),
			Pages:      b.Pages(),
		})
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("browser_list: %w", err)
	}
	return string(data), nil
}

// Ensure BrowserList satisfies the Tool interface at compile time.
var _ Tool = (*BrowserList)(nil)

// BrowserClose implements the "browser_close" tool: it tears down the manager's
// specified Browser and all of its open pages.
type BrowserClose struct {
	// mgr is the shared browsers.Manager owning the browsers.
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
		"underlying Chromium process. Pass browser_id to close a specific browser, " +
		"or omit to close all browsers. An unknown browser_id returns a not-found " +
		"error listing the running ids — call browser_list first to confirm ids. " +
		"Example: { \"browser_id\": \"browser-1\" } (omit to close all)."
}

// InputSchema returns the JSON Schema for browser_close's arguments: an optional browser_id.
func (t *BrowserClose) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"browser_id": map[string]any{
				"type":        "string",
				"description": "Optional browser_id returned by browser_create to close. Omit to close all browsers.",
			},
		},
	}
}

// browserCloseArgs is the strict decoding target for browser_close arguments.
type browserCloseArgs struct {
	BrowserID string `json:"browser_id"`
}

// Execute closes the target browser or all browsers.
func (t *BrowserClose) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a browserCloseArgs
	if len(args) > 0 && string(args) != "{}" {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("browser_close: invalid arguments: %w", err)
		}
	}
	if err := t.mgr.CloseBrowser(ctx, browsers.BrowserID(a.BrowserID)); err != nil {
		return "", fmt.Errorf("browser_close: %w", err)
	}
	if a.BrowserID == "" {
		return "all browsers closed", nil
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
		"browser before closing them with session_close. An unknown browser_id " +
		"returns a not-found error listing the running ids. " +
		"Example: { \"browser_id\": \"browser-1\" }."
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

// Execute validates the browser_id, resolves it against the manager's live
// browsers (without launching one), and returns its pages as an indented JSON
// array via Browser.Pages.
func (t *PageList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a pageListArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("page_list: invalid arguments: %w", err)
	}
	if a.BrowserID == "" {
		return "", fmt.Errorf("page_list: missing required argument %q", "browser_id")
	}
	b, err := t.mgr.Browser(browsers.BrowserID(a.BrowserID))
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
