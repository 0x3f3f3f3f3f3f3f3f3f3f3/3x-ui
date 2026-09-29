package service

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientPolicyStateProvisioningPreservesUsageAndRejectsLostActiveStore(t *testing.T) {
	setupPolicyLedgerDB(t)
	dir := filepath.Join(t.TempDir(), "managed")
	config, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, config.StateFile: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private state permissions %s: %v, %v", path, info, err)
		}
	}
	engine, err := clientpolicy.OpenPersistentEngine(config.StateFile, config.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	if err := BindClientPolicySource("local", config.InstanceID, engine.Capabilities().Epoch); err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 1000000, QuotaBytes: 10, BurstBytes: 65536}, clientpolicy.Usage{RawUpload: 9, BilledBytes: 9}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureLocalClientPolicyState(dir)
	if err != nil || again.InstanceID != config.InstanceID || again.StateFile != config.StateFile {
		t.Fatalf("provisioning changed the active source: %+v, %v", again, err)
	}
	recovered, err := clientpolicy.OpenPersistentEngine(again.StateFile, again.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	usage, err := recovered.Snapshot("owner")
	if err != nil || usage.Usage.RawUpload != 9 || usage.Usage.BilledBytes != 9 {
		t.Fatalf("provisioning reset historical usage: %+v, %v", usage, err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(config.StateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureLocalClientPolicyState(dir); !errors.Is(err, ErrClientPolicyStateMissing) {
		t.Fatalf("lost active store did not fail closed: %v", err)
	}
	if _, err := os.Stat(config.StateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost active store was recreated: %v", err)
	}
}

func TestClientPolicyStateProvisioningRetriesPendingCreation(t *testing.T) {
	setupPolicyLedgerDB(t)
	dir := filepath.Join(t.TempDir(), "managed")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state.db")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureLocalClientPolicyState(dir); !errors.Is(err, clientpolicy.ErrStorage) {
		t.Fatalf("accepted a directory as state: %v", err)
	}
	var pending model.ClientPolicySource
	if err := database.GetDB().Where("node_key = ?", "local").First(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Epoch != 0 || pending.Sequence != 0 {
		t.Fatalf("failed creation was marked active: %+v", pending)
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			config, err := EnsureLocalClientPolicyState(dir)
			if err != nil || config.InstanceID != pending.InstanceID {
				t.Errorf("pending creation changed identity: %+v, %v", config, err)
			}
		})
	}
	wg.Wait()
	engine, err := clientpolicy.OpenPersistentEngine(state, pending.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if engine.Capabilities().Epoch != 1 {
		t.Fatalf("provisioning opened or replaced the execution store: %+v", engine.Capabilities())
	}
}
