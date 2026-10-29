package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/config"
)

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
			pat := "empty"
			if cf.PAT != "" {
				pat = "set"
			}
			caido := "disabled"
			if cf.Enabled {
				caido = "enabled"
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "caido: %s, %s, pat %s\n", caido, cf.URL, pat)
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
