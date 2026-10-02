package distribution

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxArchiveBytes int64 = 512 * 1024 * 1024
const maxExpandedArchiveBytes int64 = 2 * 1024 * 1024 * 1024

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// ExtractArchive creates a new private staging tree. It never writes outside it
// or replaces an existing path; failures retain the partial tree for diagnosis.
// The returned candidate still needs Verify before it can replace an installation.
func ExtractArchive(ctx context.Context, archive, destination string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Lstat(archive)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxArchiveBytes {
		return "", errors.New("archive must be a bounded regular file without a link")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	parent, err := os.Lstat(filepath.Dir(destination))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("staging parent must be an existing directory without a link")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return "", fmt.Errorf("create new private staging tree: %w", err)
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(contextReader{ctx: ctx, reader: io.LimitReader(f, maxArchiveBytes+1)})
	if err != nil {
		return "", err
	}
	defer gz.Close()
	bounded := &io.LimitedReader{R: gz, N: maxExpandedArchiveBytes + 1}
	tr := tar.NewReader(bounded)
	seen := make(map[string]bool)
	var fileBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read package archive: %w", err)
		}
		if bounded.N <= 0 {
			return "", errors.New("expanded archive exceeds size limit")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if len(name) > 1024 || path.IsAbs(name) || path.Clean(name) != name || (name != "x-ui" && !strings.HasPrefix(name, "x-ui/")) || strings.ContainsAny(name, "\\\x00\r\n") {
			return "", fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if seen[name] || len(seen) >= 8192 {
			return "", errors.New("archive contains duplicate paths or too many entries")
		}
		seen[name] = true
		if header.Mode&06000 != 0 {
			return "", errors.New("archive contains privileged file permissions")
		}
		fileName := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return "", errors.New("archive directory declares file content")
			}
			if err := os.MkdirAll(fileName, 0700); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			if name == "x-ui" || header.Size < 0 || header.Size > maxFileBytes || fileBytes+header.Size > maxExpandedArchiveBytes {
				return "", errors.New("archive file size or root is invalid")
			}
			fileBytes += header.Size
			if err := os.MkdirAll(filepath.Dir(fileName), 0700); err != nil {
				return "", err
			}
			target, err := os.OpenFile(fileName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0777)
			if err != nil {
				return "", err
			}
			n, copyErr := io.Copy(target, tr)
			closeErr := target.Close()
			if copyErr != nil || closeErr != nil || n != header.Size {
				return "", fmt.Errorf("archive file is truncated or failed: %s", name)
			}
		default:
			return "", errors.New("archive contains a link, special file or unsupported entry")
		}
	}
	// Consume gzip trailers and tar's optional zero padding without allocating
	// the remaining archive. A second archive/nonzero trailing payload is refused.
	buffer := make([]byte, 32*1024)
	for {
		n, err := bounded.Read(buffer)
		if bounded.N <= 0 {
			return "", errors.New("expanded archive exceeds size limit")
		}
		for _, b := range buffer[:n] {
			if b != 0 {
				return "", errors.New("archive has nonzero trailing content")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("archive gzip trailer: %w", err)
		}
	}
	root := filepath.Join(destination, "x-ui")
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return "", errors.New("archive has no x-ui package directory")
	}
	return root, nil
}
