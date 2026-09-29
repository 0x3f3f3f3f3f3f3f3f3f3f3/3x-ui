//go:build linux

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

type coreUpdateTransport func(*http.Request) (*http.Response, error)

func (f coreUpdateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManagedCoreCatalogUsesForkTagsAndPreservesPrereleaseStatus(t *testing.T) {
	if _, err := managedCoreUpdatePlatform(); err != nil {
		t.Skipf("native-host release catalog requires an eligible installation: %v", err)
	}
	setupConflictDB(t)
	info, err := config.GetReleaseInfo()
	if err != nil {
		t.Fatal(err)
	}
	assets := []any{}
	base := "x-ui-" + info.Platform
	for i, name := range []string{base + ".tar.gz", base + ".tar.gz.sha256", base + ".release.json"} {
		assets = append(assets, map[string]any{"id": i + 1, "name": name, "state": "uploaded", "size": 128, "digest": "sha256:" + strings.Repeat("a", 64)})
	}
	releases := []any{
		map[string]any{"tag_name": "panel-v3.8.5", "draft": false, "prerelease": false, "assets": assets},
		map[string]any{"tag_name": "dev-latest", "draft": false, "prerelease": true, "assets": assets},
	}
	data, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	requests := 0
	http.DefaultTransport = coreUpdateTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != "https://api.github.com/repos/"+updatebundle.ReleaseRepository+"/releases?per_page=20" {
			t.Errorf("catalog selected an unexpected source: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	svc := &ServerService{}
	got, err := svc.GetManagedCoreReleases(t.Context())
	want := []updatebundle.ReleaseCandidate{{Tag: "panel-v3.8.5"}, {Tag: "dev-latest", Prerelease: true}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog = %+v, %v", got, err)
	}
	got[0].Tag = "changed-by-caller"
	versions, err := svc.GetXrayVersionsCached()
	if err != nil || !reflect.DeepEqual(versions, []string{"panel-v3.8.5", "dev-latest"}) || requests != 1 {
		t.Fatalf("legacy catalog/cache = %+v, %v, requests=%d", versions, err, requests)
	}
}

func TestManagedCoreUpdateRejectsContainerBeforeDownload(t *testing.T) {
	t.Setenv("XUI_IN_DOCKER", "true")
	svc := &ServerService{}
	if err := svc.UpdateXrayContext(context.Background(), "fixture"); !errors.Is(err, ErrCoreUpdateInContainer) {
		t.Fatalf("container update = %v", err)
	}
	if _, err := svc.GetManagedCoreReleases(context.Background()); !errors.Is(err, ErrCoreUpdateInContainer) {
		t.Fatalf("container catalog = %v", err)
	}
}
