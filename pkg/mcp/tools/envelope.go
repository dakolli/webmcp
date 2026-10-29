package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-rod/rod"

	"github.com/dakolli/webmcp/pkg/browsers"
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
	c := classifyError(err, ch)
	return marshalEnvelope(resultEnvelope{
		Error: &envelopeError{
			Type:    c.Type,
			Message: err.Error(),
			Hint:    c.Hint,
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
		return classification{Type: errUnknown}
	}
	msg := strings.ToLower(err.Error())

	switch {
	case isAs[*browsers.PageLookupError](err):
		return lookupFailure(err)
	case isAs[*browsers.PageLimitError](err):
		return classification{Type: errInvalidArgs, Hint: hintPageCap}
	case isAs[*browsers.FuzzBoundsError](err):
		return plain(errInvalidArgs)
	case errors.Is(err, browsers.ErrCanaryNeedsTracer):
		return classification{Type: errInvalidArgs, Hint: hintCanaryTracer}
	case errors.Is(err, browsers.ErrUnknownBrowser):
		return classification{Type: errInvalidArgs, Hint: hintUnknownBrowser}
	case errors.Is(err, browsers.ErrReplayTargetAmbiguous),
		errors.Is(err, browsers.ErrReplayTargetNotFound):
		return plain(errInvalidArgs)
	case containsAny(msg, notFoundMarkers):
		// Stands in for a typed not-found error from the auth store.
		return classification{Type: errNotFound, Hint: hintAuthSnapshot}
	case containsAny(msg, handleArgsMarkers):
		// Stands in for a typed stale-handle error.
		return classification{Type: errInvalidArgs, Hint: hintStaleHandle}
	case containsAny(msg, userAgentMismatchMarkers):
		// Stands in for a typed user-agent mismatch error.
		return classification{Type: errInvalidArgs, Hint: hintUserAgentForce}
	case isAs[*browsers.ScopeError](err):
		return classification{Type: errInvalidArgs, Hint: hintOf[*browsers.ScopeError](err)}
	case isAs[*browsers.RateLimitError](err):
		// A rate limit is a retryable delay, so it shares the timeout type.
		return classification{Type: errTimeout, Hint: hintOf[*browsers.RateLimitError](err)}
	case isAs[*browsers.ReplayTransportError](err):
		return replayFailure(err)
	case containsAny(msg, invalidArgsMarkers):
		// Stands in for typed validation errors raised across the tools.
		if containsAny(msg, decodeArgsMarkers) {
			return plain(errInvalidArgs)
		}
		return classification{Type: errInvalidArgs}
	case isAs[*rod.ElementNotFoundError](err), containsAny(msg, elementNotFoundMarkers):
		return plain(errElementNotFound)
	case isAs[*rod.NotInteractableError](err), containsAny(msg, notInteractableMarkers):
		return plain(errNotInteractable)
	case ch.challenged(), containsAny(msg, antiBotMarkers):
		return plain(errAntiBot)
	case isAs[*rod.PageCloseCanceledError](err), containsAny(msg, pageClosedMarkers):
		return plain(errPageClosed)
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled),
		containsAny(msg, timeoutMarkers):
		return plain(errTimeout)
	case isAs[*rod.NavigationError](err), containsAny(msg, navFailedMarkers):
		return plain(errNavFailed)
	default:
		return plain(errUnknown)
	}
}

// lookupFailure classifies a session lookup miss. An ambiguous id needs a
// browser choice, a plain miss needs the live id list.
func lookupFailure(err error) classification {
	lookup, _ := errors.AsType[*browsers.PageLookupError](err)
	if lookup.Kind == browsers.PageLookupAmbiguous {
		return classification{Type: errInvalidArgs, Hint: hintLookupAmbiguous}
	}
	return classification{Type: errNotFound, Hint: hintLookupMissing}
}

// replayFailure classifies a replay transport failure by whether it timed out.
func replayFailure(err error) classification {
	replay, _ := errors.AsType[*browsers.ReplayTransportError](err)
	if replay.TimedOut() {
		return classification{Type: errTimeout, Hint: hintReplaySlow}
	}
	return classification{Type: errNavFailed, Hint: hintReplayTransport}
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

// coerceNumberString rewrites an argument that failed to decode because a JSON
// string was sent for a numeric field, when that string is a bare number that
// fits the field's type. Clients that stringify scalars can still set numeric
// fields; a non-numeric string keeps the original type error.
func coerceNumberString(args json.RawMessage, err error) (json.RawMessage, bool) {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return nil, false
	}
	want := typeErr.Type
	for want != nil && want.Kind() == reflect.Pointer {
		want = want.Elem()
	}
	if want == nil || !isNumberKind(want.Kind()) {
		return nil, false
	}
	raw, ok := fieldValue(args, typeErr.Field)
	if !ok {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	literal := strings.TrimSpace(s)
	if !isBareNumber(literal) {
		return nil, false
	}
	if err := json.Unmarshal([]byte(literal), reflect.New(want).Interface()); err != nil {
		return nil, false
	}
	return setArgument(args, typeErr.Field, json.RawMessage(literal))
}

// isNumberKind reports whether a Go kind holds a schema number or integer.
func isNumberKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// isBareNumber reports whether s is a JSON number literal with no surrounding
// text, so "8000" and "-1.5" qualify but "8,000", "eight" and "null" do not.
func isBareNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] != '-' && (s[0] < '0' || s[0] > '9') {
		return false
	}
	return json.Valid([]byte(s))
}

// setArgument replaces a dotted field path in args with val, re-encoding each
// enclosing object so a nested numeric field is rewritten in place.
func setArgument(
	args json.RawMessage,
	field string,
	val json.RawMessage,
) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return nil, false
	}
	head, rest, nested := strings.Cut(field, ".")
	if !nested {
		obj[head] = val
	} else {
		child, ok := obj[head]
		if !ok {
			return nil, false
		}
		rewritten, ok := setArgument(child, rest, val)
		if !ok {
			return nil, false
		}
		obj[head] = rewritten
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}
