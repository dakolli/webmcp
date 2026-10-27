package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/cli/ipc"
	"github.com/dakolli/webmcp/pkg/config"
	mcpServer "github.com/dakolli/webmcp/pkg/mcp/server"
)

// serveRunOptions carries the construction-time options of one serve loop run,
// shared by the `serve` and `serve mcp` commands (foreground and daemonized).
type serveRunOptions struct {
	// headless runs Chromium headless instead of headful (default: headful).
	headless bool
	// stealth applies go-rod/stealth to every page (default: on).
	stealth bool
	// bin specifies a custom browser executable binary path or command.
	bin string
	// profile specifies a custom profile name (brave, chrome, firefox, default) or directory path.
	profile string
	// socket is the IPC socket path to bind ("" → ipc.DefaultSocketPath()).
	socket string
	// mcpPort is the HTTP port for the in-process MCP server; 0 disables the
	// MCP server entirely (plain `serve`).
	mcpPort int
	// mcpAddr is the resolved "host:port" listen address of the MCP server
	// (default loopback); empty when the MCP server is disabled.
	mcpAddr string
}

// serveFlags holds the serve-tree flags, defined once on the `serve` parent as
// persistent flags so the `serve mcp` subcommand inherits them.
type serveFlags struct {
	headless   bool
	stealth    bool
	bin        string
	profile    string
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
it returns to the prompt once the daemon answers ping, and the other browser
commands and local clients connect to the socket to drive the SAME live
browser, so pages persist across invocations. Stop the daemon with
'browser serve stop'.

  --foreground  run in the foreground instead of daemonizing (Ctrl-C stops it;
                the headful Chromium window stays open until then)
  --headless    run Chromium headless (default: headful)
  --stealth     apply go-rod/stealth to every page (default: on)
  --socket      path of the IPC socket (default: $XDG_RUNTIME_DIR/web-mcp.sock,
                else ~/.web-mcp.sock)

Subcommands:
  stop  Stop the serve daemon (server.shutdown over IPC)
  mcp   Daemonize with the MCP HTTP/SSE server on --port as well`,
		Example: `  browser serve                    # daemonize the shared browser (headful, stealth on)
  browser serve --headless         # headless Chromium daemon
  browser serve --foreground       # run in this terminal (Ctrl-C stops it)
  browser serve stop               # stop the daemon

  browser serve mcp                # daemonize + host the MCP SSE server on :4099
  browser serve mcp --port 4089    # pick a different port`,
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
				bin:      flags.bin,
				profile:  flags.profile,
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
				"--bin=" + flags.bin,
				"--profile=" + flags.profile,
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
	serveCmd.PersistentFlags().StringVar(&serveFlagsVar.bin, "bin", "",
		"optional custom browser executable path or command (e.g. /usr/bin/brave-browser, brave, google-chrome)")
	serveCmd.PersistentFlags().StringVar(&serveFlagsVar.profile, "profile", "",
		"optional browser user profile name (brave, chrome, firefox, default) or custom directory path")
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
	if f.bin, err = cmd.Flags().GetString("bin"); err != nil {
		return f, fmt.Errorf("read --bin: %w", err)
	}
	if f.profile, err = cmd.Flags().GetString("profile"); err != nil {
		return f, fmt.Errorf("read --profile: %w", err)
	}
	if f.foreground, err = cmd.Flags().GetBool("foreground"); err != nil {
		return f, fmt.Errorf("read --foreground: %w", err)
	}
	return f, nil
}

// NewServeStopCmd builds `serve stop`: it probes the IPC socket, sends the
// server.shutdown request and waits briefly for the socket to disappear, then
// prints "stopped" — or a friendly "no serve daemon running" notice (exit 0)
// when the probe fails (idempotent stop, mirroring `browser close`). The
// socket-disappearance poll, not the response, is the source of truth (the
// daemon may tear down while answering).
func NewServeStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the serve daemon",
		Long: `Stops the background serve daemon (started with 'web-cli serve' or
'web-cli serve mcp') by sending it a server.shutdown request over the IPC
socket and waiting for the socket to disappear. Prints a friendly notice (and
exits 0) when no serve daemon is running.`,
		Example: `  web-cli serve stop    # stop the daemon started with 'web-cli serve' (or 'serve mcp')`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := socketPath(cmd)
			if err != nil {
				return fmt.Errorf("serve stop: %w", err)
			}
			cl, err := ipc.Dial(path)
			if err != nil {
				// Idempotent stop: no daemon running is not an error. Mirrors
				// `browser close` (idempotent no-op, exit 0) and keeps `make
				// install` / `make launch` / scripts clean. Dial probes the
				// listener, so a stale socket from a crashed daemon lands here
				// too (same benign path, no error dump).
				fmt.Println("no serve daemon running; nothing to stop")
				return nil
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
	var bind string
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the browser over IPC and MCP (HTTP/SSE) from one daemon",
		Long: `Serves the shared browser exactly like 'web-cli serve' (same socket, same
log, same daemonization) and additionally hosts the MCP HTTP/SSE server on
--port in the same process, so LLM agents and the CLI drive the SAME live
browser. Stop the daemon with 'web-cli serve stop'.

The MCP server has no authentication, so it binds loopback only by default.
Pass --bind 0.0.0.0 to expose it to the network.

  --port <n>   HTTP port for the MCP/SSE server (default 4099)
  --bind <h>   host/interface to bind (default 127.0.0.1)`,
		Example: `  web-cli serve mcp                 # daemonize + host MCP/SSE on http://127.0.0.1:4099
  web-cli serve mcp --port 4089     # pick a different port
  web-cli serve mcp --bind 0.0.0.0  # expose MCP to the network (no auth)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags, err := serveFlagsFromCmd(cmd)
			if err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			path, err := socketPath(cmd)
			if err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			addr, err := mcpServer.ListenAddress(bind, port)
			if err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			opts := serveRunOptions{
				headless: flags.headless,
				stealth:  flags.stealth,
				bin:      flags.bin,
				profile:  flags.profile,
				socket:   path,
				mcpPort:  port,
				mcpAddr:  addr,
			}
			if flags.foreground {
				return runServeForeground(cmdCtx(cmd), opts)
			}
			childArgs := []string{
				"serve", "mcp",
				"--foreground",
				"--port=" + strconv.Itoa(port),
				"--bind=" + bind,
				"--headless=" + strconv.FormatBool(flags.headless),
				"--stealth=" + strconv.FormatBool(flags.stealth),
				"--bin=" + flags.bin,
				"--profile=" + flags.profile,
				"--socket=" + path,
			}
			if err := daemonizeServe(cmd, opts, childArgs); err != nil {
				return fmt.Errorf("serve mcp: %w", err)
			}
			return nil
		},
	}
	mcpCmd.Flags().IntVar(&port, "port", defaultServeMcpPort,
		fmt.Sprintf("HTTP port for the MCP/SSE server (default %d)", defaultServeMcpPort))
	mcpCmd.Flags().StringVar(&bind, "bind", mcpServer.DefaultBind,
		"host/interface for the MCP/SSE server (default 127.0.0.1; 0.0.0.0 exposes it to the network)")
	return mcpCmd
}

// defaultServeMcpPort is the default HTTP port for the in-process MCP/SSE
// server hosted by `serve mcp`; the --port flag default and the plain-`serve`
// one-line pointer both reference it so they can never drift.
const defaultServeMcpPort = 4099

// mcpServeHint returns the one-line pointer printed by plain `serve` (which
// does not host the MCP endpoint), steering the user to `serve mcp` — the
// full client-config snippet would be misleading there.
func mcpServeHint() string {
	return fmt.Sprintf("host the MCP SSE endpoint for LLM agents: web-cli serve mcp --port %d", defaultServeMcpPort)
}

// mcpEndpointAddress returns the address to advertise for the MCP endpoint: the
// listener's real bound address when one exists, else the requested bind (the
// detached-daemon parent never binds, so it advertises what its child binds).
func mcpEndpointAddress(ln net.Listener, requested string) string {
	if ln != nil {
		return ln.Addr().String()
	}
	return requested
}

// mcpBanner renders the one-line MCP endpoint banner for a resolved address.
func mcpBanner(addr string) string {
	return fmt.Sprintf("mcp: http://%s", addr)
}

// mcpProbeAddress rewrites a wildcard bind to a loopback address of the same
// family so the daemon readiness probe can dial it.
func mcpProbeAddress(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0":
		host = mcpServer.DefaultBind
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}

// mcpClientConfig renders the MCP client config instructions for the SSE
// endpoint on addr: a one-line explanation followed by the JSON snippet to add
// to an MCP client config (Claude Desktop, mem-mcp, ...). With the default
// loopback bind the snippet stays byte-identical to the README "## MCP" section
// (key "web-mcp", type remote, url http://127.0.0.1:<port>/sse, enabled true)
// so the CLI and the docs never diverge.
func mcpClientConfig(addr string) string {
	return fmt.Sprintf(`Add to your MCP client config, e.g. Claude Desktop / mem-mcp:

"web-mcp": {
    "type": "remote",
    "url": "http://%s/sse",
    "enabled": true
}`, addr)
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
	if serveLaunchesDefaultBrowser(opts) {
		cmd.Printf("browser_id: %s\n", singleBrowserID)
	} else {
		cmd.Printf("browser_id: none (created on demand by browser_create or fetch_page)\n")
	}
	cmd.Printf("socket: %s\n", opts.socket)
	cmd.Printf("daemonized (log: %s); stop with 'web-cli serve stop'\n", logPath)
	if opts.mcpPort != 0 {
		// Probe the daemon's MCP endpoint before advertising it and the
		// client-config snippet: the daemon may have failed to bind (e.g. port
		// already occupied), in which case a URL or snippet would point the
		// user at a dead endpoint. A short dial is enough — the bind already
		// happened (or failed) synchronously before the daemon answered ping.
		addr := mcpEndpointAddress(nil, opts.mcpAddr)
		conn, err := net.DialTimeout("tcp", mcpProbeAddress(addr), 300*time.Millisecond)
		if err != nil {
			cmd.Printf("mcp: http server did not start on %s (see log)\n", addr)
		} else {
			_ = conn.Close()
			cmd.Println(mcpBanner(addr))
			if warning := mcpServer.NoAuthWarning(addr); warning != "" {
				cmd.Println(warning)
			}
			cmd.Printf("%s\n", mcpClientConfig(addr))
		}
	} else {
		cmd.Printf("%s\n", mcpServeHint())
	}
	return nil
}

// serveLaunchesDefaultBrowser reports whether the serve run should create the
// persistent default browser up front. Only the plain human daemon does; a
// `serve mcp` run (opts.mcpPort != 0) starts no browser, headless or otherwise.
func serveLaunchesDefaultBrowser(opts serveRunOptions) bool {
	return opts.mcpPort == 0
}

// serveLogger builds the manager's production logger for one serve run. It
// always appends structured lines to the serve log file and mirrors them to
// stderr unless stderr already IS that file, which is the daemon child the
// spawner points at the log. A log file that cannot be opened degrades to
// stderr so serve still starts. The returned cleanup closes the log file and
// must run after the manager's Shutdown so the shutdown reason is recorded.
func serveLogger() (*slog.Logger, func()) {
	fallback := browsers.NewLogger(os.Stderr, nil)
	path, err := serveLogPath()
	if err != nil {
		return fallback, func() {}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fallback, func() {}
	}
	console := os.Stderr
	if sameFile(os.Stderr, file) {
		console = nil
	}
	return browsers.NewLogger(file, console), func() { _ = file.Close() }
}

// sameFile reports whether both handles refer to the same underlying file, so a
// daemon child whose stderr is already the serve log is not written twice.
func sameFile(a, b *os.File) bool {
	ai, aerr := a.Stat()
	bi, berr := b.Stat()
	if aerr != nil || berr != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// runServeForeground runs the long-lived serve loop in this process: it binds
// the IPC socket, optionally starts the MCP HTTP server on opts.mcpPort
// in-process, opens the persistent default browser for a plain `serve`, and
// blocks until ctx is cancelled (SIGINT/SIGTERM) or a server.shutdown request
// arrives — then the loop stops and the process tears down via mgr.Shutdown.
// A `serve mcp` run starts no browser at all: its agent callers create their
// own on demand.
func runServeForeground(ctx context.Context, opts serveRunOptions) error {
	// serve mcp drives an agent pool, so its default browser is ephemeral: a
	// forgotten close must not leak a profile into the config dir. A named
	// --profile, or the plain human daemon, pins a persistent profile instead.
	agentPool := opts.mcpPort != 0
	logger, closeLog := serveLogger()
	defer closeLog()
	managerOpts := []browsers.Option{
		browsers.WithHeadless(opts.headless),
		browsers.WithStealth(opts.stealth),
		browsers.WithBin(opts.bin),
		browsers.WithPersistentDefault(!agentPool),
		browsers.WithLogger(logger),
	}
	if profileDir, err := config.ResolveProfilePath(opts.profile); err != nil {
		return fmt.Errorf("serve: resolve profile path: %w", err)
	} else if opts.profile != "" || !agentPool {
		managerOpts = append(managerOpts, browsers.WithProfileDir(profileDir))
	}
	browserTTL := browsers.DefaultBrowserTTL
	if cfg, err := config.LoadConfig(); err == nil {
		browserTTL = cfg.BrowserIdleTTL(browserTTL)
	}
	managerOpts = append(managerOpts, browsers.WithBrowserTTL(browserTTL))
	mgr := browsers.NewManager(managerOpts...)
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
	// browser launch) so a leak can't leave a stale socket file/listener behind.
	// Close is idempotent, so the normal path is unaffected.
	defer srv.Close()

	// serveCtx is cancelled when the IPC serve loop exits, so a serve-mcp run's
	// HTTP server stops (gracefully drained) right after the loop does.
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()

	// serve mcp: host the MCP HTTP/SSE server in-process on the SAME manager,
	// so MCP tools and the IPC layer share one browser map. The port is
	// bound synchronously here — BEFORE the HTTP goroutine and before any
	// browser work — so a bind failure (e.g. port already occupied) surfaces
	// as a warning in the output block instead of an async log.
	var httpSrv *mcpServer.Server
	var mcpLn net.Listener
	var mcpBindErr error
	if opts.mcpPort != 0 {
		ln, err := net.Listen("tcp", opts.mcpAddr)
		if err != nil {
			// Keep httpSrv nil: no HTTP goroutine starts, and the output
			// block prints a warning instead of the client-config snippet.
			mcpBindErr = err
		} else {
			mcpLn = ln
			httpSrv = mcpServer.New(mgr, opts.mcpAddr)
			httpSrv.SetListener(ln)
			go func() {
				if err := httpSrv.Serve(serveCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("serve mcp: http server: %v", err)
				}
			}()
		}
	}

	// First run downloads Chromium (~100MB); serveCtx bounds the launch. Only
	// the plain human daemon pre-launches browser-1: `serve mcp` serves an
	// agent pool whose callers create their own browsers (browser_create, or
	// the first fetch_page), so launching one here would spend memory on a
	// browser nobody asked for.
	launchDefault := serveLaunchesDefaultBrowser(opts)
	if launchDefault {
		if _, err := mgr.Open(serveCtx); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
	}

	if launchDefault {
		fmt.Printf("browser_id: %s\n", singleBrowserID)
	} else {
		fmt.Printf("browser_id: none (created on demand by browser_create or fetch_page)\n")
	}
	fmt.Printf("socket: %s\n", path)
	mcpAddr := mcpEndpointAddress(mcpLn, opts.mcpAddr)
	if mcpBindErr != nil {
		fmt.Printf("mcp: failed to bind %s: %v\n", opts.mcpAddr, mcpBindErr)
	} else if httpSrv != nil {
		fmt.Println(mcpBanner(mcpAddr))
		if warning := mcpServer.NoAuthWarning(mcpAddr); warning != "" {
			fmt.Println(warning)
		}
	}
	fmt.Printf("press Ctrl-C to stop\n")
	if httpSrv != nil {
		fmt.Printf("%s\n", mcpClientConfig(mcpAddr))
	} else if mcpBindErr == nil {
		fmt.Printf("%s\n", mcpServeHint())
	}

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
