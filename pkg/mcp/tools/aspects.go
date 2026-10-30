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

// fetchFunc reads one page aspect through pageops.
type fetchFunc = func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error)

// aspectEntry pairs an aspect reader with its truncation policy.
type aspectEntry struct {
	read  fetchFunc
	trunc truncation
}

var (
	aspects     = map[string]aspectEntry{}
	aspectNames []string
)

// registerAspect adds one what value and its truncation policy to the registry.
func registerAspect(name string, fn fetchFunc, trunc truncation) {
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

// noArgs adapts a pageops function that only needs the page.
func noArgs(f func(context.Context, *browsers.Page) (string, error)) fetchFunc {
	return func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return f(ctx, p)
	}
}

// withArg adapts a pageops function that needs one extra argument extracted
// from pageFetchArgs.
func withArg[T any](f func(context.Context, *browsers.Page, T) (string, error), get func(pageFetchArgs) T) fetchFunc {
	return func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return f(ctx, p, get(a))
	}
}

// selector returns the selector argument of a page_fetch call.
func selector(a pageFetchArgs) string { return a.Selector }

// target returns the css/js/postmessage target argument of a page_fetch call.
func target(a pageFetchArgs) string { return a.Target }

// filter returns the network/sinks filter argument of a page_fetch call.
func filter(a pageFetchArgs) string { return a.Filter }

func init() {
	registerAspect("html", withArg(pageops.PageHTML, selector), truncationText)
	registerAspect("dom", noArgs(pageops.PageDOM), truncationJSON)
	registerAspect("text", withArg(pageops.PageText, selector), truncationText)
	registerAspect("css", withArg(pageops.PageCSS, target), truncationText)
	registerAspect("js", withArg(pageops.PageJS, target), truncationText)
	registerAspect("links", noArgs(pageops.PageLinks), truncationJSON)
	registerAspect("screenshot", readScreenshot, truncationNone)
	registerAspect("meta", noArgs(pageops.PageMeta), truncationJSON)
	registerAspect("interactives", withArg(pageops.PageInteractives, selector), truncationNone)
	registerAspect("a11y", noArgs(pageops.PageA11y), truncationNone)
	registerAspect("network", withArg(pageops.PageNetworkFiltered, filter), truncationNone)
	registerAspect("body", withArg(pageops.PageBody, target), truncationNone)
	registerAspect("console", noArgs(pageops.PageConsole), truncationNone)
	registerAspect("websocket", noArgs(pageops.PageWebSocket), truncationNone)
	registerAspect("dialogs", noArgs(pageops.PageDialogs), truncationNone)
	registerAspect("article", noArgs(pageops.PageArticle), truncationNone)
	registerAspect("tables", noArgs(pageops.PageTables), truncationNone)
	registerAspect("forms", noArgs(pageops.PageForms), truncationNone)
	registerAspect("marks", noArgs(pageops.PageMarks), truncationNone)
	registerAspect("security", noArgs(pageops.PageSecurity), truncationNone)
	registerAspect("sinks", withArg(pageops.PageSinksFiltered, filter), truncationNone)
	registerAspect("postmessage", withArg(pageops.PagePostMessage, target), truncationNone)
	registerAspect("storage", withArg(pageops.PageStorage, func(a pageFetchArgs) bool { return a.IncludeValues }), truncationNone)
	registerAspect("har", func(ctx context.Context, p *browsers.Page, _ pageFetchArgs) (string, error) {
		return pageops.PageHAR(ctx, p, 0)
	}, truncationNone)
	registerAspect("mutations", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageMutations(ctx, p, a.Since, a.Selector)
	}, truncationNone)
	registerAspect("reflections", func(ctx context.Context, p *browsers.Page, a pageFetchArgs) (string, error) {
		return pageops.PageReflections(ctx, p, a.Target, a.Selector)
	}, truncationNone)
}
