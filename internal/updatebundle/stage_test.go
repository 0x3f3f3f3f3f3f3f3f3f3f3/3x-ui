package updatebundle

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

type archiveEntry struct {
	header tar.Header
	data   string
}

func panelEntry() archiveEntry {
	return archiveEntry{tar.Header{Name: "x-ui/x-ui", Mode: 0o755, Typeflag: tar.TypeReg}, "fixture panel\n"}
}

func tarBytes(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, entry := range entries {
		h := entry.header
		h.Size = int64(len(entry.data))
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func compressed(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func preservedParent(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	for _, name := range []string{"x-ui/x-ui", "x-ui/bin/xray", "x-ui.service", "x-ui.db", "outside"} {
		p := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("old:"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return parent
}

func requirePreserved(t *testing.T, parent string) {
	t.Helper()
	for _, name := range []string{"x-ui/x-ui", "x-ui/bin/xray", "x-ui.service", "x-ui.db", "outside"} {
		data, err := os.ReadFile(filepath.Join(parent, name))
		if err != nil || string(data) != "old:"+name {
			t.Fatalf("existing %s changed: %q, %v", name, data, err)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 4 {
		t.Fatalf("partial staging directory retained: %v, %v", entries, err)
	}
}

func TestStageCompleteArchive(t *testing.T) {
	for _, format := range []tar.Format{tar.FormatUSTAR, tar.FormatGNU} {
		t.Run(format.String(), func(t *testing.T) {
			panel := panelEntry()
			panel.header.Format = format
			asset := archiveEntry{tar.Header{Name: "x-ui/bin/data.dat", Mode: 0o666, Format: format}, "asset"}
			data := compressed(t, tarBytes(t, panel, asset, archiveEntry{header: tar.Header{Name: "x-ui/", Typeflag: tar.TypeDir, Mode: 0o777, Format: format}}))
			parent := preservedParent(t)
			stage, err := Stage(t.Context(), bytes.NewReader(data), parent, digest(data))
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(stage) != parent || !strings.HasPrefix(filepath.Base(stage), ".x-ui-stage-") {
				t.Fatalf("unowned stage: %q", stage)
			}
			for name, want := range map[string]struct {
				mode os.FileMode
				data string
			}{".": {0o700, ""}, "x-ui": {0o755, ""}, "x-ui/x-ui": {0o755, panel.data}, "x-ui/bin/data.dat": {0o644, "asset"}} {
				p := filepath.Join(stage, name)
				info, err := os.Stat(p)
				if err != nil || info.Mode().Perm() != want.mode {
					t.Fatalf("%s permissions: %v, %v", name, info, err)
				}
				if want.data != "" {
					got, err := os.ReadFile(p)
					if err != nil || string(got) != want.data {
						t.Fatalf("%s bytes: %q, %v", name, got, err)
					}
				}
			}
			if err := os.RemoveAll(stage); err != nil {
				t.Fatal(err)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestStageRejectsUnsafeMembers(t *testing.T) {
	cases := map[string]archiveEntry{}
	for _, name := range []string{"../outside", "/outside", "other/file", "x-ui/../outside", "x-ui/./file", "x-ui//file", "x-ui/a\\b", "x-ui/a:b", "x-ui/a\nb", "x-ui/a b", "x-ui/中文", "x-ui/" + strings.Repeat("a", 240)} {
		cases["path:"+name] = archiveEntry{header: tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg}}
	}
	for name, kind := range map[string]byte{"symlink": tar.TypeSymlink, "hardlink": tar.TypeLink, "character-device": tar.TypeChar, "block-device": tar.TypeBlock, "fifo": tar.TypeFifo} {
		cases[name] = archiveEntry{header: tar.Header{Name: "x-ui/escape", Typeflag: kind, Mode: 0o644, Linkname: "../../outside"}}
	}
	cases["setuid"] = archiveEntry{header: tar.Header{Name: "x-ui/privileged", Mode: 0o4755}}
	cases["setgid"] = archiveEntry{header: tar.Header{Name: "x-ui/privileged", Mode: 0o2755}}
	cases["sticky"] = archiveEntry{header: tar.Header{Name: "x-ui/privileged", Mode: 0o1755}}
	cases["pax"] = archiveEntry{header: tar.Header{Name: "x-ui/pax", Mode: 0o644, PAXRecords: map[string]string{"comment": "unsupported"}}}
	cases["directory-over-panel"] = archiveEntry{header: tar.Header{Name: "x-ui/x-ui/", Typeflag: tar.TypeDir, Mode: 0o755}}
	cases["duplicate-panel"] = panelEntry()
	cases["file-over-parent"] = archiveEntry{header: tar.Header{Name: "x-ui", Mode: 0o644}}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			data := compressed(t, tarBytes(t, panelEntry(), entry))
			parent := preservedParent(t)
			stage, err := Stage(t.Context(), bytes.NewReader(data), parent, digest(data))
			if err == nil || stage != "" {
				t.Fatalf("unsafe archive accepted: stage=%q err=%v", stage, err)
			}
			requirePreserved(t, parent)
		})
	}
}

func rewriteTarHeader(block []byte, change func([]byte)) {
	change(block)
	copy(block[148:156], "        ")
	sum := 0
	for _, b := range block {
		sum += int(b)
	}
	copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

func TestStageRejectsSpecialTarHeaders(t *testing.T) {
	for _, name := range []string{"regular-trailing-slash", "sparse", "directory-payload", "unknown-type"} {
		t.Run(name, func(t *testing.T) {
			panel := panelEntry()
			panel.header.Format = tar.FormatGNU
			extra := panel
			extra.header.Name = "x-ui/extra"
			plain := tarBytes(t, panel, extra)
			rewriteTarHeader(plain[1024:1536], func(block []byte) {
				switch name {
				case "regular-trailing-slash":
					copy(block[:100], make([]byte, 100))
					copy(block[:100], "x-ui/file/")
				case "sparse":
					block[156] = tar.TypeGNUSparse
				case "directory-payload":
					block[156] = tar.TypeDir
				case "unknown-type":
					block[156] = 'Z'
				}
			})
			data := compressed(t, plain)
			parent := preservedParent(t)
			stage, err := Stage(t.Context(), bytes.NewReader(data), parent, digest(data))
			if err == nil || stage != "" {
				t.Fatalf("unsafe header accepted: stage=%q err=%v", stage, err)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestStageAcceptsBoundedZeroPadding(t *testing.T) {
	plain := tarBytes(t, panelEntry())
	data := compressed(t, append(plain, make([]byte, 8192)...))
	parent := preservedParent(t)
	stage, err := Stage(t.Context(), bytes.NewReader(data), parent, digest(data))
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(stage)
	requirePreserved(t, parent)
}

func TestStageRejectsDirectoryCollisions(t *testing.T) {
	dir := archiveEntry{header: tar.Header{Name: "x-ui/bin/", Mode: 0o755, Typeflag: tar.TypeDir}}
	file := archiveEntry{header: tar.Header{Name: "x-ui/bin", Mode: 0o644}, data: "file"}
	for name, extra := range map[string][]archiveEntry{"duplicate-directory": {dir, dir}, "file-then-directory": {file, dir}, "directory-then-file": {dir, file}} {
		t.Run(name, func(t *testing.T) {
			data := compressed(t, tarBytes(t, append([]archiveEntry{panelEntry()}, extra...)...))
			parent := preservedParent(t)
			stage, err := Stage(t.Context(), bytes.NewReader(data), parent, digest(data))
			if err == nil || stage != "" {
				t.Fatalf("collision accepted: stage=%q err=%v", stage, err)
			}
			requirePreserved(t, parent)
		})
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func TestStageCleansAlreadyExtractedFiles(t *testing.T) {
	data := compressed(t, tarBytes(t, panelEntry()))
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			parent := preservedParent(t)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			input := bytes.NewReader(data)
			observed := false
			want := errors.New("late input error")
			if cancel {
				want = context.Canceled
			}
			r := readFunc(func(p []byte) (int, error) {
				n, err := input.Read(p)
				if errors.Is(err, io.EOF) {
					paths, globErr := filepath.Glob(filepath.Join(parent, ".x-ui-stage-*", "x-ui", "x-ui"))
					if globErr != nil || len(paths) != 1 {
						t.Fatalf("failure was not after actual extraction: %v, %v", paths, globErr)
					}
					got, readErr := os.ReadFile(paths[0])
					if readErr != nil || string(got) != panelEntry().data {
						t.Fatalf("extracted payload missing: %q, %v", got, readErr)
					}
					observed = true
					if cancel {
						stop()
						return n, err
					}
					return n, want
				}
				return n, err
			})
			stage, err := Stage(ctx, r, parent, digest(data))
			if !observed || !errors.Is(err, want) || stage != "" {
				t.Fatalf("late failure was not preserved: observed=%v stage=%q err=%v", observed, stage, err)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestStageRejectsIncompleteArchive(t *testing.T) {
	plain := tarBytes(t, panelEntry())
	valid := compressed(t, plain)
	badTrailer := bytes.Clone(valid)
	badTrailer[len(badTrailer)-8] ^= 0xff
	noExec := panelEntry()
	noExec.header.Mode = 0o644
	empty := panelEntry()
	empty.data = ""
	cases := map[string][]byte{
		"not-gzip":          []byte("not an archive"),
		"bad-gzip-checksum": badTrailer,
		"truncated-gzip":    valid[:len(valid)-5],
		"no-tar-trailer":    compressed(t, plain[:len(plain)-1024]),
		"one-tar-end-block": compressed(t, plain[:len(plain)-512]),
		"bad-tar-header":    compressed(t, []byte(strings.Repeat("a", 2048))),
		"data-after-tar":    compressed(t, append(bytes.Clone(plain), 'x')),
		"second-gzip":       append(bytes.Clone(valid), valid...),
		"trailing-gzip":     append(bytes.Clone(valid), 'x'),
		"missing-panel":     compressed(t, tarBytes(t)),
		"not-executable":    compressed(t, tarBytes(t, noExec)),
		"empty-panel":       compressed(t, tarBytes(t, empty)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			parent := preservedParent(t)
			stage, err := Stage(t.Context(), bytes.NewReader(data), parent, digest(data))
			if err == nil || stage != "" {
				t.Fatalf("invalid archive accepted: stage=%q err=%v", stage, err)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestStageRequiresExactChecksum(t *testing.T) {
	data := compressed(t, tarBytes(t, panelEntry()))
	for _, sum := range []string{"", "abc", strings.Repeat("0", 64), strings.ToUpper(digest(data))} {
		t.Run(sum, func(t *testing.T) {
			parent := preservedParent(t)
			stage, err := Stage(t.Context(), bytes.NewReader(data), parent, sum)
			if err == nil || stage != "" {
				t.Fatalf("invalid checksum accepted: stage=%q err=%v", stage, err)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestStageLimits(t *testing.T) {
	panel := panelEntry()
	asset := archiveEntry{tar.Header{Name: "x-ui/asset", Mode: 0o644}, "data"}
	plain := tarBytes(t, panel, asset)
	data := compressed(t, plain)
	for _, name := range []string{"compressed", "expanded", "file", "total", "members", "expanded-padding"} {
		t.Run(name, func(t *testing.T) {
			limits := defaultLimits()
			input := data
			switch name {
			case "compressed":
				limits.compressed = int64(len(data) - 1)
			case "expanded":
				limits.expanded = int64(len(plain) - 1)
			case "file":
				limits.file = int64(len(panel.data) - 1)
			case "total":
				limits.total = int64(len(panel.data) + len(asset.data) - 1)
			case "members":
				limits.members = 1
			case "expanded-padding":
				input = compressed(t, append(bytes.Clone(plain), make([]byte, 4096)...))
				limits.expanded = int64(len(plain) + 2048)
			}
			parent := preservedParent(t)
			stage, err := stageWithLimits(t.Context(), bytes.NewReader(input), parent, digest(input), limits)
			if err == nil || stage != "" {
				t.Fatalf("limit not enforced: stage=%q err=%v", stage, err)
			}
			requirePreserved(t, parent)
		})
	}
	t.Run("exact-bounds", func(t *testing.T) {
		limits := archiveLimits{compressed: int64(len(data)), expanded: int64(len(plain)), file: int64(len(panel.data)), total: int64(len(panel.data) + len(asset.data)), members: 2}
		parent := preservedParent(t)
		stage, err := stageWithLimits(t.Context(), bytes.NewReader(data), parent, digest(data), limits)
		if err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(stage)
		requirePreserved(t, parent)
	})
}

type failingReader struct {
	io.Reader
	err error
}

func (r failingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		return n, r.err
	}
	return n, err
}

func TestStageCancelsAndPreservesReadErrors(t *testing.T) {
	data := compressed(t, tarBytes(t, panelEntry()))
	wantErr := errors.New("fixture read failure")
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := wantErr
			if canceled {
				cancel()
				want = context.Canceled
			}
			parent := preservedParent(t)
			stage, err := Stage(ctx, failingReader{bytes.NewReader(data), wantErr}, parent, digest(data))
			if !errors.Is(err, want) || stage != "" {
				t.Fatalf("lost error: stage=%q err=%v want=%v", stage, err, want)
			}
			requirePreserved(t, parent)
		})
	}
}
