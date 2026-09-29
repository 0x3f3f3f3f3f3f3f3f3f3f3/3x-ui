//go:build !linux

package xray

import "os/exec"

func prepareChildLifetime(_ *exec.Cmd) func() { return func() {} }
