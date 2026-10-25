package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/pageops"
	"webmcp/pkg/mcp/models"
)

// defaultActTimeout bounds every page_act helper so a missing element (sleeper
// retries) or a wedged page cannot block the server.
const defaultActTimeout = 30 * time.Second

// waitSleepMargin gives the wait action's "ms" sleep room to complete: the
// sleep is bounded by timeout_ms, but the tool-call deadline would otherwise
// be set to the same duration and fire first.
const waitSleepMargin = 5 * time.Second

// Act implements the "page_act" tool: it interacts with a LIVE page that was
// previously opened with fetch_page. Interaction logic is backed by package
// pageops, shared byte-for-byte with the CLI and IPC server.
type Act struct {
	// mgr is the shared browsers.Manager owning the single Browser and its
	// live pages (sessions).
	mgr *browsers.Manager
}

// NewAct constructs the page_act tool wired to the given browsers.Manager.
func NewAct(mgr *browsers.Manager) *Act {
	return &Act{mgr: mgr}
}

// Name returns the MCP tool name "page_act".
func (t *Act) Name() string {
	return "page_act"
}

// Description returns a customer/LLM-facing description of page_act.
func (t *Act) Description() string {
	return "Interacts with a live page opened by fetch_page: click (supports button selection & double-click), " +
		"input, type, press_key (send keys/combos like Enter, Escape, Control+A, Meta+C), " +
		"click_at / hover_at (coordinate-based mouse actions), drag_and_drop (mouse drag), " +
		"upload_file (set files on <input type='file'>), eval (run JS — the primary escape hatch), " +
		"navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, or emulate. " +
		"Pass session_id plus action=... and any required parameters; element actions take a selector " +
		"or a stable handle from page_fetch (handle wins when both are given)."
}

// InputSchema returns the JSON Schema for page_act's arguments.
func (t *Act) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The session_id returned by fetch_page for the page to act on.",
			},
			"action": map[string]any{
				"type": "string",
				"enum": []string{
					"click", "input", "type", "press_key", "click_at", "hover_at",
					"drag_and_drop", "upload_file", "eval", "navigate",
					"back", "forward", "reload", "hover", "scroll", "select",
					"wait", "set_viewport", "emulate",
				},
				"description": "The interaction to perform: click (selector), input (set field text), type (keystrokes), press_key (key/combo e.g. Enter, Escape, Control+A), click_at/hover_at ({x, y} coordinates), drag_and_drop (from {x,y}/selector to {target_x,target_y}/target_selector), upload_file (set file input paths), eval (run JS), navigate (URL), back/forward/reload, hover (selector), scroll (x/y), select (option value), wait (readiness mode), set_viewport (W x H), emulate (device preset).",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector or XPath identifying target element (click/input/type/press_key/hover/select/upload_file/drag_and_drop source).",
			},
			"handle": map[string]any{
				"type":        "string",
				"description": "Element handle from a previous page_fetch (what=interactives|dom|links), e.g. g3k9x2ab-b6. It carries the document that minted it: a handle from a previous document (after navigation, reload or redirect) is rejected as stale. Alternative to selector; when both given, handle wins.",
			},
			"value": map[string]any{
				"type":        "string",
				"description": "Payload value for input/type (text), press_key (key combo fallback), eval (JS source), navigate (URL), select (option value), or upload_file (single path fallback).",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Key or key combination for press_key action (e.g., Enter, Escape, Tab, Backspace, ArrowDown, Control+A, Meta+C, Shift+Tab).",
			},
			"button": map[string]any{
				"type":        "string",
				"enum":        []string{"left", "right", "middle"},
				"default":     "left",
				"description": "Mouse button for click and click_at actions: left, right, or middle.",
			},
			"click_count": map[string]any{
				"type":        "integer",
				"default":     1,
				"description": "Number of mouse clicks for click and click_at actions (1 for single click, 2 for double click).",
			},
			"x": map[string]any{
				"type":        "number",
				"description": "Horizontal coordinate in pixels (scroll, click_at, hover_at, drag_and_drop source).",
			},
			"y": map[string]any{
				"type":        "number",
				"description": "Vertical coordinate in pixels (scroll, click_at, hover_at, drag_and_drop source).",
			},
			"target_x": map[string]any{
				"type":        "number",
				"description": "Target horizontal coordinate in pixels for drag_and_drop action.",
			},
			"target_y": map[string]any{
				"type":        "number",
				"description": "Target vertical coordinate in pixels for drag_and_drop action.",
			},
			"target_selector": map[string]any{
				"type":        "string",
				"description": "Target element selector for drag_and_drop action.",
			},
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Array of local file paths for upload_file action.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional per-call timeout in milliseconds (default 30000); wait action sleeps for this long in ms mode.",
			},
			"wait_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "idle", "stable", "interactable", "ms"},
				"default":     "ms",
				"description": "What to wait for (wait action): load, idle, stable, interactable (needs selector), or ms (sleeps timeout_ms).",
			},
			"viewport": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels."},
					"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels."},
				},
				"description": "Target viewport size for set_viewport action.",
			},
			"device": map[string]any{
				"type":        "string",
				"description": "Go-rod device preset name for emulate action (e.g. IPhoneX, IPadPro, LaptopWithHiDPIScreen; 'clear' resets).",
			},
		},
		Required: []string{"session_id", "action"},
	}
}

// actArgs is the strict decoding target for page_act arguments.
type actArgs struct {
	SessionID      browsers.PageID    `json:"session_id"`
	Action         string             `json:"action"`
	Selector       string             `json:"selector"`
	Handle         string             `json:"handle"`
	Value          string             `json:"value"`
	Key            string             `json:"key"`
	Button         string             `json:"button"`
	ClickCount     int                `json:"click_count"`
	X              float64            `json:"x"`
	Y              float64            `json:"y"`
	TargetX        float64            `json:"target_x"`
	TargetY        float64            `json:"target_y"`
	TargetSelector string             `json:"target_selector"`
	Paths          []string           `json:"paths"`
	TimeoutMS      int                `json:"timeout_ms"`
	WaitMode       string             `json:"wait_mode"`
	Viewport       *browsers.Viewport `json:"viewport"`
	Device         string             `json:"device"`
}

// Execute parses the page_act arguments, resolves the session and dispatches to
// the shared pageops implementation. It returns a short confirmation string.
func (t *Act) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a actArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("page_act: invalid arguments: %w", err)
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("page_act: missing required argument %q", "session_id")
	}
	if a.Action == "" {
		return "", fmt.Errorf("page_act: missing required argument %q", "action")
	}

	if a.Action == "set_viewport" {
		if a.Viewport == nil {
			return "", fmt.Errorf("page_act: set_viewport requires a viewport")
		}
		if a.Viewport.Width <= 0 || a.Viewport.Height <= 0 {
			return "", fmt.Errorf("page_act: viewport width and height must be positive")
		}
	}

	if a.Action == "emulate" && a.Device == "" {
		return "", fmt.Errorf("page_act: emulate requires a device")
	}

	timeout := defaultActTimeout
	if a.Action == "wait" && (a.WaitMode == "" || a.WaitMode == "ms") {
		if a.TimeoutMS > 0 {
			timeout = time.Duration(a.TimeoutMS)*time.Millisecond + waitSleepMargin
		}
	} else if a.TimeoutMS > 0 {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p, err := t.mgr.Page(ctx, a.SessionID)
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}

	var out string
	switch a.Action {
	case "click":
		out, err = pageops.PageClickOpts(ctx, p, a.Handle, a.Selector, a.Button, a.ClickCount)
	case "input":
		out, err = pageops.PageInput(ctx, p, a.Handle, a.Selector, a.Value)
	case "type":
		out, err = pageops.PageType(ctx, p, a.Handle, a.Selector, a.Value)
	case "press_key":
		keyStr := a.Key
		if keyStr == "" {
			keyStr = a.Value
		}
		out, err = pageops.PagePressKey(ctx, p, a.Handle, a.Selector, keyStr)
	case "click_at":
		out, err = pageops.PageClickAt(ctx, p, a.X, a.Y, a.Button, a.ClickCount)
	case "hover_at":
		out, err = pageops.PageHoverAt(ctx, p, a.X, a.Y)
	case "drag_and_drop":
		out, err = pageops.PageDragAndDrop(ctx, p, a.X, a.Y, a.TargetX, a.TargetY, a.Selector, a.TargetSelector)
	case "upload_file":
		paths := a.Paths
		if len(paths) == 0 && a.Value != "" {
			paths = []string{a.Value}
		}
		out, err = pageops.PageUploadFile(ctx, p, a.Handle, a.Selector, paths)
	case "eval":
		out, err = pageops.PageEval(ctx, p, a.Value)
	case "navigate":
		out, err = pageops.PageNavigate(ctx, p, a.Value)
	case "back":
		out, err = pageops.PageBack(ctx, p)
	case "forward":
		out, err = pageops.PageForward(ctx, p)
	case "reload":
		out, err = pageops.PageReload(ctx, p)
	case "hover":
		out, err = pageops.PageHover(ctx, p, a.Handle, a.Selector)
	case "scroll":
		out, err = pageops.PageScroll(ctx, p, a.X, a.Y)
	case "select":
		out, err = pageops.PageSelect(ctx, p, a.Handle, a.Selector, a.Value)
	case "wait":
		d := timeout
		if a.TimeoutMS > 0 {
			d = time.Duration(a.TimeoutMS) * time.Millisecond
		}
		out, err = pageops.PageWait(ctx, p, a.WaitMode, a.Handle, a.Selector, d)
	case "set_viewport":
		out, err = pageops.PageSetViewport(ctx, p, a.Viewport.Width, a.Viewport.Height)
	case "emulate":
		out, err = pageops.PageEmulate(ctx, p, a.Device)
	default:
		return "", fmt.Errorf("page_act: unknown action=%q (must be one of click, input, type, press_key, click_at, hover_at, drag_and_drop, upload_file, eval, navigate, back, forward, reload, hover, scroll, select, wait, set_viewport, emulate)", a.Action)
	}
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}
	return out, nil
}

// Ensure Act satisfies the Tool interface at compile time.
var _ Tool = (*Act)(nil)

// FormSearchAndSubmitPrompt is a co-located prompt definition for page_act.
type FormSearchAndSubmitPrompt struct{}

// NewFormSearchAndSubmitPrompt constructs the form_search_and_submit prompt template.
func NewFormSearchAndSubmitPrompt() *FormSearchAndSubmitPrompt {
	return &FormSearchAndSubmitPrompt{}
}

// Name returns the prompt template name "form_search_and_submit".
func (p *FormSearchAndSubmitPrompt) Name() string { return "form_search_and_submit" }

// Description returns a description of the prompt template.
func (p *FormSearchAndSubmitPrompt) Description() string {
	return "Provides step-by-step guidance for inputting text into search/form fields and submitting the form."
}

// Arguments returns the parameter list accepted by the prompt template.
func (p *FormSearchAndSubmitPrompt) Arguments() []models.PromptArgument {
	return []models.PromptArgument{
		{Name: "session_id", Description: "ID of the active page session", Required: true},
		{Name: "search_query", Description: "Text query to input into search field", Required: true},
		{Name: "selector", Description: "Optional CSS selector for search field (default: input[type='text'])", Required: false},
	}
}

// GetMessages builds the prompt messages payload for the given argument map.
func (p *FormSearchAndSubmitPrompt) GetMessages(args map[string]string) ([]models.PromptMessage, error) {
	sessionID := args["session_id"]
	if sessionID == "" {
		sessionID = "page-1"
	}
	searchQuery := args["search_query"]
	if searchQuery == "" {
		searchQuery = "search query"
	}
	selector := args["selector"]
	if selector == "" {
		selector = "input[type='text'], input[name='q'], input.search"
	}

	text := fmt.Sprintf("Please enter search query %q into session %q:\n"+
		"1. Call page_act with session_id: %q, action: \"input\", selector: %q, value: %q.\n"+
		"2. Verify input value using page_act action: \"eval\" with value: \"() => document.querySelector(%q).value\".\n"+
		"3. Call page_act with action: \"click\" on search button or submit form.\n"+
		"4. Call page_act with action: \"wait\", wait_mode: \"load\" to ensure results land.",
		searchQuery, sessionID, sessionID, selector, searchQuery, selector)

	return []models.PromptMessage{
		{
			Role:    "user",
			Content: models.TextContent{Type: "text", Text: text},
		},
	}, nil
}

// Ensure FormSearchAndSubmitPrompt satisfies the Prompt interface at compile time.
var _ Prompt = (*FormSearchAndSubmitPrompt)(nil)
