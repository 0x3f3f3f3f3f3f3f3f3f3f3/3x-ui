package clientpolicy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/common"
)

func TestConfiguredCopiedStateCannotAdmitWithoutFreshAuthority(t *testing.T) {
	seed, path := persistentEngine(t)
	policy := testPolicy("canonical-copied-account")
	policy.QuotaBytes = 100
	if err := seed.Apply(policy); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.MkdirTemp("", "policy-authority-node-clones-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained execution-state fixture: %s", fixture)
	if err := os.WriteFile(filepath.Join(fixture, "original.db"), image, 0o600); err != nil {
		t.Fatal(err)
	}
	var admitted uint64
	for _, name := range []string{"node-copy-a", "node-copy-b"} {
		copyPath := filepath.Join(fixture, name+".db")
		if err := os.WriteFile(copyPath, image, 0o600); err != nil {
			t.Fatal(err)
		}
		// Use the production configured feature constructor, rather than the
		// isolated in-memory engine. Both copies retain source/epoch/cursor.
		object, err := common.CreateObject(context.Background(), &Config{StateFile: copyPath, InstanceId: "node-1"})
		if err != nil {
			t.Fatal(err)
		}
		engine := object.(*Engine)
		t.Cleanup(func() { _ = engine.Close() })
		if err := engine.Start(); err != nil {
			t.Fatal(err)
		}
		session, err := engine.Open(context.Background(), Metadata{ClientID: policy.ClientID, InboundTag: "owned-listener"}, nil)
		if err == nil {
			if err := session.Admit(Upload, 100); err == nil {
				admitted += 100
			}
			session.Close()
		}
		snapshot, err := engine.Snapshot(policy.ClientID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: source=%s epoch=%d billed=%d", name, snapshot.InstanceID, snapshot.Epoch, snapshot.Usage.BilledBytes)
	}
	if admitted != 0 {
		t.Fatalf("copied configured execution state admitted %d billed bytes without a fresh authority grant (original quota=%d)", admitted, policy.QuotaBytes)
	}
}
