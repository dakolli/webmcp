package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/go-rod/rod"

	"webmcp/pkg/browsers"
)

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
			out, err := okResult(tt.payload)
			if err != nil {
				t.Fatalf("okResult() error = %v", err)
			}
			if out != tt.want {
				t.Fatalf("okResult() = %s, want %s", out, tt.want)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("okResult() = %s, not valid JSON", out)
			}
		})
	}
}

func TestFailResultUnknownHasNoHint(t *testing.T) {
	out, err := failResult(errors.New("unexpected"))
	if err != nil {
		t.Fatalf("failResult() error = %v", err)
	}
	want := `{"ok":false,"error":{"type":"unknown","message":"unexpected"}}`
	if out != want {
		t.Fatalf("failResult() = %s, want %s", out, want)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("failResult() = %s, not valid JSON", out)
	}
}

func TestFailResultTypedErrorCarriesHint(t *testing.T) {
	out, err := failResult(errors.New("page_act: cannot find element"))
	if err != nil {
		t.Fatalf("failResult() error = %v", err)
	}
	env := mustEnvelope(t, out)
	if env.OK {
		t.Fatal("failResult() ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("failResult() error = nil, want the error branch")
	}
	if env.Error.Type != errElementNotFound {
		t.Fatalf("type = %q, want %q", env.Error.Type, errElementNotFound)
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
			want: errInvalidArgs,
		},
		{
			name: "invalid args type error",
			err:  errors.New(`browser_create: "ignore_certs" must be a boolean (true or false), but the request sent the string "yes".`),
			want: errInvalidArgs,
		},
		{
			name: "foreign session is not found",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupForeign,
				Message: `session "page-2" not found for owner a — held by browser-2 (owner b)`,
			}),
			want: errNotFound,
		},
		{
			name: "missing session is not found",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupMissing,
				Message: `session "page-9" not found (live: page-1)`,
			}),
			want: errNotFound,
		},
		{
			name: "typed missing beats the restart marker",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupMissing,
				Message: `session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs`,
			}),
			want: errNotFound,
		},
		{
			name: "ambiguous session is invalid args",
			err: fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    browsers.PageLookupAmbiguous,
				Message: `session "page-3" needs a browser — 2 browsers are live: browser-1 (owner a), browser-2 (owner b)`,
			}),
			want: errInvalidArgs,
		},
		{
			name: "element not found typed",
			err:  fmt.Errorf("page_act: %w", &rod.ElementNotFoundError{}),
			want: errElementNotFound,
		},
		{
			name: "element not found marker",
			err:  errors.New("page_act: cannot find element"),
			want: errElementNotFound,
		},
		{
			name: "not interactable typed",
			err:  fmt.Errorf("page_act: %w", &rod.NotInteractableError{}),
			want: errNotInteractable,
		},
		{
			name: "not interactable marker",
			err:  errors.New("page_act: element is not cursor interactable"),
			want: errNotInteractable,
		},
		{
			name: "anti bot marker",
			err:  errors.New("Access to this page has been denied"),
			want: errAntiBot,
		},
		{
			name: "anti bot page title",
			err:  errors.New("fetch_page: context deadline exceeded"),
			page: challengeState{title: "Just a moment..."},
			want: errAntiBot,
		},
		{
			name: "anti bot page url",
			err:  errors.New("page_act: navigation failed"),
			page: challengeState{url: "https://example.com/captcha"},
			want: errAntiBot,
		},
		{
			name: "page closed nil rod handle",
			err:  errors.New("page_act: page is closed (nil rod handle)"),
			want: errPageClosed,
		},
		{
			name: "page closed manager restart",
			err:  errors.New(`session "page-1" no longer exists: browser was restarted; call fetch_page to re-open URLs`),
			want: errPageClosed,
		},
		{
			name: "page closed inspected target",
			err:  errors.New("-32000 Inspected target navigated or closed"),
			want: errPageClosed,
		},
		{
			name: "page closed typed",
			err:  fmt.Errorf("page_act: %w", &rod.PageCloseCanceledError{}),
			want: errPageClosed,
		},
		{
			name: "timeout deadline exceeded",
			err:  fmt.Errorf("page_fetch: %w", context.DeadlineExceeded),
			want: errTimeout,
		},
		{
			name: "timeout canceled",
			err:  context.Canceled,
			want: errTimeout,
		},
		{
			name: "nav failed typed",
			err:  fmt.Errorf("page_act: %w", &rod.NavigationError{Reason: "net::ERR_NAME_NOT_RESOLVED"}),
			want: errNavFailed,
		},
		{
			name: "nav failed marker",
			err:  errors.New("page_act: navigation failed"),
			want: errNavFailed,
		},
		{
			name: "unknown eval error stays verbatim",
			err:  errors.New("page_act: eval js error: TypeError: boom"),
			want: errUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hint := classifyError(tt.err, tt.page)
			if got != tt.want {
				t.Fatalf("classifyError() = %q, want %q", got, tt.want)
			}
			if got == errUnknown && hint != "" {
				t.Fatalf("unknown hint = %q, want none", hint)
			}
			if got != errUnknown && hint == "" {
				t.Fatalf("%s hint = empty, want a remediation", got)
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
			want: errInvalidArgs,
		},
		{
			name: "page closed beats timeout",
			err:  fmt.Errorf("page session is closed: %w", context.DeadlineExceeded),
			want: errPageClosed,
		},
		{
			name: "anti bot beats nav failed",
			err:  errors.New("navigation failed: access denied"),
			want: errAntiBot,
		},
		{
			name: "timeout beats nav failed",
			err:  fmt.Errorf("navigation failed: %w", context.DeadlineExceeded),
			want: errTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := classifyError(tt.err, challengeState{}); got != tt.want {
				t.Fatalf("classifyError() = %q, want %q", got, tt.want)
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
			wantType: errNotFound,
			wantHint: "the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner",
		},
		{
			name:     "missing",
			kind:     browsers.PageLookupMissing,
			msg:      `session "page-9" not found (live: page-1)`,
			wantType: errNotFound,
			wantHint: "the session is not in that browser or owner — call session_list for the live ids, or pass the matching browser_id or owner",
		},
		{
			name:     "ambiguous",
			kind:     browsers.PageLookupAmbiguous,
			msg:      `session "page-3" needs a browser — 2 browsers are live: browser-1 (owner task-a), browser-2 (owner task-b); pass browser_id or owner`,
			wantType: errInvalidArgs,
			wantHint: "this session id needs a browser — pass browser_id or owner to choose the browser that holds it",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := failResult(fmt.Errorf("page_fetch: %w", &browsers.PageLookupError{
				Kind:    tc.kind,
				Message: tc.msg,
			}))
			if err != nil {
				t.Fatalf("failResult() error = %v", err)
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
		"challenge",
	}
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			if got, _ := classifyError(errors.New(marker), challengeState{}); got != errAntiBot {
				t.Fatalf("classifyError(%q) = %q, want %q", marker, got, errAntiBot)
			}
		})
	}
}

func mustEnvelope(t *testing.T, out string) resultEnvelope {
	t.Helper()
	if !json.Valid([]byte(out)) {
		t.Fatalf("result = %s, not valid JSON", out)
	}
	var env resultEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal result %s: %v", out, err)
	}
	return env
}
