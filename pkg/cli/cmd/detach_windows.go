//go:build windows

package cmd

import (
	"os/exec"
	"syscall"
)

// windowsDetachProcess is Windows DETACHED_PROCESS: the child gets no console.
const windowsDetachProcess = 0x00000008

// detachProcess starts the child detached from the parent's console and session.
func detachProcess(proc *exec.Cmd) {
	proc.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsDetachProcess}
}
