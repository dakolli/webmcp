package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// sinksInstalled is the JSON result of arming the tracer: installed is always
// true because arming failure returns an error instead.
type sinksInstalled struct {
	Installed bool `json:"installed"`
}

// sinksCleared is the JSON result of emptying the tracer's ring: cleared counts
// the hits removed, and is 0 on a page where the tracer was never armed.
type sinksCleared struct {
	Cleared int `json:"cleared"`
}

// PageSinks returns the source-to-sink tracer's ring as the engine envelope
// {installed,hits,dropped,truncated}. The engine owns the ring cap, the newest
// 100 window and value clipping, so this wrapper only encodes the result.
//
// Deliberately does NOT lock: Page.SinkHits takes the page lock itself and owns
// the nil-Rod guard, the same engine-seam exception as PageInteractives.
func PageSinks(ctx context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	res, err := p.SinkHits(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode sink hits: %w", err)
	}
	return string(b), nil
}

// PageSinksInstall arms the source-to-sink tracer on the current document and
// every later navigation, returning {"installed":true}; a second call is a
// no-op. It changes page behavior, so it is an explicit action, never implied by
// a read.
//
// Deliberately does NOT lock: Page.InstallSinks takes the page lock itself.
func PageSinksInstall(ctx context.Context, p *browsers.Page) (string, error) {
	if err := p.InstallSinks(ctx); err != nil {
		return "", err
	}
	b, err := json.Marshal(sinksInstalled{Installed: true})
	if err != nil {
		return "", fmt.Errorf("encode sink install: %w", err)
	}
	return string(b), nil
}

// PageSinksClear empties the tracer's hit ring and resets its dropped counter,
// returning {"cleared":N}; clearing a page that was never armed returns
// {"cleared":0} without error.
//
// Deliberately does NOT lock: Page.ClearSinks takes the page lock itself.
func PageSinksClear(ctx context.Context, p *browsers.Page) (string, error) {
	cleared, err := p.ClearSinks(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(sinksCleared{Cleared: cleared})
	if err != nil {
		return "", fmt.Errorf("encode sink clear: %w", err)
	}
	return string(b), nil
}
