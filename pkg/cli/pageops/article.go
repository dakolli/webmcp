package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// PageArticle returns the page's readable body as markdown with its title,
// byline, excerpt, word count and URL, JSON-encoded. The engine owns the rune
// cap and reports it through the truncated field; this surface emits the result
// verbatim.
func PageArticle(ctx context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	article, err := p.Article(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(article)
	if err != nil {
		return "", fmt.Errorf("encode article: %w", err)
	}
	return string(b), nil
}
