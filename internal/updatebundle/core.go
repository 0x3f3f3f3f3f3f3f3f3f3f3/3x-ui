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
)

// CoreReplacement owns a same-filesystem candidate and backup. Callers serialize
// activation and recovery with their process lifecycle lock. This supports error
// recovery; it is not a crash-recovery journal.
type CoreReplacement struct {
	target, directory string
	original          os.FileInfo
	originalDigest    string
	active            bool
	finished          bool
}

// PrepareCoreReplacement verifies the complete selected bundle and copies both
// cores before changing the live path. Runtime preflight is the caller's duty.
func PrepareCoreReplacement(ctx context.Context, directory string, identity ReleaseIdentity, target string) (_ *CoreReplacement, resultErr error) {
	manifest, err := VerifyManifest(ctx, directory, identity)
	if err != nil {
		return nil, err
	}
	parent, err := canonicalDirectory(filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	target = filepath.Join(parent, filepath.Base(target))
	info, err := coreFileInfo(target)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".xray-update-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.RemoveAll(stage))
		}
	}()
	core, _ := releaseCoreName(identity.Platform)
	want := manifest.Files[core]
	if _, err := copyCoreFile(ctx, filepath.Join(directory, core), filepath.Join(stage, "candidate"), 0o755, &want); err != nil {
		return nil, err
	}
	digest, err := copyCoreFile(ctx, target, filepath.Join(stage, "previous"), info.Mode().Perm(), nil)
	if err != nil {
		return nil, err
	}
	if err := syncCoreDirectory(stage); err != nil {
		return nil, err
	}
	return &CoreReplacement{target: target, directory: stage, original: info, originalDigest: digest}, nil
}

func coreFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() < 1 || info.Size() > defaultLimits().file {
		return nil, errors.New("core must be a bounded executable regular file without special mode bits")
	}
	return info, nil
}

func copyCoreFile(ctx context.Context, source, destination string, mode os.FileMode, want *ReleaseFile) (string, error) {
	info, err := coreFileInfo(source)
	if err != nil {
		return "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.Join(errors.New("core changed while opening"), err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(contextReader{ctx, input}, info.Size()+1))
	digest := hex.EncodeToString(hash.Sum(nil))
	if n != info.Size() || (want != nil && (want.Size != n || want.SHA256 != digest)) {
		copyErr = errors.Join(copyErr, errors.New("core changed or differs from the selected manifest"))
	}
	if copyErr == nil {
		copyErr = errors.Join(output.Chmod(mode), output.Sync())
	}
	return digest, errors.Join(copyErr, output.Close())
}

// Activate atomically replaces the executable without stopping its old process.
// On any error the caller must call Restore before releasing its lifecycle lock.
func (r *CoreReplacement) Activate(ctx context.Context) error {
	if r.finished || r.active || r.directory == "" {
		return errors.New("core replacement is no longer prepared")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := coreFileInfo(r.target)
	if err != nil || !os.SameFile(info, r.original) || info.Mode() != r.original.Mode() || info.Size() != r.original.Size() {
		return errors.Join(errors.New("installed core changed during preparation"), err)
	}
	f, err := os.Open(r.target)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(contextReader{ctx, f}, info.Size()+1))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if n != info.Size() || hex.EncodeToString(hash.Sum(nil)) != r.originalDigest {
		return errors.New("installed core content changed during preparation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(r.directory, "candidate"), r.target); err != nil {
		return err
	}
	r.active = true
	return syncCoreDirectory(filepath.Dir(r.target))
}

// Restore must run even after cancellation; losing the request does not permit
// leaving an unverified replacement installed. Failed restores retain the backup.
func (r *CoreReplacement) Restore() error {
	if !r.active {
		return nil
	}
	if err := os.Rename(filepath.Join(r.directory, "previous"), r.target); err != nil {
		return fmt.Errorf("restore previous core from %s: %w", r.directory, err)
	}
	r.active = false
	r.finished = true
	return syncCoreDirectory(filepath.Dir(r.target))
}

// Commit confirms runtime readiness; Close may now discard the backup.
func (r *CoreReplacement) Commit() { r.active = false; r.finished = true }

func (r *CoreReplacement) Close() error {
	if r.active {
		return fmt.Errorf("core recovery backup retained at %s", r.directory)
	}
	if r.directory == "" {
		return nil
	}
	if err := os.RemoveAll(r.directory); err != nil {
		return err
	}
	r.directory = ""
	return nil
}

func syncCoreDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
