package tools

import (
	"context"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/pageops"
)

// fullPageScreenshotTimeout bounds a full_page screenshot capture. A full-page
// capture scrolls, composites and encodes a potentially very tall image, which
// can exceed the 30s per-call default on heavy pages; this dedicated budget is
// decoupled from the caller's deadline (via context.WithoutCancel) so a heavy
// capture gets room, while a genuinely wedged page still fails at 90s.
const fullPageScreenshotTimeout = 90 * time.Second

// truncation selects how the tool layer bounds one aspect's output.
type truncation uint8

const (
	// truncationText cuts plain text on a UTF-8 rune boundary.
	truncationText truncation = iota
	// truncationJSON keeps array and object roots parseable while cutting.
	truncationJSON
	// truncationNone returns the output untouched.
	truncationNone
)

// aspectFunc reads one page aspect through pageops.
type aspectFunc func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error)

// aspectEntry pairs an aspect reader with its truncation policy.
type aspectEntry struct {
	read  aspectFunc
	trunc truncation
}

var (
	aspects     = map[string]aspectEntry{}
	aspectNames []string
)

// registerAspect adds one what value and its truncation policy to the registry.
func registerAspect(name string, fn aspectFunc, trunc truncation) {
	aspects[name] = aspectEntry{read: fn, trunc: trunc}
	aspectNames = append(aspectNames, name)
}

// validAspects lists the registered what values in registration order.
func validAspects() string {
	return strings.Join(aspectNames, ", ")
}

// readScreenshot captures a PNG data-URI, giving full-page captures their own
// longer budget decoupled from the 30s per-call deadline.
func readScreenshot(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
	if !a.FullPage {
		return pageops.PageScreenshot(ctx, p, false)
	}
	captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fullPageScreenshotTimeout)
	defer cancel()
	return pageops.PageScreenshot(captureCtx, p, true)
}

func init() {
	registerAspect("html", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageHTML(ctx, p, a.Selector)
	}, truncationText)
	registerAspect("dom", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageDOM(ctx, p)
	}, truncationJSON)
	registerAspect("text", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageText(ctx, p, a.Selector)
	}, truncationText)
	registerAspect("css", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageCSS(ctx, p, a.Target)
	}, truncationText)
	registerAspect("js", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageJS(ctx, p, a.Target)
	}, truncationText)
	registerAspect("links", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageLinks(ctx, p)
	}, truncationJSON)
	registerAspect("screenshot", readScreenshot, truncationNone)
	registerAspect("meta", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageMeta(ctx, p)
	}, truncationJSON)
	registerAspect("interactives", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageInteractives(ctx, p, a.Selector)
	}, truncationNone)
	registerAspect("a11y", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageA11y(ctx, p)
	}, truncationNone)
	registerAspect("network", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageNetworkFiltered(ctx, p, a.Filter)
	}, truncationNone)
	registerAspect("body", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageBody(ctx, p, a.Target)
	}, truncationNone)
	registerAspect("console", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageConsole(ctx, p)
	}, truncationNone)
	registerAspect("websocket", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageWebsocket(ctx, p)
	}, truncationNone)
	registerAspect("article", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageArticle(ctx, p)
	}, truncationNone)
	registerAspect("tables", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageTables(ctx, p)
	}, truncationNone)
	registerAspect("forms", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageForms(ctx, p)
	}, truncationNone)
	registerAspect("marks", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageMarks(ctx, p)
	}, truncationNone)
	registerAspect("security", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageSecurity(ctx, p)
	}, truncationNone)
	registerAspect("sinks", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageSinksFiltered(ctx, p, a.Filter)
	}, truncationNone)
	registerAspect("postmessage", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PagePostMessage(ctx, p, a.Target)
	}, truncationNone)
	registerAspect("storage", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageStorage(ctx, p, a.IncludeValues)
	}, truncationNone)
	registerAspect("har", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageHAR(ctx, p, 0)
	}, truncationNone)
}
