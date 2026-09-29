package updatebundle

import (
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

// PrepareInstalledUpdater copies only the installed release's verified updater.
// Runtime files need not be release members. The caller supplies its compiled,
// unmodified panel commit/platform; this does not attest against a hostile root.
func PrepareInstalledUpdater(ctx context.Context, directory, parent, commit, platform string) (path string, resultErr error) {
	return prepareInstalledScript(ctx, directory, parent, commit, platform, "update.sh")
}

// PrepareInstalledMenu copies the verified control menu for the running panel.
// The caller owns the returned private file and must remove it after use.
func PrepareInstalledMenu(ctx context.Context, directory, parent, commit, platform string) (path string, resultErr error) {
	return prepareInstalledScript(ctx, directory, parent, commit, platform, "x-ui.sh")
}

func prepareInstalledScript(ctx context.Context, directory, parent, commit, platform, name string) (path string, resultErr error) {
	label, pattern := "updater", "3x-ui-update-*.sh"
	if name == "x-ui.sh" {
		label, pattern = "menu", "3x-ui-menu-*.sh"
	} else if name != "update.sh" {
		return "", errors.New("unsupported installed script")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	installed, err := canonicalDirectory(directory)
	if err != nil {
		return "", err
	}
	staging, err := canonicalDirectory(parent)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(installed, staging)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", errors.New("updater staging parent must be outside the installation")
	}
	root, err := os.OpenRoot(installed)
	if err != nil {
		return "", err
	}
	defer root.Close()
	manifest, err := readReleaseManifest(ctx, root)
	if err != nil {
		return "", err
	}
	expected := ReleaseIdentity{Repository: ReleaseRepository, Commit: commit, Tag: manifest.Identity.Tag, Platform: platform}
	if err := expected.validate(); err != nil {
		return "", err
	}
	if manifest.Identity != expected {
		return "", fmt.Errorf("installed %s source differs from the running panel", label)
	}
	want := manifest.Files[name]
	if want.Size > 2<<20 {
		return "", fmt.Errorf("installed %s exceeds size limit", label)
	}
	info, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() != want.Size {
		return "", fmt.Errorf("installed %s type, size or executable mode differs from its manifest", label)
	}
	source, err := root.Open(name)
	if err != nil {
		return "", err
	}
	opened, statErr := source.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		return "", errors.Join(fmt.Errorf("installed %s changed while opening", label), statErr, source.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, source}, want.Size+1))
	if err := errors.Join(readErr, source.Close()); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != want.Size || hex.EncodeToString(digest[:]) != want.SHA256 {
		return "", fmt.Errorf("installed %s checksum differs from its manifest", label)
	}
	file, err := os.CreateTemp(staging, pattern)
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.Remove(path))
			path = ""
		}
	}()
	if _, err := file.Write(data); err != nil {
		return path, err
	}
	if err := file.Chmod(0o700); err != nil {
		return path, err
	}
	return path, ctx.Err()
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}
