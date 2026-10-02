package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// PreserveResources copies current application state and customized geodata
// into a verified candidate. Unchanged distributed geodata uses the new package.
// The caller must first stop and settle the old service. Neither executables nor
// either manifest can be replaced; rooted operations contain resource copies.
func PreserveResources(previous, candidate string, m *Manifest) error {
	var current *Manifest
	if _, err := os.Lstat(filepath.Join(previous, ManifestName)); err == nil {
		current, err = VerifyFiles(previous)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return preserveResources(previous, candidate, m, current, false)
}

func preserveResources(previous, candidate string, m, current *Manifest, rollback bool) error {
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
	if m != nil {
		for _, entry := range m.Files {
			managed[entry.Path] = true
		}
	}
	currentFiles := map[string]File{}
	if current != nil {
		for _, entry := range current.Files {
			currentFiles[entry.Path] = entry
			if rollback && entry.Role != "geodata" {
				managed[entry.Path] = true
			}
		}
	}
	return fs.WalkDir(source.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		role, roleErr := packageRole(name, CurrentTarget())
		geodata := roleErr == nil && role == "geodata"
		legacyCode := rollback && m == nil && (roleErr == nil || name == "x-ui.service")
		if !geodata && (managed[name] || legacyCode) {
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
			if geodata {
				return fmt.Errorf("current geodata is not a regular file: %s", name)
			}
			return target.MkdirAll(name, info.Mode().Perm())
		}
		if geodata {
			snapshot, err := snapshotResource(previous, name, "geodata", maxFileBytes)
			if err != nil {
				return err
			}
			original, declared := currentFiles[name]
			if !rollback && managed[name] && declared && snapshot.Size == original.Size && snapshot.SHA256 == original.SHA256 {
				return nil
			}
		}
		if existing, err := target.Lstat(name); !os.IsNotExist(err) {
			if err != nil || !geodata || !(managed[name] || legacyCode) || !existing.Mode().IsRegular() {
				return fmt.Errorf("candidate already contains undeclared or unsafe resource: %s", name)
			}
			if err := target.Remove(name); err != nil {
				return err
			}
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
		syncErr := output.Sync()
		sourceErr, targetErr := input.Close(), output.Close()
		if copyErr != nil || syncErr != nil || sourceErr != nil || targetErr != nil || n != info.Size() {
			return fmt.Errorf("application resource changed while copying: %s", name)
		}
		return nil
	})
}

func snapshotResource(root, name, role string, limit int64) (File, error) {
	filename, info, err := regularFile(root, name)
	if err != nil {
		return File{}, err
	}
	if info.Size() <= 0 || info.Size() > limit {
		return File{}, fmt.Errorf("resource receipt size is invalid: %s", name)
	}
	f, err := os.Open(filename)
	if err != nil {
		return File{}, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return File{}, fmt.Errorf("resource changed while opening: %s", name)
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, io.LimitReader(f, info.Size()+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n != info.Size() {
		return File{}, fmt.Errorf("resource changed while hashing: %s", name)
	}
	return File{Path: name, Role: role, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func resourceOverrides(root string, m *Manifest) ([]File, error) {
	distributed := map[string]File{}
	if m != nil {
		for _, f := range m.Files {
			distributed[f.Path] = f
		}
	}
	var overrides []File
	err := filepath.WalkDir(filepath.Join(root, "bin"), func(name string, e fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && name == filepath.Join(root, "bin") {
			return nil // Legacy installations may not have a bin directory.
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		role, err := packageRole(rel, CurrentTarget())
		if err != nil || role != "geodata" {
			return nil
		}
		snapshot, err := snapshotResource(root, rel, role, maxFileBytes)
		if err != nil {
			return err
		}
		if original, ok := distributed[rel]; !ok || original.Size != snapshot.Size || original.SHA256 != snapshot.SHA256 {
			overrides = append(overrides, snapshot)
		}
		return nil
	})
	return overrides, err
}
