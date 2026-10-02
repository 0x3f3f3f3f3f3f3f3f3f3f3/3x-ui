//go:build !linux

package distribution

import "os/exec"

func configureLifecycleCommand(*exec.Cmd) {}
func killLifecycleCommand(*exec.Cmd)      {}
