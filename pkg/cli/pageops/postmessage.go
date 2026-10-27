package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// postMessageInstalled is the JSON result of arming the recorder: installed is
// always true because arming failure returns an error instead.
type postMessageInstalled struct {
	Installed bool `json:"installed"`
}

// postMessageCleared is the JSON result of emptying the recorder rings.
type postMessageCleared struct {
	Cleared int `json:"cleared"`
}

// PagePostMessage returns the postMessage recorder log as the engine envelope
// {installed,messages[],dropped,truncated,frames[]}. target=clear empties the
// ring and returns {"cleared":N} instead, mirroring the network clear
// convention. The engine owns the ring caps, the aggregation across frames and
// value clipping, so this wrapper only encodes the result.
//
// Deliberately does NOT lock: Page.PostMessages and Page.ClearPostMessages take
// the page lock themselves, the same engine-seam exception as PageSinks.
func PagePostMessage(
	ctx context.Context,
	p *browsers.Page,
	target string,
) (string, error) {
	if strings.EqualFold(strings.TrimSpace(target), "clear") {
		cleared, err := p.ClearPostMessages(ctx)
		if err != nil {
			return "", err
		}
		return encodePostMessage(postMessageCleared{Cleared: cleared}, "postMessage clear")
	}
	res, err := p.PostMessages(ctx)
	if err != nil {
		return "", err
	}
	return encodePostMessage(res, "postMessage log")
}

// PagePostMessageInstall arms the postMessage recorder on the current document,
// every reachable iframe and every later navigation, returning
// {"installed":true}; a second call is a no-op.
//
// Deliberately does NOT lock: Page.InstallPostMessages takes the page lock
// itself.
func PagePostMessageInstall(ctx context.Context, p *browsers.Page) (string, error) {
	if err := p.InstallPostMessages(ctx); err != nil {
		return "", err
	}
	return encodePostMessage(postMessageInstalled{Installed: true}, "postMessage install")
}

// PagePostMessageClear empties every reachable recorder ring, returning
// {"cleared":N}; clearing a page that was never armed returns {"cleared":0}
// without error.
//
// Deliberately does NOT lock: Page.ClearPostMessages takes the page lock itself.
func PagePostMessageClear(ctx context.Context, p *browsers.Page) (string, error) {
	cleared, err := p.ClearPostMessages(ctx)
	if err != nil {
		return "", err
	}
	return encodePostMessage(postMessageCleared{Cleared: cleared}, "postMessage clear")
}

// PagePostMessageSend delivers one message to window.top (selector "top" or
// empty) or to the iframe named by selector, using origin as the target origin
// (default "*"), and returns {origin,target} naming what was used. The engine
// arms the recorder first so the delivered message can be read back.
//
// Deliberately does NOT lock: Page.SendPostMessage takes the page lock itself.
func PagePostMessageSend(
	ctx context.Context,
	p *browsers.Page,
	value, selector, origin string,
) (string, error) {
	res, err := p.SendPostMessage(ctx, value, selector, origin)
	if err != nil {
		return "", err
	}
	return encodePostMessage(res, "postMessage send")
}

// encodePostMessage marshals one postMessage envelope and names it in an encode
// error.
func encodePostMessage(v any, what string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", what, err)
	}
	return string(b), nil
}
