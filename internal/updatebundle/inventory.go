package updatebundle

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

func manifestMemberName(name string, directory bool) error {
	if name == ManifestName {
		return errors.New("manifest cannot list itself")
	}
	header := &tar.Header{Name: "x-ui/" + name, Typeflag: tar.TypeReg}
	if directory {
		header.Typeflag = tar.TypeDir
	}
	_, err := memberName(header)
	return err
}

func releaseInventory(ctx context.Context, root *os.Root) (map[string]ReleaseFile, error) {
	files := make(map[string]ReleaseFile)
	limits := defaultLimits()
	var total int64
	members := 2
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || name == ManifestName {
			return nil
		}
		members++
		if members > limits.members {
			return errors.New("too many release members")
		}
		if err := manifestMemberName(name, entry.IsDir()); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsupported release file type/mode: %q", name)
		}
		if info.IsDir() {
			return nil
		}
		if info.Size() < 0 || info.Size() > limits.file || info.Size() > limits.total-total {
			return fmt.Errorf("release payload exceeds size limit: %q", name)
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, info.Size()+1))
		err = errors.Join(readErr, file.Close())
		if err != nil {
			return err
		}
		if n != info.Size() {
			return fmt.Errorf("release file size changed while reading: %q", name)
		}
		total += n
		files[name] = ReleaseFile{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n, Executable: info.Mode()&0o111 != 0}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
