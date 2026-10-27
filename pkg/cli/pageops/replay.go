package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// ReplayRequest is one captured network entry to resend with optional
// overrides. A zero Method and nil Headers keep the captured values, Body is
// sent only when BodySet, and CheckScope authorizes each URL before it is
// requested; the surface wires the manager's scope allowlist here.
type ReplayRequest struct {
	Target     string
	Method     string
	Headers    map[string]string
	Body       string
	BodySet    bool
	MaxBytes   int64
	CheckScope func(rawURL string) error
}

// PageReplay resends one captured network entry through the engine, which reads
// the retained request body over CDP and enforces CheckScope before any network
// call. The result is the engine's JSON envelope.
func PageReplay(ctx context.Context, p *browsers.Page, req ReplayRequest) (string, error) {
	result, err := p.Replay(ctx, browsers.ReplaySpec{
		Target:     req.Target,
		Method:     req.Method,
		Headers:    req.Headers,
		Body:       req.Body,
		BodySet:    req.BodySet,
		MaxBytes:   req.MaxBytes,
		CheckScope: req.CheckScope,
	})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("replay: encode result for %s: %w", req.Target, err)
	}
	return string(encoded), nil
}
