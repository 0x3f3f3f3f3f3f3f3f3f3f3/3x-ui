//go:build linux

package updatebundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func coreReplacementFixture(t *testing.T) (string, ReleaseIdentity, string) {
	t.Helper()
	directory, identity := manifestFixture(t, "linux-arm64")
	manifest, err := BuildManifest(t.Context(), directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "xray-linux-arm64")
	if err := os.WriteFile(target, []byte("previous working core"), 0o755); err != nil {
		t.Fatal(err)
	}
	return directory, identity, target
}

func TestCoreReplacementRestoresOriginalOrCommitsCandidate(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "commit"}[commit], func(t *testing.T) {
			directory, identity, target := coreReplacementFixture(t)
			replacement, err := PrepareCoreReplacement(t.Context(), directory, identity, target)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = replacement.Close() })
			assertCore := func(want string) {
				t.Helper()
				data, err := os.ReadFile(target)
				if err != nil || string(data) != want {
					t.Fatalf("core = %q, %v; want %q", data, err, want)
				}
			}
			assertCore("previous working core")
			if err := replacement.Activate(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertCore("fixture:bin/xray-linux-arm64")
			if err := replacement.Close(); err == nil {
				t.Fatal("discarded backup before the caller confirmed readiness or restored the core")
			}
			if commit {
				replacement.Commit()
			} else {
				if err := replacement.Restore(); err != nil {
					t.Fatal(err)
				}
				assertCore("previous working core")
			}
			if err := replacement.Close(); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Dir(target))
			if err != nil || len(entries) != 1 {
				t.Fatalf("replacement left temporary files: %v, %v", entries, err)
			}
		})
	}
}

func TestCoreReplacementRejectsUnsafePreparationWithoutChangingTarget(t *testing.T) {
	for _, name := range []string{"changed-release", "wrong-identity", "symlink", "directory", "not-executable", "canceled"} {
		t.Run(name, func(t *testing.T) {
			directory, identity, target := coreReplacementFixture(t)
			ctx := t.Context()
			switch name {
			case "changed-release":
				if err := os.WriteFile(filepath.Join(directory, "update.sh"), []byte("changed"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "wrong-identity":
				identity.Commit = strings.Repeat("b", 40)
			case "symlink", "directory":
				if err := os.Rename(target, target+".original"); err != nil {
					t.Fatal(err)
				}
				if name == "symlink" {
					if err := os.Symlink(target+".original", target); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			case "not-executable":
				if err := os.Chmod(target, 0o644); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := PrepareCoreReplacement(ctx, directory, identity, target)
			if err == nil || replacement != nil {
				t.Fatalf("unsafe preparation accepted: %v, %v", replacement, err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("failed preparation changed live target: %v", err)
			}
			entries, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".xray-update-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed preparation leaked backup: %v, %v", entries, err)
			}
		})
	}
}

func TestCoreReplacementRejectsConcurrentFileChangeAndCancellation(t *testing.T) {
	for _, name := range []string{"replaced", "edited", "canceled"} {
		t.Run(name, func(t *testing.T) {
			directory, identity, target := coreReplacementFixture(t)
			replacement, err := PrepareCoreReplacement(t.Context(), directory, identity, target)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := replacement.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := t.Context()
			want := "a concurrent operator changed the core"
			if name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = "previous working core"
			} else {
				if name == "replaced" {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(target, []byte(want), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := replacement.Activate(ctx); err == nil {
				t.Fatal("activated over a changed target or canceled operation")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != want {
				t.Fatalf("replaced concurrent target: %q, %v", data, err)
			}
		})
	}
}
