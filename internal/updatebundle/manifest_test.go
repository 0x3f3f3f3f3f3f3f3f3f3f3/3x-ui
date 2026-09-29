package updatebundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseIdentity() ReleaseIdentity {
	return ReleaseIdentity{Repository: "0x3f3f3f3f3f3f3f3f3f3f3/3x-ui", Commit: strings.Repeat("a", 40), Tag: "v3.8.5-managed.1", Platform: "linux-arm64"}
}

func manifestFixture(t *testing.T, platform string) (string, ReleaseIdentity) {
	t.Helper()
	identity := releaseIdentity()
	identity.Platform = platform
	dir := t.TempDir()
	files := map[string]bool{
		"x-ui": true, "update-stage": true, "update.sh": true, "install.sh": true,
		"x-ui.sh": true, "x-ui.rc": true, "x-ui.service.debian": false,
		"x-ui.service.arch": false, "x-ui.service.rhel": false, "bin/geoip.dat": false,
	}
	arch := strings.TrimPrefix(platform, "linux-")
	if strings.HasPrefix(arch, "armv") {
		arch = "arm32"
	}
	files["bin/xray-linux-"+arch] = true
	for name, executable := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte("fixture:"+name), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir, identity
}

func writeFixtureManifest(t *testing.T, dir string, manifest *ReleaseManifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRoundTripAndHashes(t *testing.T) {
	for _, platform := range []string{"linux-arm64", "linux-armv7", "linux-amd64"} {
		t.Run(platform, func(t *testing.T) {
			dir, identity := manifestFixture(t, platform)
			manifest, err := BuildManifest(t.Context(), dir, identity)
			if err != nil {
				t.Fatal(err)
			}
			if len(manifest.Files) != 11 || manifest.Identity != identity || manifest.Schema != 1 || manifest.PolicyABI != 1 || manifest.RoutingABI != 1 {
				t.Fatalf("incomplete identity/inventory: %+v", manifest)
			}
			for name, file := range manifest.Files {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || file.SHA256 != digest(data) || file.Size != int64(len(data)) {
					t.Fatalf("incorrect file digest: %s %+v, %v", name, file, err)
				}
			}
			if err := WriteManifest(dir, manifest); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyManifest(t.Context(), dir, identity); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "release.json"))
			if err := WriteManifest(dir, manifest); err == nil {
				t.Fatal("overwrote existing manifest")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "release.json"))
			if string(after) != string(before) {
				t.Fatal("existing manifest changed")
			}
		})
	}
}

func TestManifestRejectsIncompatibleIdentity(t *testing.T) {
	for _, name := range []string{"repository", "commit", "tag", "platform", "schema", "policy", "routing"} {
		t.Run(name, func(t *testing.T) {
			dir, identity := manifestFixture(t, "linux-arm64")
			manifest, err := BuildManifest(t.Context(), dir, identity)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "repository":
				manifest.Identity.Repository = "MHSanaei/3x-ui"
			case "commit":
				manifest.Identity.Commit = strings.Repeat("b", 40)
			case "tag":
				manifest.Identity.Tag = "dev-latest"
			case "platform":
				manifest.Identity.Platform = "linux-amd64"
			case "schema":
				manifest.Schema++
			case "policy":
				manifest.PolicyABI = 0
			case "routing":
				manifest.RoutingABI = 0
			}
			writeFixtureManifest(t, dir, manifest)
			if got, err := VerifyManifest(t.Context(), dir, identity); err == nil || got != nil {
				t.Fatalf("incompatible manifest accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestManifestRejectsInvalidBuildIdentity(t *testing.T) {
	for _, name := range []string{"repo", "short-commit", "upper-commit", "empty-tag", "tag-path", "unsupported-platform"} {
		t.Run(name, func(t *testing.T) {
			dir, identity := manifestFixture(t, "linux-arm64")
			switch name {
			case "repo":
				identity.Repository = "MHSanaei/3x-ui"
			case "short-commit":
				identity.Commit = "deadbeef"
			case "upper-commit":
				identity.Commit = strings.Repeat("A", 40)
			case "empty-tag":
				identity.Tag = ""
			case "tag-path":
				identity.Tag = "../../main"
			case "unsupported-platform":
				identity.Platform = "linux-unknown"
			}
			if got, err := BuildManifest(t.Context(), dir, identity); err == nil || got != nil {
				t.Fatalf("invalid build identity accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestManifestRejectsChangedInventory(t *testing.T) {
	for _, name := range []string{"missing-file", "missing-core", "wrong-core-arch", "not-executable", "tampered", "extra-file", "symlink-file", "symlink-directory", "privileged-file", "privileged-directory", "empty-required", "unlisted-file", "forged-size", "forged-hash", "unsafe-manifest-path"} {
		t.Run(name, func(t *testing.T) {
			dir, identity := manifestFixture(t, "linux-arm64")
			manifest, err := BuildManifest(t.Context(), dir, identity)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing-file":
				os.Remove(filepath.Join(dir, "update.sh"))
			case "missing-core":
				os.Remove(filepath.Join(dir, "bin/xray-linux-arm64"))
			case "wrong-core-arch":
				os.Rename(filepath.Join(dir, "bin/xray-linux-arm64"), filepath.Join(dir, "bin/xray-linux-amd64"))
			case "not-executable":
				os.Chmod(filepath.Join(dir, "x-ui"), 0o644)
			case "tampered":
				os.WriteFile(filepath.Join(dir, "x-ui"), []byte("changed"), 0o755)
			case "extra-file":
				os.WriteFile(filepath.Join(dir, "extra"), []byte("extra"), 0o644)
			case "symlink-file":
				os.Remove(filepath.Join(dir, "x-ui"))
				os.Symlink("update.sh", filepath.Join(dir, "x-ui"))
			case "symlink-directory":
				os.Rename(filepath.Join(dir, "bin"), filepath.Join(dir, "moved"))
				os.Symlink("moved", filepath.Join(dir, "bin"))
			case "privileged-file":
				os.Chmod(filepath.Join(dir, "x-ui"), 0o755|os.ModeSetuid)
			case "privileged-directory":
				os.Chmod(filepath.Join(dir, "bin"), 0o755|os.ModeSticky)
			case "empty-required":
				os.WriteFile(filepath.Join(dir, "x-ui"), nil, 0o755)
			case "unlisted-file":
				delete(manifest.Files, "update.sh")
			case "forged-size":
				file := manifest.Files["x-ui"]
				file.Size++
				manifest.Files["x-ui"] = file
			case "forged-hash":
				file := manifest.Files["x-ui"]
				file.SHA256 = strings.Repeat("0", 64)
				manifest.Files["x-ui"] = file
			case "unsafe-manifest-path":
				manifest.Files["../outside"] = manifest.Files["x-ui"]
			}
			writeFixtureManifest(t, dir, manifest)
			if got, err := VerifyManifest(t.Context(), dir, identity); err == nil || got != nil {
				t.Fatalf("modified inventory accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestManifestRejectsAmbiguousJSON(t *testing.T) {
	for _, name := range []string{"unknown", "duplicate-top", "duplicate-nested", "case-alias-top", "case-alias-identity", "case-alias-file", "missing-field", "null-executable", "trailing", "oversized", "symlink-manifest", "executable-manifest"} {
		t.Run(name, func(t *testing.T) {
			dir, identity := manifestFixture(t, "linux-arm64")
			manifest, err := BuildManifest(t.Context(), dir, identity)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(manifest)
			switch name {
			case "unknown":
				data = append([]byte(`{"unknown":true,`), data[1:]...)
			case "duplicate-top":
				data = append([]byte(`{"schema":1,`), data[1:]...)
			case "duplicate-nested":
				data = []byte(strings.Replace(string(data), `"identity":{`, `"identity":{"repository":"wrong",`, 1))
			case "case-alias-top":
				data = append([]byte(`{"Schema":0,`), data[1:]...)
			case "case-alias-identity":
				data = []byte(strings.Replace(string(data), `"repository":`, `"Repository":`, 1))
			case "case-alias-file":
				data = []byte(strings.Replace(string(data), `"sha256":`, `"SHA256":`, 1))
			case "missing-field":
				data = []byte(strings.Replace(string(data), `,"executable":false`, ``, 1))
			case "null-executable":
				data = []byte(strings.Replace(string(data), `"executable":false`, `"executable":null`, 1))
			case "trailing":
				data = append(data, []byte(`{}`)...)
			case "oversized":
				data = append(data, []byte(strings.Repeat(" ", 1<<20))...)
			}
			path := filepath.Join(dir, "release.json")
			os.WriteFile(path, data, 0o644)
			if name == "symlink-manifest" {
				os.Rename(path, filepath.Join(dir, "original.json"))
				os.Symlink("original.json", path)
			}
			if name == "executable-manifest" {
				os.Chmod(path, 0o755)
			}
			if got, err := VerifyManifest(t.Context(), dir, identity); err == nil || got != nil {
				t.Fatalf("ambiguous manifest accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestManifestCancellation(t *testing.T) {
	dir, identity := manifestFixture(t, "linux-arm64")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := BuildManifest(ctx, dir, identity); err == nil || got != nil {
		t.Fatalf("canceled build accepted: %+v, %v", got, err)
	}
}

func TestBuildManifestRejectsIncompleteBundle(t *testing.T) {
	for _, name := range []string{"x-ui", "update-stage", "install.sh", "update.sh", "x-ui.sh", "x-ui.rc", "x-ui.service.debian", "x-ui.service.arch", "x-ui.service.rhel", "bin/xray-linux-arm64"} {
		t.Run(name, func(t *testing.T) {
			dir, identity := manifestFixture(t, "linux-arm64")
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if got, err := BuildManifest(t.Context(), dir, identity); err == nil || got != nil {
				t.Fatalf("incomplete release accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestBuildManifestRejectsOversizedFileBeforeReading(t *testing.T) {
	dir, identity := manifestFixture(t, "linux-arm64")
	file, err := os.OpenFile(filepath.Join(dir, "bin/large.dat"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((512 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if got, err := BuildManifest(t.Context(), dir, identity); err == nil || got != nil {
		t.Fatalf("oversized release accepted: %+v, %v", got, err)
	}
}
