package tools

import "github.com/dakolli/webmcp/pkg/mcp/models"

// FetchPageDescription is the LLM-facing description of the Fetch tool.
const FetchPageDescription = "Opens a URL in a new live browser session and returns a page " +
	"summary (title, url, session_id, byte-size hints). Call this FIRST to " +
	"load a page; then use page_fetch with the returned session_id to read " +
	"its html/dom/text/css/js/links/screenshot/meta, and page_act to " +
	"interact with it. Each fetch_page call creates a new session. Set owner " +
	"(1 to 64 characters from A-Z, a-z, 0-9, '.', '_', '-') to use your own " +
	"browser when several agents share one session; with an owner set it never " +
	"falls back to the shared unowned browser. Example: " +
	"{ \"owner\": \"scraper-a\", \"url\": \"https://example.com\" } and " +
	"{ \"owner\": \"scraper-b\", \"url\": \"https://example.com\" } open two " +
	"separate browsers. identity opens the page in an incognito context " +
	"inside the target browser and defaults to 'default': a new name is " +
	"created on first use, each name is one isolated context, and contexts " +
	"keep separate cookies and storage while sharing one Chromium process. " +
	"Example: { \"url\": \"https://x.test\", \"identity\": \"victim\" }." +
	ResultEnvelopeDoc

// FetchAndSummarizePromptDescription is the LLM-facing description of the FetchAndSummarizePrompt tool.
const FetchAndSummarizePromptDescription = "Loads a web page and provides step-by-step instructions to read metadata and extract main content."

// newFetchInputSchema returns the input schema of the fetch_page tool.
func newFetchInputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The absolute URL (http/https) to open in a new browser session.",
			},
			"browser_id": map[string]any{
				"type":        "string",
				"description": "Optional target browser_id (e.g. 'browser-1', 'browser-2'). Defaults to the default or active browser.",
			},
			"wait_for": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "idle", "stable"},
				"default":     "idle",
				"description": "How long to wait before returning: 'load' = window.onload, 'idle' = no network requests for ~500ms, 'stable' = DOM unchanged for ~500ms. Defaults to 'idle' when omitted. Example: { \"url\": \"https://example.com\", \"wait_for\": \"load\" }.",
			},
			"viewport": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels."},
					"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels."},
				},
				"description": "Optional MANUAL viewport size for the session's page (an explicit option, never a default — pages launch plain with rod's own defaults; applied per-page via emulation incl. matching screen metrics). When both device and viewport are given, device wins.",
			},
			"device": map[string]any{
				"type":        "string",
				"description": "Optional go-rod device preset to emulate on the new page (the primary, opt-in emulation mechanism): a preset name or Title, case-insensitive, e.g. IPhoneX, iphone x, IPadPro, LaptopWithHiDPIScreen; 'clear' resets emulation. Applied BEFORE navigation, so the device UA + metrics govern the load itself (no re-navigate needed). When both device and viewport are given, device wins.",
			},
			"user_agent": map[string]any{
				"type":        "string",
				"description": "Optional User-Agent override for the session's page. The default identity sends Accept-Language: en-US,en;q=0.9.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional per-call timeout in milliseconds (default 30000).",
			},
			"owner": map[string]any{
				"type": "string",
				"description": "Optional owner label for your agent (1 to 64 characters from A-Z, a-z, 0-9, '.', '_', '-'). " +
					"Uses the browser owned by this label, creating one if needed; it never falls back to the shared unowned browser. " +
					"Defaults to the session identity, else 'unowned'.",
			},
			"identity": map[string]any{
				"type": "string",
				"description": "Optional incognito identity inside the target browser (default 'default'). " +
					"Each name is one incognito context created on first use and reused afterwards. " +
					"Identities share one Chromium process but keep separate cookies and storage, so two accounts stay isolated. " +
					"Use one identity per account. The page's identity is reported as context by page_list and session_list. " +
					"Example: { \"url\": \"https://app.test/orders\", \"identity\": \"victim\" }. " +
					"An invalid identity error means the name must be 1 to 64 characters from A-Z, a-z, 0-9, '.', '_' and '-'.",
			},
		},
		Required: []string{"url"},
	}
}
