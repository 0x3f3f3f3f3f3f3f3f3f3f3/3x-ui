package updatebundle

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func downloadArchiveFixture(t *testing.T) ([]byte, ReleaseIdentity) {
	t.Helper()
	dir, identity := manifestFixture(t, "linux-arm64")
	manifest, err := BuildManifest(t.Context(), dir, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	var entries []archiveEntry
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
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
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, archiveEntry{header: tar.Header{Name: "x-ui/" + filepath.ToSlash(name), Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()), Format: tar.FormatGNU}, data: string(data)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return compressed(t, tarBytes(t, entries...)), identity
}

func downloadServer(t *testing.T, mode string, archive []byte, identity ReleaseIdentity) *httptest.Server {
	t.Helper()
	name := "x-ui-" + identity.Platform + ".tar.gz"
	checksum := []byte(digest(archive) + "  " + name + "\n")
	if mode == "wrong-checksum" {
		checksum = []byte(strings.Repeat("0", 64) + "  " + name + "\n")
	}
	archiveAsset := map[string]any{"id": 17, "name": name, "size": len(archive), "digest": "sha256:" + digest(archive), "state": "uploaded"}
	checksumAsset := map[string]any{"id": 19, "name": name + ".sha256", "size": len(checksum), "digest": "sha256:" + digest(checksum), "state": "uploaded"}
	assets := []any{archiveAsset, checksumAsset}
	if mode == "missing-checksum" {
		assets = assets[:1]
	}
	if mode == "wrong-api-digest" {
		archiveAsset["digest"] = "sha256:" + strings.Repeat("0", 64)
	}
	release := map[string]any{"tag_name": identity.Tag, "target_commitish": "main", "body": "commit=" + strings.Repeat("d", 40), "draft": mode == "draft", "prerelease": mode == "prerelease", "assets": assets}
	if mode == "other-tag" {
		release["tag_name"] = "another-release"
	}
	prefix := "/repos/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			http.Error(w, "wrong method or API version", http.StatusBadRequest)
			return
		}
		if strings.Contains(r.URL.Path, "/releases/assets/") && r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "asset content was not requested", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case prefix + "/releases/latest", prefix + "/releases/tags/" + identity.Tag:
			_ = json.NewEncoder(w).Encode(release)
		case prefix + "/git/ref/tags/" + identity.Tag:
			commit, kind := identity.Commit, "commit"
			if mode == "annotated-redirect" {
				commit, kind = strings.Repeat("b", 40), "tag"
			}
			if mode == "moved-tag" {
				commit = strings.Repeat("c", 40)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": kind, "sha": commit}})
		case prefix + "/git/tags/" + strings.Repeat("b", 40):
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": "commit", "sha": identity.Commit}})
		case prefix + "/releases/assets/19":
			_, _ = w.Write(checksum)
		case prefix + "/releases/assets/17":
			if mode == "annotated-redirect" {
				http.Redirect(w, r, "/download/asset-17", http.StatusFound)
				return
			}
			_, _ = w.Write(archive)
		case "/download/asset-17":
			_, _ = w.Write(archive)
		default:
			http.Error(w, "unexpected repository or resource", http.StatusNotFound)
		}
	}))
}

func TestDownloadReleaseStagesSelectedForkCommit(t *testing.T) {
	for _, mode := range []string{"latest", "explicit", "annotated-redirect", "prerelease"} {
		t.Run(mode, func(t *testing.T) {
			archive, identity := downloadArchiveFixture(t)
			server := downloadServer(t, mode, archive, identity)
			defer server.Close()
			parent := preservedParent(t)
			tag := identity.Tag
			if mode == "latest" {
				tag = ""
			}
			stage, selected, err := downloadRelease(t.Context(), server.Client(), server.URL, tag, identity.Platform, parent)
			if err != nil {
				t.Fatal(err)
			}
			if selected != identity || filepath.Dir(stage) != parent {
				t.Fatalf("selected wrong release or stage: %+v, %q", selected, stage)
			}
			if _, err := VerifyManifest(t.Context(), filepath.Join(stage, "x-ui"), identity); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(stage, "x-ui", "x-ui"))
			if err != nil || string(data) != "fixture:x-ui" {
				t.Fatalf("downloaded panel changed: %q, %v", data, err)
			}
			if err := os.RemoveAll(stage); err != nil {
				t.Fatal(err)
			}
			requirePreserved(t, parent)
		})
	}
}

type downloadRoundTripper func(*http.Request) (*http.Response, error)

func (f downloadRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedDownloadBody struct {
	io.ReadCloser
	once    sync.Once
	reading chan struct{}
}

func (b *observedDownloadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.reading) })
	return b.ReadCloser.Read(p)
}

func TestDownloadReleaseCancelsStalledBodies(t *testing.T) {
	for _, resource := range []string{"/releases/latest", "/releases/assets/17"} {
		t.Run(resource, func(t *testing.T) {
			archive, identity := downloadArchiveFixture(t)
			fixture := downloadServer(t, "latest", archive, identity)
			fixture.Close()
			reading := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, resource) {
					fixture.Config.Handler.ServeHTTP(w, r)
					return
				}
				_, _ = w.Write([]byte("{"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			client := server.Client()
			transport := client.Transport
			client.Transport = downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				if err == nil && strings.HasSuffix(r.URL.Path, resource) {
					response.Body = &observedDownloadBody{ReadCloser: response.Body, reading: reading}
				}
				return response, err
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			parent := preservedParent(t)
			type result struct {
				stage string
				err   error
			}
			done := make(chan result, 1)
			go func() {
				stage, _, err := downloadRelease(ctx, client, server.URL, "", identity.Platform, parent)
				done <- result{stage, err}
			}()
			select {
			case <-reading:
			case <-time.After(2 * time.Second):
				t.Fatal("download never reached stalled body")
			}
			cancel()
			select {
			case got := <-done:
				if got.stage != "" || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("canceled download returned %q, %v", got.stage, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled download did not return")
			}
			requirePreserved(t, parent)
		})
	}
}

func TestDownloadReleaseRejectsUnselectedOrUnverifiedPayload(t *testing.T) {
	for mode, reason := range map[string]string{
		"missing-checksum": "checksum", "wrong-checksum": "checksum", "wrong-api-digest": "checksum",
		"moved-tag": "identity", "draft": "draft", "prerelease": "prerelease", "other-tag": "tag",
	} {
		t.Run(mode, func(t *testing.T) {
			archive, identity := downloadArchiveFixture(t)
			server := downloadServer(t, mode, archive, identity)
			defer server.Close()
			parent := preservedParent(t)
			tag := identity.Tag
			if mode == "prerelease" {
				tag = ""
			}
			stage, _, err := downloadRelease(t.Context(), server.Client(), server.URL, tag, identity.Platform, parent)
			if err == nil || stage != "" || !strings.Contains(err.Error(), reason) {
				t.Fatalf("wrong rejection for %s: %q, %v; want %s", mode, stage, err, reason)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestDownloadReleaseRejectsMalformedHTTPResponses(t *testing.T) {
	for _, mode := range []string{
		"api-error", "metadata-oversize", "metadata-trailing-json", "publication-missing",
		"ref-short", "ref-tree", "ref-cycle", "ref-deep", "missing-archive", "duplicate-archive",
		"asset-pending", "asset-too-large", "sidecar-too-large", "digest-missing",
		"truncated-archive", "overlong-chunked-archive", "same-size-corrupt-archive", "checksum-filename",
	} {
		t.Run(mode, func(t *testing.T) {
			archive, identity := downloadArchiveFixture(t)
			fixture := downloadServer(t, "latest", archive, identity)
			fixture.Close()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/releases/latest") {
					switch mode {
					case "api-error":
						http.Error(w, "private response body", http.StatusTooManyRequests)
						return
					case "metadata-oversize":
						_, _ = io.WriteString(w, strings.Repeat(" ", (1<<20)+1))
						return
					}
					recorder := httptest.NewRecorder()
					fixture.Config.Handler.ServeHTTP(recorder, r)
					if mode == "metadata-trailing-json" {
						_, _ = w.Write(append(recorder.Body.Bytes(), []byte("{}")...))
						return
					}
					var data map[string]any
					_ = json.Unmarshal(recorder.Body.Bytes(), &data)
					assets := data["assets"].([]any)
					switch mode {
					case "publication-missing":
						delete(data, "prerelease")
					case "missing-archive":
						data["assets"] = assets[1:]
					case "duplicate-archive":
						data["assets"] = append(assets, assets[0])
					case "asset-pending":
						assets[0].(map[string]any)["state"] = "new"
					case "asset-too-large":
						assets[0].(map[string]any)["size"] = (512 << 20) + 1
					case "sidecar-too-large":
						assets[1].(map[string]any)["size"] = 4097
					case "digest-missing":
						delete(assets[0].(map[string]any), "digest")
					case "checksum-filename":
						bad := []byte(digest(archive) + "  some-other-archive.tar.gz\n")
						assets[1].(map[string]any)["size"] = len(bad)
						assets[1].(map[string]any)["digest"] = "sha256:" + digest(bad)
					}
					_ = json.NewEncoder(w).Encode(data)
					return
				}
				if strings.Contains(r.URL.Path, "/git/") && strings.HasPrefix(mode, "ref-") {
					calls++
					kind, sha := "commit", identity.Commit
					switch mode {
					case "ref-short":
						sha = sha[:7]
					case "ref-tree":
						kind = "tree"
					case "ref-cycle":
						kind = "tag"
					case "ref-deep":
						kind, sha = "tag", fmt.Sprintf("%040x", calls)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": kind, "sha": sha}})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/releases/assets/19") && mode == "checksum-filename" {
					_, _ = fmt.Fprintln(w, digest(archive)+"  some-other-archive.tar.gz")
					return
				}
				if strings.HasSuffix(r.URL.Path, "/releases/assets/17") {
					switch mode {
					case "truncated-archive":
						w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
						_, _ = w.Write(archive[:len(archive)-1])
						return
					case "overlong-chunked-archive":
						w.(http.Flusher).Flush()
						_, _ = w.Write(append(archive, 'x'))
						return
					case "same-size-corrupt-archive":
						_, _ = w.Write(bytes.Repeat([]byte{'x'}, len(archive)))
						return
					}
				}
				fixture.Config.Handler.ServeHTTP(w, r)
			}))
			parent := preservedParent(t)
			stage, _, err := downloadRelease(t.Context(), server.Client(), server.URL, "", identity.Platform, parent)
			server.Close()
			if err == nil || stage != "" || strings.Contains(err.Error(), "private response body") {
				t.Fatalf("bad response published stage or exposed body: %q, %v", stage, err)
			}
			if mode == "ref-deep" && calls != 5 {
				t.Fatalf("unbounded tag resolution: %d", calls)
			}
			requirePreserved(t, parent)
		})
	}
}

func TestDownloadReleaseDoesNotExposeProxyCredentials(t *testing.T) {
	client := &http.Client{Transport: downloadRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("proxy http://fixture-user:fixture-secret@invalid: connect failed")
	})}
	parent := preservedParent(t)
	stage, _, err := DownloadRelease(t.Context(), client, "", "linux-arm64", parent)
	if err == nil || stage != "" || strings.Contains(err.Error(), "fixture-") {
		t.Fatalf("request failure leaked proxy credentials: %q, %v", stage, err)
	}
	requirePreserved(t, parent)
}
