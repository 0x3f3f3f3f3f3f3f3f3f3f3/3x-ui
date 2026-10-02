package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testArchive(t *testing.T, headers []*tar.Header, bodies [][]byte) string {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(bodies[i]) > 0 {
			if _, err := tw.Write(bodies[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "package.tar.gz")
	if err := os.WriteFile(p, raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractArchiveCreatesPrivateStagingWithoutReplacingExistingTree(t *testing.T) {
	archive := testArchive(t, []*tar.Header{{Name: "x-ui/", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "x-ui/bin/", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "x-ui/bin/xray", Typeflag: tar.TypeReg, Mode: 0755, Size: 4}}, [][]byte{nil, nil, []byte("core")})
	dest := filepath.Join(t.TempDir(), "candidate")
	root, err := ExtractArchive(context.Background(), archive, dest)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(dest, "x-ui") {
		t.Fatalf("wrong root: %s", root)
	}
	b, err := os.ReadFile(filepath.Join(root, "bin/xray"))
	if err != nil || string(b) != "core" {
		t.Fatal("wrong staged content", err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("staging directory is not private", err)
	}
	if _, err := ExtractArchive(context.Background(), archive, dest); err == nil {
		t.Fatal("existing staging tree was replaced")
	}
	after, err := os.ReadFile(filepath.Join(root, "bin/xray"))
	if err != nil || string(b) != string(after) {
		t.Fatal("prior tree changed")
	}
}

func TestExtractArchiveRejectsTraversalLinksDuplicateEntriesAndPrivileges(t *testing.T) {
	for _, mode := range []string{"absolute", "escape", "unnormalized", "backslash", "wrong-root", "symlink", "hardlink", "fifo", "duplicate", "setuid", "non-directory-parent"} {
		t.Run(mode, func(t *testing.T) {
			h := &tar.Header{Name: "x-ui/payload", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}
			headers, bodies := []*tar.Header{h}, [][]byte{[]byte("x")}
			switch mode {
			case "absolute":
				h.Name = "/x-ui/payload"
			case "escape":
				h.Name = "x-ui/../../outside"
			case "unnormalized":
				h.Name = "x-ui/bin/../payload"
			case "backslash":
				h.Name = `x-ui\payload`
			case "wrong-root":
				h.Name = "other/payload"
			case "symlink":
				h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, "../../outside", 0
				bodies[0] = nil
			case "hardlink":
				h.Typeflag, h.Linkname, h.Size = tar.TypeLink, "../../outside", 0
				bodies[0] = nil
			case "fifo":
				h.Typeflag, h.Size = tar.TypeFifo, 0
				bodies[0] = nil
			case "duplicate":
				headers = append(headers, h)
				bodies = append(bodies, []byte("y"))
			case "setuid":
				h.Mode = 04755
			case "non-directory-parent":
				headers = append(headers, &tar.Header{Name: "x-ui/payload/child", Typeflag: tar.TypeReg, Mode: 0600, Size: 1})
				bodies = append(bodies, []byte("y"))
			}
			archive := testArchive(t, headers, bodies)
			parent := t.TempDir()
			sentinel := filepath.Join(parent, "outside")
			if err := os.WriteFile(sentinel, []byte("old installation"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ExtractArchive(context.Background(), archive, filepath.Join(parent, "candidate")); err == nil {
				t.Fatalf("accepted %s archive", mode)
			}
			after, err := os.ReadFile(sentinel)
			if err != nil || string(after) != "old installation" {
				t.Fatal("escaped staging directory")
			}
		})
	}
}

func TestExtractArchiveRejectsTruncationOversizeAndCancellation(t *testing.T) {
	for _, mode := range []string{"truncated", "oversize", "cancelled", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			var raw bytes.Buffer
			gz := gzip.NewWriter(&raw)
			tw := tar.NewWriter(gz)
			size := int64(4)
			if mode == "oversize" {
				size = 512*1024*1024 + 1
			}
			if err := tw.WriteHeader(&tar.Header{Name: "x-ui/payload", Typeflag: tar.TypeReg, Mode: 0600, Size: size}); err != nil {
				t.Fatal(err)
			}
			if mode == "trailing" {
				_, _ = tw.Write([]byte("data"))
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
				_, _ = gz.Write([]byte(strings.Repeat("evil", 512)))
			}
			// Deliberately leave declared bytes missing in the other inputs.
			_ = gz.Close()
			archive := filepath.Join(t.TempDir(), "package.tar.gz")
			if err := os.WriteFile(archive, raw.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if _, err := ExtractArchive(ctx, archive, filepath.Join(t.TempDir(), "candidate")); err == nil {
				t.Fatalf("accepted %s archive", mode)
			}
		})
	}
}
