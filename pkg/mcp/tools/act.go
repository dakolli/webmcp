package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"webmcp/pkg/mcp/models"
)

// Act implements the "page_act" tool: it interacts with a LIVE page that was
// previously opened with fetch_page. The action enum covers the common fast
// paths (click, input, type, navigate, back, forward, reload, hover, scroll,
// select, wait) and "eval" — the primary escape hatch for arbitrary page
// manipulation via JavaScript (s.Page.Eval). Rod built-ins are the fast path;
// eval is the general path for anything not covered by a built-in.
type Act struct {
	// sessions is the shared SessionManager owning the live browser sessions.
	sessions *SessionManager
}

// NewAct constructs the page_act tool wired to the given SessionManager.
func NewAct(sessions *SessionManager) *Act {
	return &Act{sessions: sessions}
}

// Name returns the MCP tool name "page_act".
func (t *Act) Name() string {
	return "page_act"
}

// Description returns a customer/LLM-facing description of page_act.
func (t *Act) Description() string {
	return "Interacts with a live page opened by fetch_page: click, input, " +
		"type, eval (run arbitrary JavaScript — the primary escape hatch), " +
		"navigate, back, forward, reload, hover, scroll, select, or wait. " +
		"Pass the session_id returned by fetch_page plus action=...; most " +
		"actions take a CSS/XPath selector, and input/type/eval/navigate/select " +
		"take a value."
}

// InputSchema returns the JSON Schema for page_act's arguments: a required
// session_id and action, plus optional selector / value / x / y / timeout_ms.
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
					"click", "input", "type", "eval", "navigate",
					"back", "forward", "reload", "hover", "scroll", "select", "wait",
				},
				"description": "The interaction to perform: click (CSS/XPath selector), input (set field value), type (type keystrokes), eval (run JS — the primary escape hatch), navigate (go to a URL), back/forward/reload (history), hover (selector), scroll (by x/y pixels), select (option by value), wait (idle for timeout_ms).",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector or XPath identifying the target element (click/input/type/hover/select).",
			},
			"value": map[string]any{
				"type":        "string",
				"description": "Value for input/type (text), eval (JavaScript function source), navigate (URL) or select (option value).",
			},
			"x": map[string]any{
				"type":        "number",
				"description": "Horizontal scroll offset in pixels (scroll action).",
			},
			"y": map[string]any{
				"type":        "number",
				"description": "Vertical scroll offset in pixels (scroll action).",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Optional per-action timeout in milliseconds (default 30000; used by wait).",
			},
		},
		Required: []string{"session_id", "action"},
	}
}

// actArgs is the strict decoding target for page_act arguments.
type actArgs struct {
	// SessionID identifies the live session to act on (required).
	SessionID SessionID `json:"session_id"`
	// Action is the interaction to perform (required).
	Action string `json:"action"`
	// Selector is the CSS/XPath target for element actions.
	Selector string `json:"selector"`
	// Value is the payload for input/type/eval/navigate/select.
	Value string `json:"value"`
	// X is the horizontal scroll offset (scroll action).
	X float64 `json:"x"`
	// Y is the vertical scroll offset (scroll action).
	Y float64 `json:"y"`
	// TimeoutMS bounds the action in milliseconds (default 30000).
	TimeoutMS int `json:"timeout_ms"`
}

// Execute parses the page_act arguments, resolves the session and dispatches to
// the per-action helper. It returns a short JSON/text confirmation string for
// the LLM.
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

	s, err := t.sessions.Get(ctx, a.SessionID)
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}

	var out string
	switch a.Action {
	case "click":
		out, err = t.actClick(ctx, s, a.Selector)
	case "input":
		out, err = t.actInput(ctx, s, a.Selector, a.Value)
	case "type":
		out, err = t.actType(ctx, s, a.Selector, a.Value)
	case "eval":
		out, err = t.actEval(ctx, s, a.Value)
	case "navigate":
		out, err = t.actNavigate(ctx, s, a.Value)
	case "back":
		out, err = t.actBack(ctx, s)
	case "forward":
		out, err = t.actForward(ctx, s)
	case "reload":
		out, err = t.actReload(ctx, s)
	case "hover":
		out, err = t.actHover(ctx, s, a.Selector)
	case "scroll":
		out, err = t.actScroll(ctx, s, a.X, a.Y)
	case "select":
		out, err = t.actSelect(ctx, s, a.Selector, a.Value)
	case "wait":
		out, err = t.actWait(ctx, s, a.TimeoutMS)
	default:
		return "", fmt.Errorf("page_act: unknown action=%q (must be one of click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait)", a.Action)
	}
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}
	return out, nil
}

// actClick clicks the element matched by selector (CSS or XPath). Backed by
// s.Page.Element(selector) -> WaitInteractable() -> Click(left, 1).
func (t *Act) actClick(ctx context.Context, s *Session, selector string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   el, err := s.Page.Element(selector)
	//   if _, err := el.WaitInteractable(); err != nil { return "", err }
	//   return el.Click(proto.InputMouseButtonLeft, 1)
	return "", fmt.Errorf("page_act.actClick: not implemented")
}

// actInput sets the value of the element matched by selector. Backed by
// s.Page.Element(selector).Input(value).
func (t *Act) actInput(ctx context.Context, s *Session, selector, value string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   el, err := s.Page.Element(selector)
	//   return el.Input(value)
	return "", fmt.Errorf("page_act.actInput: not implemented")
}

// actType types keystrokes into the element matched by selector. Backed by
// Element.Focus() + Page.KeyActions().Type(...).Do() or el.Type(...).
func (t *Act) actType(ctx context.Context, s *Session, selector, value string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   el, err := s.Page.Element(selector)
	//   if err := el.Focus(); err != nil { return "", err }
	//   return s.Page.InsertText(value)
	return "", fmt.Errorf("page_act.actType: not implemented")
}

// actEval runs arbitrary JavaScript in the page — the primary escape hatch for
// any page manipulation not covered by a rod built-in. Backed by
// s.Page.Eval(js) and returns the JSON result. js must be a JS function
// definition, e.g. "() => document.title".
func (t *Act) actEval(ctx context.Context, s *Session, js string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   res, err := s.Page.Eval(js)   // Eval only accepts a JS function definition
	//   return res.JSON, nil          // gson marshals to JSON text
	return "", fmt.Errorf("page_act.actEval: not implemented")
}

// actNavigate navigates the session's page to url and waits for load. Backed by
// s.Page.Navigate(url) -> WaitLoad() (callers may follow with waitForPage).
func (t *Act) actNavigate(ctx context.Context, s *Session, url string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   if err := s.Page.Navigate(url); err != nil { return "", err }
	//   return s.Page.WaitLoad()
	return "", fmt.Errorf("page_act.actNavigate: not implemented")
}

// actBack navigates one step back in the page's history. Backed by
// s.Page.NavigateBack().
func (t *Act) actBack(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   return s.Page.NavigateBack()
	return "", fmt.Errorf("page_act.actBack: not implemented")
}

// actForward navigates one step forward in the page's history. Backed by
// s.Page.NavigateForward().
func (t *Act) actForward(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   return s.Page.NavigateForward()
	return "", fmt.Errorf("page_act.actForward: not implemented")
}

// actReload reloads the page and waits for load. Backed by s.Page.Reload()
// -> WaitLoad().
func (t *Act) actReload(ctx context.Context, s *Session) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   if err := s.Page.Reload(); err != nil { return "", err }
	//   return s.Page.WaitLoad()
	return "", fmt.Errorf("page_act.actReload: not implemented")
}

// actHover moves the mouse over the element matched by selector. Backed by
// s.Page.Element(selector).Hover().
func (t *Act) actHover(ctx context.Context, s *Session, selector string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   el, err := s.Page.Element(selector)
	//   return el.Hover()
	return "", fmt.Errorf("page_act.actHover: not implemented")
}

// actScroll scrolls the page by x/y pixels. Backed by
// s.Page.Mouse.Scroll(x, y, steps).
func (t *Act) actScroll(ctx context.Context, s *Session, x, y float64) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   return s.Page.Mouse.Scroll(x, y, 1)
	return "", fmt.Errorf("page_act.actScroll: not implemented")
}

// actSelect selects an option with the given value in the element matched by
// selector. Backed by s.Page.Element(selector).Select([value], true, ...).
func (t *Act) actSelect(ctx context.Context, s *Session, selector, value string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   s.Lock(); defer s.Unlock()
	//   el, err := s.Page.Element(selector)
	//   return el.Select([]string{value}, true, SelectorTypeValue)
	return "", fmt.Errorf("page_act.actSelect: not implemented")
}

// actWait idles the page for timeoutMS milliseconds (default 30000), letting
// in-flight requests and animations settle before the next action. Backed by
// time.Sleep / WaitRequestIdle.
func (t *Act) actWait(ctx context.Context, s *Session, timeoutMS int) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   d := time.Duration(timeoutMS) * time.Millisecond
	//   if d <= 0 { d = 30 * time.Second }
	//   select { case <-time.After(d): case <-ctx.Done(): return ctx.Err() }
	_ = timeoutMS
	_ = time.Second
	return "", fmt.Errorf("page_act.actWait: not implemented")
}

// Ensure Act satisfies the Tool interface at compile time.
var _ Tool = (*Act)(nil)
