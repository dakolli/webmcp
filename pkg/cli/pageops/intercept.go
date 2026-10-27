package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/browsers"
)

// interceptState is the JSON shape of intercept_add and intercept_list.
type interceptState struct {
	Rules  []browsers.InterceptRule `json:"rules"`
	Count  int                      `json:"count"`
	Active bool                     `json:"active"`
}

// interceptClearResult is the JSON shape of intercept_clear.
type interceptClearResult struct {
	RulesRemoved int  `json:"rules_removed"`
	Active       bool `json:"active"`
}

// PageInterceptAdd adds page-scoped request rules, arming the in-flight
// interceptor on first use. Rules are bounded, per session, and disappear when
// the page closes; listing and clearing are the only reads.
func PageInterceptAdd(
	ctx context.Context,
	p *browsers.Page,
	rules []browsers.InterceptRule,
) (string, error) {
	ctx, cancel := interceptContext(ctx)
	defer cancel()
	current, err := p.InterceptAdd(ctx, rules)
	if err != nil {
		return "", fmt.Errorf("intercept add: %w", err)
	}
	return marshalIntercept(interceptState{
		Rules:  current,
		Count:  len(current),
		Active: true,
	})
}

// PageInterceptList returns the page's rules with sensitive header values
// redacted, plus whether interception is armed.
func PageInterceptList(_ context.Context, p *browsers.Page) (string, error) {
	rules, active := p.InterceptList()
	return marshalIntercept(interceptState{
		Rules:  rules,
		Count:  len(rules),
		Active: active,
	})
}

// PageInterceptClear removes every rule, continues any paused request and
// disables interception.
func PageInterceptClear(ctx context.Context, p *browsers.Page) (string, error) {
	ctx, cancel := interceptContext(ctx)
	defer cancel()
	removed := p.InterceptClear(ctx)
	return marshalIntercept(interceptClearResult{RulesRemoved: removed, Active: false})
}

// interceptContext gives ctx the shared page-operation deadline when it has
// none, so a wedged CDP call cannot hold the interceptor open forever.
func interceptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, defaultPageOpTimeout)
	return ctx, cancel
}

// marshalIntercept renders a result as compact JSON.
func marshalIntercept(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("intercept: encode result: %w", err)
	}
	return string(b), nil
}
