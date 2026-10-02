package distribution

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunLocked holds a separate kernel lock across the complete installer child,
// including service stop, recovery, promotion, configuration and activation.
// Promotion's shorter lock remains usable by the child's package commands.
func RunLocked(ctx context.Context, installed string, args []string, output io.Writer) error {
	installed, err := filepath.Abs(installed)
	if err != nil {
		return err
	}
	file, err := lockPromotionFile(installed + ".lifecycle")
	if err != nil {
		return err
	}
	defer file.Close()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// The inherited descriptor keeps kernel ownership if this parent is killed
	// while its installer child still modifies the installation.
	cmd.ExtraFiles = []*os.File{file}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "XUI_PAIRED_LIFECYCLE_LOCK=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "XUI_PAIRED_LIFECYCLE_LOCK="+installed)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, output, os.Stderr
	configureLifecycleCommand(cmd)
	err = cmd.Run()
	if ctx.Err() != nil {
		killLifecycleCommand(cmd)
	}
	return err
}
