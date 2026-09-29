package xray

import (
	"os/exec"
	"runtime"
	"syscall"
)

func prepareChildLifetime(cmd *exec.Cmd) func() {
	// Pdeathsig follows the creating OS thread, which must live until Wait.
	// Keep it private to this child: https://go.dev/issue/27505.
	runtime.LockOSThread()
	var attributes syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	attributes.Pdeathsig = syscall.SIGKILL
	cmd.SysProcAttr = &attributes
	return runtime.UnlockOSThread
}
