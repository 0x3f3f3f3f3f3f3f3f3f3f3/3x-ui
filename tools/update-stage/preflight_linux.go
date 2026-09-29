//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func verifyCandidate(ctx context.Context, stage string, identity updatebundle.ReleaseIdentity) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	directory := filepath.Join(stage, "x-ui")
	cmd := exec.CommandContext(ctx, filepath.Join(directory, "x-ui"), "verify-release",
		"--directory", directory, "--commit", identity.Commit, "--tag", identity.Tag, "--platform", identity.Platform)
	cmd.Dir = directory
	cmd.Stderr = os.Stderr
	// Give the candidate time to stop and reap its owned core before force-kill.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("candidate runtime preflight failed: %w", err)
	}
	return ctx.Err()
}
