// Retarget helpers shared by the web-cli commands that forward their work to
// the long-lived `web-cli serve` process over IPC when it is running, falling
// back to the in-process single-shot path otherwise (WEB-29).
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/cli/ipc"
)

// serveHint is printed to stderr (never stdout — stdout must stay byte-identical
// for scripts) when no serve process is reachable and the command falls back to
// the in-process path.
const serveHint = "no serve process running (browser dies with this command); run 'web-cli serve' for a persistent browser"

// pingTimeout bounds the serve-process liveness probe: a dead or stale socket
// must fall back to the in-process path fast, not hang the command.
const pingTimeout = 500 * time.Millisecond

// socketPath resolves the IPC socket path: the global --socket flag when set,
// else the shared default (ipc.DefaultSocketPath: $XDG_RUNTIME_DIR/web-mcp.sock,
// else ~/.web-mcp.sock). The flag is registered once on the root command (see
// NewRootCmd) and only consumed here and by serve — never redefined.
func socketPath(cmd *cobra.Command) (string, error) {
	path, err := cmd.Flags().GetString("socket")
	if err != nil {
		return "", fmt.Errorf("read --socket: %w", err)
	}
	if path == "" {
		path = ipc.DefaultSocketPath()
	}
	return path, nil
}

// dialServe detects a live serve process and returns a client for it, or nil
// when none is reachable (no socket, stale socket, or a server that does not
// answer ping within pingTimeout). Every retargeted command calls this once at
// the top of RunE: non-nil means "forward over IPC", nil means "run the
// in-process path and print the serve hint to stderr". Errors are deliberately
// swallowed — the caller's fallback + hint is the user-facing story.
func dialServe(cmd *cobra.Command) *ipc.Client {
	path, err := socketPath(cmd)
	if err != nil {
		return nil
	}
	cl, err := ipc.Dial(path)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmdCtx(cmd), pingTimeout)
	defer cancel()
	if err := cl.Ping(ctx); err != nil {
		_ = cl.Close()
		return nil
	}
	return cl
}

// printServeHint writes the serve-process hint to stderr (cmd.ErrOrStderr), so
// stdout stays byte-identical to the single-shot CLI output.
func printServeHint(cmd *cobra.Command) {
	fmt.Fprintln(cmd.ErrOrStderr(), serveHint)
}
