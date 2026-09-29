//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type releaseProbeObservation struct {
	PID     int    `json:"pid"`
	Address string `json:"address"`
}

func TestReleaseProbeCoreHelper(t *testing.T) {
	if os.Getenv("XUI_RELEASE_CORE_HELPER") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "-version" {
		fmt.Println("Xray 26.9.9 owned handshake barrier")
		return
	}
	data, err := os.ReadFile(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || len(cfg.Inbounds) != 1 || cfg.Inbounds[0].Listen != "127.0.0.1" {
		t.Fatalf("unexpected owned probe configuration: %v", err)
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", cfg.Inbounds[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		var first [1]byte
		if _, err := io.ReadFull(conn, first[:]); err != nil {
			conn.Close()
			continue
		}
		observation, err := json.Marshal(releaseProbeObservation{PID: os.Getpid(), Address: listener.Addr().String()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("XUI_RELEASE_PROBE_READY"), observation, 0o600); err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, conn)
		conn.Close()
	}
}

func TestVerifyReleaseCLITerminationCleansProbe(t *testing.T) {
	panel := os.Getenv("XUI_E2E_PANEL")
	if panel == "" {
		t.Skip("set XUI_E2E_PANEL to a stamped or unmodified panel binary")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	control := t.TempDir()
	core := filepath.Join(control, "core-barrier")
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	script := "#!/bin/sh\nexec " + quoted + " -test.run '^TestReleaseProbeCoreHelper$' -- \"$@\"\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, identity := releaseRuntimeFixture(t, panel, core)
	temporary := t.TempDir()
	ready := filepath.Join(control, "handshake.json")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "x-ui"), "verify-release", "--directory", dir, "--commit", identity.Commit, "--tag", identity.Tag, "--platform", identity.Platform)
	cmd.Env = append(os.Environ(), "TMPDIR="+temporary, "XUI_RELEASE_CORE_HELPER=1", "XUI_RELEASE_PROBE_READY="+ready)
	output, err := os.Create(filepath.Join(control, "panel.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	var observed releaseProbeObservation
	deadline := time.Now().Add(8 * time.Second)
	for observed.PID == 0 && time.Now().Before(deadline) {
		if data, err := os.ReadFile(ready); err == nil {
			_ = json.Unmarshal(data, &observed)
		}
		if observed.PID == 0 {
			select {
			case err := <-done:
				data, _ := os.ReadFile(output.Name())
				t.Fatalf("probe exited before handshake barrier: %v: %s", err, data)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	if observed.PID == 0 || observed.Address == "" {
		t.Fatal("did not observe an actual pending core handshake")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled verification reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not cancel pending verification within two seconds")
	}
	if err := syscall.Kill(observed.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("owned probe process %d remains: %v", observed.PID, err)
	}
	listener, err := net.Listen("tcp4", observed.Address)
	if err != nil {
		t.Fatalf("owned probe listener was not released: %v", err)
	}
	listener.Close()
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe temporary files remain after cancellation: %v, %v", entries, err)
	}
}
