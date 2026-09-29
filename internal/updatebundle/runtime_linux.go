//go:build linux

package updatebundle

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// PreflightRelease verifies the inventory and runs the candidate in a subprocess.
// Its standalone command owns its environment, probe core and temporary files.
func PreflightRelease(ctx context.Context, directory string, identity ReleaseIdentity, diagnostics io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if _, err := VerifyManifest(ctx, directory, identity); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(directory, "x-ui"), "verify-release",
		"--directory", directory, "--commit", identity.Commit, "--tag", identity.Tag, "--platform", identity.Platform)
	cmd.Dir = directory
	cmd.Stderr = diagnostics
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
