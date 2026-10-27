// Command web-cli is the lightweight CLI mirror of the web-mcp MCP tools:
// it drives the same pkg/browsers core engine from the command line, so we
// (the developers) can experiment interactively with the exact functionality
// the agent gets over MCP. It talks directly to pkg/browsers — never through
// pkg/mcp — mirroring the core-vs-MCP boundary kept by pkg/mcp/tools.
//
// Example usage (human-facing):
//
//	# 1. Start the shared browser as a background daemon (optional — commands
//	#    launch the browser lazily). fetch/page commands then persist across
//	#    invocations over the IPC socket.
//	web-cli serve
//
//	# 2. Open a URL. The printed session id (page-1) is what you pass to the
//	#    page commands — with one page open, you can omit it entirely.
//	web-cli fetch https://news.ycombinator.com
//
//	# 3. Read and drive the live page. 'last' means the most recently opened
//	#    page; bare numbers resolve to page-N.
//	web-cli page list
//	web-cli page fetch links
//	web-cli page act click "#search"
//	web-cli page fetch 12 text
//
//	# 4. Close the page, the browser, and the daemon.
//	web-cli page close
//	web-cli serve stop
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/cmd"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/store"
	"github.com/dakolli/webmcp/pkg/version"
)

func main() {
	// Cancel ctx on SIGINT/SIGTERM so a hung engine call cannot outlive the
	// process; ExecuteContext plumbs it into every cobra command.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The shared manager: headful Chromium, stealth on, ephemeral profiles by
	// default (a caller opts into persistence), and the configured browser idle
	// TTL. Constructed once and injected into the root command so every
	// subcommand operates on the SAME browser (mirrors cmd/mcp/main.go's
	// registry wiring).
	ttl := browsers.DefaultBrowserTTL
	if cfg, err := config.LoadConfig(); err == nil {
		ttl = cfg.BrowserIdleTTL(ttl)
	}
	mgr := browsers.NewManager(
		browsers.WithPersistentDefault(false),
		browsers.WithBrowserTTL(ttl),
		browsers.WithLogger(browsers.NewLogger(os.Stderr, nil)),
	)
	defer mgr.Shutdown(context.Background()) // graceful teardown (best-effort)

	root := cmd.NewRootCmd(mgr)
	root.Version = version.String()
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(newStoreCmd())
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

// newStoreCmd builds the "store" command tree. It is the only CLI surface over
// the embedded state database; MCP gets no store tool.
func newStoreCmd() *cobra.Command {
	storeCmd := &cobra.Command{
		Use:   "store",
		Short: "Inspect the embedded state database",
		Long: `Inspect the embedded SQLite state database at <config dir>/state.db.
Opening it creates the file (mode 0600) and applies pending migrations.`,
	}
	storeCmd.AddCommand(&cobra.Command{
		Use:   "info",
		Short: "Print the state database path, journal mode, schema version and tables",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			path, err := config.StateDBPath()
			if err != nil {
				return err
			}
			s, err := store.Open(ctx, path)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := s.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			mode, err := s.JournalMode(ctx)
			if err != nil {
				return err
			}
			version, err := s.Version(ctx)
			if err != nil {
				return err
			}
			tables, err := s.Tables(ctx)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "path: %s\n", path)
			fmt.Fprintf(out, "journal_mode: %s\n", mode)
			fmt.Fprintf(out, "schema_version: %d\n", version)
			fmt.Fprintf(out, "tables: %s\n", strings.Join(tables, ", "))
			return nil
		},
	})
	return storeCmd
}
