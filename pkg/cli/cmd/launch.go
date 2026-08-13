package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"webmcp/pkg/cli/ipc"
)

// browserID is the id reported for the manager's single Browser. The Manager
// owns one Browser and does not expose an id, so this mirrors the MCP
// single-browser id (pkg/mcp/tools/browser.go singleBrowserID).
const browserID = "browser-1"

// launchPollInterval is how often launch re-probes ping while waiting for the
// daemonized serve process to come up.
const launchPollInterval = 200 * time.Millisecond

// launchPingTimeout bounds launch's wait for the daemonized serve process to
// answer ping. First-run Chromium downloads can exceed this; the log file
// (serveLogPath) carries the progress, and a retried launch picks up the
// already-running server.
const launchPingTimeout = 10 * time.Second

// serveLogPath returns where the daemonized serve process's stdout/stderr are
// logged: $XDG_STATE_HOME/web-cli/serve.log, else ~/.web-cli/serve.log. The
// directory is created if missing.
func serveLogPath() (string, error) {
	var dir string
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		dir = filepath.Join(state, "web-cli")
	} else {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("resolve log dir (no XDG_STATE_HOME and no home dir): %w", err)
		}
		dir = filepath.Join(home, ".web-cli")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create log dir %s: %w", dir, err)
	}
	return filepath.Join(dir, "serve.log"), nil
}

// NewLaunchCmd constructs the "launch" command: it spawns the long-lived serve
// process (`web-cli serve --headless <h> --stealth <s> --socket <path>`)
// detached in the background (setsid, stdio redirected to serveLogPath), polls
// ping until the daemon answers (bounded), then prints its browser_id and
// returns immediately — preserving today's fast-return contract so
// `web-cli launch && web-cli fetch <url>` and existing scripts keep working.
// The --headless/--stealth flags are passed to the daemonized serve invocation
// (Manager options are construction-time; browser.open's headless/stealth
// params are accepted but inert — see WEB-30's handler note). When a server is
// already running at the socket, launch reuses it. Stop the daemon with
// 'web-cli browser close' or by killing the serve process.
func NewLaunchCmd() *cobra.Command {
	var headless, stealth bool

	launchCmd := &cobra.Command{
		Use:   "launch",
		Short: "Launch the shared browser as a persistent background server",
		Long: "Launch starts the shared browser as a persistent background serve " +
			"process: it spawns 'web-cli serve --headless <h> --stealth <s> --socket " +
			"<path>' detached (setsid, logged to $XDG_STATE_HOME/web-cli/serve.log, " +
			"else ~/.web-cli/serve.log), polls until the server answers ping " +
			"(bounded to 10s), then prints browser_id and returns. The browser " +
			"and its pages persist across invocations: subsequent web-cli commands " +
			"connect over IPC. Stop it with 'web-cli browser close'.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A serve process may already be running at the socket (a prior
			// launch, or 'web-cli serve' in another terminal): reuse it.
			if cl := dialServe(cmd); cl != nil {
				_ = cl.Close()
				cmd.Printf("browser_id: %s\n", browserID)
				return nil
			}

			path, err := socketPath(cmd)
			if err != nil {
				return fmt.Errorf("launch: %w", err)
			}
			// os.Executable resolves the running binary, so 'go run', a built
			// binary and an installed one all spawn the right process.
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("launch: resolve executable: %w", err)
			}
			logPath, err := serveLogPath()
			if err != nil {
				return fmt.Errorf("launch: %w", err)
			}
			logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return fmt.Errorf("launch: open log %s: %w", logPath, err)
			}
			defer logFile.Close()

			proc := exec.Command(exe, "serve",
				"--headless", strconv.FormatBool(headless),
				"--stealth", strconv.FormatBool(stealth),
				"--socket", path,
			)
			proc.Stdin = nil // /dev/null
			proc.Stdout = logFile
			proc.Stderr = logFile
			// Setsid detaches the child into its own session: it is re-parented
			// to PID 1 when this command exits (and reaped there), so no zombie
			// is left behind and Ctrl-C on the parent cannot reach it.
			proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := proc.Start(); err != nil {
				return fmt.Errorf("launch: start serve: %w", err)
			}
			// Intentionally no proc.Wait: the daemon outlives this command.

			// Poll ping until the daemon answers, bounded by launchPingTimeout.
			deadline := time.Now().Add(launchPingTimeout)
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(cmdCtx(cmd), pingTimeout)
				cl, derr := ipc.Dial(path)
				ok := derr == nil
				if ok {
					ok = cl.Ping(ctx) == nil
					_ = cl.Close()
				}
				cancel()
				if ok {
					cmd.Printf("browser_id: %s\n", browserID)
					return nil
				}
				time.Sleep(launchPollInterval)
			}
			return fmt.Errorf("launch: serve process did not answer ping within %s (first-run Chromium download may exceed this; retry launch, and see %s)", launchPingTimeout, logPath)
		},
	}

	launchCmd.Flags().BoolVar(&headless, "headless", false,
		"run the browser headless instead of headful (passed to the daemonized serve process)")
	launchCmd.Flags().BoolVar(&stealth, "stealth", true,
		"apply go-rod/stealth to every page (passed to the daemonized serve process)")

	return launchCmd
}
