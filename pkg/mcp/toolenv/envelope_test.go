package toolenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// decodeProbe is the subset of page_fetch arguments these tests decode into.
type decodeProbe struct {
	SessionID string `json:"session_id"`
	What      string `json:"what"`
	MaxBytes  int    `json:"max_bytes"`
}

// TestDecodeArgsCoercesNumericStrings pins that a quoted integer is accepted for
// max_bytes while a non-numeric string keeps the existing invalid_args sentence.
func TestDecodeArgsCoercesNumericStrings(t *testing.T) {
	t.Run("quoted number is coerced", func(t *testing.T) {
		var a decodeProbe
		err := DecodeArgs("page_fetch", json.RawMessage(
			`{"session_id":"page-1","what":"dom","max_bytes":"8000"}`,
		), &a)
		if err != nil {
			t.Fatalf("DecodeArgs() error = %v", err)
		}
		if a.MaxBytes != 8000 {
			t.Fatalf("max_bytes = %d, want 8000", a.MaxBytes)
		}
	})
	t.Run("non-numeric string is refused", func(t *testing.T) {
		var a decodeProbe
		err := DecodeArgs("page_fetch", json.RawMessage(
			`{"session_id":"page-1","what":"dom","max_bytes":"eight"}`,
		), &a)
		if err == nil {
			t.Fatal("DecodeArgs() = nil, want an argument type error")
		}
		want := `page_fetch: "max_bytes" must be a number, but the request sent the string "eight".`
		if err.Error() != want {
			t.Fatalf("DecodeArgs() = %q, want %q", err, want)
		}
	})
}

// TestDecodeArgsKeepsNonNumericWrongType pins that only numeric strings are
// coerced: a string sent for a string field and an object sent for a number are
// still rejected.
func TestDecodeArgsKeepsNonNumericWrongType(t *testing.T) {
	var a decodeProbe
	err := DecodeArgs("page_fetch", json.RawMessage(
		`{"session_id":"page-1","what":"dom","max_bytes":{"value":8000}}`,
	), &a)
	if err == nil {
		t.Fatal("DecodeArgs() = nil, want an argument type error")
	}
	if !strings.Contains(err.Error(), `"max_bytes" must be a number`) {
		t.Fatalf("DecodeArgs() = %q, want the max_bytes type error", err)
	}
}

func TestOkResultEmbedsJSONPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "object stays addressable",
			payload: `{"session_id":"page-1","title":"Example"}`,
			want:    `{"ok":true,"data":{"session_id":"page-1","title":"Example"}}`,
		},
		{
			name:    "array stays addressable",
			payload: `[{"href":"https://example.com"}]`,
			want:    `{"ok":true,"data":[{"href":"https://example.com"}]}`,
		},
		{
			name:    "prose becomes a JSON string",
			payload: "clicked selector=a",
			want:    `{"ok":true,"data":"clicked selector=a"}`,
		},
		{
			name:    "empty payload becomes an empty string",
			payload: "",
			want:    `{"ok":true,"data":""}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := OKResult(tt.payload)
			if err != nil {
				t.Fatalf("OKResult() error = %v", err)
			}
			if out != tt.want {
				t.Fatalf("OKResult() = %s, want %s", out, tt.want)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("OKResult() = %s, not valid JSON", out)
			}
		})
	}
}

func TestFailResultUnknownHasNoHint(t *testing.T) {
	out, err := FailResult(errors.New("unexpected"))
	if err != nil {
		t.Fatalf("FailResult() error = %v", err)
	}
	want := `{"ok":false,"error":{"type":"unknown","message":"unexpected"}}`
	if out != want {
		t.Fatalf("FailResult() = %s, want %s", out, want)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("FailResult() = %s, not valid JSON", out)
	}
}

func TestFailResultTypedErrorCarriesHint(t *testing.T) {
	out, err := FailResult(errors.New("page_act: cannot find element"))
	if err != nil {
		t.Fatalf("FailResult() error = %v", err)
	}
	env := mustEnvelope(t, out)
	if env.OK {
		t.Fatal("FailResult() ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("FailResult() error = nil, want the error branch")
	}
	if env.Error.Type != ErrElementNotFound {
		t.Fatalf("type = %q, want %q", env.Error.Type, ErrElementNotFound)
	}
	if env.Error.Message != "page_act: cannot find element" {
		t.Fatalf("message = %q, want the original sentence", env.Error.Message)
	}
	if env.Error.Hint == "" {
		t.Fatal("hint = empty, want the remediation")
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		page challengeState
		want string
	}{
		{
			name: "invalid args marker",
			err:  errors.New(`fetch_page: missing required argument "url"`),
			want: ErrInvalidArgs,
		},
		{
			name: "invalid args type error",
			err:  errors.New(`browser_create: "ignore_certs" must be a boolean (true or false), but the request sent the string "yes".`),
			want: ErrInvalidArgs,
		},
		{
			name: "foreign session is not found",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupForeign,
				Message: `session "page-2" not found for owner a — held by browser-2 (owner b)`,
			}),
			want: ErrNotFound,
		},
		{
			name: "missing session is not found",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupMissing,
				Message: `session "page-9" not found (live: page-1)`,
			}),
			want: ErrNotFound,
		},
		{
			name: "typed missing beats the restart marker",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupMissing,
				Message: `session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs`,
			}),
			want: ErrNotFound,
		},
		{
			name: "ambiguous session is invalid args",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupAmbiguous,
				Message: `session "page-3" needs a browser — 2 browsers are live: browser-1 (owner a), browser-2 (owner b)`,
			}),
			want: ErrInvalidArgs,
		},
		{
			name: "page limit is invalid args",
			err:  &browsers.PageLimitError{Browser: "browser-1", Count: 20, Limit: 20},
			want: ErrInvalidArgs,
		},
		{
			name: "element not found typed",
			err:  fmt.Errorf("page_act: %w", &rod.ElementNotFoundError{}),
			want: ErrElementNotFound,
		},
		{
			name: "element not found marker",
			err:  errors.New("page_act: cannot find element"),
			want: ErrElementNotFound,
		},
		{
			name: "not interactable typed",
			err:  fmt.Errorf("page_act: %w", &rod.NotInteractableError{}),
			want: ErrNotInteractable,
		},
		{
			name: "not interactable marker",
			err:  errors.New("page_act: element is not cursor interactable"),
			want: ErrNotInteractable,
		},
		{
			name: "anti bot marker",
			err:  errors.New("Access to this page has been denied"),
			want: ErrAntiBot,
		},
		{
			name: "anti bot page title",
			err:  errors.New("fetch_page: context deadline exceeded"),
			page: challengeState{title: "Just a moment..."},
			want: ErrAntiBot,
		},
		{
			name: "anti bot page url",
			err:  errors.New("page_act: navigation failed"),
			page: challengeState{url: "https://example.com/captcha"},
			want: ErrAntiBot,
		},
		{
			name: "page closed nil rod handle",
			err:  errors.New("page_act: page is closed (nil rod handle)"),
			want: ErrPageClosed,
		},
		{
			name: "page closed manager restart",
			err:  errors.New(`session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs`),
			want: ErrPageClosed,
		},
		{
			name: "page closed inspected target",
			err:  errors.New("-32000 Inspected target navigated or closed"),
			want: ErrPageClosed,
		},
		{
			name: "page closed typed",
			err:  fmt.Errorf("page_act: %w", &rod.PageCloseCanceledError{}),
			want: ErrPageClosed,
		},
		{
			name: "timeout deadline exceeded",
			err:  fmt.Errorf("page_fetch: %w", context.DeadlineExceeded),
			want: ErrTimeout,
		},
		{
			name: "timeout canceled",
			err:  context.Canceled,
			want: ErrTimeout,
		},
		{
			name: "nav failed typed",
			err:  fmt.Errorf("page_act: %w", &rod.NavigationError{Reason: "net::ERR_NAME_NOT_RESOLVED"}),
			want: ErrNavFailed,
		},
		{
			name: "nav failed marker",
			err:  errors.New("page_act: navigation failed"),
			want: ErrNavFailed,
		},
		{
			name: "over-bound fuzz matrix is invalid args",
			err: fmt.Errorf("page_act: %w", &browsers.FuzzBoundsError{
				Unit:     "requests",
				Computed: "243",
				Bound:    "100",
			}),
			want: ErrInvalidArgs,
		},
		{
			name: "rate-limited call is a timeout",
			err: fmt.Errorf("page_act: %w", &browsers.RateLimitError{
				Host: "site.test",
				Wait: time.Second,
				Err:  context.DeadlineExceeded,
			}),
			want: ErrTimeout,
		},
		{
			name: "stale handle is invalid args",
			err:  errors.New(`page_act: click: handle "g1-b7" is stale — minted for a previous document (re-run page_fetch what=interactives to re-mint handles)`),
			want: ErrInvalidArgs,
		},
		{
			name: "unknown mouse button is invalid args",
			err:  errors.New(`page_act: click: unknown mouse button "side" (must be left, right, or middle)`),
			want: ErrInvalidArgs,
		},
		{
			name: "canary without the tracer is invalid args",
			err:  fmt.Errorf("page_act: %w", browsers.ErrCanaryNeedsTracer),
			want: ErrInvalidArgs,
		},
		{
			name: "intercept rule validation is invalid args",
			err:  errors.New(`page_act: intercept: rule "r1" has status 700 (must be 100-599)`),
			want: ErrInvalidArgs,
		},
		{
			name: "poc_bundle relative path is invalid args",
			err:  errors.New(`page_act: poc_bundle: "poc-xss" must be absolute`),
			want: ErrInvalidArgs,
		},
		{
			name: "poc_bundle non-empty directory is invalid args",
			err:  errors.New("page_act: poc_bundle: /tmp/poc-xss is not empty — pass a new directory or remove its contents"),
			want: ErrInvalidArgs,
		},
		{
			name: "auth user-agent mismatch is invalid args",
			err:  errors.New(`page_act: auth_restore: stored user agent "Chrome/1" does not match the live page user agent "Chrome/2" — re-capture on this browser, or pass force=true to restore anyway`),
			want: ErrInvalidArgs,
		},
		{
			name: "missing auth snapshot is not found",
			err:  errors.New("page_act: auth_restore: store: auth snapshot not found: example.org/main"),
			want: ErrNotFound,
		},
		{
			name: "wait_open on a closed browser is page closed",
			err:  errors.New("page_act: browsers: wait_open: browser browser-1 is closed"),
			want: ErrPageClosed,
		},
		{
			name: "unknown eval error stays verbatim",
			err:  errors.New("page_act: eval js error: TypeError: boom"),
			want: ErrUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := classifyError(tt.err, tt.page)
			if c.Type != tt.want {
				t.Fatalf("classifyError() = %q, want %q", c.Type, tt.want)
			}
			if c.Type == ErrUnknown && c.Hint != "" {
				t.Fatalf("unknown hint = %q, want none", c.Hint)
			}
			if c.Type != ErrUnknown && c.Type != ErrInvalidArgs && c.Hint == "" {
				t.Fatalf("%s hint = empty, want a remediation", c.Type)
			}
		})
	}
}

// TestPolicyRefusalHints pins the WEB-226 contract: a scope or rate refusal is
// a policy decision, so its hint names the real fix and never the schema
// remediation that sends an agent to retry the same call.
func TestPolicyRefusalHints(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType string
		wantHint []string
	}{
		{
			name: "scope refusal",
			err: fmt.Errorf("page_act: replay_fuzz: %w", &browsers.ScopeError{
				Host:    "fonts.googleapis.com",
				Allowed: "developer.android.com",
				Scope:   "developer.android.com",
			}),
			wantType: ErrInvalidArgs,
			wantHint: []string{"browser_create", "scope"},
		},
		{
			name: "rate refusal",
			err: fmt.Errorf("page_act: %w", &browsers.RateLimitError{
				Host: "site.test",
				Wait: time.Second,
				Err:  context.DeadlineExceeded,
			}),
			wantType: ErrTimeout,
			wantHint: []string{"rps", "burst"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := FailResult(tt.err)
			if err != nil {
				t.Fatalf("FailResult() error = %v", err)
			}
			env := mustEnvelope(t, out)
			if env.Error == nil {
				t.Fatal("envelope has no error branch")
			}
			if env.Error.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", env.Error.Type, tt.wantType)
			}
			for _, want := range tt.wantHint {
				if !strings.Contains(env.Error.Hint, want) {
					t.Fatalf("hint = %q, want it to name %q", env.Error.Hint, want)
				}
			}
			if strings.Contains(env.Error.Hint, "InputSchema") {
				t.Fatalf("hint = %q, want the policy fix, not the schema remediation", env.Error.Hint)
			}
			if env.Error.Hint == hintFor(ErrInvalidArgs) {
				t.Fatalf("hint = %q, want the policy hint, not the schema hint", env.Error.Hint)
			}
		})
	}
}

func TestClassifyErrorPrecedence(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid args beats page closed",
			err:  errors.New(`missing required argument "session_id": page session is closed`),
			want: ErrInvalidArgs,
		},
		{
			name: "page closed beats timeout",
			err:  fmt.Errorf("page session is closed: %w", context.DeadlineExceeded),
			want: ErrPageClosed,
		},
		{
			name: "anti bot beats nav failed",
			err:  errors.New("navigation failed: access denied"),
			want: ErrAntiBot,
		},
		{
			name: "timeout beats nav failed",
			err:  fmt.Errorf("navigation failed: %w", context.DeadlineExceeded),
			want: ErrTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err, challengeState{}); got.Type != tt.want {
				t.Fatalf("classifyError() = %q, want %q", got.Type, tt.want)
			}
			out, err := FailResult(tt.err)
			if err != nil {
				t.Fatalf("FailResult() error = %v", err)
			}
			if env := mustEnvelope(t, out); env.Error == nil || env.Error.Type != tt.want {
				t.Fatalf("FailResult() type = %v, want %q", env.Error, tt.want)
			}
		})
	}
}

// TestSessionLookupEnvelopes pins the two resolver payloads: a foreign id is
// not_found and an id held by two browsers is invalid_args, each with its own
// hint and never page_closed.
func TestSessionLookupEnvelopes(t *testing.T) {
	tests := []struct {
		name     string
		kind     browsers.PageLookupKind
		msg      string
		wantType string
		wantHint string
	}{
		{
			name:     "foreign",
			kind:     browsers.PageLookupForeign,
			msg:      `session "page-2" is not in owner task-a — held by browser-2 (owner task-b); pass browser_id to reach it on purpose`,
			wantType: ErrNotFound,
			wantHint: "the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner",
		},
		{
			name:     "missing",
			kind:     browsers.PageLookupMissing,
			msg:      `session "page-9" not found (live: page-1)`,
			wantType: ErrNotFound,
			wantHint: "the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner",
		},
		{
			name:     "ambiguous",
			kind:     browsers.PageLookupAmbiguous,
			msg:      `session "page-3" needs a browser — 2 browsers are live: browser-1 (owner task-a), browser-2 (owner task-b); pass browser_id or owner`,
			wantType: ErrInvalidArgs,
			wantHint: "this session id needs a browser — pass browser_id or owner to choose the browser that holds it",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FailResult(fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    tc.kind,
				Message: tc.msg,
			}))
			if err != nil {
				t.Fatalf("FailResult() error = %v", err)
			}
			t.Logf("payload: %s", out)
			env := mustEnvelope(t, out)
			if env.Error == nil {
				t.Fatal("envelope has no error branch")
			}
			if env.Error.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", env.Error.Type, tc.wantType)
			}
			if env.Error.Hint != tc.wantHint {
				t.Fatalf("hint = %q, want %q", env.Error.Hint, tc.wantHint)
			}
			if want := "page_fetch: " + tc.msg; env.Error.Message != want {
				t.Fatalf("message = %q, want %q", env.Error.Message, want)
			}
		})
	}
}

func TestAntiBotMarkersArePinned(t *testing.T) {
	markers := []string{
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
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			if got := classifyError(errors.New(marker), challengeState{}); got.Type != ErrAntiBot {
				t.Fatalf("classifyError(%q) = %q, want %q", marker, got.Type, ErrAntiBot)
			}
		})
	}
}

// TestChallengeURLIsNotAntiBot pins the false-positive fix: the word "challenge"
// inside a URL is not a bot signal.
func TestChallengeURLIsNotAntiBot(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "replay ambiguity naming a challenge URL is invalid args",
			err: fmt.Errorf("page_act: %w", fmt.Errorf(
				"%w: target %q matches 2 network entries — pass a seq or a longer URL substring: seq=1 GET https://site.test/challenge, seq=2 GET https://site.test/challenge/verify",
				browsers.ErrReplayTargetAmbiguous,
				"site.test",
			)),
			want: ErrInvalidArgs,
		},
		{
			name: "replay transport failure naming a challenge URL is nav_failed",
			err: fmt.Errorf("page_act: %w", &browsers.ReplayTransportError{
				Method: "GET",
				URL:    "https://site.test/challenge",
				Budget: 30 * time.Second,
				Err:    errors.New("dial tcp: connection refused"),
			}),
			want: ErrNavFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err, challengeState{}); got.Type != tt.want {
				t.Fatalf("classifyError() = %q, want %q", got.Type, tt.want)
			}
		})
	}
}

// TestClassifyReplayErrors pins the type-first replay classification: an
// ambiguous or missing target is invalid_args, and a transport failure is a
// timeout or a failed navigation.
func TestClassifyReplayErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "ambiguous target",
			err:  fmt.Errorf("page_act: %w: target %q matches 2 network entries: seq=1 GET https://a.test/x, seq=2 GET https://a.test/x/y", browsers.ErrReplayTargetAmbiguous, "a.test"),
			want: ErrInvalidArgs,
		},
		{
			name: "missing target",
			err:  fmt.Errorf("page_act: %w: no network entry matches target %q", browsers.ErrReplayTargetNotFound, "nope"),
			want: ErrInvalidArgs,
		},
		{
			name: "replay timeout is a timeout",
			err: fmt.Errorf("page_act: %w", &browsers.ReplayTransportError{
				Method: "GET",
				URL:    "https://slow.test/",
				Budget: 30 * time.Second,
				Err:    fmt.Errorf("Get %q: %w", "https://slow.test/", context.DeadlineExceeded),
			}),
			want: ErrTimeout,
		},
		{
			name: "replay transport failure is nav_failed",
			err: fmt.Errorf("page_act: %w", &browsers.ReplayTransportError{
				Method: "GET",
				URL:    "https://down.test/",
				Budget: 30 * time.Second,
				Err:    errors.New("dial tcp: no such host"),
			}),
			want: ErrNavFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err, challengeState{}); got.Type != tt.want {
				t.Fatalf("classifyError() = %q, want %q", got.Type, tt.want)
			}
			out, err := FailResult(tt.err)
			if err != nil {
				t.Fatalf("FailResult() error = %v", err)
			}
			env := mustEnvelope(t, out)
			if env.Error == nil || env.Error.Type != tt.want {
				t.Fatalf("FailResult() error branch = %+v, want type %q", env.Error, tt.want)
			}
		})
	}
}

// TestPageLimitEnvelopeIsInvalidArgs pins the WEB-188 cap contract: the 21st
// page is invalid_args naming the browser and cap with a session_close hint,
// never the page_closed reopen hint.
func TestPageLimitEnvelopeIsInvalidArgs(t *testing.T) {
	err := fmt.Errorf("fetch_page: %w", &browsers.PageLimitError{
		Browser: "browser-1",
		Count:   20,
		Limit:   20,
	})
	out, marshalErr := FailResult(err)
	if marshalErr != nil {
		t.Fatalf("FailResult() error = %v", marshalErr)
	}
	env := mustEnvelope(t, out)
	if env.Error == nil {
		t.Fatal("envelope has no error branch")
	}
	if env.Error.Type != ErrInvalidArgs {
		t.Fatalf("type = %q, want %q", env.Error.Type, ErrInvalidArgs)
	}
	for _, want := range []string{"browser browser-1", "20 live pages", "max 20"} {
		if !strings.Contains(env.Error.Message, want) {
			t.Fatalf("message = %q, want it to contain %q", env.Error.Message, want)
		}
	}
	if !strings.Contains(env.Error.Hint, "session_close") {
		t.Fatalf("hint = %q, want it to say session_close", env.Error.Hint)
	}
	if strings.Contains(env.Error.Hint, "fetch_page") {
		t.Fatalf("hint = %q, want no fetch_page reopen advice", env.Error.Hint)
	}
}

// TestOverBoundFuzzEnvelopeIsInvalidArgs pins the WEB-219 contract: the live
// refusal keeps its sentence, the type is invalid_args, and the hint names both
// knobs that fit the matrix.
func TestOverBoundFuzzEnvelopeIsInvalidArgs(t *testing.T) {
	err := fmt.Errorf("page_act: %w", &browsers.FuzzBoundsError{
		Unit:     "requests",
		Computed: "243",
		Bound:    "100",
	})
	out, marshalErr := FailResult(err)
	if marshalErr != nil {
		t.Fatalf("FailResult() error = %v", marshalErr)
	}
	env := mustEnvelope(t, out)
	if env.Error == nil {
		t.Fatal("envelope has no error branch")
	}
	if env.Error.Type != ErrInvalidArgs {
		t.Fatalf("type = %q, want %q", env.Error.Type, ErrInvalidArgs)
	}
	if want := "page_act: replay_fuzz: requests computed 243, over the 100 bound"; env.Error.Message != want {
		t.Fatalf("message = %q, want %q", env.Error.Message, want)
	}
	for _, want := range []string{"payload", "sniper"} {
		if !strings.Contains(env.Error.Hint, want) {
			t.Fatalf("hint = %q, want it to name %q", env.Error.Hint, want)
		}
	}
}

// TestActionTimeoutEnvelopeNamesBothEscapes pins that a humanized timeout
// carries its own raw:true / timeout_ms hint rather than the generic one.
func TestActionTimeoutEnvelopeNamesBothEscapes(t *testing.T) {
	err := fmt.Errorf("page_act: %w", &browsers.ActionTimeoutError{
		Op:   "click",
		Page: "page-1",
		Err:  context.DeadlineExceeded,
	})
	out, marshalErr := FailResult(err)
	if marshalErr != nil {
		t.Fatalf("FailResult() error = %v", marshalErr)
	}
	env := mustEnvelope(t, out)
	if env.Error == nil {
		t.Fatal("envelope has no error branch")
	}
	if env.Error.Type != ErrTimeout {
		t.Fatalf("type = %q, want %q", env.Error.Type, ErrTimeout)
	}
	for _, want := range []string{"raw:true", "timeout_ms"} {
		if !strings.Contains(env.Error.Hint, want) {
			t.Fatalf("hint = %q, want it to name %q", env.Error.Hint, want)
		}
	}
}

// TestSpecificHintBeatsGenericHint pins that an error's own Hint() replaces the
// generic per-type remediation instead of sitting beside it.
func TestSpecificHintBeatsGenericHint(t *testing.T) {
	timeoutErr := fmt.Errorf("page_act: %w", &browsers.ActionTimeoutError{
		Op:   "click",
		Page: "page-1",
		Err:  context.DeadlineExceeded,
	})
	got := classifyError(timeoutErr, challengeState{})
	if got.Type != ErrTimeout {
		t.Fatalf("type = %q, want %q", got.Type, ErrTimeout)
	}
	if got.Hint == hintFor(ErrTimeout) {
		t.Fatalf("hint = %q, want the typed hint, not the generic one", got.Hint)
	}
	if !strings.Contains(got.Hint, "raw:true") {
		t.Fatalf("hint = %q, want raw:true", got.Hint)
	}

	fuzzErr := fmt.Errorf("page_act: %w", &browsers.FuzzBoundsError{
		Unit:     "payloads",
		Computed: "60",
		Bound:    "50",
	})
	got = classifyError(fuzzErr, challengeState{})
	if got.Type != ErrInvalidArgs {
		t.Fatalf("type = %q, want %q", got.Type, ErrInvalidArgs)
	}
	if got.Hint == hintFor(ErrInvalidArgs) {
		t.Fatalf("hint = %q, want the typed hint, not the generic one", got.Hint)
	}
	if !strings.Contains(got.Hint, "50") {
		t.Fatalf("hint = %q, want the payload bound", got.Hint)
	}
}

// TestInvalidArgsHintOnlyForDecodeFailures pins that a prose-marked invalid_args
// refusal carries no generic schema hint, while a decode failure still does, and
// that a prose refusal's own Hint() wins over the generic sentence.
func TestInvalidArgsHintOnlyForDecodeFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantHint string
	}{
		{
			name:     "prose path refusal omits the generic hint",
			err:      errors.New(`page_act: poc_bundle: "_trash/x" must be absolute`),
			wantHint: "",
		},
		{
			name:     "prose refusal keeps its own hint",
			err:      hintedArgsError{msg: `page_act: poc_bundle: "/tmp/x" is not empty — pass a new directory`},
			wantHint: "pass an absolute directory that does not exist yet",
		},
		{
			name:     "type mismatch keeps the schema hint",
			err:      errors.New(`browser_create: "ignore_certs" must be a boolean (true or false), but the request sent the string "yes".`),
			wantHint: hintFor(ErrInvalidArgs),
		},
		{
			name:     "structured decode failure keeps the schema hint",
			err:      errors.New(`page_act: invalid arguments: json: cannot unmarshal number into Go struct field args.timeout_ms`),
			wantHint: hintFor(ErrInvalidArgs),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := FailResult(tt.err)
			if err != nil {
				t.Fatalf("FailResult() error = %v", err)
			}
			env := mustEnvelope(t, out)
			if env.Error == nil {
				t.Fatal("envelope has no error branch")
			}
			if env.Error.Type != ErrInvalidArgs {
				t.Fatalf("type = %q, want %q", env.Error.Type, ErrInvalidArgs)
			}
			if env.Error.Hint != tt.wantHint {
				t.Fatalf("hint = %q, want %q", env.Error.Hint, tt.wantHint)
			}
			if tt.wantHint == "" && strings.Contains(out, `"hint"`) {
				t.Fatalf("payload = %s, want the hint field omitted", out)
			}
		})
	}
}

type hintedArgsError struct {
	msg string
}

func (e hintedArgsError) Error() string { return e.msg }

func (e hintedArgsError) Hint() string {
	return "pass an absolute directory that does not exist yet"
}

func mustEnvelope(t *testing.T, out string) ResultEnvelope {
	t.Helper()
	if !json.Valid([]byte(out)) {
		t.Fatalf("result = %s, not valid JSON", out)
	}
	var env ResultEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result %s: %v", out, err)
	}
	return env
}
