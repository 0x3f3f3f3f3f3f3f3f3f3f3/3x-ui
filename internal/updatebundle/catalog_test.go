package updatebundle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func catalogRelease(tag string, prerelease bool) map[string]any {
	base := "x-ui-linux-arm64"
	assets := make([]map[string]any, 0, 3)
	for i, name := range []string{base + ".tar.gz", base + ".tar.gz.sha256", base + ".release.json"} {
		assets = append(assets, map[string]any{"id": i + 1, "name": name, "state": "uploaded", "size": 128, "digest": "sha256:" + strings.Repeat("a", 64)})
	}
	return map[string]any{"tag_name": tag, "draft": false, "prerelease": prerelease, "assets": assets}
}

func TestListReleaseCandidatesFiltersIncompleteForkMetadata(t *testing.T) {
	releases := []map[string]any{catalogRelease("stable", false), catalogRelease("dev-latest", true), catalogRelease("stable", false)}
	for _, mode := range []string{"draft", "missing-status", "unsafe-tag", "missing-archive", "missing-checksum", "missing-manifest", "asset-digest", "asset-state", "asset-size", "asset-duplicate", "other-platform"} {
		release := catalogRelease(mode, false)
		assets := release["assets"].([]map[string]any)
		switch mode {
		case "draft":
			release["draft"] = true
		case "missing-status":
			delete(release, "prerelease")
		case "unsafe-tag":
			release["tag_name"] = "../../upstream"
		case "missing-archive":
			release["assets"] = assets[1:]
		case "missing-checksum":
			release["assets"] = append(assets[:1:1], assets[2])
		case "missing-manifest":
			release["assets"] = assets[:2]
		case "asset-digest":
			assets[0]["digest"] = ""
		case "asset-state":
			assets[0]["state"] = "new"
		case "asset-size":
			assets[2]["size"] = maxManifestBytes + 1
		case "asset-duplicate":
			release["assets"] = append(assets, assets[0])
		case "other-platform":
			assets[0]["name"] = "x-ui-linux-amd64.tar.gz"
		}
		releases = append(releases, release)
	}
	data, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != "https://api.github.com/repos/"+ReleaseRepository+"/releases?per_page=20" {
			t.Errorf("catalog left fixed fork metadata endpoint: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	got, err := ListReleaseCandidates(t.Context(), client, "linux-arm64")
	want := []ReleaseCandidate{{Tag: "stable"}, {Tag: "dev-latest", Prerelease: true}}
	if err != nil || !reflect.DeepEqual(got, want) || requests != 1 {
		t.Fatalf("catalog=%+v error=%v requests=%d", got, err, requests)
	}
}

func TestListReleaseCandidatesRejectsInvalidResponsesAndRequests(t *testing.T) {
	for _, mode := range []string{"empty", "null", "object", "trailing", "oversized", "too-many", "status", "nil-client", "platform", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			body, status := "[]", http.StatusOK
			switch mode {
			case "null":
				body = "null"
			case "object":
				body = `{}`
			case "trailing":
				body = `[] {}`
			case "oversized":
				body = strings.Repeat(" ", (1<<20)+1)
			case "too-many":
				body = "[" + strings.Repeat("{},", 20) + "{}]"
			case "status":
				status = http.StatusServiceUnavailable
			}
			requested := false
			client := &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
				requested = true
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			platform := "linux-arm64"
			switch mode {
			case "nil-client":
				client = nil
			case "platform":
				platform = "unsupported"
			case "canceled":
				cancel()
			}
			got, err := ListReleaseCandidates(ctx, client, platform)
			if mode == "empty" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty published list: %+v %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("invalid catalog accepted: %+v %v", got, err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if (mode == "nil-client" || mode == "platform" || mode == "canceled") && requested {
				t.Fatal("invalid request reached HTTP transport")
			}
		})
	}
}
