package updatebundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzStageArchive(f *testing.F) {
	var seed bytes.Buffer
	w := tar.NewWriter(&seed)
	if err := w.WriteHeader(&tar.Header{Name: "x-ui/x-ui", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1}); err != nil {
		f.Fatal(err)
	}
	w.Write([]byte("x"))
	w.Close()
	f.Add(seed.Bytes())
	f.Add([]byte{})
	f.Add([]byte(strings.Repeat("\x00", 1024)))
	f.Fuzz(func(t *testing.T, plain []byte) {
		if len(plain) > 1<<20 {
			return
		}
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		gz.Write(plain)
		gz.Close()
		parent := preservedParent(t)
		limits := archiveLimits{compressed: 2 << 20, expanded: 2 << 20, file: 1 << 20, total: 1 << 20, members: 64}
		stage, err := stageWithLimits(t.Context(), bytes.NewReader(compressed.Bytes()), parent, digest(compressed.Bytes()), limits)
		if err != nil && stage != "" {
			t.Fatalf("failed validation published output: %q, %v", stage, err)
		}
		if err == nil {
			if filepath.Dir(stage) != parent || !strings.HasPrefix(filepath.Base(stage), ".x-ui-stage-") {
				t.Fatalf("unowned output: %q", stage)
			}
			if err := filepath.WalkDir(stage, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				rel, err := filepath.Rel(stage, path)
				if err != nil || (rel != "." && rel != "x-ui" && !strings.HasPrefix(rel, "x-ui"+string(filepath.Separator))) {
					return fmt.Errorf("unexpected output: %s, %w", path, err)
				}
				if !entry.IsDir() && !entry.Type().IsRegular() {
					return fmt.Errorf("special file created: %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(stage); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"x-ui", "x-ui.service", "x-ui.db", "outside"} {
			info, err := os.Lstat(filepath.Join(parent, name))
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("existing path replaced: %s, %v", name, err)
			}
		}
		requirePreserved(t, parent)
	})
}
