//go:build linux

package xray

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticTrafficControlPinsCustomChildWithoutChangingDesiredConfig(t *testing.T) {
	p, final, address := finalTrafficProcess(t, false)
	if final.TrafficDrainBootID() == "" {
		t.Fatal("ordinary custom child has no boot-pinned control")
	}
	if len(p.GetConfig().TrafficControl) != 0 {
		t.Fatal("runtime control changed desired configuration")
	}
	data, err := os.ReadFile(p.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeConfig Config
	if err := json.Unmarshal(data, &runtimeConfig); err != nil {
		t.Fatal(err)
	}
	endpoint, err := trafficControlEndpoint(&runtimeConfig)
	if err != nil || endpoint == "" || endpoint != p.trafficEndpoint {
		t.Fatalf("runtime endpoint: %q %v", endpoint, err)
	}
	privateDir := filepath.Dir(endpoint)
	firstBoot := final.TrafficDrainBootID()
	exchangeFinalTraffic(t, address, []byte("automatic final accounting"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var upload, download int64
	if err := final.SettleFinalTraffic(ctx, func(batch *TrafficBatch) error {
		for _, traffic := range batch.ClientTraffics {
			upload += traffic.Up
			download += traffic.Down
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if upload != 26 || download != 26 {
		t.Fatalf("automatic final accounting: %d %d", upload, download)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("owned control directory survived child exit: %v", err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if final.TrafficDrainBootID() == firstBoot || filepath.Dir(p.trafficEndpoint) == privateDir {
		t.Fatal("new child reused old control identity")
	}
	exchangeFinalTraffic(t, address, []byte("restarted"))
}

func TestAutomaticTrafficControlCleansRejectedChild(t *testing.T) {
	owner, _, address := finalTrafficProcess(t, false)
	privateRoot, err := os.MkdirTemp("", "auto-control-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(privateRoot)
	t.Setenv("TMPDIR", privateRoot)
	config := *owner.GetConfig()
	config.InboundConfigs = append([]InboundConfig(nil), config.InboundConfigs...)
	config.InboundConfigs[0].Protocol = "invalid-protocol"
	rejected := NewTestProcess(&config, filepath.Join(privateRoot, "invalid.json"))
	defer rejected.Stop()
	if err := rejected.Start(); err == nil || rejected.IsRunning() {
		t.Fatalf("rejected core remained active: %v", err)
	}
	entries, err := os.ReadDir(privateRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("rejected child leaked private control: %q", entry.Name())
		}
	}
	exchangeFinalTraffic(t, address, []byte("owner unaffected"))
}

func TestAutomaticTrafficControlPreservesExplicitDirectory(t *testing.T) {
	p, _, address := finalTrafficProcess(t, true)
	dir := filepath.Dir(p.trafficEndpoint)
	marker := filepath.Join(dir, "caller-owned")
	if err := os.WriteFile(marker, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	exchangeFinalTraffic(t, address, []byte("explicit control"))
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "retained" {
		t.Fatalf("explicit directory was removed: %v", err)
	}
}

func TestAutomaticTrafficControlVersionHint(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		want         bool
	}{
		{"old custom", "Xray 26.9.9 (Custom Xray-core 26.9.9-custom.1) Custom", false},
		{"upstream", "Xray 26.9.9 (Xray, Penetrates Everything.) Custom", false},
		{"config supported", "Xray 26.9.9 (Custom Xray-core 26.9.9-custom.1) Custom\nCustom configuration: traffic-control-v1", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XUI_BIN_FOLDER", dir)
			fixture := "#!/bin/sh\ncat <<'VERSION'\n" + tt.output + "\nVERSION\n"
			if err := os.WriteFile(filepath.Join(dir, GetBinaryName()), []byte(fixture), 0o700); err != nil {
				t.Fatal(err)
			}
			p := newProcess(&Config{})
			if got := p.refreshVersion(); got != tt.want {
				t.Fatalf("automatic config hint = %v, want %v", got, tt.want)
			}
			if p.GetXrayVersion() != "26.9.9" {
				t.Fatal("legacy numeric version parser changed")
			}
		})
	}
}

func TestAutomaticTrafficControlLegacyCustomRemainsUnpinned(t *testing.T) {
	binary := os.Getenv("XRAY_LEGACY_CUSTOM_BINARY")
	if binary == "" {
		t.Skip("set XRAY_LEGACY_CUSTOM_BINARY to a custom build predating the private control hint")
	}
	t.Setenv("XRAY_E2E_BINARY", binary)
	p, final, address := finalTrafficProcess(t, false)
	if final.TrafficDrainBootID() != "" || len(p.GetConfig().TrafficControl) != 0 {
		t.Fatal("legacy custom child received automatic control")
	}
	exchangeFinalTraffic(t, address, []byte("legacy custom still serves"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := final.SettleFinalTraffic(ctx, func(*TrafficBatch) error { t.Fatal("unsupported settlement called"); return nil }); !errors.Is(err, ErrTrafficDrainCapability) {
		t.Fatalf("unsupported drain: %v", err)
	}
	exchangeFinalTraffic(t, address, []byte("legacy custom stays alive"))
}
