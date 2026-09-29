//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stageFixture(t *testing.T) (string, string, string) {
	t.Helper()
	parent := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gz)
	data := []byte("fixture program")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "x-ui/x-ui", Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	tarWriter.Write(data)
	tarWriter.Close()
	gz.Close()
	archive := filepath.Join(parent, "release.tar.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return parent, archive, fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
}

func TestRunStagesCompleteArchive(t *testing.T) {
	parent, archive, sum := stageFixture(t)
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--archive", archive, "--sha256", sum, "--parent", parent}, &out); err != nil {
		t.Fatal(err)
	}
	stage := strings.TrimSuffix(out.String(), "\n")
	if filepath.Dir(stage) != parent || strings.Contains(stage, "\n") {
		t.Fatalf("invalid output: %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(stage, "x-ui", "x-ui"))
	if err != nil || string(data) != "fixture program" {
		t.Fatalf("invalid staged program: %q, %v", data, err)
	}
}

func TestRunRejectsInvalidArguments(t *testing.T) {
	parent, archive, sum := stageFixture(t)
	symlink := filepath.Join(t.TempDir(), "archive-link")
	if err := os.Symlink(archive, symlink); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"no-flags":        {},
		"no-parent":       {"--archive", archive, "--sha256", sum},
		"no-checksum":     {"--archive", archive, "--parent", parent},
		"no-archive":      {"--sha256", sum, "--parent", parent},
		"extra-arg":       {"--archive", archive, "--sha256", sum, "--parent", parent, "unexpected"},
		"unknown-flag":    {"--unknown"},
		"missing-archive": {"--archive", filepath.Join(parent, "missing"), "--sha256", sum, "--parent", parent},
		"directory-input": {"--archive", parent, "--sha256", sum, "--parent", parent},
		"symlink-input":   {"--archive", symlink, "--sha256", sum, "--parent", parent},
		"bad-checksum":    {"--archive", archive, "--sha256", strings.Repeat("0", 64), "--parent", parent},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(t.Context(), args, &out); err == nil || out.Len() != 0 {
				t.Fatalf("invalid arguments published stage: %q, %v", out.String(), err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 {
				t.Fatalf("partial output retained: %v, %v", entries, err)
			}
		})
	}
}

type badOutput struct{ err error }

func (w badOutput) Write([]byte) (int, error) { return 0, w.err }

func TestRunCleansStageWhenOutputFails(t *testing.T) {
	parent, archive, sum := stageFixture(t)
	want := errors.New("fixture output failure")
	err := run(t.Context(), []string{"--archive", archive, "--sha256", sum, "--parent", parent}, badOutput{want})
	if !errors.Is(err, want) {
		t.Fatalf("lost output failure: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unpublished stage retained: %v, %v", entries, err)
	}
}

func TestRunRejectsMissingParent(t *testing.T) {
	parent, archive, sum := stageFixture(t)
	ctx := t.Context()
	args := []string{"--archive", archive, "--sha256", sum, "--parent", filepath.Join(parent, "missing")}
	if err := run(ctx, args, io.Discard); err == nil {
		t.Fatal("nonexistent parent accepted")
	}
}

func TestRunRejectsCanceledContext(t *testing.T) {
	parent, archive, sum := stageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := run(ctx, []string{"--archive", archive, "--sha256", sum, "--parent", parent}, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("canceled stage retained: %v, %v", entries, err)
	}
}
