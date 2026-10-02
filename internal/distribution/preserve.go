package distribution

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// PreserveResources copies application-owned, undeclared state/resources into
// the staged candidate. The caller must first stop the old service and finish
// its traffic settlement. Neither managed executables nor either tree's manifest
// can be replaced. Rooted file operations never follow links outside the app.
func PreserveResources(previous, candidate string, m *Manifest) error {
	for _, directory := range []string{previous, candidate} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("resource roots must be directories without links")
		}
	}
	source, err := os.OpenRoot(previous)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenRoot(candidate)
	if err != nil {
		return err
	}
	defer target.Close()
	managed := map[string]bool{ManifestName: true}
	for _, entry := range m.Files {
		managed[entry.Path] = true
	}
	return fs.WalkDir(source.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if managed[name] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// Authentication/cache trees are not application distribution resources.
		switch name {
		case ".git", ".ssh", ".aws", ".codex", ".agents":
			if entry.IsDir() {
				return fs.SkipDir
			}
			return errors.New("authentication/cache path cannot be copied into an installation")
		}
		info, err := source.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return target.MkdirAll(name, info.Mode().Perm())
		}
		if _, err := target.Lstat(name); !os.IsNotExist(err) {
			return fmt.Errorf("candidate already contains undeclared resource: %s", name)
		}
		if err := target.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := source.Readlink(name)
			if err != nil {
				return err
			}
			return target.Symlink(link, name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot preserve a special application file: %s", name)
		}
		input, err := source.Open(name)
		if err != nil {
			return err
		}
		opened, err := input.Stat()
		if err != nil || !os.SameFile(info, opened) {
			_ = input.Close()
			return errors.New("application resource changed while opening")
		}
		output, err := target.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		n, copyErr := io.Copy(output, io.LimitReader(input, info.Size()+1))
		sourceErr, targetErr := input.Close(), output.Close()
		if copyErr != nil || sourceErr != nil || targetErr != nil || n != info.Size() {
			return fmt.Errorf("application resource changed while copying: %s", name)
		}
		return nil
	})
}
