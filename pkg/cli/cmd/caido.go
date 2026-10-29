package cmd

import (
	"context"
	"encoding/json"
	"fmt"

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
