//go:build linux

package xray

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

func TestTrafficHandoffImageMustExecuteBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	source := filepath.Join(dir, GetBinaryName())
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := NewProcess(&Config{})
	var err error
	p.trafficExecutable, err = digestTrafficExecutable(source)
	if err != nil {
		t.Fatal(err)
	}
	image, err := p.PinTrafficHandoffExecutable()
	if image != nil {
		defer image.Close()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 {
		t.Fatalf("unusable copied executable was not refused: %v", err)
	}
}

func TestTrafficHandoffImagePreservesDefaultResourceDirectory(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY")
	}
	// This exercises Xray's executable-relative defaults, so inherited
	// overrides from the test runner or earlier tests must be absent.
	for _, key := range []string{"XRAY_LOCATION_ASSET", "xray.location.asset", "XRAY_LOCATION_CERT", "xray.location.cert"} {
		t.Setenv(key, "") // Register restoration of the original environment.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	copied := filepath.Join(dir, "xray")
	input, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(copied, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(&geodata.GeoSiteList{Entry: []*geodata.GeoSite{{Code: "HANDOFF", Domain: []*geodata.Domain{{Type: geodata.Domain_Full, Value: "example.test"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geosite.dat"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_E2E_BINARY", copied)
	p, _, address := finalTrafficProcess(t, true)
	image, err := p.PinTrafficHandoffExecutable()
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	config := *p.GetConfig()
	config.RouterConfig = []byte(`{"rules":[{"type":"field","domain":["geosite:HANDOFF"],"outboundTag":"direct"}]}`)
	config.OutboundConfigs = []byte(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]`)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	replacement := image.NewProcess(&config)
	defer replacement.Stop()
	if err := replacement.Start(); err != nil {
		t.Fatalf("copied image lost original geodata directory: %v", err)
	}
	for _, key := range []string{"XRAY_LOCATION_ASSET", "XRAY_LOCATION_CERT"} {
		if !slices.Contains(replacement.cmd.Env, key+"="+dir) {
			t.Fatalf("original default %s was not preserved", key)
		}
	}
	exchangeFinalTraffic(t, address, []byte("asset preserved"))
	runtime.KeepAlive(replacement)
}
