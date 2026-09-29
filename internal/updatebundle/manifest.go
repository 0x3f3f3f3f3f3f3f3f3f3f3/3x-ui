package updatebundle

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

const (
	ReleaseRepository = "0x3f3f3f3f3f3f3f3f3f3f3/3x-ui"
	ManifestName      = "release.json"
	maxManifestBytes  = 1 << 20
)

var (
	releaseCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	releaseTagPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
)

type ReleaseIdentity struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Tag        string `json:"tag"`
	Platform   string `json:"platform"`
}

type ReleaseFile struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

type ReleaseManifest struct {
	Schema     int                    `json:"schema"`
	Identity   ReleaseIdentity        `json:"identity"`
	PolicyABI  int                    `json:"policyABI"`
	RoutingABI int                    `json:"routingABI"`
	Files      map[string]ReleaseFile `json:"files"`
}

func (identity ReleaseIdentity) validate() error {
	if identity.Repository != ReleaseRepository || !releaseCommitPattern.MatchString(identity.Commit) || !releaseTagPattern.MatchString(identity.Tag) {
		return errors.New("release requires the managed fork repository, full commit and valid tag")
	}
	if _, err := releaseCoreName(identity.Platform); err != nil {
		return err
	}
	return nil
}

func releaseCoreName(platform string) (string, error) {
	var arch string
	switch platform {
	case "linux-amd64", "linux-arm64", "linux-386", "linux-s390x":
		arch = platform[len("linux-"):]
	case "linux-armv5", "linux-armv6", "linux-armv7":
		arch = "arm32"
	default:
		return "", fmt.Errorf("unsupported release platform %q", platform)
	}
	return "bin/xray-linux-" + arch, nil
}

func (manifest *ReleaseManifest) validate() error {
	if manifest == nil || manifest.Schema != 1 || manifest.PolicyABI != 1 || manifest.RoutingABI != 1 {
		return errors.New("incompatible release schema or managed policy/routing ABI")
	}
	if err := manifest.Identity.validate(); err != nil {
		return err
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > defaultLimits().members-2 {
		return errors.New("invalid release file count")
	}
	var total int64
	for name, file := range manifest.Files {
		if err := manifestMemberName(name, false); err != nil {
			return err
		}
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != file.SHA256 || file.Size < 0 || file.Size > defaultLimits().file || file.Size > defaultLimits().total-total {
			return fmt.Errorf("invalid release file metadata: %q", name)
		}
		total += file.Size
	}
	core, _ := releaseCoreName(manifest.Identity.Platform)
	for _, name := range []string{"x-ui", "update-stage", "update.sh", "install.sh", "x-ui.sh", "x-ui.rc", core} {
		if file, ok := manifest.Files[name]; !ok || file.Size == 0 || !file.Executable {
			return fmt.Errorf("release requires nonempty executable %q", name)
		}
	}
	for _, name := range []string{"x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel"} {
		if file, ok := manifest.Files[name]; !ok || file.Size == 0 || file.Executable {
			return fmt.Errorf("release requires nonempty nonexecutable unit %q", name)
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(data) > maxManifestBytes || int64(len(data))+total > defaultLimits().total {
		return errors.New("release manifest or aggregate payload exceeds size limit")
	}
	return nil
}

// BuildManifest records a complete bundle inventory; ABI fields are declarations.
// Actual panel/core capability checks must run before activating the bundle.
func BuildManifest(ctx context.Context, directory string, identity ReleaseIdentity) (*ReleaseManifest, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files, err := releaseInventory(ctx, root)
	if err != nil {
		return nil, err
	}
	manifest := &ReleaseManifest{Schema: 1, Identity: identity, PolicyABI: 1, RoutingABI: 1, Files: files}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// WriteManifest creates release.json exclusively and never replaces an existing file.
func WriteManifest(directory string, manifest *ReleaseManifest) error {
	if err := manifest.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(ManifestName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Close())
	if err != nil {
		return errors.Join(err, root.Remove(ManifestName))
	}
	return nil
}

// VerifyManifest checks exact source identity and every file before activation.
func VerifyManifest(ctx context.Context, directory string, expected ReleaseIdentity) (*ReleaseManifest, error) {
	if err := expected.validate(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	manifest, err := readReleaseManifest(ctx, root)
	if err != nil {
		return nil, err
	}
	if manifest.Identity != expected {
		return nil, errors.New("release identity differs from the selected repository/commit/tag/platform")
	}
	files, err := releaseInventory(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(files) != len(manifest.Files) {
		return nil, errors.New("release inventory contains missing or unlisted files")
	}
	for name, want := range manifest.Files {
		if files[name] != want {
			return nil, fmt.Errorf("release file differs from its manifest: %q", name)
		}
	}
	return manifest, nil
}

func readReleaseManifest(ctx context.Context, root *os.Root) (*ReleaseManifest, error) {
	info, err := root.Lstat(ManifestName)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() > maxManifestBytes {
		return nil, errors.New("release manifest must be a bounded nonexecutable regular file")
	}
	f, err := root.Open(ManifestName)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, f}, maxManifestBytes+1))
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
		return nil, errors.New("release manifest exceeds size limit")
	}
	if err := uniqueJSON(data); err != nil {
		return nil, err
	}
	var manifest ReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}
