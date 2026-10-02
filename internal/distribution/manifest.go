// Package distribution validates source-matched panel/core packages before
// installed resources or execution state can change.
package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const ManifestName = "custom-core-package.json"
const maxManifestBytes = 1024 * 1024
const maxFileBytes = 512 * 1024 * 1024

type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	ARM  string `json:"arm,omitempty"`
}

type File struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	FormatVersion        int               `json:"formatVersion"`
	SourceRevision       string            `json:"sourceRevision"`
	Compatibility        string            `json:"compatibility"`
	Target               Target            `json:"target"`
	Files                []File            `json:"files"`
	RequiredCapabilities []string          `json:"requiredCapabilities"`
	Toolchains           map[string]string `json:"toolchains,omitempty"`
}

func RequiredCapabilities() []string {
	return []string{"trusted-snell-client-id-v1", "trusted-mieru-client-id-v1", "trusted-ssh-client-id-v1", "trusted-tunnel-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "quota-window-baseline-v1", "tunnel-source-acl-v1", "tunnel-fixed-outbound-v1"}
}

func CoreBinaryName(osName, arch string) string {
	if arch == "arm" {
		arch = "arm32"
	}
	return "xray-" + osName + "-" + arch
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func regularFile(root, name string) (string, os.FileInfo, error) {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n") {
		return "", nil, fmt.Errorf("unsafe package path %q", name)
	}
	current := root
	parts := strings.Split(name, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, fmt.Errorf("package path %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) {
			return "", nil, fmt.Errorf("package path %s contains a link or non-directory", name)
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return "", nil, fmt.Errorf("package path %s is not a regular file", name)
			}
			return current, info, nil
		}
	}
	return "", nil, errors.New("invalid package path")
}

func readManifest(root string) (*Manifest, error) {
	fileName, info, err := regularFile(root, ManifestName)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maxManifestBytes {
		return nil, errors.New("package manifest size is invalid")
	}
	f, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return nil, errors.New("read bounded package manifest")
	}
	var m Manifest
	if err := decodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("decode package manifest: %w", err)
	}

	return &m, nil
}

// VerifyFiles verifies the complete declared pair for this host without
// executing binaries, loading service settings or opening accounting state.
func VerifyFiles(root string) (*Manifest, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("package root must be an existing directory without a link")
	}
	m, err := readManifest(root)
	if err != nil {
		return nil, err
	}
	if m.FormatVersion != 1 || !revisionPattern.MatchString(m.SourceRevision) || m.Compatibility != "traffic-control-v1" {
		return nil, errors.New("package format, source revision or compatibility is invalid")
	}
	if m.Toolchains["go"] != GoToolchain || m.Toolchains["node"] != NodeToolchain {
		return nil, errors.New("package toolchains are missing or differ from the pinned versions")
	}
	if m.Target.OS != runtime.GOOS || m.Target.Arch != runtime.GOARCH {
		return nil, errors.New("package target does not match this host")
	}
	if m.Target.Arch == "arm" && m.Target.ARM != "5" && m.Target.ARM != "6" && m.Target.ARM != "7" || m.Target.Arch != "arm" && m.Target.ARM != "" {
		return nil, errors.New("package ARM variant is invalid")
	}
	capabilities := make(map[string]bool)
	for _, name := range m.RequiredCapabilities {
		if name == "" || capabilities[name] {
			return nil, errors.New("package capabilities are empty or duplicated")
		}
		capabilities[name] = true
	}
	for _, required := range RequiredCapabilities() {
		if !capabilities[required] {
			return nil, fmt.Errorf("package lacks required capability %s", required)
		}
	}
	if len(m.Files) < 2 || len(m.Files) > 4096 {
		return nil, errors.New("package file count is invalid")
	}
	paths, roles := make(map[string]bool), make(map[string]int)
	for _, entry := range m.Files {
		if paths[entry.Path] || entry.Size <= 0 || entry.Size > maxFileBytes || !hashPattern.MatchString(entry.SHA256) {
			return nil, fmt.Errorf("invalid or duplicate package entry %s", entry.Path)
		}
		paths[entry.Path] = true
		switch entry.Role {
		case "panel":
			if entry.Path != "x-ui" {
				return nil, errors.New("panel path is invalid")
			}
		case "core":
			if entry.Path != "bin/"+CoreBinaryName(m.Target.OS, m.Target.Arch) {
				return nil, errors.New("core path is invalid")
			}
		case "control", "service", "license", "geodata", "legacy-helper":
		default:
			return nil, fmt.Errorf("unknown package role %s", entry.Role)
		}
		roles[entry.Role]++
		name, fileInfo, err := regularFile(root, entry.Path)
		if err != nil {
			return nil, err
		}
		if fileInfo.Size() != entry.Size {
			return nil, fmt.Errorf("package file size changed: %s", entry.Path)
		}
		if (entry.Role == "panel" || entry.Role == "core") && fileInfo.Mode().Perm()&0111 == 0 {
			return nil, fmt.Errorf("package executable is not executable: %s", entry.Path)
		}
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(f, entry.Size+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return nil, fmt.Errorf("package file checksum changed: %s", entry.Path)
		}
	}
	if roles["panel"] != 1 || roles["core"] != 1 {
		return nil, errors.New("package must contain exactly one panel/core pair")
	}
	return m, nil
}
