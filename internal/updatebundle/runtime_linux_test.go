//go:build linux

package updatebundle

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeCandidateFixture(t *testing.T, script string) (string, ReleaseIdentity, string) {
	t.Helper()
	directory, identity := manifestFixture(t, "linux-arm64")
	marker := filepath.Join(t.TempDir(), "candidate-marker")
	t.Setenv("XUI_CANDIDATE_FIXTURE_MARKER", marker)
	if err := os.WriteFile(filepath.Join(directory, "x-ui"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(t.Context(), directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	return directory, identity, marker
}

func TestPreflightReleaseVerifiesInventoryBeforeExecuting(t *testing.T) {
	for _, mode := range []string{"valid", "source", "changed-script", "canceled", "exit-error"} {
		t.Run(mode, func(t *testing.T) {
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$XUI_CANDIDATE_FIXTURE_MARKER\"\n"
			if mode == "exit-error" {
				script += "exit 29\n"
			}
			directory, identity, marker := runtimeCandidateFixture(t, script)
			if mode == "source" {
				identity.Commit = strings.Repeat("b", 40)
			}
			if mode == "changed-script" {
				if err := os.WriteFile(filepath.Join(directory, "update.sh"), []byte("changed"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			err := PreflightRelease(ctx, directory, identity, io.Discard)
			if mode == "valid" || mode == "exit-error" {
				data, readErr := os.ReadFile(marker)
				want := "verify-release\n--directory\n" + directory + "\n--commit\n" + identity.Commit + "\n--tag\n" + identity.Tag + "\n--platform\n" + identity.Platform + "\n"
				if readErr != nil || string(data) != want || (err == nil) != (mode == "valid") {
					t.Fatalf("candidate execution: %q %v %v", data, readErr, err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid preflight accepted")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("unverified candidate executed: %v", err)
				}
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestPreflightReleaseCancellationAllowsCandidateCleanup(t *testing.T) {
	directory, identity, marker := runtimeCandidateFixture(t, `#!/bin/sh
trap 'printf stopped >"$XUI_CANDIDATE_FIXTURE_MARKER"; exit 0' TERM
printf started >"$XUI_CANDIDATE_FIXTURE_MARKER"
while :; do sleep 0.02; done
`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- PreflightRelease(ctx, directory, identity, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Error("candidate cancellation did not finish")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(marker); err == nil && string(data) == "started" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("candidate never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("candidate did not exit after cancellation")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "stopped" {
		t.Fatalf("candidate cleanup did not run: %q %v", data, err)
	}
}
