//go:build unix

package cmd

import (
	"os/exec"
	"syscall"
)

// detachProcess starts the child in its own session so it outlives the parent.
func detachProcess(proc *exec.Cmd) {
	proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
