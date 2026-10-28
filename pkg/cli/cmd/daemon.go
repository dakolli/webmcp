package cmd

// Daemon-spawn machinery behind `web-cli serve` and `web-cli serve mcp`: it
// re-invokes the same executable detached (setsid, stdio redirected to the
// serve log) and polls ping until the daemon answers (WEB-42). The stop side
// lives in serve.go (`serve stop` → the server.shutdown IPC message).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dakolli/webmcp/pkg/cli/ipc"
)

// servePollInterval is how often the spawner re-probes ping while waiting for
// the daemonized serve process to come up.
const servePollInterval = 200 * time.Millisecond

// servePingTimeout bounds the spawner's wait for the daemonized serve process
// to answer ping. First-run Chromium downloads can exceed this; the log file
// (serveLogPath) carries the progress, and a retried serve picks up the
// already-running server.
const servePingTimeout = 10 * time.Second

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

// spawnServeDaemon starts the serve loop (the given web-cli args, e.g.
// ["serve", "--foreground", ...]) as a detached background process: setsid so
// it is re-parented to PID 1 when this command exits (and reaped there — no
// zombie, and the parent's Ctrl-C cannot reach it), stdio redirected to
// serveLogPath, and intentionally no proc.Wait — the daemon outlives this
// command. It returns the log path the daemon writes to.
func spawnServeDaemon(args []string) (string, error) {
	// os.Executable resolves the running binary, so 'go run', a built binary
	// and an installed one all spawn the right process.
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	logPath, err := serveLogPath()
	if err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logFile.Close()

	proc := exec.Command(exe, args...)
	proc.Stdin = nil // /dev/null
	proc.Stdout = logFile
	proc.Stderr = logFile
	// detachProcess puts the child in its own session on unix and in a
	// detached process on Windows, so it is re-parented instead of dying with
	// this command and Ctrl-C on the parent cannot reach it.
	detachProcess(proc)
	if err := proc.Start(); err != nil {
		return "", fmt.Errorf("start serve: %w", err)
	}
	// Intentionally no proc.Wait: the daemon outlives this command.
	return logPath, nil
}

// waitForServeDaemon polls ping until the daemon at path answers, bounded by
// servePingTimeout. logPath is included in the timeout error so the user knows
// where the daemon's progress is written.
func waitForServeDaemon(ctx context.Context, path, logPath string) error {
	deadline := time.Now().Add(servePingTimeout)
	for time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		cl, derr := ipc.Dial(path)
		ok := derr == nil
		if ok {
			ok = cl.Ping(pollCtx) == nil
			_ = cl.Close()
		}
		cancel()
		if ok {
			return nil
		}
		time.Sleep(servePollInterval)
	}
	return fmt.Errorf("serve process did not answer ping within %s (first-run Chromium download may exceed this; retry, and see %s)",
		servePingTimeout, logPath)
}
