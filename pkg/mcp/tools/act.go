package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/pageops"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// defaultActTimeout bounds every page_act helper so a missing element (sleeper
// retries) or a wedged page cannot block the server.
const defaultActTimeout = 30 * time.Second

// waitSleepMargin gives the wait action's "ms" sleep room to complete: the
// sleep is bounded by timeout_ms, but the tool-call deadline would otherwise
// be set to the same duration and fire first.
const waitSleepMargin = 5 * time.Second

// actActionNames is page_act's action vocabulary in schema order, taken from
// the pageops action table so the schema enum and the unknown-action error
// cannot drift from the CLI and IPC surfaces.
var actActionNames = pageops.ActionNames()

// actActionList renders actActionNames for an error message.
func actActionList() string {
	return strings.Join(actActionNames, ", ")
}

// failWithHint builds a failure envelope whose remediation is more specific than
// the generic classifier hint, which cannot infer a per-page budget.
func failWithHint(err error, errType, hint string) (string, error) {
	return marshalEnvelope(resultEnvelope{
		Error: &envelopeError{
			Type:    errType,
			Message: err.Error(),
			Hint:    hint,
		},
	})
}

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
	return PageActDescription
}

// InputSchema returns the JSON Schema for page_act's arguments.
func (t *Act) InputSchema() models.InputSchema {
	return newActInputSchema()
}

// actArgs is the strict decoding target for page_act arguments.
type actArgs struct {
	SessionID      browsers.PageID          `json:"session_id"`
	BrowserID      string                   `json:"browser_id"`
	Owner          string                   `json:"owner"`
	Action         string                   `json:"action"`
	Selector       string                   `json:"selector"`
	Handle         string                   `json:"handle"`
	Value          string                   `json:"value"`
	Key            string                   `json:"key"`
	Button         string                   `json:"button"`
	ClickCount     int                      `json:"click_count"`
	X              float64                  `json:"x"`
	Y              float64                  `json:"y"`
	TargetX        float64                  `json:"target_x"`
	TargetY        float64                  `json:"target_y"`
	TargetSelector string                   `json:"target_selector"`
	Origin         string                   `json:"origin"`
	Target         string                   `json:"target"`
	Targets        []string                 `json:"targets"`
	Method         string                   `json:"method"`
	Headers        map[string]string        `json:"headers"`
	Query          map[string]string        `json:"query"`
	MaxBytes       int64                    `json:"max_bytes"`
	Points         []browsers.FuzzPoint     `json:"points"`
	Payloads       []string                 `json:"payloads"`
	Strategy       string                   `json:"strategy"`
	Paths          []string                 `json:"paths"`
	TimeoutMS      int                      `json:"timeout_ms"`
	Raw            bool                     `json:"raw"`
	WaitMode       string                   `json:"wait_mode"`
	Viewport       *browsers.Viewport       `json:"viewport"`
	Device         string                   `json:"device"`
	Site           string                   `json:"site"`
	Name           string                   `json:"name"`
	Mode           string                   `json:"mode"`
	Force          bool                     `json:"force"`
	Rules          []browsers.InterceptRule `json:"rules"`
	Context        string                   `json:"context"`
	Variants       []string                 `json:"variants"`
	Fields         map[string]string        `json:"fields"`
	Values         map[string]string        `json:"values"`
	Submit         string                   `json:"submit"`
}

// validate checks the arguments an action requires and returns a one-line
// recovery hint when one is missing. provided is the raw argument object, so
// optional numeric fields can be distinguished from an explicit zero.
func (a *actArgs) validate(provided map[string]json.RawMessage) error {
	switch a.Action {
	case "click", "input", "type", "hover", "select", "upload_file", "set_style", "set_attr", "set_html":
		if a.Handle == "" && a.Selector == "" {
			return fmt.Errorf("%s requires selector (or a handle from page_fetch what=interactives)", a.Action)
		}
		if a.Action == "set_attr" && a.Name == "" {
			return fmt.Errorf("set_attr requires name (the attribute to set, e.g. href)")
		}
		if a.Action == "set_html" {
			if _, err := pageops.SetHTMLMode(a.Mode); err != nil {
				return fmt.Errorf("set_html: %w", err)
			}
		}
	case "click_at", "hover_at":
		if _, ok := provided["x"]; !ok {
			return fmt.Errorf("%s requires x and y coordinates (e.g. {\"action\":\"%s\",\"x\":100,\"y\":200})", a.Action, a.Action)
		}
		if _, ok := provided["y"]; !ok {
			return fmt.Errorf("%s requires x and y coordinates (e.g. {\"action\":\"%s\",\"x\":100,\"y\":200})", a.Action, a.Action)
		}
	case "eval":
		if a.Value == "" {
			return fmt.Errorf("eval requires value (JavaScript function source, e.g. \"() => document.title\")")
		}
	case "navigate":
		if a.Value == "" {
			return fmt.Errorf("navigate requires value (the target URL)")
		}
	case "post_message":
		if _, ok := provided["value"]; !ok {
			return fmt.Errorf("post_message requires value (the message payload, e.g. {\"t\":\"wm\"})")
		}
	case "replay":
		if a.Target == "" {
			return fmt.Errorf("replay requires target=<seq> or target=<url substring> from page_fetch what=network")
		}
		if a.MaxBytes < 0 {
			return fmt.Errorf("replay max_bytes must be >= 0")
		}
	case "replay_fuzz":
		if a.Target == "" {
			return fmt.Errorf("replay_fuzz requires target=<seq> or target=<url substring> from page_fetch what=network")
		}
		if a.MaxBytes < 0 {
			return fmt.Errorf("replay_fuzz max_bytes must be >= 0")
		}
	case "intercept_add":
		if len(a.Rules) == 0 {
			return fmt.Errorf("intercept_add requires rules (e.g. [{\"url_pattern\":\"*api*\",\"action\":\"block\"}])")
		}
	case "auth_save", "auth_restore":
		if a.Site == "" {
			return fmt.Errorf("%s requires site (the host, e.g. example.org)", a.Action)
		}
		if a.Name == "" {
			return fmt.Errorf("%s requires name (the snapshot name, e.g. main)", a.Action)
		}
	case "poc_bundle":
		if a.Value == "" {
			return fmt.Errorf("poc_bundle requires value=<finding title>")
		}
		if len(a.Paths) == 0 || strings.TrimSpace(a.Paths[0]) == "" {
			return fmt.Errorf("poc_bundle requires paths=[<absolute output directory>]")
		}
	case "fill_form":
		mode, err := pageops.ValidateFillForm(a.Selector, a.Submit, a.Fields, a.Values)
		if err != nil {
			return err
		}
		a.Submit = mode
	}
	return nil
}

// Execute parses the page_act arguments, resolves the session and dispatches to
// the shared pageops implementation. It returns a short confirmation string.
func (t *Act) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a actArgs
	if err := decodeArgs("page_act", args, &a); err != nil {
		return failResult(err)
	}
	if a.Action == "" {
		return failResult(fmt.Errorf("page_act: missing required argument %q", "action"))
	}

	var provided map[string]json.RawMessage
	if err := json.Unmarshal(args, &provided); err != nil {
		return failResult(fmt.Errorf("page_act: invalid arguments: %w", err))
	}
	if err := a.validate(provided); err != nil {
		return failResult(fmt.Errorf("page_act: %w", err))
	}
	if a.Action == "auth_list" {
		out, err := pageops.PageAuthList(ctx, a.Site)
		if err != nil {
			return failResult(fmt.Errorf("page_act: %w", err))
		}
		return okResult(out)
	}
	if pageops.PageFreeAction(a.Action) {
		out, err := pageops.EncodePayload(a.Value, a.Context, a.Variants)
		if err != nil {
			return failResult(fmt.Errorf("page_act: %w", err))
		}
		return okResult(out)
	}
	if a.SessionID == "" {
		return failResult(fmt.Errorf("page_act: missing required argument %q", "session_id"))
	}

	if a.Action == "set_viewport" {
		if a.Viewport == nil {
			return failResult(fmt.Errorf("page_act: set_viewport requires a viewport"))
		}
		if a.Viewport.Width <= 0 || a.Viewport.Height <= 0 {
			return failResult(fmt.Errorf("page_act: viewport width and height must be positive"))
		}
	}

	if a.Action == "emulate" && a.Device == "" {
		return failResult(fmt.Errorf("page_act: emulate requires a device"))
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
	if a.Raw {
		ctx = browsers.WithRawCall(ctx)
	}

	p, err := t.mgr.Page(ctx, a.SessionID, browsers.BrowserID(a.BrowserID), a.Owner)
	if err != nil {
		return failResult(fmt.Errorf("page_act: %w", err))
	}
	if a.Action == "navigate" {
		if err := t.mgr.CheckPageScope(a.SessionID, a.Value); err != nil {
			return failResultForPage(ctx, p, fmt.Errorf("page_act: %w", err))
		}
	}

	before, stateErr := pageops.PageStateIfFree(ctx, p)

	var out string
	switch a.Action {
	case "click":
		out, err = pageops.PageClickFrom(ctx, p, a.Handle, a.Selector, a.Button, a.ClickCount, before)
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
	case "set_style":
		out, err = pageops.PageSetStyle(ctx, p, a.Handle, a.Selector, a.Value)
	case "set_attr":
		out, err = pageops.PageSetAttr(ctx, p, a.Handle, a.Selector, a.Name, a.Value)
	case "set_html":
		out, err = pageops.PageSetHTML(ctx, p, a.Handle, a.Selector, a.Value, a.Mode)
	case "scroll":
		out, err = pageops.PageScroll(ctx, p, a.X, a.Y)
	case "select":
		out, err = pageops.PageSelect(ctx, p, a.Handle, a.Selector, a.Value)
	case "fill_form":
		out, err = pageops.PageFillForm(ctx, p, a.Selector, a.Fields, a.Values, a.Submit, a.WaitMode)
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
	case "clear_logs":
		out, err = pageops.PageClearCapture(p)
	case "dialog_accept":
		out, err = pageops.PageDialogAccept(ctx, p, a.Value)
	case "dialog_dismiss":
		out, err = pageops.PageDialogDismiss(ctx, p, a.Value)
	case "http_auth":
		out, err = pageops.PageHTTPAuth(ctx, p, a.Value)
	case "sinks_install":
		out, err = pageops.PageSinksInstall(ctx, p)
	case "sinks_clear":
		out, err = pageops.PageSinksClear(ctx, p)
	case "replay":
		_, bodySet := provided["value"]
		out, err = pageops.PageReplay(ctx, p, pageops.ReplayRequest{
			Target:   a.Target,
			Method:   a.Method,
			Headers:  a.Headers,
			Query:    a.Query,
			Body:     a.Value,
			BodySet:  bodySet,
			MaxBytes: a.MaxBytes,
			CheckScope: func(rawURL string) error {
				return t.mgr.CheckPageScope(a.SessionID, rawURL)
			},
		})
	case "replay_fuzz":
		var fuzz browsers.FuzzResult
		fuzz, err = p.ReplayFuzz(ctx, browsers.FuzzSpec{
			Target:   a.Target,
			Strategy: browsers.FuzzStrategy(a.Strategy),
			Points:   a.Points,
			Payloads: a.Payloads,
			MaxBytes: a.MaxBytes,
			CheckScope: func(rawURL string) error {
				return t.mgr.CheckPageScope(a.SessionID, rawURL)
			},
		})
		if err == nil {
			var data []byte
			data, err = json.Marshal(fuzz)
			out = string(data)
		}
	case "inject_canary":
		out, err = pageops.PageCanaryInject(ctx, p, a.Targets, a.Value)
	case "postmessage_install":
		out, err = pageops.PagePostMessageInstall(ctx, p)
	case "postmessage_clear":
		out, err = pageops.PagePostMessageClear(ctx, p)
	case "post_message":
		out, err = pageops.PagePostMessageSend(ctx, p, a.Value, a.TargetSelector, a.Origin)
	case "wait_open":
		var popup browsers.Popup
		popup, err = t.mgr.WaitOpen(ctx, a.SessionID, browsers.BrowserID(a.BrowserID), a.Owner)
		if err == nil {
			var data []byte
			data, err = json.Marshal(popup)
			out = string(data)
		}
	case "intercept_add":
		out, err = pageops.PageInterceptAdd(ctx, p, a.Rules)
		var limit *browsers.InterceptRuleLimitError
		if errors.As(err, &limit) {
			return failWithHint(
				fmt.Errorf("page_act: %w", err),
				errInvalidArgs,
				fmt.Sprintf(
					"the page holds the %d-rule cap — reuse an id to replace a rule, or call intercept_clear first",
					limit.Cap,
				),
			)
		}
	case "intercept_list":
		out, err = pageops.PageInterceptList(ctx, p)
	case "intercept_clear":
		out, err = pageops.PageInterceptClear(ctx, p)
	case "auth_save":
		out, err = pageops.PageAuthSave(ctx, p, a.Site, a.Name)
	case "auth_restore":
		out, err = pageops.PageAuthRestore(ctx, p, a.Site, a.Name, a.Force)
	case "poc_bundle":
		out, err = pageops.PagePocBundle(ctx, p, a.Value, a.Paths, a.Target)
	default:
		return failResult(fmt.Errorf("page_act: unknown action=%q (must be one of %s)", a.Action, actActionList()))
	}
	if err != nil {
		return failResultForPage(ctx, p, fmt.Errorf("page_act: %w", err))
	}
	if stateErr == nil {
		after := pageops.PageStateAfterAct(ctx, p, a.Action, before)
		out += pageops.ActStateDeltaFor(a.Action, before, after)
	}
	return okResult(out)
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
	return FormSearchAndSubmitPromptDescription
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
