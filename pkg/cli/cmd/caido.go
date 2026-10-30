package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
)

// NewCaidoCmd builds the `caido` command group, the human view over the same
// Caido instance the caido_search and caido_status MCP tools read. It talks to
// Caido directly, so it never needs the serve daemon or the MCP server.
func NewCaidoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "caido",
		Short: "Inspect the configured Caido instance",
		Long: `Read the Caido project, scopes, capture mode and history size that the
caido_search MCP tool sees. Caido must run on the same host. The command reads
webmcp.toml for the endpoint, credential and configured project/scope, then
queries Caido directly without starting the serve daemon.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newCaidoStatusCmd())
	cmd.AddCommand(newCaidoSearchCmd())
	cmd.AddCommand(newCaidoSitemapCmd())
	cmd.AddCommand(newCaidoFindingCmd())
	return cmd
}

// newCaidoStatusCmd constructs the "caido status" command: one query reports the
// project, every scope, the capture settings and the history count.
func newCaidoStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the Caido project, scopes, capture mode and history count",
		Long: `Reads the current Caido project, every scope, the capture settings and the
history row count in one GraphQL request. Call it before a search returns
nothing: an empty scope allowlist matches all traffic, while a scope with an
allowlist matches only its hosts.`,
		Example: `  browser caido status`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := config.LoadCaido()
			if err != nil {
				return fmt.Errorf("caido status: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			client := caido.NewClient(cf.URL)
			if _, err := client.Login(
				ctx,
				cf.AccessToken,
				cf.PAT,
				cf.AllowGuest,
			); err != nil {
				return fmt.Errorf("caido status: %w", err)
			}
			project, scope := caido.Selection()
			status, err := client.Status(ctx, project, scope)
			if err != nil {
				return fmt.Errorf("caido status: %w", err)
			}
			printCaidoStatus(cmd, status)
			return nil
		},
	}
}

// newCaidoSearchCmd constructs the "caido search" command: the human view over
// the same server-side filter the caido_search MCP tool sends.
func newCaidoSearchCmd() *cobra.Command {
	var (
		httpql       string
		scopeID      string
		host         string
		pathContains string
		method       string
		urlSub       string
		part         string
		status       int
		statusGTE    int
		since        int
		limit        int
		bodyBytes    int
		raw          bool
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search the Caido history with HTTPQL, typed keys, or a scope",
		Long: `Reads recorded history newest first. A raw HTTPQL expression or the typed
keys (host, method, status, status_gte, path_contains) filter on the server and
join with AND; a non-empty --httpql wins and the typed keys are ignored.
--scope-id selects the scope: empty uses the configured focus scope, "*" searches
every scope, any other value is a scope id from "browser caido status".
Without an expression or a scope the scan is bounded to the newest 500 rows.`,
		Example: `  browser caido search --host www.gnu.org
  browser caido search --httpql 'req.host.eq:"www.gnu.org"' --limit 50
  browser caido search --scope-id '*' --method POST --status-gte 400`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := config.LoadCaido()
			if err != nil {
				return fmt.Errorf("caido search: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			client := caido.NewClient(cf.URL)
			if _, err := client.Login(
				ctx,
				cf.AccessToken,
				cf.PAT,
				cf.AllowGuest,
			); err != nil {
				return fmt.Errorf("caido search: %w", err)
			}

			scope := strings.TrimSpace(scopeID)
			scopeName := ""
			allScopes := scope == "*"
			if !allScopes {
				if scope == "" {
					_, scopeName = caido.Selection()
				}
				if scope != "" || scopeName != "" {
					resolved, err := client.ResolveScope(ctx, scope, scopeName)
					if err != nil {
						return fmt.Errorf("caido search: %w", err)
					}
					scope, scopeName = resolved.ID, resolved.Name
				}
			}

			result, err := client.Search(ctx, caido.Query{
				URL:          strings.TrimSpace(urlSub),
				Method:       strings.ToUpper(strings.TrimSpace(method)),
				Status:       status,
				StatusGTE:    statusGTE,
				Host:         strings.TrimSpace(host),
				PathContains: strings.TrimSpace(pathContains),
				HTTPQL:       strings.TrimSpace(httpql),
				ScopeID:      scope,
				ScopeName:    scopeName,
				Limit:        limit,
				BodyBytes:    bodyBytes,
				Since:        time.Duration(since) * time.Second,
				Part:         part,
				Raw:          raw,
			})
			if err != nil {
				return fmt.Errorf("caido search: %w", err)
			}
			if jsonOut {
				data, err := json.Marshal(result)
				if err != nil {
					return fmt.Errorf("caido search: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			printCaidoSearch(cmd, result)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&httpql, "httpql", "", "raw HTTPQL expression sent to the instance unchanged")
	f.StringVar(&scopeID, "scope-id", "", "scope id, \"*\" for every scope, empty for the configured focus scope")
	f.StringVar(&host, "host", "", "typed host filter (req.host.eq)")
	f.StringVar(&method, "method", "", "HTTP method filter (req.method.eq)")
	f.IntVar(&status, "status", 0, "exact status code (resp.code.eq), 0 for all")
	f.IntVar(&statusGTE, "status-gte", 0, "minimum status code (resp.code.gte), 0 to disable")
	f.StringVar(&pathContains, "path-contains", "", "path substring (req.path.cont)")
	f.StringVar(&urlSub, "url", "", "URL substring matched on the returned rows")
	f.IntVar(&since, "since", 900, "lookback window in seconds, 0 to disable")
	f.IntVar(&limit, "limit", 20, "rows returned, clamps at 100")
	f.IntVar(&bodyBytes, "body-bytes", 2000, "per-body byte cap, 0 for no body")
	f.StringVar(&part, "part", "both", "which sides carry a body: request, response, or both")
	f.BoolVar(&raw, "raw", false, "disable credential redaction")
	f.BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// newCaidoSitemapCmd constructs the "caido sitemap" command: the human view over
// the same endpoint tree the caido_sitemap MCP tool reads.
func newCaidoSitemapCmd() *cobra.Command {
	var (
		pathPrefix string
		depth      int
		scopeID    string
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "sitemap <host>",
		Short: "List the endpoint tree the Caido sitemap recorded for one host",
		Long: `Reads one host's sitemap tree and collapses it to one row per method and path
shape, with numeric and UUID segments replaced by {id}. host is a bare host such as
www.gnu.org, with no scheme and no path. --path keeps only endpoints under that
prefix. --scope selects the scope: empty uses the configured focus scope, "*" reads
every scope, any other value is a scope id from "browser caido status". The result
caps at 200 rows.`,
		Example: `  browser caido sitemap www.gnu.org
  browser caido sitemap www.gnu.org --path /software --depth 2
  browser caido sitemap www.gnu.org --scope '*' --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := strings.TrimSpace(args[0])
			if host == "" {
				return fmt.Errorf("caido sitemap: host is required")
			}
			if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") {
				return fmt.Errorf("caido sitemap: host must be a bare host such as www.gnu.org — no scheme and no path")
			}
			if depth < 1 {
				depth = 1
			}
			if depth > 5 {
				depth = 5
			}
			cf, err := config.LoadCaido()
			if err != nil {
				return fmt.Errorf("caido sitemap: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()

			client := caido.NewClient(cf.URL)
			if _, err := client.Login(
				ctx,
				cf.AccessToken,
				cf.PAT,
				cf.AllowGuest,
			); err != nil {
				return fmt.Errorf("caido sitemap: %w", err)
			}

			scope := strings.TrimSpace(scopeID)
			scopeName := ""
			allScopes := scope == "*"
			if !allScopes {
				if scope == "" {
					_, scopeName = caido.Selection()
				}
				if scope != "" || scopeName != "" {
					resolved, err := client.ResolveScope(ctx, scope, scopeName)
					if err != nil {
						return fmt.Errorf("caido sitemap: %w", err)
					}
					scope, scopeName = resolved.ID, resolved.Name
				}
			}

			result, err := client.Sitemap(ctx, caido.SitemapQuery{
				Host:    host,
				Path:    strings.TrimSpace(pathPrefix),
				Depth:   depth,
				ScopeID: scope,
			})
			if err != nil {
				return fmt.Errorf("caido sitemap: %w", err)
			}
			if jsonOut {
				data, err := json.Marshal(result)
				if err != nil {
					return fmt.Errorf("caido sitemap: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			printCaidoSitemap(cmd, result)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&pathPrefix, "path", "", "keep only endpoints whose path starts with this prefix")
	f.IntVar(&depth, "depth", 5, "sitemap tree depth to walk, clamped 1..5")
	f.StringVar(&scopeID, "scope", "", "scope id, \"*\" for every scope, empty for the configured focus scope")
	f.BoolVar(&jsonOut, "json", false, "print the envelope as JSON")
	return cmd
}

// printCaidoSitemap renders the envelope header and one table row per endpoint.
func printCaidoSitemap(cmd *cobra.Command, result caido.SitemapResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "host: %s\n", result.Host)
	fmt.Fprintf(out, "project: %s\n", result.Project)
	if result.ScopeID == "" {
		fmt.Fprintf(out, "scope_id: (none)\n")
	} else {
		fmt.Fprintf(out, "scope_id: %s\n", result.ScopeID)
	}
	fmt.Fprintf(out, "count: %d scanned: %d dropped: %d truncated: %d\n",
		result.Count, result.Scanned, result.Dropped, result.Truncated)
	if result.Note != "" {
		fmt.Fprintf(out, "note: %s\n", result.Note)
	}
	if len(result.Rows) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tKIND\tSTATUS\tREQUESTS\tMIME\tPARAMS")
	for _, row := range result.Rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			row.Method,
			row.Path,
			row.Kind,
			row.Status,
			row.Requests,
			row.Mime,
			strings.Join(row.Params, ","),
		)
	}
	_ = tw.Flush()
}

// printCaidoSearch renders the filter that ran and one line per transaction.
func printCaidoSearch(cmd *cobra.Command, result caido.Result) {
	out := cmd.OutOrStdout()
	httpql := result.HTTPQL
	if httpql == "" {
		httpql = "(none)"
	}
	source := result.HTTPQLSource
	if source == "" {
		source = "none"
	}
	fmt.Fprintf(out, "httpql: %s\n", httpql)
	fmt.Fprintf(out, "source: %s\n", source)
	if result.AllScopes {
		fmt.Fprintf(out, "scope_id: *\n")
	} else if result.ScopeID != "" {
		fmt.Fprintf(out, "scope_id: %s\n", result.ScopeID)
	} else {
		fmt.Fprintf(out, "scope_id: (none)\n")
	}
	if result.ScopeName != "" {
		fmt.Fprintf(out, "scope_name: %s\n", result.ScopeName)
	}
	fmt.Fprintf(out, "count: %d total: %d scanned: %d dropped: %d\n",
		result.Count, result.Total, result.Scanned, result.Dropped)
	fmt.Fprintf(out, "since: %ds dropped_since: %d\n",
		result.SinceSeconds, result.DroppedSince)
	if result.Note != "" {
		fmt.Fprintf(out, "note: %s\n", result.Note)
	}
	for _, e := range result.Entries {
		fmt.Fprintf(out, "%s %d %s %s\n", e.Method, e.Status, e.ID, e.URL)
	}
}

// printCaidoStatus renders the status as one field per line, with each scope
// indented under its name.
func printCaidoStatus(cmd *cobra.Command, status caido.Status) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "auth: %s\n", status.Auth)
	fmt.Fprintf(out, "url: %s\n", status.URL)
	name := status.Project.Name
	if name == "" {
		name = "(none selected)"
	}
	fmt.Fprintf(out, "project: %s\n", name)
	fmt.Fprintf(out, "  id: %s\n", status.Project.ID)
	fmt.Fprintf(out, "  path: %s\n", status.Project.Path)
	fmt.Fprintf(out, "  status: %s\n", status.Project.Status)
	fmt.Fprintf(out, "  temporary: %t\n", status.Project.Temporary)
	fmt.Fprintf(out, "  read_only: %t\n", status.Project.ReadOnly)
	fmt.Fprintf(out, "  selected: %s\n", status.Project.Selected)
	fmt.Fprintf(out, "  configured: %s\n", status.Project.Configured)
	fmt.Fprintf(out, "history: %d\n", status.History)
	fmt.Fprintf(out, "scopes: %d\n", len(status.Scopes))
	for _, s := range status.Scopes {
		fmt.Fprintf(out, "  scope: %s\n", s.Name)
		fmt.Fprintf(out, "    id: %s\n", s.ID)
		fmt.Fprintf(out, "    allowlist: %s\n", jsonList(s.Allowlist))
		fmt.Fprintf(out, "    denylist: %s\n", jsonList(s.Denylist))
		fmt.Fprintf(out, "    matches: %s\n", s.Matches)
		fmt.Fprintf(out, "    focus: %t\n", s.Focus)
	}
	fmt.Fprintf(out, "capture: %s\n", jsonValue(status.Capture))
}

// jsonList renders a string slice as compact JSON so [] stays distinct from an
// absent value.
func jsonList(values []string) string {
	if values == nil {
		values = []string{}
	}
	return jsonValue(values)
}

// jsonValue marshals one value for the text output; a marshal failure leaves an
// empty field rather than aborting the whole status print.
func jsonValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

// newCaidoFindingCmd constructs the "caido finding" command group: the human
// view over the findings the caido_finding MCP tool lists and creates.
func newCaidoFindingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finding",
		Short: "List, read and create Caido findings",
		Long: `Reads and creates Caido findings. A finding links a title and notes to one
captured request id, and the Caido UI shows it. list shows the newest findings, get
reads one by id, and create records a new finding. The command never deletes or
updates a finding.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newCaidoFindingListCmd())
	cmd.AddCommand(newCaidoFindingGetCmd())
	cmd.AddCommand(newCaidoFindingCreateCmd())
	return cmd
}

// caidoFindingClient loads the Caido settings and returns a logged-in client with
// a bounded context, so every finding subcommand shares one auth path.
func caidoFindingClient(cmd *cobra.Command) (*caido.Client, context.Context, context.CancelFunc, error) {
	cf, err := config.LoadCaido()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("caido finding: %w", err)
	}
	ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
	client := caido.NewClient(cf.URL)
	if _, err := client.Login(ctx, cf.AccessToken, cf.PAT, cf.AllowGuest); err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("caido finding: %w", err)
	}
	return client, ctx, cancel, nil
}

// newCaidoFindingListCmd constructs the "caido finding list" command.
func newCaidoFindingListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the newest Caido findings",
		Long: `Lists the instance's findings newest first. Each row carries its id, title,
linked request id and creation time, so a finding can be linked back to a
caido_search row.`,
		Example: `  browser caido finding list
  browser caido finding list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, ctx, cancel, err := caidoFindingClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			findings, err := client.Findings(ctx)
			if err != nil {
				return fmt.Errorf("caido finding: %w", err)
			}
			if jsonOut {
				payload := struct {
					Findings []caido.Finding `json:"findings"`
					Count    int             `json:"count"`
				}{Findings: findings, Count: len(findings)}
				data, err := json.Marshal(payload)
				if err != nil {
					return fmt.Errorf("caido finding: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			printCaidoFindings(cmd, findings)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// newCaidoFindingGetCmd constructs the "caido finding get" command.
func newCaidoFindingGetCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Read one Caido finding by id",
		Long: `Reads one finding by id and prints its title, linked request id, creation
time and description. A missing id is an error naming "finding list".`,
		Example: `  browser caido finding get 1`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return fmt.Errorf("caido finding: get needs id")
			}
			client, ctx, cancel, err := caidoFindingClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			finding, err := client.Finding(ctx, id)
			if err != nil {
				return fmt.Errorf("caido finding: %w", err)
			}
			if jsonOut {
				data, err := json.Marshal(finding)
				if err != nil {
					return fmt.Errorf("caido finding: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			printCaidoFinding(cmd, finding)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// newCaidoFindingCreateCmd constructs the "caido finding create" command.
func newCaidoFindingCreateCmd() *cobra.Command {
	var (
		title     string
		requestID string
		notes     string
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Caido finding linked to a captured request",
		Long: `Creates a finding from a title and a captured request id. request_id is a
request id from caido_search, not a URL. notes becomes the description and is cut
at 4 KB. The command prints the new finding so its id can be reused.`,
		Example: `  browser caido finding create --title "IDOR on /api/orders" --request-id 994
  browser caido finding create --title "IDOR" --request-id 994 --notes "User A reads user B's order" --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			title = strings.TrimSpace(title)
			requestID = strings.TrimSpace(requestID)
			if title == "" || requestID == "" {
				return fmt.Errorf("caido finding: create needs title and request_id — take a request id from caido_search")
			}
			client, ctx, cancel, err := caidoFindingClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			finding, err := client.CreateFinding(ctx, title, requestID, notes)
			if err != nil {
				return fmt.Errorf("caido finding: %w", err)
			}
			if jsonOut {
				data, err := json.Marshal(finding)
				if err != nil {
					return fmt.Errorf("caido finding: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			printCaidoFinding(cmd, finding)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "finding title")
	f.StringVar(&requestID, "request-id", "", "captured request id from caido_search")
	f.StringVar(&notes, "notes", "", "finding description, cut at 4096 bytes")
	f.BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// printCaidoFindings renders the count and one table row per finding.
func printCaidoFindings(cmd *cobra.Command, findings []caido.Finding) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "count: %d\n", len(findings))
	if len(findings) == 0 {
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tREQUEST\tCREATED\tTITLE")
	for _, f := range findings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.ID, f.RequestID, f.CreatedAt, f.Title)
	}
	_ = tw.Flush()
}

// printCaidoFinding renders one finding as field per line.
func printCaidoFinding(cmd *cobra.Command, finding caido.Finding) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "id: %s\n", finding.ID)
	fmt.Fprintf(out, "title: %s\n", finding.Title)
	fmt.Fprintf(out, "request_id: %s\n", finding.RequestID)
	fmt.Fprintf(out, "created_at: %s\n", finding.CreatedAt)
	if finding.DescriptionCut {
		fmt.Fprintf(out, "description_cut: true\n")
	}
	if finding.Description != "" {
		fmt.Fprintf(out, "description: %s\n", finding.Description)
	}
}
