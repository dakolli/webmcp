package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/browsers"
)

// PageMarks captures the set-of-marks overlay — numbered boxes over the live
// page plus the annotated screenshot — and returns it as JSON: the mark table
// mapping each number to a role, label and viewport coordinate usable with
// click_at. The output is never truncated: the screenshot data-URI must stay
// intact.
func PageMarks(ctx context.Context, p *browsers.Page) (string, error) {
	res, err := p.Marks(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode marks: %w", err)
	}
	return string(b), nil
}
