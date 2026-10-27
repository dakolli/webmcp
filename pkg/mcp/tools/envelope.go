package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"

	"webmcp/pkg/browsers"
)

// Error types carried in resultEnvelope.Error.Type. They are the agent's
// machine branch signal; the human sentence stays in Error.Message.
const (
	errInvalidArgs     = "invalid_args"
	errNotFound        = "not_found"
	errElementNotFound = "element_not_found"
	errNotInteractable = "not_interactable"
	errAntiBot         = "anti_bot"
	errPageClosed      = "page_closed"
	errTimeout         = "timeout"
	errNavFailed       = "nav_failed"
	errUnknown         = "unknown"
)

// resultEnvelopeDoc is appended to every tool description so an agent knows
// where the success payload and the failure branch live.
const resultEnvelopeDoc = " Every result is one JSON envelope: " +
	`{"ok":true,"data":<payload>}` +
	" on success (data is the JSON object/array to address directly, or a JSON " +
	"string for prose), or " +
	`{"ok":false,"error":{"type":<type>,"message":<human sentence>,"hint":<one-line fix>}}` +
	" on failure. Branch on error.type (invalid_args, not_found, element_not_found, " +
	"not_interactable, anti_bot, page_closed, timeout, nav_failed, unknown), " +
	"apply error.hint when present, and read success payloads under data."

// resultEnvelope is the single result shape every tool returns. Delta is the
// reserved slot for the post-action state delta; it stays omitted until that
// feature fills it.
type resultEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *envelopeError  `json:"error,omitempty"`
	Delta json.RawMessage `json:"delta,omitempty"`
}

// envelopeError is the machine-readable failure branch: a stable type, the
// existing human sentence, and an optional one-line remediation.
type envelopeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// challengeState is the page title and URL read after a failure so a bot
// challenge can be classified even when the error text is a bare timeout or
// navigation failure.
type challengeState struct {
	title string
	url   string
}

// invalidArgsMarkers identify argument-validation failures by their prose.
var invalidArgsMarkers = []string{
	"invalid arguments",
	"missing required argument",
	"unknown what=",
	"unknown action=",
	"invalid wait_for",
	"unknown wait_mode",
	"must be positive",
	"must be >= 0",
	"must be absolute",
	"is not empty — pass a new directory",
	"but the request sent",
	"1 to 64 characters",
	"mutually exclusive",
	"takes no browser_id",
	"requires a browser_id",
	"requires a viewport",
	"requires a device",
	"requires selector",
	"requires a selector",
	"requires value",
	"requires target",
	"requires x and y",
	"unknown device",
	"unknown identity",
	"invalid identity",
	"either selector or handle is required",
	"unknown mouse button",
	"unknown key name",
	"unknown key modifier",
	"empty key token",
	"key specification cannot be empty",
	"no file paths provided",
	"invalid url",
	"invalid --wait",
	"invalid wait option",
	"invalid viewport",
	"invalid canary target",
	"auth: invalid site",
	"auth: empty site",
	"auth: empty name",
	"method override is blank",
	"no network entry matches target",
	"unknown strategy",
	"replay_fuzz: unknown location",
	"at least one injection point",
	"at least one payload",
	"has an empty param",
	"intercept: no rules supplied",
	"intercept: duplicate rule id",
	"intercept: rule id is required",
	"intercept: rule",
}

// handleArgsMarkers identify a handle that this document did not mint. Both
// fixes are the same: re-read the page for a fresh handle, or use a selector.
var handleArgsMarkers = []string{
	"minted for a previous document",
	"element detached from the document",
	"invalid handle",
}

// userAgentMismatchMarkers identify an auth_restore refused because the live
// page agent differs from the stored one.
var userAgentMismatchMarkers = []string{
	"does not match the live page user agent",
}

// notFoundMarkers identify a named resource missing from a local store.
var notFoundMarkers = []string{
	"auth snapshot not found",
}

// hintedError is an error that carries its own one-line remediation. The
// envelope prefers it over the generic per-type hint.
type hintedError interface {
	Hint() string
}

// elementNotFoundMarkers identify a selector that matched nothing.
var elementNotFoundMarkers = []string{"cannot find element"}

// notInteractableMarkers identify an element that exists but cannot be used.
var notInteractableMarkers = []string{
	"not cursor interactable",
	"not interactable",
}

// antiBotMarkers identify a bot challenge from the error prose. A bare
// "challenge" is deliberately absent: it appears in ordinary URLs and replay
// targets, so only multi-word challenge signals count.
var antiBotMarkers = []string{
	"access to this page has been denied",
	"access denied",
	"captcha",
	"attention required",
	"unusual traffic",
	"just a moment",
	"challenge required",
	"verify you are human",
	"checking your browser",
	"bot detection",
	"ddos protection",
}

// antiBotPageMarkers identify a bot challenge from the live page title or URL.
var antiBotPageMarkers = []string{
	"captcha",
	"access denied",
	"attention required",
	"unusual traffic",
	"just a moment",
}

// pageClosedMarkers identify a page that was closed, swept, or lost with its
// browser process.
var pageClosedMarkers = []string{
	"page session is closed",
	"page is closed (nil rod handle)",
	"browsers: page ",
	"no longer exists",
	"inspected target navigated or closed",
	"no target with given id found",
	"page close canceled",
	"wait_open: browser",
}

// navFailedMarkers identify a failed navigation.
var navFailedMarkers = []string{
	"navigation failed",
	"net::err_",
}

// okResult builds the success envelope. A JSON payload is embedded raw so its
// fields stay addressable as data.<field>; prose becomes a JSON string.
func okResult(payload string) (string, error) {
	env := resultEnvelope{OK: true}
	if json.Valid([]byte(payload)) {
		env.Data = json.RawMessage(payload)
	} else {
		quoted, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("encode result: %w", err)
		}
		env.Data = json.RawMessage(quoted)
	}
	return marshalEnvelope(env)
}

// failResult builds the failure envelope from err alone.
func failResult(err error) (string, error) {
	return marshalFailure(err, challengeState{})
}

// failResultForPage builds the failure envelope, using the live page's title
// and URL to recognize a bot challenge when the error text is generic.
func failResultForPage(
	ctx context.Context,
	p *browsers.Page,
	err error,
) (string, error) {
	return marshalFailure(err, pageChallenge(ctx, p))
}

// marshalFailure is the single failure-envelope builder.
func marshalFailure(err error, ch challengeState) (string, error) {
	errType, hint := classifyError(err, ch)
	return marshalEnvelope(resultEnvelope{
		Error: &envelopeError{
			Type:    errType,
			Message: err.Error(),
			Hint:    hint,
		},
	})
}

// marshalEnvelope encodes one compact envelope.
func marshalEnvelope(env resultEnvelope) (string, error) {
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(out), nil
}

// classifyError maps a wrapped tool error to a stable type and an actionable
// hint. An error that carries Hint() supplies its own remediation, which always
// beats the generic per-type hint.
func classifyError(err error, ch challengeState) (string, string) {
	errType, hint := classifyErrorType(err, ch)
	var hinted hintedError
	if errors.As(err, &hinted) {
		if specific := hinted.Hint(); specific != "" {
			return errType, specific
		}
	}
	return errType, hint
}

// classifyErrorType resolves the type and the generic hint. The order is
// deliberate: invalid_args first, page_closed before timeout (reopening is the
// fix), anti_bot before nav_failed (the navigation failed because of the
// challenge), and timeout before nav_failed.
func classifyErrorType(err error, ch challengeState) (string, string) {
	if err == nil {
		return errUnknown, ""
	}
	msg := strings.ToLower(err.Error())

	var (
		missingElement *rod.ElementNotFoundError
		notClickable   *rod.NotInteractableError
		closeCanceled  *rod.PageCloseCanceledError
		navigation     *rod.NavigationError
		lookup         *browsers.PageLookupError
		replay         *browsers.ReplayTransportError
		pageLimit      *browsers.PageLimitError
		fuzzBounds     *browsers.FuzzBoundsError
		rateLimit      *browsers.RateLimitError
		scopeRefused   *browsers.ScopeError
	)

	switch {
	case errors.As(err, &lookup) && lookup.Kind == browsers.PageLookupAmbiguous:
		return errInvalidArgs, "this session id needs a browser — pass browser_id or owner to choose the browser that holds it"
	case errors.As(err, &lookup):
		return errNotFound, "the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner"
	case errors.As(err, &pageLimit):
		return errInvalidArgs, "the browser is at its page cap — call session_close on a session you no longer need, then retry"
	case errors.As(err, &fuzzBounds):
		return errInvalidArgs, hintFor(errInvalidArgs)
	case errors.Is(err, browsers.ErrCanaryNeedsTracer):
		return errInvalidArgs, "arm the tracer first with page_act action=sinks_install, then retry inject_canary"
	case errors.Is(err, browsers.ErrUnknownBrowser):
		return errInvalidArgs, "that browser_id is not live — call browser_list for the running ids and pass one of them"
	case errors.Is(err, browsers.ErrReplayTargetAmbiguous),
		errors.Is(err, browsers.ErrReplayTargetNotFound):
		return errInvalidArgs, hintFor(errInvalidArgs)
	case containsAny(msg, notFoundMarkers):
		return errNotFound, "no snapshot is stored for that site and name — call page_act action=auth_list to see the saved names, or auth_save first"
	case containsAny(msg, handleArgsMarkers):
		return errInvalidArgs, "the handle was not minted by this document — re-read page_fetch what=interactives and pass a fresh handle, or use a selector"
	case containsAny(msg, userAgentMismatchMarkers):
		return errInvalidArgs, "pass force=true to restore anyway, or re-save the snapshot on this browser with auth_save"
	case errors.As(err, &scopeRefused):
		return errInvalidArgs, scopeRefused.Hint()
	case errors.As(err, &rateLimit):
		return errTimeout, rateLimit.Hint()
	case errors.As(err, &replay):
		if replay.TimedOut() {
			return errTimeout, "the replay exceeded its fixed 30s budget — the target is slow or wedged; retry or replay a lighter entry"
		}
		return errNavFailed, "the replay transport failed — verify the URL is reachable and the host's DNS and TLS trust are correct, then retry"
	case containsAny(msg, invalidArgsMarkers):
		return errInvalidArgs, hintFor(errInvalidArgs)
	case errors.As(err, &missingElement) || containsAny(msg, elementNotFoundMarkers):
		return errElementNotFound, hintFor(errElementNotFound)
	case errors.As(err, &notClickable) || containsAny(msg, notInteractableMarkers):
		return errNotInteractable, hintFor(errNotInteractable)
	case ch.challenged() || containsAny(msg, antiBotMarkers):
		return errAntiBot, hintFor(errAntiBot)
	case errors.As(err, &closeCanceled) || containsAny(msg, pageClosedMarkers):
		return errPageClosed, hintFor(errPageClosed)
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled),
		strings.Contains(msg, "context deadline exceeded"):
		return errTimeout, hintFor(errTimeout)
	case errors.As(err, &navigation) || containsAny(msg, navFailedMarkers):
		return errNavFailed, hintFor(errNavFailed)
	default:
		return errUnknown, hintFor(errUnknown)
	}
}

// hintFor returns the one-line remediation for an error type. The unknown type
// has no obvious fix, so it carries no hint.
func hintFor(errType string) string {
	switch errType {
	case errInvalidArgs:
		return "fix the arguments per the tool InputSchema (required fields, enum values)"
	case errNotFound:
		return "the session is not in that browser or owner, or no longer exists — call session_list for the live ids, then pass the matching browser_id or owner"
	case errElementNotFound:
		return "no element matched the selector — read what=dom or what=links and retry with an exact selector"
	case errNotInteractable:
		return "element exists but is not clickable/visible (covered or off-viewport) — scroll it into view or pick a visible element from what=dom"
	case errAntiBot:
		return "the site presented a bot challenge — retry after a wait, toggle stealth via browser_create, or hand off to a human"
	case errPageClosed:
		return "the page was closed, swept by the idle TTL, or the browser restarted — call fetch_page to re-open the URL"
	case errTimeout:
		return "the call exceeded its timeout (default 30s) — retry with a higher timeout_ms, narrow the selector, or the page is wedged"
	case errNavFailed:
		return "the page navigation failed — verify the URL is reachable, retry, or re-fetch the page"
	default:
		return ""
	}
}

// challenged reports whether the live page's title or URL looks like a bot
// challenge page.
func (c challengeState) challenged() bool {
	text := strings.ToLower(c.title + " " + c.url)
	return containsAny(text, antiBotPageMarkers)
}

// pageChallenge reads the page title and URL under a short lock so a challenge
// page can be classified after a generic failure. Any read failure skips the
// heuristic instead of masking the original error.
func pageChallenge(ctx context.Context, p *browsers.Page) challengeState {
	if p == nil {
		return challengeState{}
	}
	unlock, ok := p.TryLock()
	if !ok {
		return challengeState{}
	}
	defer unlock()
	if p.Rod == nil {
		return challengeState{}
	}
	readCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	info, err := p.Rod.Context(readCtx).Info()
	if err != nil {
		return challengeState{}
	}
	return challengeState{title: info.Title, url: info.URL}
}

// containsAny reports whether s contains any of the markers.
func containsAny(s string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
