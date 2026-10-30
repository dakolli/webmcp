package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
)

// caidoAuthProbeTimeout bounds the PAT probe behind `config show`, so a stalled
// or absent instance does not turn a settings dump into a long wait.
const caidoAuthProbeTimeout = 2 * time.Second

// NewConfigCmd builds the `config` command group for webmcp.toml.
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and seed the browser configuration file",
		Long: `Manage ~/.config/browser-mcp/webmcp.toml, the optional file carrying the
Caido and Chromium-browser settings. The file is created with mode 0600 and is
never overwritten.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newConfigInitCmd(), newConfigPathCmd(), newConfigShowCmd())
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create webmcp.toml when it does not exist",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.CaidoConfigPath()
			_, statErr := os.Stat(path)
			existed := statErr == nil
			if err := config.SeedCaido(); err != nil {
				return fmt.Errorf("config init: %w", err)
			}
			state := "created"
			if existed {
				state = "exists"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", state, path)
			return nil
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the webmcp.toml path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), config.CaidoConfigPath())
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved browser and Caido settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := config.LoadCaido()
			if err != nil {
				return fmt.Errorf("config show: %w", err)
			}
			bf, err := config.ResolveBrowser(nil)
			if err != nil {
				return fmt.Errorf("config show: %w", err)
			}
			caidoState := "disabled"
			if cf.Enabled {
				caidoState = "enabled"
			}
			out := cmd.OutOrStdout()
			authCtx, cancel := context.WithTimeout(cmdCtx(cmd), caidoAuthProbeTimeout)
			defer cancel()
			fmt.Fprintf(out, "caido: %s, %s\n", caidoState, cf.URL)
			fmt.Fprintf(out, "auth: %s\n", caidoAuthState(authCtx, cf))
			project := cf.Project
			if project == "" {
				project = "(instance default)"
			}
			scope := cf.ScopeName
			if scope == "" {
				scope = "(none)"
			}
			fmt.Fprintf(out, "project: %s\n", project)
			fmt.Fprintf(out, "scope: %s allow=%v deny=%v\n", scope, cf.ScopeAllow, cf.ScopeDeny)
			fmt.Fprintf(out, "capture: %s\n", captureLabel(cf))
			quiet := "quiet"
			if !bf.Quiet {
				quiet = "quiet off"
			}
			fmt.Fprintf(out, "browser: %s, %d flags\n", quiet, len(bf.ChromiumFlags))
			for _, flag := range bf.ChromiumFlags {
				fmt.Fprintf(out, "  %s\n", flag)
			}
			return nil
		},
	}
}

// captureLabel renders the configured capture mode for `config show`.
func captureLabel(cf config.CaidoFile) string {
	mode, err := cf.CaptureMode()
	if err != nil {
		return "invalid"
	}
	switch mode {
	case config.CaptureAll:
		return "all"
	case config.CaptureInScope:
		return "in-scope"
	default:
		return "unchanged"
	}
}

// caidoAuthState reports which credential caido_search will use. A PAT is
// probed so a rejected one shows the guest fallback instead of a usable PAT.
func caidoAuthState(ctx context.Context, cf config.CaidoFile) string {
	switch {
	case cf.AccessToken != "":
		return string(caido.AuthAccessToken)
	case cf.PAT != "":
		c := caido.NewClient(cf.URL)
		path, err := c.Login(ctx, "", cf.PAT, cf.AllowGuest)
		if err != nil {
			return string(caido.AuthNone)
		}
		return string(path)
	case cf.AllowGuest:
		return string(caido.AuthGuest)
	default:
		return string(caido.AuthNone)
	}
}
