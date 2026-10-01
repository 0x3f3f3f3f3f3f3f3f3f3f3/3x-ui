package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type customUpdateTransport struct{ requests atomic.Int32 }

func (c *customUpdateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.requests.Add(1)
	if r.URL.Path == "/repos/XTLS/Xray-core/releases" {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[{"tag_name":"v26.9.9"}]`)), Request: r}, nil
	}
	return nil, errors.New("test refuses official binary downloads")
}

func requireBundledCustomCoreError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("official core replacement must require the matching custom package")
	}
	for _, detail := range []string{"Custom Xray-core", "Snell", "mieru", "SSH", "package"} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("replacement error %q does not explain %q", err, detail)
		}
	}
}

func TestCustomCoreOfficialUpdateRefusesBeforeDownloadOrReplacement(t *testing.T) {
	setupPolicyLedgerDB(t)
	folder := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", folder)
	binary := filepath.Join(folder, xray.GetBinaryName())
	original := []byte("retained custom core artifact")
	if err := os.WriteFile(binary, original, 0o700); err != nil {
		t.Fatal(err)
	}
	transport := new(customUpdateTransport)
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	s := new(ServerService)
	requireBundledCustomCoreError(t, s.UpdateXray("v26.9.9"))
	if transport.requests.Load() != 0 {
		t.Fatal("official core update fetched releases/downloads before refusing replacement")
	}
	retained, err := os.ReadFile(binary)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatalf("core artifact changed during refused update: %v", err)
	}
}

func TestCustomCoreOfficialVersionsCannotOfferReplacement(t *testing.T) {
	setupPolicyLedgerDB(t)
	transport := new(customUpdateTransport)
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	s := new(ServerService)
	versions, err := s.GetXrayVersions()
	requireBundledCustomCoreError(t, err)
	if len(versions) != 0 || transport.requests.Load() != 0 {
		t.Fatal("official releases were fetched or offered for the required custom core")
	}
}

func TestCustomCoreOfficialVersionCacheCannotBypassReplacementGuard(t *testing.T) {
	setupPolicyLedgerDB(t)
	transport := new(customUpdateTransport)
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, age := range []time.Duration{0, 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			s := &ServerService{versionsCache: &cachedXrayVersions{versions: []string{"v26.9.9"}, fetchedAt: time.Now().Add(-age)}}
			versions, err := s.GetXrayVersionsCached()
			requireBundledCustomCoreError(t, err)
			if len(versions) != 0 {
				t.Fatal("a cached official release escaped the custom core replacement guard")
			}
		})
	}
}
