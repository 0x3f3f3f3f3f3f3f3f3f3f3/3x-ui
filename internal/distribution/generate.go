package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const GoToolchain = "go1.27.1"
const NodeToolchain = "v26.10.0"

func packageRole(name string, target Target) (string, error) {
	switch {
	case name == "x-ui":
		return "panel", nil
	case name == "bin/"+CoreBinaryName(target.OS, target.Arch):
		return "core", nil
	case name == "x-ui-package" || name == "x-ui.sh" || name == "install.sh" || name == "update.sh" || name == "install-paired-package.sh" || name == "DockerEntrypoint.sh" || strings.HasPrefix(name, "internal/web/translation/"):
		return "control", nil
	case name == "x-ui.rc" || strings.HasPrefix(name, "x-ui.service."):
		return "service", nil
	case strings.HasPrefix(name, "licenses/"):
		return "license", nil
	case strings.HasPrefix(name, "bin/geo") && strings.HasSuffix(name, ".dat"):
		return "geodata", nil
	case strings.HasPrefix(name, "bin/mtg-linux-") || name == "bin/tuic-server":
		return "legacy-helper", nil
	default:
		return "", fmt.Errorf("undeclared resource or runtime state cannot be packaged: %s", name)
	}
}

// Generate writes an exclusive manifest over explicit distribution resources.
// It also works for cross-built Linux targets without executing their binaries.
func Generate(root string, target Target, revision string, toolchains map[string]string) (*Manifest, error) {
	if !revisionPattern.MatchString(revision) {
		return nil, errors.New("a clean full source revision is required")
	}
	if toolchains["go"] != GoToolchain || toolchains["node"] != NodeToolchain {
		return nil, errors.New("package requires the pinned Go and Node toolchains")
	}
	if target.OS != "linux" {
		return nil, errors.New("paired distribution currently requires Linux")
	}
	switch target.Arch {
	case "amd64", "arm64", "386", "s390x":
		if target.ARM != "" {
			return nil, errors.New("ARM variant on a non-ARM target")
		}
	case "arm":
		if target.ARM != "5" && target.ARM != "6" && target.ARM != "7" {
			return nil, errors.New("ARM variant must be 5, 6 or 7")
		}
	default:
		return nil, errors.New("unsupported package architecture")
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("package root must be a directory without a link")
	}
	if _, err := os.Lstat(filepath.Join(root, ManifestName)); !os.IsNotExist(err) {
		return nil, errors.New("refusing to replace an existing package manifest")
	}
	m := &Manifest{FormatVersion: 1, SourceRevision: revision, Compatibility: "traffic-control-v1", Target: target, RequiredCapabilities: RequiredCapabilities(), Toolchains: toolchains}
	roles := make(map[string]int)
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		role, err := packageRole(relative, target)
		if err != nil {
			return err
		}
		safeName, info, err := regularFile(root, relative)
		if err != nil {
			return err
		}
		if info.Size() <= 0 || info.Size() > maxFileBytes {
			return fmt.Errorf("invalid package resource size: %s", relative)
		}
		if (role == "panel" || role == "core") && info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("package binary is not executable: %s", relative)
		}
		f, err := os.Open(safeName)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(f, info.Size()+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return fmt.Errorf("package resource changed while hashing: %s", relative)
		}
		m.Files = append(m.Files, File{Path: relative, Role: role, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
		roles[role]++
		if len(m.Files) > 4096 {
			return errors.New("too many package resources")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if roles["panel"] != 1 || roles["core"] != 1 {
		return nil, errors.New("package must contain exactly one panel/core pair")
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil || len(data)+1 > maxManifestBytes {
		return nil, errors.New("generated package manifest is too large")
	}
	f, err := os.OpenFile(filepath.Join(root, ManifestName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, err
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return m, nil
}
