package toolenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// ResultEnvelopeDoc is appended to every tool description so an agent knows
// where the success payload and the failure branch live.
const ResultEnvelopeDoc = " Every result is one JSON envelope: " +
	`{"ok":true,"data":<payload>}` +
	" on success (data is the JSON object/array to address directly, or a JSON " +
	"string for prose), or " +
	`{"ok":false,"error":{"type":<type>,"message":<human sentence>,"hint":<one-line fix>}}` +
	" on failure. Branch on error.type (invalid_args, not_found, element_not_found, " +
	"not_interactable, anti_bot, page_closed, timeout, nav_failed, unknown), " +
	"apply error.hint when present, and read success payloads under data."

// Error types carried in ResultEnvelope.Error.Type. They are the agent's
// machine branch signal; the human sentence stays in Error.Message.
const (
	ErrInvalidArgs     = "invalid_args"
	ErrNotFound        = "not_found"
	ErrElementNotFound = "element_not_found"
	ErrNotInteractable = "not_interactable"
	ErrAntiBot         = "anti_bot"
	ErrPageClosed      = "page_closed"
	ErrTimeout         = "timeout"
	ErrNavFailed       = "nav_failed"
	ErrUnknown         = "unknown"
)

// ResultEnvelope is the single result shape every tool returns. Delta is the
// reserved slot for the post-action state delta; it stays omitted until that
// feature fills it.
type ResultEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *EnvelopeError  `json:"error,omitempty"`
	Delta json.RawMessage `json:"delta,omitempty"`
}

// EnvelopeError is the machine-readable failure branch: a stable type, the
// existing human sentence, and an optional one-line remediation.
type EnvelopeError struct {
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

// decodeArgsMarkers identify a failure raised while decoding or validating
// tool arguments, where the generic schema hint is the real remediation. Every
// other invalid_args marker is a runtime refusal whose own message already
// states the fix, so it carries no generic hint.
var decodeArgsMarkers = []string{
	"invalid arguments",
	"but the request sent",
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
	"caido: no request with id",
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

// timeoutMarkers identify a deadline that an inner library flattened with %v,
// so errors.Is can no longer see context.DeadlineExceeded.
var timeoutMarkers = []string{"context deadline exceeded"}

// navFailedMarkers identify a failed navigation.
var navFailedMarkers = []string{
	"navigation failed",
	"net::err_",
}

// OKResult builds the success envelope. A JSON payload is embedded raw so its
// fields stay addressable as data.<field>; prose becomes a JSON string.
func OKResult(payload string) (string, error) {
	env := ResultEnvelope{OK: true}
	if json.Valid([]byte(payload)) {
		env.Data = json.RawMessage(payload)
	} else {
		quoted, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("encode result: %w", err)
		}
		env.Data = json.RawMessage(quoted)
	}
	return MarshalEnvelope(env)
}

// FailResult builds the failure envelope from err alone.
func FailResult(err error) (string, error) {
	return marshalFailure(err, challengeState{})
}

// FailResultForPage builds the failure envelope, using the live page's title
// and URL to recognize a bot challenge when the error text is generic.
func FailResultForPage(
	ctx context.Context,
	p *browsers.Page,
	err error,
) (string, error) {
	return marshalFailure(err, pageChallenge(ctx, p))
}

// marshalFailure is the single failure-envelope builder.
func marshalFailure(err error, ch challengeState) (string, error) {
	c := classifyError(err, ch)
	return MarshalEnvelope(ResultEnvelope{
		Error: &EnvelopeError{
			Type:    c.Type,
			Message: err.Error(),
			Hint:    c.Hint,
		},
	})
}

// MarshalEnvelope encodes one compact envelope.
func MarshalEnvelope(env ResultEnvelope) (string, error) {
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(out), nil
}

// Remediations that name the exact next call. They stay in the tool layer so
// the engine never carries LLM-facing prose.
const (
	hintLookupAmbiguous = "this session id needs a browser — pass browser_id or owner to choose the browser that holds it"
	hintLookupMissing   = "the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner"
	hintPageCap         = "the browser is at its page cap — call session_close on a session you no longer need, then retry"
	hintCanaryTracer    = "arm the tracer first with page_act action=sinks_install, then retry inject_canary"
	hintUnknownBrowser  = "that browser_id is not live — call browser_list for the running ids and pass one of them"
	hintReplaySlow      = "the replay exceeded its fixed 30s budget — the target is slow or wedged; retry or replay a lighter entry"
	hintReplayTransport = "the replay transport failed — verify the URL is reachable and the host's DNS and TLS trust are correct, then retry"
	hintAuthSnapshot    = "no snapshot is stored for that site and name — call page_act action=auth_list to see the saved names, or auth_save first"
	hintStaleHandle     = "the handle was not minted by this document — re-read page_fetch what=interactives and pass a fresh handle, or use a selector"
	hintUserAgentForce  = "pass force=true to restore anyway, or re-save the snapshot on this browser with auth_save"
)

// classification is one resolved failure: the stable type an agent branches on
// and the one-line remediation it can act on.
type classification struct {
	Type string
	Hint string
}

// plain pairs a type with its generic remediation.
func plain(errType string) classification {
	return classification{Type: errType, Hint: hintFor(errType)}
}

// isAs reports whether err carries a T anywhere in its chain.
func isAs[T error](err error) bool {
	_, ok := errors.AsType[T](err)
	return ok
}

// hintOf returns the remediation that the first T in the chain carries.
func hintOf[T interface {
	error
	hintedError
}](err error) string {
	hinted, ok := errors.AsType[T](err)
	if !ok {
		return ""
	}
	return hinted.Hint()
}

// classifyError maps a wrapped tool error to a stable type and an actionable
// hint. An error that carries Hint() supplies its own remediation, which always
// beats the generic per-type hint.
func classifyError(err error, ch challengeState) classification {
	c := classifyErrorType(err, ch)
	var hinted hintedError
	if errors.As(err, &hinted) {
		if specific := hinted.Hint(); specific != "" {
			c.Hint = specific
		}
	}
	return c
}

// classifyErrorType resolves the type and the generic hint. Typed errors win
// over prose markers, and the typed order matters: invalid_args first, scope
// before rate limit, page_closed before timeout (reopening is the fix),
// anti_bot before nav_failed (the navigation failed because of the challenge),
// and timeout before nav_failed.
func classifyErrorType(err error, ch challengeState) classification {
	if err == nil {
		return classification{Type: ErrUnknown}
	}
	msg := strings.ToLower(err.Error())

	switch {
	case isAs[*browsers.PageLookupError](err):
		return lookupFailure(err)
	case isAs[*browsers.PageLimitError](err):
		return classification{Type: ErrInvalidArgs, Hint: hintPageCap}
	case isAs[*browsers.FuzzBoundsError](err):
		return plain(ErrInvalidArgs)
	case errors.Is(err, browsers.ErrCanaryNeedsTracer):
		return classification{Type: ErrInvalidArgs, Hint: hintCanaryTracer}
	case errors.Is(err, browsers.ErrUnknownBrowser):
		return classification{Type: ErrInvalidArgs, Hint: hintUnknownBrowser}
	case errors.Is(err, browsers.ErrReplayTargetAmbiguous),
		errors.Is(err, browsers.ErrReplayTargetNotFound):
		return plain(ErrInvalidArgs)
	case containsAny(msg, notFoundMarkers):
		// Stands in for a typed not-found error from the auth store.
		return classification{Type: ErrNotFound, Hint: hintAuthSnapshot}
	case containsAny(msg, handleArgsMarkers):
		// Stands in for a typed stale-handle error.
		return classification{Type: ErrInvalidArgs, Hint: hintStaleHandle}
	case containsAny(msg, userAgentMismatchMarkers):
		// Stands in for a typed user-agent mismatch error.
		return classification{Type: ErrInvalidArgs, Hint: hintUserAgentForce}
	case isAs[*browsers.ScopeError](err):
		return classification{Type: ErrInvalidArgs, Hint: hintOf[*browsers.ScopeError](err)}
	case isAs[*browsers.RateLimitError](err):
		// A rate limit is a retryable delay, so it shares the timeout type.
		return classification{Type: ErrTimeout, Hint: hintOf[*browsers.RateLimitError](err)}
	case isAs[*browsers.ReplayTransportError](err):
		return replayFailure(err)
	case containsAny(msg, invalidArgsMarkers):
		// Stands in for typed validation errors raised across the tools.
		if containsAny(msg, decodeArgsMarkers) {
			return plain(ErrInvalidArgs)
		}
		return classification{Type: ErrInvalidArgs}
	case isAs[*rod.ElementNotFoundError](err), containsAny(msg, elementNotFoundMarkers):
		return plain(ErrElementNotFound)
	case isAs[*rod.NotInteractableError](err), containsAny(msg, notInteractableMarkers):
		return plain(ErrNotInteractable)
	case ch.challenged(), containsAny(msg, antiBotMarkers):
		return plain(ErrAntiBot)
	case isAs[*rod.PageCloseCanceledError](err), containsAny(msg, pageClosedMarkers):
		return plain(ErrPageClosed)
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled),
		containsAny(msg, timeoutMarkers):
		return plain(ErrTimeout)
	case isAs[*rod.NavigationError](err), containsAny(msg, navFailedMarkers):
		return plain(ErrNavFailed)
	default:
		return plain(ErrUnknown)
	}
}

// lookupFailure classifies a session lookup miss. An ambiguous id needs a
// browser choice, a plain miss needs the live id list.
func lookupFailure(err error) classification {
	lookup, _ := errors.AsType[*browsers.PageLookupError](err)
	if lookup.Kind == browsers.PageLookupAmbiguous {
		return classification{Type: ErrInvalidArgs, Hint: hintLookupAmbiguous}
	}
	return classification{Type: ErrNotFound, Hint: hintLookupMissing}
}

// replayFailure classifies a replay transport failure by whether it timed out.
func replayFailure(err error) classification {
	replay, _ := errors.AsType[*browsers.ReplayTransportError](err)
	if replay.TimedOut() {
		return classification{Type: ErrTimeout, Hint: hintReplaySlow}
	}
	return classification{Type: ErrNavFailed, Hint: hintReplayTransport}
}

// hintFor returns the one-line remediation for an error type. The unknown type
// has no obvious fix, so it carries no hint.
func hintFor(errType string) string {
	switch errType {
	case ErrInvalidArgs:
		return "fix the arguments per the tool InputSchema (required fields, enum values)"
	case ErrNotFound:
		return "the session is not in that browser or owner, or no longer exists — call session_list for the live ids, then pass the matching browser_id or owner"
	case ErrElementNotFound:
		return "no element matched the selector — read what=dom or what=links and retry with an exact selector"
	case ErrNotInteractable:
		return "element exists but is not clickable/visible (covered or off-viewport) — scroll it into view or pick a visible element from what=dom"
	case ErrAntiBot:
		return "the site presented a bot challenge — retry after a wait, toggle stealth via browser_create, or hand off to a human"
	case ErrPageClosed:
		return "the page was closed, swept by the idle TTL, or the browser restarted — call fetch_page to re-open the URL"
	case ErrTimeout:
		return "the call exceeded its timeout (default 30s) — retry with a higher timeout_ms, narrow the selector, or the page is wedged"
	case ErrNavFailed:
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
