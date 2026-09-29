// Package updatebundle validates release archives without touching an installation.
package updatebundle

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type archiveLimits struct {
	compressed, expanded, file, total int64
	members                           int
}

func defaultLimits() archiveLimits {
	return archiveLimits{compressed: 512 << 20, expanded: 2 << 30, file: 512 << 20, total: 1536 << 20, members: 4096}
}

// Stage returns an owned directory only after validating the complete archive.
// The caller must remove the returned directory after using its x-ui child.
func Stage(ctx context.Context, archive io.Reader, parent, expectedSHA256 string) (string, error) {
	return stageWithLimits(ctx, archive, parent, expectedSHA256, defaultLimits())
}

func stageWithLimits(ctx context.Context, archive io.Reader, parent, expectedSHA256 string, limits archiveLimits) (result string, err error) {
	want, decodeErr := hex.DecodeString(expectedSHA256)
	if decodeErr != nil || len(want) != sha256.Size || expectedSHA256 != strings.ToLower(expectedSHA256) {
		return "", errors.New("expected SHA256 must be 64 lowercase hexadecimal characters")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".x-ui-stage-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(stage))
		}
	}()
	root, err := os.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	defer root.Close()

	hash := sha256.New()
	input := &io.LimitedReader{R: contextReader{ctx, archive}, N: limits.compressed + 1}
	compressed := bufio.NewReader(io.TeeReader(input, hash))
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	gz.Multistream(false)
	expanded := &io.LimitedReader{R: contextReader{ctx, gz}, N: limits.expanded + 1}
	if err := extract(root, expanded, limits); err != nil {
		return "", err
	}
	if _, err := io.Copy(zeroPadding{}, expanded); err != nil {
		return "", fmt.Errorf("complete gzip stream: %w", err)
	}
	if expanded.N <= 0 {
		return "", errors.New("expanded archive exceeds size limit")
	}
	if _, err := compressed.ReadByte(); !errors.Is(err, io.EOF) {
		if err != nil {
			return "", fmt.Errorf("complete compressed input: %w", err)
		}
		return "", errors.New("trailing compressed data or multiple gzip members")
	}
	if input.N <= 0 {
		return "", errors.New("compressed archive exceeds size limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return "", errors.New("archive SHA256 mismatch")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return stage, nil
}

func extract(root *os.Root, input *io.LimitedReader, limits archiveLimits) error {
	r := tar.NewReader(input)
	seen := make(map[string]bool)
	var total, padding int64
	panel := false
	for {
		before := input.N
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			if before-input.N != padding+1024 {
				return errors.New("tar archive is missing its complete end marker")
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}
		name, err := memberName(h)
		if err != nil {
			return err
		}
		if seen[name] || len(seen) >= limits.members {
			return fmt.Errorf("duplicate archive member or too many entries: %q", name)
		}
		seen[name] = true
		if h.Size < 0 || h.Size > limits.file || h.Size > limits.total-total {
			return fmt.Errorf("archive payload exceeds size limit: %q", name)
		}
		total += h.Size
		padding = (512 - h.Size%512) % 512
		if h.Typeflag == tar.TypeDir {
			if h.Size != 0 {
				return fmt.Errorf("directory contains payload: %q", name)
			}
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := writeMember(root, r, h, name); err != nil {
			return err
		}
		if name == "x-ui/x-ui" && h.Size > 0 && h.Mode&0o111 != 0 {
			panel = true
		}
	}
	if !panel {
		return errors.New("archive requires a nonempty executable x-ui/x-ui")
	}
	return nil
}

func writeMember(root *os.Root, r io.Reader, h *tar.Header, name string) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if h.Mode&0o111 != 0 {
		mode = 0o755
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, r)
	err = errors.Join(copyErr, f.Close())
	if err != nil {
		return fmt.Errorf("extract %q: %w", name, err)
	}
	if n != h.Size {
		return fmt.Errorf("truncated archive member: %q", name)
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type zeroPadding struct{}

func (zeroPadding) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, errors.New("nonzero data after tar end marker")
		}
	}
	return len(p), nil
}
