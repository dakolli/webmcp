package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// defaultActTimeout bounds every page_act helper so a missing element (sleeper
// retries) or a wedged page cannot block the server: each helper clones the
// live page bound to this timeout before touching rod.
const defaultActTimeout = 30 * time.Second

// Act implements the "page_act" tool: it interacts with a LIVE page that was
// previously opened with fetch_page. The action enum covers the common fast
// paths (click, input, type, navigate, back, forward, reload, hover, scroll,
// select, wait) and "eval" — the primary escape hatch for arbitrary page
// manipulation via JavaScript (p.Rod.Eval). Rod built-ins are the fast path;
// eval is the general path for anything not covered by a built-in.
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
	return "Interacts with a live page opened by fetch_page: click, input, " +
		"type, eval (run arbitrary JavaScript — the primary escape hatch), " +
		"navigate, back, forward, reload, hover, scroll, select, or wait. " +
		"Pass the session_id returned by fetch_page plus action=...; most " +
		"actions take a CSS/XPath selector, and input/type/eval/navigate/select " +
		"take a value."
}

// InputSchema returns the JSON Schema for page_act's arguments: a required
// session_id and action, plus optional selector / value / x / y / timeout_ms /
// wait_mode.
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
				"description": "The interaction to perform: click (CSS/XPath selector), input (set field value), type (type keystrokes), eval (run JS — the primary escape hatch), navigate (go to a URL), back/forward/reload (history), hover (selector), scroll (by x/y pixels), select (option by value), wait (wait_mode: load/idle/stable/interactable/ms).",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector or XPath identifying the target element (click/input/type/hover/select/wait interactable).",
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
				"description": "Optional per-call timeout in milliseconds (default 30000); the wait action's 'ms' mode sleeps for this long.",
			},
			"wait_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"load", "idle", "stable", "interactable", "ms"},
				"default":     "ms",
				"description": "What to wait for (wait action): 'load' = window.onload, 'idle' = no network requests for ~500ms, 'stable' = DOM unchanged for ~500ms, 'interactable' = the element matched by selector is clickable, 'ms' = sleep for timeout_ms. Defaults to 'ms'.",
			},
		},
		Required: []string{"session_id", "action"},
	}
}

// actArgs is the strict decoding target for page_act arguments.
type actArgs struct {
	// SessionID identifies the live page/session to act on (required).
	SessionID browsers.PageID `json:"session_id"`
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
	// TimeoutMS bounds the action in milliseconds (default 30000); the wait
	// action's "ms" mode sleeps for this long.
	TimeoutMS int `json:"timeout_ms"`
	// WaitMode selects the readiness level the wait action waits for:
	// "load" | "idle" | "stable" | "interactable" | "ms" (default "ms").
	WaitMode string `json:"wait_mode"`
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

	// Enforce an explicit per-call timeout so a wedged page (or a stuck wait)
	// cannot block the server: default 30s, honoring timeout_ms from the
	// caller. The derived ctx flows through the per-helper page clones and
	// every rod call below.
	timeout := defaultActTimeout
	if a.TimeoutMS > 0 {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	b, err := t.mgr.Open(ctx) // lazily launches (or returns) the single Browser
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}
	p, err := b.Page(a.SessionID)
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}

	var out string
	switch a.Action {
	case "click":
		out, err = t.actClick(ctx, p, a.Selector)
	case "input":
		out, err = t.actInput(ctx, p, a.Selector, a.Value)
	case "type":
		out, err = t.actType(ctx, p, a.Selector, a.Value)
	case "eval":
		out, err = t.actEval(ctx, p, a.Value)
	case "navigate":
		out, err = t.actNavigate(ctx, p, a.Value)
	case "back":
		out, err = t.actBack(ctx, p)
	case "forward":
		out, err = t.actForward(ctx, p)
	case "reload":
		out, err = t.actReload(ctx, p)
	case "hover":
		out, err = t.actHover(ctx, p, a.Selector)
	case "scroll":
		out, err = t.actScroll(ctx, p, a.X, a.Y)
	case "select":
		out, err = t.actSelect(ctx, p, a.Selector, a.Value)
	case "wait":
		out, err = t.actWait(ctx, p, a.WaitMode, a.Selector, a.TimeoutMS)
	default:
		return "", fmt.Errorf("page_act: unknown action=%q (must be one of click, input, type, eval, navigate, back, forward, reload, hover, scroll, select, wait)", a.Action)
	}
	if err != nil {
		return "", fmt.Errorf("page_act: %w", err)
	}
	return out, nil
}

// actClick clicks the element matched by selector (CSS or XPath). It resolves
// the element, waits until it is interactable, then clicks it with the left
// button. Returns a short confirmation for the LLM.
func (t *Act) actClick(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actClick: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	// Bind ctx once so every rod call honors the Execute ctx and a missing
	// element (sleeper retries) cannot hang the server.
	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("page_act.actClick: element %q: %w", selector, err)
	}
	if _, err := el.WaitInteractable(); err != nil {
		return "", fmt.Errorf("page_act.actClick: element %q: %w", selector, err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return "", fmt.Errorf("page_act.actClick: element %q: %w", selector, err)
	}
	return fmt.Sprintf("clicked selector=%q", selector), nil
}

// actInput sets the value of the element matched by selector. rod's Input
// focuses the element, clears its existing content and types the new value.
// Returns a short confirmation for the LLM.
func (t *Act) actInput(ctx context.Context, p *browsers.Page, selector, value string) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actInput: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("page_act.actInput: element %q: %w", selector, err)
	}
	if err := el.Input(value); err != nil {
		return "", fmt.Errorf("page_act.actInput: element %q: %w", selector, err)
	}
	return fmt.Sprintf("set input selector=%q value=%q", selector, value), nil
}

// actType types keystrokes into the element matched by selector. Backed by
// Element.Focus() + Page.KeyActions().Type(...).Do() or el.Type(...).
func (t *Act) actType(ctx context.Context, p *browsers.Page, selector, value string) (string, error) {
	// TODO(web-mcp): implement — see go-rod-primer.md
	//
	// Intended flow (Phase 1):
	//   p.Lock(); defer p.Unlock()
	//   el, err := p.Rod.Element(selector)
	//   if err := el.Focus(); err != nil { return "", err }
	//   return p.Rod.InsertText(value)
	return "", fmt.Errorf("page_act.actType: not implemented")
}

// actEval runs arbitrary JavaScript in the page — the primary escape hatch for
// any page manipulation not covered by a rod built-in. js must be a JS
// function definition (Eval accepts only functions), e.g. "() => document.title".
// The result is a plain JSON value: scalars are returned verbatim, objects and
// arrays are pretty-printed JSON. JS exceptions surface as *rod.EvalError and
// are returned verbatim so the LLM can fix its script.
func (t *Act) actEval(ctx context.Context, p *browsers.Page, js string) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actEval: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)

	res, err := page.Eval(js)
	if err != nil {
		var evalErr *rod.EvalError
		if errors.As(err, &evalErr) {
			// Surface JS errors verbatim so the LLM can fix its script.
			return "", evalErr
		}
		return "", fmt.Errorf("page_act.actEval: %w", err)
	}

	// res.Value is a gson.JSON: .Str() renders scalars (string/number/bool)
	// as-is, .JSON() pretty-prints structured values.
	out := res.Value.Str()
	switch res.Value.Raw().(type) {
	case nil, bool, float64, string:
	default:
		out = res.Value.JSON("", "  ")
	}
	return fmt.Sprintf("eval result: %s", out), nil
}

// actNavigate navigates the session's page to url and waits for the new page
// to load (rod's Navigate returns after the response header, so WaitLoad is
// mandatory). Returns the final URL and title for the LLM.
func (t *Act) actNavigate(ctx context.Context, p *browsers.Page, url string) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actNavigate: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)
	if err := page.Navigate(url); err != nil {
		return "", fmt.Errorf("page_act.actNavigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("page_act.actNavigate: wait for load: %w", err)
	}
	// Info() reports the final URL (post-redirect) and title; fall back to the
	// requested URL if the metadata read fails — the navigation already landed.
	msg := fmt.Sprintf("navigated to %q", url)
	if info, err := page.Info(); err == nil {
		msg = fmt.Sprintf("navigated to %q (title %q)", info.URL, info.Title)
	}
	return msg, nil
}

// actBack navigates one step back in the page's history and waits for the
// previous page to load (history.back() triggers a fresh navigation). Returns
// the resulting URL.
func (t *Act) actBack(ctx context.Context, p *browsers.Page) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actBack: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)
	if err := page.NavigateBack(); err != nil {
		return "", fmt.Errorf("page_act.actBack: %w", err)
	}
	// WaitLoad returns immediately if no navigation happened (no history); when
	// it does, the URL below reflects the previous page.
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("page_act.actBack: wait for load: %w", err)
	}
	info, err := page.Info()
	if err != nil {
		return "", fmt.Errorf("page_act.actBack: read page info: %w", err)
	}
	return fmt.Sprintf("navigated back to %q", info.URL), nil
}

// actForward navigates one step forward in the page's history and waits for
// the page to load. Returns the resulting URL.
func (t *Act) actForward(ctx context.Context, p *browsers.Page) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actForward: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)
	if err := page.NavigateForward(); err != nil {
		return "", fmt.Errorf("page_act.actForward: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("page_act.actForward: wait for load: %w", err)
	}
	info, err := page.Info()
	if err != nil {
		return "", fmt.Errorf("page_act.actForward: read page info: %w", err)
	}
	return fmt.Sprintf("navigated forward to %q", info.URL), nil
}

// actReload reloads the page and waits for it to load again. Returns a short
// confirmation for the LLM.
func (t *Act) actReload(ctx context.Context, p *browsers.Page) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actReload: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)
	if err := page.Reload(); err != nil {
		return "", fmt.Errorf("page_act.actReload: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("page_act.actReload: wait for load: %w", err)
	}
	return "reloaded page", nil
}

// actHover moves the mouse over the element matched by selector (CSS or
// XPath), triggering hover-dependent UI. Returns a short confirmation.
func (t *Act) actHover(ctx context.Context, p *browsers.Page, selector string) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actHover: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("page_act.actHover: element %q: %w", selector, err)
	}
	if err := el.Hover(); err != nil {
		return "", fmt.Errorf("page_act.actHover: element %q: %w", selector, err)
	}
	return fmt.Sprintf("hovered selector=%q", selector), nil
}

// actScroll scrolls the window by x/y pixels (window.scrollBy via Eval — the
// primary path) and returns the resulting scroll position [scrollX, scrollY].
// Missing x/y are treated as 0.
func (t *Act) actScroll(ctx context.Context, p *browsers.Page, x, y float64) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actScroll: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)

	res, err := page.Eval(
		"(x, y) => { window.scrollBy(x, y); return [window.scrollX, window.scrollY]; }",
		x, y,
	)
	if err != nil {
		return "", fmt.Errorf("page_act.actScroll: %w", err)
	}
	return fmt.Sprintf("scrolled to %s", res.Value.JSON("", "  ")), nil
}

// actSelect selects an <option> with the given value in the element matched by
// selector. rod v0.116.2 has no SelectorTypeValue, so the option is matched
// with a CSS attribute selector [value="..."]. Returns a short confirmation.
func (t *Act) actSelect(ctx context.Context, p *browsers.Page, selector, value string) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actSelect: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	page := p.Rod.Context(ctx).Timeout(defaultActTimeout)

	el, err := page.Element(selector)
	if err != nil {
		return "", fmt.Errorf("page_act.actSelect: element %q: %w", selector, err)
	}
	opt := `[value="` + value + `"]`
	if err := el.Select([]string{opt}, true, rod.SelectorTypeCSSSector); err != nil {
		return "", fmt.Errorf("page_act.actSelect: element %q: %w", selector, err)
	}
	return fmt.Sprintf("selected option value=%q in selector=%q", value, selector), nil
}

// actWait blocks until the page satisfies the requested readiness level, or
// sleeps for a fixed duration. It supports the readiness modes shared with
// fetch_page ("load", "idle", "stable"), an element-level "interactable" mode
// (requires selector), and the default "ms" mode — a ctx-aware sleep honoring
// timeout_ms (default 30000) that lets in-flight requests and animations
// settle before the next action. The "ms" sleep touches no rod state, so it
// does not take the page lock; every rod-backed mode is fully locked.
func (t *Act) actWait(ctx context.Context, p *browsers.Page, mode, selector string, timeoutMS int) (string, error) {
	if p.Rod == nil {
		return "", fmt.Errorf("page_act.actWait: page is closed (nil rod handle)")
	}

	switch mode {
	case "", "ms":
		// Backward-compatible default: sleep (ctx-aware) for timeout_ms.
		d := time.Duration(timeoutMS) * time.Millisecond
		if d <= 0 {
			d = defaultActTimeout
		}
		select {
		case <-time.After(d):
			return fmt.Sprintf("waited %s", d), nil
		case <-ctx.Done():
			return "", fmt.Errorf("page_act.actWait: %w", ctx.Err())
		}

	case "load", "idle", "stable":
		// waitForPage takes the page lock itself (shared with fetch_page).
		if err := waitForPage(ctx, p, mode); err != nil {
			return "", fmt.Errorf("page_act.actWait: %w", err)
		}
		return fmt.Sprintf("waited for %q", mode), nil

	case "interactable":
		if selector == "" {
			return "", fmt.Errorf("page_act.actWait: interactable mode requires a selector")
		}
		p.Lock()
		defer p.Unlock()
		page := p.Rod.Context(ctx).Timeout(defaultActTimeout)
		el, err := page.Element(selector)
		if err != nil {
			return "", fmt.Errorf("page_act.actWait: element %q: %w", selector, err)
		}
		if _, err := el.WaitInteractable(); err != nil {
			return "", fmt.Errorf("page_act.actWait: element %q: %w", selector, err)
		}
		return fmt.Sprintf("element %q is interactable", selector), nil

	default:
		return "", fmt.Errorf("page_act.actWait: unknown wait_mode %q (must be one of load, idle, stable, interactable, ms)", mode)
	}
}

// waitForPage blocks until the page satisfies the requested readiness mode. It
// is the single shared mapping of the readiness levels used by fetch_page
// (wait_for) and page_act (wait_mode): "load" → WaitLoad, "idle" →
// WaitRequestIdle (its returned blocking func is invoked), "stable" →
// WaitDOMStable; an empty mode is treated as "idle". The wait is bound to ctx
// (via a page clone) so cancellation aborts a stuck wait. The page_act-only
// "interactable" and "ms" modes are handled in actWait, not here.
func waitForPage(ctx context.Context, p *browsers.Page, mode string) error {
	if p.Rod == nil {
		return fmt.Errorf("waitForPage: page is closed (nil rod handle)")
	}
	p.Lock()
	defer p.Unlock()

	pc := p.Rod.Context(ctx)
	switch mode {
	case "", "idle":
		// WaitRequestIdle RETURNS a blocking func() — call it, do not return it.
		wait := pc.WaitRequestIdle(500*time.Millisecond, nil, nil, nil)
		wait()
	case "load":
		return pc.WaitLoad()
	case "stable":
		return pc.WaitDOMStable(500*time.Millisecond, 0.05)
	default:
		return fmt.Errorf("waitForPage: unknown wait mode %q (must be one of load, idle, stable)", mode)
	}
	return nil
}

// Ensure Act satisfies the Tool interface at compile time.
var _ Tool = (*Act)(nil)
