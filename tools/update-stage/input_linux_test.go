//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunRejectsFIFO(t *testing.T) {
	parent := t.TempDir()
	pipe := filepath.Join(parent, "archive.fifo")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStageFIFOHelper$")
	cmd.Env = append(os.Environ(), "XUI_STAGE_FIFO_FIXTURE="+pipe)
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("archive open blocked on a FIFO before regular-file validation")
	}
	if err != nil {
		t.Fatalf("helper failed: %v: %s", err, out)
	}
}

func TestStageFIFOHelper(t *testing.T) {
	pipe := os.Getenv("XUI_STAGE_FIFO_FIXTURE")
	if pipe == "" {
		return
	}
	err := run(t.Context(), []string{"--archive", pipe, "--parent", filepath.Dir(pipe), "--sha256", strings.Repeat("0", 64)}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO was not rejected: %v", err)
	}
}
