package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
	"webmcp/pkg/cli/pageops"
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
			if err := pageops.ValidateURL(urlStr); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			if err := pageops.ValidateWait(waitFor); err != nil {
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

			// Forward to a live serve process when one is running (the browser
			// and its pages then persist across invocations); else fall back to
			// the in-process path below, which works exactly as before.
			if cl := dialServe(cmd); cl != nil {
				defer cl.Close()
				return fetchViaIPC(cmd, ctx, cl, urlStr, waitFor, asJSON)
			}
			printServeHint(cmd)

			start := time.Now() // load timing for the summary

			b, err := mgr.Open(ctx) // lazily launches the single Browser
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			p, err := b.NewPage(ctx, urlStr) // new tab; stealth applied by default
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			if err := pageops.WaitForPage(ctx, p, waitFor); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			summary, err := pageops.PageSummary(ctx, p, start)
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

// fetchViaIPC implements the fetch command against a live serve process: it
// opens the browser (idempotent — mirrors Manager.Open's laziness) and the
// page over IPC, then prints the summary byte-identically to the in-process
// path (the IPC page.open result IS the pageops.FetchSummary shape).
func fetchViaIPC(cmd *cobra.Command, ctx context.Context, cl *ipc.Client, urlStr, waitFor string, asJSON bool) error {
	var open ipc.BrowserOpenResult
	if err := cl.Call(ctx, ipc.MethodBrowserOpen, &ipc.BrowserOpenParams{}, &open); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	var summary ipc.PageOpenResult
	if err := cl.Call(ctx, ipc.MethodPageOpen, &ipc.PageOpenParams{URL: urlStr, Wait: waitFor}, &summary); err != nil {
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
}
