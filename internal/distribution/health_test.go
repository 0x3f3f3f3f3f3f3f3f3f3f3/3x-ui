package distribution

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeHealthChild(t *testing.T) {
	if os.Getenv("XUI_HEALTH_TEST_CHILD") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestRuntimeHealthBindsLiveInstalledProcesses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process identity")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	start := func(name string) (*exec.Cmd, string) {
		t.Helper()
		own, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(own)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		destination, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(destination, source)
		closeErr := destination.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal(copyErr, closeErr)
		}
		child := exec.Command(name, "-test.run=^TestRuntimeHealthChild$")
		child.Env = append(os.Environ(), "XUI_HEALTH_TEST_CHILD=1")
		pipe, err := child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pipe.Close(); _ = child.Process.Kill(); _ = child.Wait() })
		ticks, err := processIdentity(child.Process.Pid, name)
		if err != nil {
			t.Fatal(err)
		}
		return child, ticks
	}
	panel, panelTicks := start(filepath.Join(root, "x-ui"))
	core, coreTicks := start(filepath.Join(root, "bin", CoreBinaryName(CurrentTarget().OS, CurrentTarget().Arch)))
	file := filepath.Join(t.TempDir(), HealthName)
	source := strings.Repeat("a", 40)
	healthy := RuntimeHealth{FormatVersion: 1, SourceRevision: source, InstallationRoot: root, ObservedAt: time.Now().UnixMilli(), Ready: true, PanelPID: panel.Process.Pid, PanelStart: panelTicks, CorePID: core.Process.Pid, CoreStart: coreTicks}
	if err := writeHealth(file, &healthy); err != nil {
		t.Fatal(err)
	}
	if err := checkHealthRecord(context.Background(), root, file, source); err != nil {
		t.Fatal("live matching pair rejected", err)
	}
	for _, mode := range []string{"missing", "stale", "future", "unready", "foreign-source", "foreign-root", "reused-panel", "reused-core", "wrong-core-executable", "linked", "public"} {
		t.Run(mode, func(t *testing.T) {
			r := healthy
			r.ObservedAt = time.Now().UnixMilli()
			switch mode {
			case "stale":
				r.ObservedAt = time.Now().Add(-time.Minute).UnixMilli()
			case "future":
				r.ObservedAt = time.Now().Add(time.Minute).UnixMilli()
			case "unready":
				r.Ready = false
			case "foreign-source":
				r.SourceRevision = strings.Repeat("b", 40)
			case "foreign-root":
				r.InstallationRoot = t.TempDir()
			case "reused-panel":
				r.PanelStart = "invalid"
			case "reused-core":
				r.CoreStart = "invalid"
			case "wrong-core-executable":
				r.CorePID = r.PanelPID
				r.CoreStart = r.PanelStart
			}
			if err := writeHealth(file, &r); err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "linked" {
				if err := os.Rename(file, file+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".saved", file); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "public" {
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkHealthRecord(context.Background(), root, file, source); err == nil {
				t.Fatal("accepted", mode)
			}
			_ = os.Remove(file)
		})
	}
	if err := core.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = core.Wait()
	healthy.ObservedAt = time.Now().UnixMilli()
	if err := writeHealth(file, &healthy); err != nil {
		t.Fatal(err)
	}
	if err := checkHealthRecord(context.Background(), root, file, source); err == nil {
		t.Fatal("accepted dead core behind live panel")
	}
}
