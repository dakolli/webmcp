package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
)

// defaultFetchTimeout bounds the whole fetch command when --timeout-ms is not
// given (or is zero): a hung page must not block the CLI forever.
const defaultFetchTimeout = 30 * time.Second

// NewFetchCmd builds the fetch command: the CLI equivalent of the MCP
// fetch_page tool. It opens a URL in a new page of the manager's live browser,
// waits for the page to reach the requested readiness level, and prints a
// compact page summary (session id, title, url, text snippet, size hints).
func NewFetchCmd(mgr *browsers.Manager) *cobra.Command {
	var (
		asJSON    bool
		waitFor   string
		timeoutMS int
	)

	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Open a URL in the live browser and print a page summary (CLI fetch_page)",
		Long: "Fetch opens a URL in a new page of the live browser, waits for the page " +
			"to load, and prints a compact summary (session_id, title, url, text snippet, " +
			"html byte size, load time). It mirrors the MCP fetch_page tool; the printed " +
			"session_id can be used with the page command to read or interact with the page.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			urlStr := args[0]
			if err := validateURL(urlStr); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			if err := validateWait(waitFor); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}

			// Enforce an explicit per-call timeout so a hung page (or a
			// first-run Chromium download) cannot block the CLI: default 30s,
			// honoring --timeout-ms. The derived ctx flows through Open,
			// NewPage and every wait below.
			timeout := defaultFetchTimeout
			if timeoutMS > 0 {
				timeout = time.Duration(timeoutMS) * time.Millisecond
			}
			ctx := cmd.Context()
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := time.Now() // load timing for the summary

			b, err := mgr.Open(ctx) // lazily launches the single Browser
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			p, err := b.NewPage(ctx, urlStr) // new tab; stealth applied by default
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			if err := waitForPage(ctx, p, waitFor); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			summary, err := pageSummary(ctx, p, start)
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}

			if asJSON {
				out, err := json.Marshal(summary)
				if err != nil {
					return fmt.Errorf("fetch: marshal summary: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"session_id: %s\ntitle: %s\nurl: %s\ntext_snippet: %s\nhtml_bytes: %d\nload_ms: %d\n",
				summary.SessionID, summary.Title, summary.URL,
				summary.TextSnippet, summary.HTMLBytes, summary.LoadMs)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print the summary as machine-readable JSON")
	cmd.Flags().StringVar(&waitFor, "wait", "idle",
		"wait strategy before returning: idle (no network requests for ~500ms), load (window.onload), stable (DOM unchanged for ~500ms)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 30000,
		"per-call timeout in milliseconds (default 30000)")
	return cmd
}

// validateURL checks that s is an absolute http(s) URL the browser can
// navigate to: it must parse, be absolute and carry a host.
func validateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", s, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("invalid url %q: must be an absolute http(s) URL", s)
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("invalid url %q: scheme %q not supported (must be http or https)", s, u.Scheme)
	}
}

// validateWait checks that the --wait value is one of idle, load, stable.
func validateWait(waitFor string) error {
	switch waitFor {
	case "idle", "load", "stable":
		return nil
	default:
		return fmt.Errorf("invalid --wait %q (must be one of idle, load, stable)", waitFor)
	}
}

// waitForPage blocks until the page satisfies the requested readiness level.
// It maps "load" to WaitLoad, "idle" to WaitRequestIdle, and "stable" to
// WaitDOMStable. All rod page calls are serialized with the per-page lock.
func waitForPage(ctx context.Context, p *browsers.Page, waitFor string) error {
	p.Lock()
	defer p.Unlock()

	pc := p.Rod.Context(ctx) // bound to ctx so cancellation aborts a stuck wait
	switch waitFor {
	case "idle":
		// WaitRequestIdle RETURNS a blocking func() — call it, do not return it.
		wait := pc.WaitRequestIdle(500*time.Millisecond, nil, nil, nil)
		wait()
		return nil
	case "load":
		return pc.WaitLoad()
	case "stable":
		return pc.WaitDOMStable(500*time.Millisecond, 0.05)
	default:
		return fmt.Errorf("invalid wait option %q", waitFor)
	}
}

// fetchSummary is the output shape of the fetch command, identical between the
// human-readable and --json forms. It carries the session_id (usable with the
// page command), page metadata and compact size/timing hints — never the full
// HTML (first-50KB rule).
type fetchSummary struct {
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	TextSnippet string `json:"text_snippet"`
	HTMLBytes   int    `json:"html_bytes"`
	LoadMs      int64  `json:"load_ms"`
}

// pageSummary renders a compact summary of a live page: the page ID, its title
// and URL (via p.Rod.Info()), plus a short text snippet and byte-size hints.
// loadStarted anchors the load_ms timing (captured around open → navigate →
// wait).
func pageSummary(ctx context.Context, p *browsers.Page, loadStarted time.Time) (fetchSummary, error) {
	p.Lock()
	defer p.Unlock()

	info, err := p.Rod.Context(ctx).Info() // title + url
	if err != nil {
		return fetchSummary{}, fmt.Errorf("page info: %w", err)
	}

	html, err := p.Rod.Context(ctx).HTML() // byte-size hint only; not printed in full
	if err != nil {
		return fetchSummary{}, fmt.Errorf("read html: %w", err)
	}

	text, err := p.Rod.Context(ctx).Eval(`() => document.body.innerText.slice(0, 500)`)
	if err != nil {
		return fetchSummary{}, fmt.Errorf("read text: %w", err)
	}

	return fetchSummary{
		SessionID:   string(p.ID),
		Title:       info.Title,
		URL:         info.URL,
		TextSnippet: text.Value.Str(),
		HTMLBytes:   len(html),
		LoadMs:      time.Since(loadStarted).Milliseconds(),
	}, nil
}
