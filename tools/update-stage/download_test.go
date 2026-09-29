//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunDownloadsAndPreflightsSelectedRelease(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "executed")
			t.Setenv("XUI_PREFLIGHT_TEST_MARKER", marker)
			panel := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$XUI_PREFLIGHT_TEST_MARKER\"\n")
			parent, archivePath, sum, identity := releaseStageFixturePanel(t, panel)
			archive, err := os.ReadFile(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			name := "x-ui-" + identity.Platform + ".tar.gz"
			checksum := []byte(sum + "  " + name + "\n")
			digest := func(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
			if bad {
				archive[0] ^= 1
			}
			prefix := "/repos/" + updatebundle.ReleaseRepository
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case prefix + "/releases/latest":
					_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": identity.Tag, "draft": false, "prerelease": false, "assets": []any{
						map[string]any{"id": 1, "name": name, "state": "uploaded", "size": len(archive), "digest": "sha256:" + sum},
						map[string]any{"id": 2, "name": name + ".sha256", "state": "uploaded", "size": len(checksum), "digest": digest(checksum)},
					}})
				case prefix + "/git/ref/tags/" + identity.Tag:
					_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": "commit", "sha": identity.Commit}})
				case prefix + "/releases/assets/1":
					_, _ = w.Write(archive)
				case prefix + "/releases/assets/2":
					_, _ = w.Write(checksum)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			endpoint, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client := server.Client()
			transport := client.Transport
			client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
					return nil, fmt.Errorf("unexpected production API source")
				}
				redirected := r.Clone(r.Context())
				redirected.URL.Scheme, redirected.URL.Host = endpoint.Scheme, endpoint.Host
				return transport.RoundTrip(redirected)
			})
			var out bytes.Buffer
			err = runWithClient(t.Context(), []string{"--download", "--preflight", "--parent", parent, "--release-platform", identity.Platform}, &out, client)
			if bad {
				if err == nil || out.Len() != 0 {
					t.Fatalf("bad download published: %q, %v", out.String(), err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("bad download executed: %v", err)
				}
				stages, _ := filepath.Glob(filepath.Join(parent, ".x-ui-*"))
				if len(stages) != 0 {
					t.Fatalf("bad download left files: %v", stages)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(marker)
			if err != nil || !strings.Contains(string(data), identity.Commit) {
				t.Fatalf("selected identity never preflighted: %s %v", data, err)
			}
			if _, err := updatebundle.VerifyManifest(t.Context(), filepath.Join(strings.TrimSuffix(out.String(), "\n"), "x-ui"), identity); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunRejectsAmbiguousDownloadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--download"},
		{"--download", "--release-platform", "linux-arm64", "--archive", "a"},
		{"--download", "--release-platform", "linux-arm64", "--sha256", strings.Repeat("0", 64)},
		{"--download", "--release-platform", "linux-arm64", "--release-commit", strings.Repeat("0", 40)},
	} {
		var out bytes.Buffer
		parent := t.TempDir()
		client := &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) {
			t.Error("invalid arguments made network request")
			return nil, fmt.Errorf("unexpected request")
		})}
		err := runWithClient(t.Context(), append(args, "--parent", parent), &out, client)
		if err == nil || out.Len() != 0 {
			t.Fatalf("ambiguous invocation accepted: %q %v", out.String(), err)
		}
	}
}
