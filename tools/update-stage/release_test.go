//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func releaseStageFixture(t *testing.T) (string, string, string, updatebundle.ReleaseIdentity) {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := updatebundle.ReleaseIdentity{Repository: updatebundle.ReleaseRepository, Commit: strings.Repeat("b", 40), Tag: "dev-latest", Platform: "linux-arm64"}
	for _, name := range []string{"x-ui", "update-stage", "install.sh", "update.sh", "x-ui.sh", "x-ui.rc", "x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel", "bin/xray-linux-arm64"} {
		path := filepath.Join(source, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		mode := os.FileMode(0o755)
		if strings.Contains(name, ".service.") {
			mode = 0o644
		}
		if err := os.WriteFile(path, []byte("fixture:"+name), mode); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := updatebundle.BuildManifest(t.Context(), source, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := updatebundle.WriteManifest(source, manifest); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	err = filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if err := w.WriteHeader(&tar.Header{Name: "x-ui/" + filepath.ToSlash(rel), Mode: int64(info.Mode().Perm()), Size: info.Size()}); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	gz.Close()
	archive := filepath.Join(parent, "release.tar.gz")
	os.WriteFile(archive, buf.Bytes(), 0o600)
	return parent, archive, fmt.Sprintf("%x", sha256.Sum256(buf.Bytes())), identity
}

func TestRunVerifiesSelectedReleaseBeforePublishingStage(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			parent, archive, sum, identity := releaseStageFixture(t)
			if mismatch {
				identity.Commit = strings.Repeat("c", 40)
			}
			args := []string{"--archive", archive, "--sha256", sum, "--parent", parent, "--release-commit", identity.Commit, "--release-tag", identity.Tag, "--release-platform", identity.Platform}
			var out bytes.Buffer
			err := run(t.Context(), args, &out)
			if mismatch {
				if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "identity") {
					t.Fatalf("mismatched identity published: %q, %v", out.String(), err)
				}
				stages, _ := filepath.Glob(filepath.Join(parent, ".x-ui-stage-*"))
				if len(stages) != 0 {
					t.Fatalf("invalid stage retained: %v", stages)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := updatebundle.VerifyManifest(t.Context(), filepath.Join(strings.TrimSpace(out.String()), "x-ui"), identity); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunReleaseRequiresAllIdentityFieldsAndManifest(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			parent, archive, sum := stageFixture(t)
			args := []string{"--archive", archive, "--sha256", sum, "--parent", parent, "--release-commit", strings.Repeat("b", 40)}
			if complete {
				args = append(args, "--release-tag", "dev-latest", "--release-platform", "linux-arm64")
			}
			var out bytes.Buffer
			if err := run(t.Context(), args, &out); err == nil || out.Len() != 0 {
				t.Fatalf("incomplete release accepted: %q, %v", out.String(), err)
			}
			stages, _ := filepath.Glob(filepath.Join(parent, ".x-ui-stage-*"))
			if len(stages) != 0 {
				t.Fatalf("invalid release retained: %v", stages)
			}
		})
	}
}
