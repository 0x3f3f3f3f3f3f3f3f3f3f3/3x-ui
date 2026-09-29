package updatebundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type releaseAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

type releaseResponse struct {
	Tag        string         `json:"tag_name"`
	Draft      *bool          `json:"draft"`
	Prerelease *bool          `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseDownloader struct {
	client *http.Client
	api    string
}

// DownloadRelease binds a fork release to its tag commit and verified asset bytes.
// It returns an owned stage; the candidate must still pass its runtime preflight.
func DownloadRelease(ctx context.Context, client *http.Client, tag, platform, parent string) (string, ReleaseIdentity, error) {
	return downloadRelease(ctx, client, "https://api.github.com", tag, platform, parent)
}

func downloadRelease(ctx context.Context, client *http.Client, api, tag, platform, parent string) (stage string, identity ReleaseIdentity, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	d := releaseDownloader{client: client, api: strings.TrimRight(api, "/") + "/repos/" + ReleaseRepository}
	selected, err := d.selectRelease(ctx, tag, platform)
	if err != nil {
		return "", identity, err
	}
	identity = selected.identity
	archive, checksum := selected.archive, selected.checksum
	name := archive.Name
	var sum bytes.Buffer
	if err := d.asset(ctx, checksum, &sum); err != nil {
		return "", identity, err
	}
	fields := strings.Fields(sum.String())
	if len(fields) != 2 || fields[1] != name || !validReleaseDigest(fields[0]) || archive.Digest != "sha256:"+fields[0] {
		return "", identity, errors.New("release checksum differs from the selected asset")
	}
	download, err := os.MkdirTemp(parent, ".x-ui-download-")
	if err != nil {
		return "", identity, err
	}
	file, err := os.CreateTemp(download, "archive-")
	if err != nil {
		return "", identity, errors.Join(err, os.RemoveAll(download))
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close(), os.RemoveAll(download))
		if resultErr != nil && stage != "" {
			resultErr = errors.Join(resultErr, os.RemoveAll(stage))
			stage = ""
		}
	}()
	if err := d.asset(ctx, archive, file); err != nil {
		return "", identity, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", identity, err
	}
	stage, err = Stage(ctx, file, parent, fields[0])
	if err != nil {
		return "", identity, err
	}
	if _, err := VerifyManifest(ctx, filepath.Join(stage, "x-ui"), identity); err != nil {
		return stage, identity, err
	}
	return stage, identity, nil
}

type releaseSelection struct {
	identity ReleaseIdentity
	archive  releaseAsset
	checksum releaseAsset
}

// ResolveReleaseIdentity selects the same published tag and archive metadata as
// DownloadRelease without fetching asset bodies. Runtime compatibility still
// requires the complete download, manifest and candidate preflight.
func ResolveReleaseIdentity(ctx context.Context, client *http.Client, tag, platform string) (ReleaseIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := releaseDownloader{client: client, api: "https://api.github.com/repos/" + ReleaseRepository}
	selected, err := d.selectRelease(ctx, tag, platform)
	return selected.identity, err
}

func (d releaseDownloader) selectRelease(ctx context.Context, tag, platform string) (releaseSelection, error) {
	if d.client == nil {
		return releaseSelection{}, errors.New("release download requires an HTTP client")
	}
	if tag != "" && !releaseTagPattern.MatchString(tag) {
		return releaseSelection{}, errors.New("invalid selected release tag")
	}
	if _, err := releaseCoreName(platform); err != nil {
		return releaseSelection{}, err
	}
	resource := "/releases/latest"
	if tag != "" {
		resource = "/releases/tags/" + url.PathEscape(tag)
	}
	var release releaseResponse
	if err := d.metadata(ctx, resource, &release); err != nil {
		return releaseSelection{}, err
	}
	if release.Draft == nil || release.Prerelease == nil || !releaseTagPattern.MatchString(release.Tag) {
		return releaseSelection{}, errors.New("release metadata lacks a valid tag or publication status")
	}
	if *release.Draft {
		return releaseSelection{}, errors.New("refusing draft release")
	}
	if tag == "" && *release.Prerelease {
		return releaseSelection{}, errors.New("latest stable release is a prerelease")
	}
	if tag != "" && release.Tag != tag {
		return releaseSelection{}, errors.New("release tag differs from the selection")
	}
	commit, err := d.tagCommit(ctx, release.Tag)
	if err != nil {
		return releaseSelection{}, err
	}
	identity := ReleaseIdentity{Repository: ReleaseRepository, Commit: commit, Tag: release.Tag, Platform: platform}
	name := "x-ui-" + platform + ".tar.gz"
	archive, err := selectReleaseAsset(release.Assets, name, defaultLimits().compressed)
	if err != nil {
		return releaseSelection{}, err
	}
	checksum, err := selectReleaseAsset(release.Assets, name+".sha256", 4096)
	if err != nil {
		return releaseSelection{}, fmt.Errorf("release checksum: %w", err)
	}
	return releaseSelection{identity: identity, archive: archive, checksum: checksum}, nil
}

func validReleaseDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func selectReleaseAsset(assets []releaseAsset, name string, limit int64) (releaseAsset, error) {
	var selected releaseAsset
	for _, asset := range assets {
		if asset.Name != name {
			continue
		}
		if selected.ID != 0 {
			return releaseAsset{}, fmt.Errorf("duplicate release asset %q", name)
		}
		if asset.ID <= 0 || asset.State != "uploaded" || asset.Size <= 0 || asset.Size > limit || !strings.HasPrefix(asset.Digest, "sha256:") || !validReleaseDigest(strings.TrimPrefix(asset.Digest, "sha256:")) {
			return releaseAsset{}, fmt.Errorf("release asset %q has invalid size, state or checksum", name)
		}
		selected = asset
	}
	if selected.ID == 0 {
		return releaseAsset{}, fmt.Errorf("missing release asset %q", name)
	}
	return selected, nil
}

func (d releaseDownloader) request(ctx context.Context, resource, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.api+resource, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "3x-ui-managed-release/1")
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := d.client.Do(req)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("release HTTP request failed")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("release request returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (d releaseDownloader) metadata(ctx context.Context, resource string, value any) error {
	response, err := d.request(ctx, resource, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("reading release metadata failed")
	}
	if len(data) > 1<<20 {
		return errors.New("release metadata exceeds size limit")
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("invalid release metadata: %w", err)
	}
	return nil
}

func (d releaseDownloader) tagCommit(ctx context.Context, tag string) (string, error) {
	resource := "/git/ref/tags/" + url.PathEscape(tag)
	seen := make(map[string]bool)
	for range 5 {
		var ref struct {
			Object struct {
				Type string `json:"type"`
				SHA  string `json:"sha"`
			} `json:"object"`
		}
		if err := d.metadata(ctx, resource, &ref); err != nil {
			return "", err
		}
		if !releaseCommitPattern.MatchString(ref.Object.SHA) || seen[ref.Object.SHA] {
			return "", errors.New("invalid or cyclic release tag reference")
		}
		if ref.Object.Type == "commit" {
			return ref.Object.SHA, nil
		}
		if ref.Object.Type != "tag" {
			return "", errors.New("release tag does not reference a commit")
		}
		seen[ref.Object.SHA] = true
		resource = "/git/tags/" + ref.Object.SHA
	}
	return "", errors.New("release tag reference nesting exceeds limit")
}

func (d releaseDownloader) asset(ctx context.Context, asset releaseAsset, out io.Writer) error {
	response, err := d.request(ctx, "/releases/assets/"+strconv.FormatInt(asset.ID, 10), "application/octet-stream")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != asset.Size {
		return errors.New("release asset size differs from selected metadata")
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(response.Body, asset.Size+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("reading release asset failed")
	}
	if n != asset.Size {
		return errors.New("release asset size differs from selected metadata")
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != asset.Digest {
		return errors.New("release asset checksum differs from selected metadata")
	}
	return nil
}
