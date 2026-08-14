package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
	mcpServer "webmcp/pkg/mcp/server"
)

// serveRunOptions carries the construction-time options of one serve loop run,
// shared by the `serve` and `serve mcp` commands (foreground and daemonized).
type serveRunOptions struct {
	// headless runs Chromium headless instead of headful (default: headful).
	headless bool
	// stealth applies go-rod/stealth to every page (default: on).
	stealth bool
	// socket is the IPC socket path to bind ("" → ipc.DefaultSocketPath()).
	socket string
	// mcpPort is the HTTP port for the in-process MCP server; 0 disables the
	// MCP server entirely (plain `serve`).
	mcpPort int
}

// serveFlags holds the serve-tree flags, defined once on the `serve` parent as
// persistent flags so the `serve mcp` subcommand inherits them.
type serveFlags struct {
	headless   bool
	stealth    bool
	foreground bool
}

// serveFlagsVar is the package-level storage the serve-tree persistent flags
// bind to (cobra's BoolVar needs a stable pointer; subcommands read the values
// via serveFlagsFromCmd).
var serveFlagsVar serveFlags

// serveStopWait bounds the wait for the daemon's socket to disappear after a
// server.shutdown request; serveStopPollInterval is the probe cadence and
// serveStopProbeTimeout bounds each individual probe dial.
const (
	serveStopWait         = 2 * time.Second
	serveStopPollInterval = 100 * time.Millisecond
	serveStopProbeTimeout = 200 * time.Millisecond
)

// NewServeCmd constructs the "serve" command tree — serve (run), serve stop and
// serve mcp — which owns the shared browser for the process's lifetime and
// exposes it over a local JSON-RPC 2.0 unix socket, so the other web-cli
// commands (and any local client) can drive the SAME live browser across
// invocations. `serve` daemonizes by default; --foreground keeps the Ctrl-C
// debugging mode. `serve mcp` additionally hosts the MCP HTTP/SSE server
// in-process on --port.
//
// Like the old serve it deliberately takes NO injected *browsers.Manager: the
// loop must construct its own manager with construction-time options
// (--headless, --stealth — browsers.NewManager(WithHeadless, WithStealth)), so
// the root-injected manager (constructed with engine defaults) cannot be reused.
func NewServeCmd() *cobra.Command {
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the live browser over a local IPC socket",
		Long: `Serve starts the shared browser as a persistent background daemon and
exposes it over a local JSON-RPC 2.0 unix socket (newline-delimited JSON):
it returns to the prompt once the daemon answers ping, and the other web-cli
commands and local clients connect to the socket to drive the SAME live
browser, so pages persist across invocations. Stop the daemon with
'web-cli serve stop'.

  --foreground  run in the foreground instead of daemonizing (Ctrl-C stops it;
                the headful Chromium window stays open until then)
  --headless    run Chromium headless (default: headful)
  --stealth     apply go-rod/stealth to every page (default: on)
  --socket      path of the IPC socket (default: $XDG_RUNTIME_DIR/web-mcp.sock,
                else ~/.web-mcp.sock)

Subcommands:
  stop  Stop the serve daemon (server.shutdown over IPC)
  mcp   Daemonize with the MCP HTTP/SSE server on --port as well`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags, err := serveFlagsFromCmd(cmd)
			if err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			path, err := socketPath(cmd)
			if err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			opts := serveRunOptions{
				headless: flags.headless,
				stealth:  flags.stealth,
				socket:   path,
			}
			if flags.foreground {
				return runServeForeground(cmdCtx(cmd), opts)
			}
			childArgs := []string{
				"serve",
				"--foreground",
				"--headless=" + strconv.FormatBool(flags.headless),
				"--stealth=" + strconv.FormatBool(flags.stealth),
				"--socket=" + path,
			}
			if err := daemonizeServe(cmd, opts, childArgs); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}

	serveCmd.PersistentFlags().BoolVar(&serveFlagsVar.headless, "headless", false,
		"run the browser headless instead of headful (default: headful)")
	serveCmd.PersistentFlags().BoolVar(&serveFlagsVar.stealth, "stealth", true,
		"apply go-rod/stealth to every page (default: on)")
	serveCmd.PersistentFlags().BoolVar(&serveFlagsVar.foreground, "foreground", false,
		"run in the foreground instead of daemonizing (Ctrl-C stops it)")

	serveCmd.AddCommand(NewServeStopCmd(), NewServeMcpCmd())
	return serveCmd
}

// serveFlagsFromCmd resolves the serve-tree persistent flags from any command
// in the tree (serve, serve stop, serve mcp) — they are inherited from the
// `serve` parent.
func serveFlagsFromCmd(cmd *cobra.Command) (serveFlags, error) {
	var f serveFlags
	var err error
	if f.headless, err = cmd.Flags().GetBool("headless"); err != nil {
		return f, fmt.Errorf("read --headless: %w", err)
	}
	if f.stealth, err = cmd.Flags().GetBool("stealth"); err != nil {
		return f, fmt.Errorf("read --stealth: %w", err)
	}
	if f.foreground, err = cmd.Flags().GetBool("foreground"); err != nil {
		return f, fmt.Errorf("read --foreground: %w", err)
	}
	return f, nil
}

// NewServeStopCmd builds `serve stop`: it probes the IPC socket, sends the
// server.shutdown request and waits briefly for the socket to disappear, then
// prints "stopped" — or a friendly "no serve daemon running" error when the
// probe fails. The socket-disappearance poll, not the response, is the source
// of truth (the daemon may tear down while answering).
func NewServeStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the serve daemon",
		Long: `Stops the background serve daemon (started with 'web-cli serve' or
'web-cli serve mcp') by sending it a server.shutdown request over the IPC
socket and waiting for the socket to disappear. Prints a friendly error when
no serve daemon is running.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := socketPath(cmd)
			if err != nil {
				return fmt.Errorf("serve stop: %w", err)
			}
			cl, err := ipc.Dial(path)
			if err != nil {
				return fmt.Errorf("serve stop: no serve daemon running at %s", path)
			}
			defer cl.Close()

			ctx, cancel := context.WithTimeout(cmdCtx(cmd), defaultCmdTimeout)
			defer cancel()
			// The response is best-effort: the daemon may tear down while
			// answering. The socket-disappearance poll is the source of truth.
			_ = cl.Call(ctx, ipc.MethodServerShutdown, nil, nil)

			deadline := time.Now().Add(serveStopWait)
			for time.Now().Before(deadline) {
				probe, perr := net.DialTimeout("unix", path, serveStopProbeTimeout)
				if perr != nil {
					fmt.Println("stopped")
					return nil
				}
				_ = probe.Close()
				time.Sleep(serveStopPollInterval)
			}
			return fmt.Errorf("serve stop: daemon at %s did not stop within %s", path, serveStopWait)
		},
	}
}

// NewServeMcpCmd builds `serve mcp`: it daemonizes exactly like `serve` (same
// log, same socket, same readiness ping) and additionally starts the MCP
// HTTP/SSE server on --port in the SAME process, so one daemon serves both the
// IPC layer and MCP, and the MCP tools drive the same live browser. `serve
// stop` reaches it through the same IPC socket.
func NewServeMcpCmd() *cobra.Command {
	var port int
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the browser over IPC and MCP (HTTP/SSE) from one daemon",
		Long: `Serves the shared browser exactly like 'web-cli serve' (same socket, same
log, same daemonization) and additionally hosts the MCP HTTP/SSE server on
--port in the same process, so LLM agents and the CLI drive the SAME live
browser. Stop the daemon with 'web-cli serve stop'.

  --port <n>  HTTP port for the MCP/SSE server (default 4099)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags, err := serveFlagsFromCmd(cmd)
			if err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			path, err := socketPath(cmd)
			if err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			opts := serveRunOptions{
				headless: flags.headless,
				stealth:  flags.stealth,
				socket:   path,
				mcpPort:  port,
			}
			if flags.foreground {
				return runServeForeground(cmdCtx(cmd), opts)
			}
			childArgs := []string{
				"serve", "mcp",
				"--foreground",
				"--port=" + strconv.Itoa(port),
				"--headless=" + strconv.FormatBool(flags.headless),
				"--stealth=" + strconv.FormatBool(flags.stealth),
				"--socket=" + path,
			}
			if err := daemonizeServe(cmd, opts, childArgs); err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			return nil
		},
	}
	mcpCmd.Flags().IntVar(&port, "port", 4099,
		"HTTP port for the MCP/SSE server (default 4099)")
	return mcpCmd
}

// daemonizeServe spawns the serve loop (childArgs, e.g. ["serve",
// "--foreground", ...] or ["serve", "mcp", ...]) as a detached daemon, polls
// ping until it answers, then prints the daemon status. When a serve daemon is
// already running at the socket it fails fast — one daemon at a time.
func daemonizeServe(cmd *cobra.Command, opts serveRunOptions, childArgs []string) error {
	// A daemon may already be running at the socket: fail cheaply with a
	// probe (no browser launch involved) instead of spawning a second one.
	if cl := dialServe(cmd); cl != nil {
		_ = cl.Close()
		return fmt.Errorf("web-cli server already running at %s", opts.socket)
	}

	logPath, err := spawnServeDaemon(childArgs)
	if err != nil {
		return err
	}
	if err := waitForServeDaemon(cmdCtx(cmd), opts.socket, logPath); err != nil {
		return err
	}
	cmd.Printf("browser_id: %s\n", singleBrowserID)
	cmd.Printf("socket: %s\n", opts.socket)
	if opts.mcpPort != 0 {
		cmd.Printf("mcp: http://localhost:%d\n", opts.mcpPort)
	}
	cmd.Printf("daemonized (log: %s); stop with 'web-cli serve stop'\n", logPath)
	return nil
}

// runServeForeground runs the long-lived serve loop in this process: it binds
// the IPC socket (before launching the browser, so a second serve fails cheaply
// instead of paying for a Chromium launch), optionally starts the MCP HTTP
// server on opts.mcpPort in-process, opens the browser, and blocks until ctx is
// cancelled (SIGINT/SIGTERM) or a server.shutdown request arrives — then the
// loop stops and the process tears the browser down via mgr.Shutdown.
func runServeForeground(ctx context.Context, opts serveRunOptions) error {
	mgr := browsers.NewManager(
		browsers.WithHeadless(opts.headless),
		browsers.WithStealth(opts.stealth),
	)
	// Graceful teardown of the browser and all of its pages when the loop
	// stops (SIGINT/SIGTERM cancels ctx, or a server.shutdown request makes
	// Serve return): Shutdown closes the browser and every page.
	defer mgr.Shutdown(ctx)

	path := opts.socket
	if path == "" {
		path = ipc.DefaultSocketPath()
	}

	// Bind the socket BEFORE launching the browser: when another serve process
	// already holds the socket, Bind fails cheaply with the "already running"
	// error instead of paying for the first-run Chromium download (~100MB) and
	// browser launch below.
	srv := ipc.NewServer(mgr) // construction only — no I/O
	if err := srv.Bind(path); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	// Close the bound socket on any error path after this point (e.g. a failed
	// mgr.Open below) so a leak can't leave a stale socket file/listener behind.
	// Close is idempotent, so the normal path is unaffected.
	defer srv.Close()

	// serveCtx is cancelled when the IPC serve loop exits, so a serve-mcp run's
	// HTTP server stops (gracefully drained) right after the loop does.
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()

	// serve mcp: host the MCP HTTP/SSE server in-process on the SAME manager,
	// so MCP tools drive the very browser the IPC layer serves.
	var httpSrv *mcpServer.Server
	if opts.mcpPort != 0 {
		httpSrv = mcpServer.New(mgr, fmt.Sprintf(":%d", opts.mcpPort))
		go func() {
			if err := httpSrv.Serve(serveCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("serve mcp: http server: %v", err)
			}
		}()
	}

	// First run downloads Chromium (~100MB); serveCtx bounds the launch.
	if _, err := mgr.Open(serveCtx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	fmt.Printf("browser_id: %s\n", singleBrowserID)
	fmt.Printf("socket: %s\n", path)
	if httpSrv != nil {
		fmt.Printf("mcp: http://localhost:%d\n", opts.mcpPort)
	}
	fmt.Printf("press Ctrl-C to stop\n")

	// Blocks until SIGINT/SIGTERM (ctx cancellation) or a server.shutdown
	// request (the internal shutdown hook makes Serve return), or an
	// unrecoverable server error.
	if err := srv.Serve(serveCtx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	// Serve already shut the listener/conns down on the stop; Close is
	// idempotent and also removes the socket file.
	_ = srv.Close()
	return nil
}
