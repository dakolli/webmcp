package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/ipc"
	"github.com/dakolli/webmcp/pkg/cli/pageops"
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
		viewport  string
		device    string
	)

	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Open a URL in the live browser and print a page summary (CLI fetch_page)",
		Long: "Fetch opens a URL in a new page of the live browser, waits for the page " +
			"to load, and prints a compact summary (session_id, title, url, text snippet, " +
			"html byte size, load time). It mirrors the MCP fetch_page tool; the printed " +
			"session_id can be used with the page command to read or interact with the page. " +
			"A global --browser-id pins the browser; the id must already be live and is " +
			"never replaced by a default or by the only open browser.",
		Example: `  web-cli fetch https://news.ycombinator.com          # open a page; prints session id + summary
  web-cli fetch https://example.com --wait stable     # wait for the DOM to settle first
  web-cli fetch https://example.com --device IPhoneX  # emulate an iPhone X (opt-in)
  web-cli fetch https://example.com --json            # machine-readable summary
  browser --browser-id browser-2 fetch https://example.com  # open on a pinned live browser`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			urlStr := args[0]
			browserID := explicitBrowserID(cmd)
			if err := pageops.ValidateURL(urlStr); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			if err := pageops.ValidateWait(waitFor); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			// Parse --viewport "WxH" (empty/absent = plain launch, rod defaults).
			vp, err := pageops.ParseViewport(viewport)
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			// Pre-flight the --device preset (same fail-fast doctrine as the
			// MCP fetch_page validate): an unknown name fails before any
			// browser work, with the exact pinned unknown-device error.
			if device != "" {
				if err := browsers.ValidateDeviceName(device); err != nil {
					return fmt.Errorf("fetch: %w", err)
				}
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
				return fetchViaIPC(cmd, ctx, cl, urlStr, waitFor, asJSON, vp, device, browserID)
			}
			printServeHint(cmd)

			start := time.Now() // load timing for the summary

			b, err := openTargetBrowser(ctx, mgr, browserID, "fetch")
			if err != nil {
				return err
			}
			reportBrowserTarget(cmd, browserID)
			// NewPage applies the device preset pre-navigation (governs the
			// load itself); the CLI has no --user-agent flag (MCP-only).
			p, err := b.NewPage(ctx, urlStr, vp, device, "") // new tab; stealth applied by default
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
	cmd.Flags().StringVar(&viewport, "viewport", "",
		"viewport size as WxH, e.g. 1440x900 (explicit override only; pages launch plain by default)")
	cmd.Flags().StringVar(&device, "device", "",
		"go-rod device preset to emulate BEFORE navigation (primary, opt-in mechanism; e.g. IPhoneX, IPadPro; empty = plain launch)")
	return cmd
}

// fetchViaIPC implements the fetch command against a live serve process: it
// validates the pinned browser (when --browser-id is set) or opens the default
// one, opens the page in that browser, then prints the summary byte-identically
// to the in-process path (the IPC page.open result IS the
// pageops.FetchSummary shape). browserID is forwarded so the daemon opens the
// page on the browser the user pinned; an unknown id fails with the live ids.
func fetchViaIPC(
	cmd *cobra.Command,
	ctx context.Context,
	cl *ipc.Client,
	urlStr, waitFor string,
	asJSON bool,
	vp *browsers.Viewport,
	device, browserID string,
) error {
	if browserID == "" {
		var open ipc.BrowserOpenResult
		if err := cl.Call(ctx, ipc.MethodBrowserOpen, &ipc.BrowserOpenParams{}, &open); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
	} else if err := requireLiveBrowser(ctx, cl, browserID, "fetch"); err != nil {
		return err
	}
	var summary ipc.PageOpenResult
	if err := cl.Call(ctx, ipc.MethodPageOpen, &ipc.PageOpenParams{
		URL:       urlStr,
		Wait:      waitFor,
		Viewport:  vp,
		Device:    device,
		BrowserID: browserID,
	}, &summary); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	reportBrowserTarget(cmd, browserID)

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

// openTargetBrowser returns the browser fetch opens its page in: the pinned
// browser when --browser-id is set (it must already be live, so an unknown id
// fails with the live ids), else the lazily opened default browser.
func openTargetBrowser(
	ctx context.Context,
	mgr *browsers.Manager,
	id, label string,
) (*browsers.Browser, error) {
	if id == "" {
		b, err := mgr.Open(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		return b, nil
	}
	b, err := mgr.Browser(browsers.BrowserID(id))
	if err != nil {
		if errors.Is(err, browsers.ErrUnknownBrowser) {
			return nil, unknownBrowserErr(label, id, managerBrowserIDs(mgr))
		}
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return b, nil
}
