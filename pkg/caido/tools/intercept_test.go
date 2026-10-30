package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCaidoInterceptClearRequiresConfirm pins that an id-less clear without
// confirm is refused as invalid_args before the client is contacted, because the
// kill switch must never run by accident.
func TestCaidoInterceptClearRequiresConfirm(t *testing.T) {
	out, err := NewCaidoIntercept().Execute(context.Background(), json.RawMessage(
		`{"action":"clear"}`,
	))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertFailure(t, out, "invalid_args", "confirm")
}

// TestCaidoInterceptAddRequiresURLFilter pins that an add without a usable HTTPQL
// expression is refused as invalid_args before the client is contacted, because an
// add must never arm a rewrite over every response.
func TestCaidoInterceptAddRequiresURLFilter(t *testing.T) {
	cases := map[string]string{
		"missing":    `{"action":"add","status":599}`,
		"blank":      `{"action":"add","status":599,"url_filter":"  "}`,
		"wildcard":   `{"action":"add","status":599,"url_filter":"*"}`,
		"res only":   `{"action":"add","status":599,"url_filter":"res.code.eq:200"}`,
		"no req dot": `{"action":"add","status":599,"url_filter":"resp.code.eq:200"}`,
		"bare req":   `{"action":"add","status":599,"url_filter":"req."}`,
		"no op":      `{"action":"add","status":599,"url_filter":"req.host"}`,
		"no value":   `{"action":"add","status":599,"url_filter":"req.host.eq:"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := NewCaidoIntercept().Execute(context.Background(), json.RawMessage(args))
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			assertFailure(t, out, "invalid_args", "url_filter")
		})
	}
}

// assertFailure decodes one tool envelope and requires a failure of the given
// error type whose message contains want.
func assertFailure(t *testing.T, out, wantType, want string) {
	t.Helper()
	var env envelopeProbe
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", out, err)
	}
	if env.OK || env.Error == nil {
		t.Fatalf("envelope = %s, want a failure branch", out)
	}
	if env.Error.Type != wantType {
		t.Fatalf("error type = %q, want %q", env.Error.Type, wantType)
	}
	if !strings.Contains(env.Error.Message, want) {
		t.Fatalf("error message = %q, want it to contain %q", env.Error.Message, want)
	}
}
