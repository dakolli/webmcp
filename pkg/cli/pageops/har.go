package pageops

import (
	"context"
	"fmt"

	"webmcp/pkg/browsers"
	"webmcp/pkg/har"
)

// PageHAR returns the page's observed network traffic as a HAR 1.2 document.
// The export is read-only and never arms capture: it reads the existing
// network ring, so call clear_logs before the step whose traffic matters.
// Sensitive header values are redacted by the HAR package, the output is capped
// with the oldest entries dropped first, and entries the ring evicted are
// reported in log.truncated and log.comment.
func PageHAR(ctx context.Context, p *browsers.Page) (string, error) {
	entries, dropped := p.NetworkEntries()

	title := ""
	if state, err := PageStateOf(ctx, p); err == nil {
		title = state.Title
	}

	out, err := har.Encode(
		har.PageInfo{Title: title},
		entries,
		har.Options{Dropped: dropped},
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
