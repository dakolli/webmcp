package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/browsers"
)

// PageCanaryInject injects a canary into the requested client-side sources and
// returns the engine envelope {canary, injected, hits, installed}. An empty
// value asks the engine to mint wm-canary-<8 hex>; an empty target list means
// all four sources.
//
// Deliberately does NOT lock: Page.InjectCanary takes the page lock itself and
// owns the tracer guard, the nil-Rod guard and the frame cap.
func PageCanaryInject(
	ctx context.Context,
	p *browsers.Page,
	targets []string,
	value string,
) (string, error) {
	res, err := p.InjectCanary(ctx, targets, value)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode canary result: %w", err)
	}
	return string(b), nil
}
