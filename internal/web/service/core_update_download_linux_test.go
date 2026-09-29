//go:build linux

package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type coreReleaseHTTPFixture struct {
	identity    updatebundle.ReleaseIdentity
	archive     string
	archiveSize int64
	archiveHash string
}

func realCoreReleaseHTTPFixture(t *testing.T, panel, core string) coreReleaseHTTPFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, panel, "release-info").Output()
	if err != nil {
		t.Fatal(err)
	}
	var info config.ReleaseInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Modified || len(info.Commit) != 40 {
		t.Fatal("actual panel fixture requires a known, unmodified source")
	}
	directory, identity := coreUpdateTestBundle(t, core)
	identity.Commit = info.Commit
	for _, path := range []string{"x-ui", updatebundle.ManifestName} {
		if err := os.Remove(filepath.Join(directory, path)); err != nil {
			t.Fatal(err)
		}
	}
	copyUpdateTestCore(t, panel, filepath.Join(directory, "x-ui"))
	manifest, err := updatebundle.BuildManifest(t.Context(), directory, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := updatebundle.WriteManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	archive, err := os.CreateTemp(t.TempDir(), "release-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	hash := sha256.New()
	zipped, err := gzip.NewWriterLevel(io.MultiWriter(archive, hash), gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(zipped)
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if err := writer.WriteHeader(&tar.Header{Name: "x-ui/" + filepath.ToSlash(name), Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg, Format: tar.FormatGNU}); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, input)
		_ = input.Close()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := archive.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return coreReleaseHTTPFixture{identity: identity, archive: archive.Name(), archiveSize: stat.Size(), archiveHash: hex.EncodeToString(hash.Sum(nil))}
}

func (fixture coreReleaseHTTPFixture) client(t *testing.T, mode string, beforeDownload func()) *http.Client {
	t.Helper()
	name := "x-ui-" + fixture.identity.Platform + ".tar.gz"
	checksum := fixture.archiveHash + "  " + name + "\n"
	if mode == "checksum" {
		checksum = strings.Repeat("0", 64) + "  " + name + "\n"
	}
	sumHash := sha256.Sum256([]byte(checksum))
	release := map[string]any{"tag_name": fixture.identity.Tag, "draft": false, "prerelease": false, "assets": []any{
		map[string]any{"id": 17, "name": name, "state": "uploaded", "size": fixture.archiveSize, "digest": "sha256:" + fixture.archiveHash},
		map[string]any{"id": 19, "name": name + ".sha256", "state": "uploaded", "size": len(checksum), "digest": fmt.Sprintf("sha256:%x", sumHash)},
	}}
	metadata, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: coreUpdateTransport(func(r *http.Request) (*http.Response, error) {
		prefix := "https://api.github.com/repos/" + updatebundle.ReleaseRepository
		var body string
		switch r.URL.String() {
		case prefix + "/releases/tags/" + fixture.identity.Tag:
			body = string(metadata)
		case prefix + "/git/ref/tags/" + fixture.identity.Tag:
			commit := fixture.identity.Commit
			if mode == "source" {
				commit = strings.Repeat("b", 40)
			}
			body = fmt.Sprintf(`{"object":{"type":"commit","sha":%q}}`, commit)
		case prefix + "/releases/assets/19":
			beforeDownload()
			body = checksum
		case prefix + "/releases/assets/17":
			file, err := os.Open(fixture.archive)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: file, ContentLength: fixture.archiveSize}, nil
		default:
			t.Errorf("unexpected source request: %s", r.URL)
			return nil, fmt.Errorf("unexpected fixture source")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
	})}
}

func TestManagedCoreDownloadPreflightAndActivationWithRealCores(t *testing.T) {
	panel, managed, stock := os.Getenv("XUI_E2E_PANEL"), os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY"), os.Getenv("XUI_STOCK_XRAY_E2E_BINARY")
	if panel == "" || managed == "" || stock == "" {
		t.Skip("set actual panel, managed and stock core binaries for update acceptance")
	}
	good := realCoreReleaseHTTPFixture(t, panel, managed)
	unmanaged := realCoreReleaseHTTPFixture(t, panel, stock)
	for _, mode := range []string{"checksum", "source", "stock", "managed"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, "tcp")
			if err := os.Remove(xray.GetBinaryPath()); err != nil {
				t.Fatal(err)
			}
			copyUpdateTestCore(t, managed, xray.GetBinaryPath())
			original := currentXrayProcess()
			flows := openProductionMieruFlows(t, fixture.client)
			selected := good
			if mode == "stock" {
				selected = unmanaged
			}
			downloadObserved := false
			client := selected.client(t, mode, func() {
				downloadObserved = true
				if currentXrayProcess() != original || !original.IsRunning() {
					t.Fatal("stopped original core before downloading the release")
				}
				for _, flow := range flows {
					flow.echo(t)
				}
			})
			parent := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			err := (&ServerService{}).updateManagedCore(ctx, client, selected.identity.Tag, selected.identity.Platform, parent)
			if !downloadObserved {
				t.Fatal("did not reach actual release download")
			}
			if mode == "managed" {
				if err != nil {
					t.Fatal(err)
				}
				if currentXrayProcess() == original || !currentXrayProcess().IsRunning() {
					t.Fatal("verified managed core was not activated")
				}
				openProductionMieruFlows(t, fixture.client)
			} else {
				if err == nil {
					t.Fatal("unverified or stock release was accepted")
				}
				if mode == "stock" && !strings.Contains(err.Error(), "candidate runtime preflight failed") {
					t.Fatalf("stock core was not rejected by actual runtime preflight: %v", err)
				}
				if currentXrayProcess() != original || !original.IsRunning() {
					t.Fatalf("failed preflight changed original process: %v", err)
				}
				for _, flow := range flows {
					flow.echo(t)
				}
			}
			record := lookupClientRecord(t, fixture.user.Email)
			account, err := database.NewClientUsageLedger(database.GetDB()).Read(t.Context(), record.PolicyID)
			if err != nil || account.Up != 132 || account.Down != 132 || account.Billed != 396 {
				t.Fatalf("download/preflight/update lost current billed payload: %+v, %v", account, err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("download or preflight stage leaked: %v, %v", entries, err)
			}
		})
	}
}
