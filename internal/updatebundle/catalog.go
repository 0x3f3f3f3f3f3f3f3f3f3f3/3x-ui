package updatebundle

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type ReleaseCandidate struct {
	Tag        string `json:"tag"`
	Prerelease bool   `json:"prerelease"`
}

// ListReleaseCandidates filters one bounded page of this fork's release metadata.
// Candidates still require full download, source verification and runtime preflight.
func ListReleaseCandidates(ctx context.Context, client *http.Client, platform string) ([]ReleaseCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("release catalog requires an HTTP client")
	}
	if _, err := releaseCoreName(platform); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := releaseDownloader{client: client, api: "https://api.github.com/repos/" + ReleaseRepository}
	var releases []releaseResponse
	if err := d.metadata(ctx, "/releases?per_page=20", &releases); err != nil {
		return nil, err
	}
	if releases == nil || len(releases) > 20 {
		return nil, errors.New("release catalog is null or exceeds its page limit")
	}
	base := "x-ui-" + platform
	result := make([]ReleaseCandidate, 0, len(releases))
	seen := make(map[string]bool)
	for _, release := range releases {
		if release.Draft == nil || *release.Draft || release.Prerelease == nil || !releaseTagPattern.MatchString(release.Tag) || seen[release.Tag] {
			continue
		}
		if _, err := selectReleaseAsset(release.Assets, base+".tar.gz", defaultLimits().compressed); err != nil {
			continue
		}
		if _, err := selectReleaseAsset(release.Assets, base+".tar.gz.sha256", 4096); err != nil {
			continue
		}
		if _, err := selectReleaseAsset(release.Assets, base+".release.json", maxManifestBytes); err != nil {
			continue
		}
		seen[release.Tag] = true
		result = append(result, ReleaseCandidate{Tag: release.Tag, Prerelease: *release.Prerelease})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
