package pageops

import (
	"context"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/har"
)

// harPage is the engine surface the HAR export reads: the observed entries,
// the two on-demand body seams and the cookie jar snapshot. It exists so the
// wiring can be exercised with a fake page; *browsers.Page is the only
// production implementation.
type harPage interface {
	NetworkEntries() ([]browsers.NetworkEntry, int)
	NetworkBody(ctx context.Context, seq int64) (browsers.NetworkBody, error)
	NetworkRequestBody(ctx context.Context, seq int64) (browsers.NetworkBody, error)
	Cookies(ctx context.Context) ([]browsers.Cookie, error)
	SecretsCaptured() bool
}

// PageHAR returns the page's observed network traffic as a HAR 1.2 document.
// The export is read-only and never arms capture: it reads the existing network
// ring, so call clear_logs before the step whose traffic matters.
// maxBytes caps the encoded document: zero uses the encoder's default, a
// negative value exports every buffered entry, and a positive value drops the
// oldest entries first until the document fits. Response and request bodies are
// fetched on demand for the entries that survive the cap, and request.cookies
// is filled from the browser cookie jar at export time. Sensitive header and
// cookie values are redacted unless secret capture is on.
func PageHAR(ctx context.Context, p *browsers.Page, maxBytes int) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	title := ""
	if state, err := PageStateOf(ctx, p); err == nil {
		title = state.Title
	}
	return pageHAR(ctx, p, har.PageInfo{Title: title}, maxBytes)
}

// pageHAR encodes the page's observed traffic with both body fetchers bound to
// the page. Each fetch takes the page lock only for its own CDP call, and the
// encoder calls the fetchers serially, so no lock is held across the export.
func pageHAR(
	ctx context.Context,
	p harPage,
	page har.PageInfo,
	maxBytes int,
) (string, error) {
	entries, dropped := p.NetworkEntries()

	out, err := har.Encode(
		page,
		entries,
		har.Options{
			MaxBytes: maxBytes,
			Dropped:  dropped,
			ResponseBody: harBodyFetcher(func(seq int64) (browsers.NetworkBody, error) {
				return p.NetworkBody(ctx, seq)
			}),
			RequestBody: harBodyFetcher(func(seq int64) (browsers.NetworkBody, error) {
				return p.NetworkRequestBody(ctx, seq)
			}),
			Cookies: func() ([]browsers.Cookie, error) {
				return p.Cookies(ctx)
			},
			Secrets: p.SecretsCaptured(),
		},
	)
	if err != nil {
		return "", fmt.Errorf(
			"export HAR: encode %d observed network entries: %w",
			len(entries),
			err,
		)
	}
	return string(out), nil
}

// harBodyFetcher adapts an engine body read to the encoder's fetcher, carrying
// the decoded size and keeping binary content in Base64.
func harBodyFetcher(
	fetch func(seq int64) (browsers.NetworkBody, error),
) har.BodyFetcher {
	return func(seq int64) (har.Body, error) {
		body, err := fetch(seq)
		if err != nil {
			return har.Body{}, err
		}
		return har.Body{
			Text:   body.Body,
			Base64: body.Base64,
			Bytes:  body.Bytes,
		}, nil
	}
}
