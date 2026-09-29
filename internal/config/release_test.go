package config

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestReleaseInfoSourceAndPlatform(t *testing.T) {
	for _, tc := range []struct {
		name, os, arch, arm, stamp, vcs, wantCommit, wantPlatform string
	}{
		{name: "stamp", os: "linux", arch: "arm64", stamp: strings.Repeat("a", 40), vcs: strings.Repeat("b", 40), wantCommit: strings.Repeat("a", 40), wantPlatform: "linux-arm64"},
		{name: "vcs-fallback", os: "linux", arch: "amd64", vcs: strings.Repeat("b", 40), wantCommit: strings.Repeat("b", 40), wantPlatform: "linux-amd64"},
		{name: "arm5", os: "linux", arch: "arm", arm: "5,softfloat", wantPlatform: "linux-armv5"},
		{name: "arm6", os: "linux", arch: "arm", arm: "6", wantPlatform: "linux-armv6"},
		{name: "arm7", os: "linux", arch: "arm", arm: "7,hardfloat", wantPlatform: "linux-armv7"},
		{name: "unknown-arm", os: "linux", arch: "arm", wantPlatform: "linux-arm"},
		{name: "windows", os: "windows", arch: "amd64", wantPlatform: "windows-amd64"},
		{name: "mac", os: "darwin", arch: "arm64", wantPlatform: "darwin-arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "GOARM", Value: tc.arm}, {Key: "vcs.revision", Value: tc.vcs}}}
			info, err := releaseInfoFromBuild(tc.os, tc.arch, tc.stamp, build)
			if err != nil {
				t.Fatal(err)
			}
			if info.Commit != tc.wantCommit || info.Platform != tc.wantPlatform || info.PanelVersion != GetPanelVersion() || info.Repository != "0x3f3f3f3f3f3f3f3f3f3f3/3x-ui" || info.PolicyABI != 1 || info.RoutingABI != 1 {
				t.Fatalf("incorrect release info: %+v", info)
			}
		})
	}
}

func TestReleaseInfoRejectsMalformedCommit(t *testing.T) {
	for _, value := range []string{"deadbeef", strings.Repeat("A", 40), strings.Repeat("z", 40), " " + strings.Repeat("a", 40)} {
		t.Run(value, func(t *testing.T) {
			if _, err := releaseInfoFromBuild("linux", "arm64", value, nil); err == nil {
				t.Fatal("malformed build source accepted")
			}
			build := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: value}}}
			if _, err := releaseInfoFromBuild("linux", "arm64", "", build); err == nil {
				t.Fatal("malformed VCS source accepted")
			}
		})
	}
}

func TestReleaseInfoDoesNotInventSource(t *testing.T) {
	info, err := releaseInfoFromBuild("linux", "arm64", "", nil)
	if err != nil || info.Commit != "" {
		t.Fatalf("unknown build must report an empty source: %+v, %v", info, err)
	}
}

func TestReleaseInfoReportsModifiedSource(t *testing.T) {
	build := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: strings.Repeat("b", 40)}, {Key: "vcs.modified", Value: "true"}}}
	for _, stamp := range []string{"", strings.Repeat("a", 40)} {
		info, err := releaseInfoFromBuild("linux", "arm64", stamp, build)
		if err != nil || !info.Modified {
			t.Fatalf("modified source hidden by stamp %q: %+v, %v", stamp, info, err)
		}
	}
}
