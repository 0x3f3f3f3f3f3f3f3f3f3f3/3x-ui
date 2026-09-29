package config

import (
	"encoding/hex"
	"errors"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

var buildSourceCommit string

type ReleaseInfo struct {
	Repository   string `json:"repository"`
	Commit       string `json:"commit"`
	Modified     bool   `json:"modified"`
	Platform     string `json:"platform"`
	PanelVersion string `json:"panelVersion"`
	PolicyABI    int    `json:"policyABI"`
	RoutingABI   int    `json:"routingABI"`
}

// GetReleaseInfo reports compiled declarations without reading service configuration.
func GetReleaseInfo() (ReleaseInfo, error) {
	build, _ := debug.ReadBuildInfo()
	return releaseInfoFromBuild(runtime.GOOS, runtime.GOARCH, buildSourceCommit, build)
}

func releaseInfoFromBuild(goos, arch, stamp string, build *debug.BuildInfo) (ReleaseInfo, error) {
	commit, arm := stamp, ""
	modified := false
	if build != nil {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" && stamp == "" {
				commit = setting.Value
			}
			if setting.Key == "GOARM" {
				arm, _, _ = strings.Cut(setting.Value, ",")
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				modified = true
			}
		}
	}
	if commit != "" {
		decoded, err := hex.DecodeString(commit)
		if err != nil || len(decoded) != 20 || hex.EncodeToString(decoded) != commit {
			return ReleaseInfo{}, errors.New("compiled source commit is not a full lowercase Git SHA")
		}
	}
	if arch == "arm" && (arm == "5" || arm == "6" || arm == "7") {
		arch = "armv" + arm
	}
	return ReleaseInfo{
		Repository: updatebundle.ReleaseRepository, Commit: commit, Modified: modified, Platform: goos + "-" + arch,
		PanelVersion: GetPanelVersion(), PolicyABI: 1, RoutingABI: 1,
	}, nil
}
