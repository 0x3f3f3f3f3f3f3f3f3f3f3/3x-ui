package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
)

func nodeDelegationFixture(t *testing.T) (string, panelruntime.NodeDelegationRequest) {
	t.Helper()
	setupPolicyLedgerDB(t)
	t.Cleanup(SetXrayProcessForTest(nil))
	return filepath.Join(config.GetDBFolderPath(), "client-policy"), panelruntime.NodeDelegationRequest{AuthorityID: "coordinator-authority", Generation: 1, NodeID: "node-stable-identity"}
}

func TestNodeDelegationFreshManifestAndRetries(t *testing.T) {
	dir, request := nodeDelegationFixture(t)
	node := &ClientPolicyNodeService{}
	first, err := node.ConfigureDelegation(context.Background(), request)
	if err != nil || first == nil || first.InstanceID == "" || first.Role != request.Role() {
		t.Fatalf("fresh stopped delegation unavailable: %+v/%v", first, err)
	}
	state, err := openAuthorityState(filepath.Join(dir, "authority"))
	if err != nil {
		t.Fatal(err)
	}
	id := state.Journal.Identity()
	if state.SourceID != first.InstanceID || state.Role != request.Role() {
		t.Fatal("independent manifest lost source or delegated binding")
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := node.ConfigureDelegation(context.Background(), request)
	if err != nil || again == nil || *again != *first {
		t.Fatalf("exact retry changed delegation: %+v/%v", again, err)
	}
	state, err = openAuthorityState(filepath.Join(dir, "authority"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Journal.Identity() != id || state.Role != request.Role() {
		t.Fatal("setup retry replaced independent identity")
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Generation++
	if got, err := node.ConfigureDelegation(context.Background(), changed); err == nil || got != nil {
		t.Fatal("delegation replaced its pinned coordinator generation")
	}
	data, err := os.ReadFile(filepath.Join(dir, "authority", "authority.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if string(manifest["schema"]) != "2" || len(manifest["executionRole"]) == 0 {
		t.Fatal("delegation not committed in schema2 manifest")
	}
}

func TestNodeDelegationRejectsActivatedOrUnsafeState(t *testing.T) {
	for _, name := range []string{"activated-sql", "activated-core", "local-journal", "active-restore", "sql-restore-admission", "historical-sql-usage", "canceled", "nil-context"} {
		t.Run(name, func(t *testing.T) {
			dir, request := nodeDelegationFixture(t)
			state, err := EnsureLocalClientPolicyState(dir)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch name {
			case "activated-sql":
				if err := database.GetDB().Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Update("epoch", 1).Error; err != nil {
					t.Fatal(err)
				}
			case "activated-core":
				engine, err := clientpolicy.OpenPersistentEngine(state.StateFile, state.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				if err := engine.Close(); err != nil {
					t.Fatal(err)
				}
			case "local-journal":
				if err := initializeFreshAuthorityLocked(ctx, state); err != nil {
					t.Fatal(err)
				}
			case "active-restore":
				owner, err := acquireDatabaseRestore()
				if err != nil {
					t.Fatal(err)
				}
				defer owner.release()
			case "sql-restore-admission":
				lease, err := database.BeginRestore()
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
			case "historical-sql-usage":
				if err := database.GetDB().Create(&model.ClientRecord{Email: "legacy-used"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Create(&panelxray.ClientTraffic{Email: "legacy-used", Up: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			}
			if result, err := (&ClientPolicyNodeService{}).ConfigureDelegation(ctx, request); err == nil || result != nil {
				t.Fatal("unsafe node was delegated")
			}
		})
	}
}

func TestNodeDelegationRejectsHiddenHistoricalState(t *testing.T) {
	for _, name := range []string{"frozen-only-total", "orphan-legacy-traffic", "legacy-masked-by-zero-total", "orphan-receipt-usage", "receipt-seed-history", "orphan-reset-history"} {
		t.Run(name, func(t *testing.T) {
			dir, request := nodeDelegationFixture(t)
			state, err := EnsureLocalClientPolicyState(dir)
			if err != nil {
				t.Fatal(err)
			}
			client := model.ClientRecord{Email: "hidden-history"}
			if err := database.GetDB().Create(&client).Error; err != nil {
				t.Fatal(err)
			}
			switch name {
			case "frozen-only-total":
				if err := database.GetDB().Create(&model.ClientPolicyTotal{ClientID: client.StableID, UncertainBytes: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "orphan-legacy-traffic":
				if err := database.GetDB().Create(&panelxray.ClientTraffic{Email: "absent-owner", Up: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "legacy-masked-by-zero-total":
				if err := database.GetDB().Create(&model.ClientPolicyTotal{ClientID: client.StableID}).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Create(&panelxray.ClientTraffic{Email: client.Email, Down: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "orphan-receipt-usage":
				if err := database.GetDB().Create(&model.ClientPolicyReceipt{InstanceID: state.InstanceID, ClientID: "f5913b70-2e06-4a60-a664-b022debc01dc", PolicyVersion: 1, RawUpload: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "receipt-seed-history":
				if err := database.GetDB().Create(&model.ClientPolicyReceipt{InstanceID: state.InstanceID, ClientID: client.StableID, PolicyVersion: 1, SeedBilled: 1}).Error; err != nil {
					t.Fatal(err)
				}
			case "orphan-reset-history":
				if err := database.GetDB().Create(&model.ClientPolicyReset{InstanceID: state.InstanceID, ClientID: "f5913b70-2e06-4a60-a664-b022debc01dc", RequestID: "previous-reset", PolicyVersion: 1, RawUpload: 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if result, err := (&ClientPolicyNodeService{}).ConfigureDelegation(context.Background(), request); err == nil || result != nil {
				t.Fatal("historical consumption/liability was delegated as fresh")
			}
			if _, err := os.Lstat(filepath.Join(dir, "authority", "authority.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected history published a role: %v", err)
			}
		})
	}
}

func TestNodeDelegationZeroHistoricalRecordsRemainFresh(t *testing.T) {
	dir, request := nodeDelegationFixture(t)
	state, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	client := model.ClientRecord{Email: "fresh-zero-records"}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientPolicyTotal{ClientID: client.StableID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientPolicyReceipt{InstanceID: state.InstanceID, ClientID: client.StableID, PolicyVersion: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{client.Email, "orphan-zero-record"} {
		if err := database.GetDB().Create(&panelxray.ClientTraffic{Email: email}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if result, err := (&ClientPolicyNodeService{}).ConfigureDelegation(context.Background(), request); err != nil || result == nil || result.Role != request.Role() {
		t.Fatalf("zero historical records prevented fresh setup: %+v/%v", result, err)
	}
}

func TestNodeDelegationConfigureResumesPreparedRole(t *testing.T) {
	dir, request := nodeDelegationFixture(t)
	original := publishAuthorityManifest
	failure := errors.New("configuration final publication lost")
	publishAuthorityManifest = func(dir string, manifest authorityManifest, create bool) error {
		if manifest.Phase == "committed" {
			return failure
		}
		return original(dir, manifest, create)
	}
	t.Cleanup(func() { publishAuthorityManifest = original })
	node := &ClientPolicyNodeService{}
	if result, err := node.ConfigureDelegation(context.Background(), request); !errors.Is(err, failure) || result != nil {
		t.Fatalf("configuration failure not preserved: %+v/%v", result, err)
	}
	publishAuthorityManifest = original
	prepared, err := readAuthorityManifest(filepath.Join(dir, "authority"))
	if err != nil || prepared.Phase != "preparing" || prepared.role() != request.Role() {
		t.Fatalf("lost prepared binding: %+v/%v", prepared, err)
	}
	changed := request
	changed.NodeID = "another-node"
	if result, err := node.ConfigureDelegation(context.Background(), changed); err == nil || result != nil {
		t.Fatal("incomplete setup replaced its pinned node")
	}
	result, err := node.ConfigureDelegation(context.Background(), request)
	if err != nil || result == nil || result.InstanceID != prepared.SourceID || result.Role != request.Role() {
		t.Fatalf("exact setup retry did not recover original role: %+v/%v", result, err)
	}
	state, err := openAuthorityState(filepath.Join(dir, "authority"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Journal.Identity() != prepared.Identity {
		t.Fatal("setup retry replaced prepared identity")
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeDelegationManifestPreparationRetainsRole(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	role := panelruntime.NodeDelegationRequest{AuthorityID: "coordinator", Generation: 4, NodeID: "node-a"}.Role()
	original := publishAuthorityManifest
	failure := errors.New("role final publication failed")
	publishAuthorityManifest = func(dir string, manifest authorityManifest, create bool) error {
		if manifest.Phase == "committed" {
			return failure
		}
		return original(dir, manifest, create)
	}
	t.Cleanup(func() { publishAuthorityManifest = original })
	if state, err := initializeAuthorityStateWithRole(dir, "fresh-source", snapshot, role); !errors.Is(err, failure) || state != nil {
		t.Fatalf("role preparing failure was not preserved: %+v/%v", state, err)
	}
	publishAuthorityManifest = original
	if state, err := openAuthorityState(dir); err == nil {
		_ = state.Journal.Close()
		t.Fatal("incomplete role publication opened")
	}
	state, err := initializeAuthorityStateWithRole(dir, "fresh-source", snapshot, role)
	if err != nil {
		t.Fatal(err)
	}
	if state.Role != role {
		t.Fatal("publication recovery replaced execution role")
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeDelegationManifestRejectsCorruption(t *testing.T) {
	for _, name := range []string{"missing-role", "changed-binding", "schema-downgrade", "case-alias", "duplicate-mode", "null-role", "wrong-source", "public-permissions", "symlink", "missing-manifest"} {
		t.Run(name, func(t *testing.T) {
			dir, snapshot := durableAuthorityFixture(t)
			role := panelruntime.NodeDelegationRequest{AuthorityID: "coordinator", Generation: 4, NodeID: "node-a"}.Role()
			state, err := initializeAuthorityStateWithRole(dir, "fresh-source", snapshot, role)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "authority.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing-role":
				delete(m, "executionRole")
			case "changed-binding":
				m["executionRole"] = json.RawMessage(`{"mode":"delegated","authorityId":"different-authority","generation":4,"nodeId":"node-a"}`)
			case "schema-downgrade":
				m["schema"] = json.RawMessage(`1`)
				delete(m, "executionRole")
			case "case-alias":
				m["ExecutionRole"] = m["executionRole"]
				delete(m, "executionRole")
			case "duplicate-mode":
				m["executionRole"] = json.RawMessage(`{"mode":"delegated","authorityId":"coordinator","generation":4,"nodeId":"node-a","mode":"local"}`)
			case "null-role":
				m["executionRole"] = json.RawMessage(`null`)
			case "wrong-source":
				m["sourceId"] = json.RawMessage(`"other-source"`)
			case "public-permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".retained", path); err != nil {
					t.Fatal(err)
				}
			case "missing-manifest":
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
			}
			if name != "public-permissions" && name != "symlink" && name != "missing-manifest" {
				raw, err = json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if state, err := openAuthorityState(dir); err == nil {
				_ = state.Journal.Close()
				t.Fatal("unsafe or changed role evidence opened an authority")
			}
		})
	}
}

func TestNodeDelegationLegacySchemaOneRemainsLocal(t *testing.T) {
	dir, snapshot := durableAuthorityFixture(t)
	id := policyauthority.Identity{AuthorityID: strings.Repeat("a", 32), Generation: 1}
	journal, err := policyauthority.CreateWithMigrationSourceIdentity(filepath.Join(dir, "journal.db"), id, "legacy-source", snapshot.Seeds, snapshot.Deleted, snapshot.Records)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := authoritySnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthorityManifest(dir, authorityManifest{Schema: 1, Identity: id, SourceID: "legacy-source", SnapshotDigest: digest, Phase: "committed"}, true); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Role != (panelruntime.NodeExecutionRole{Mode: panelruntime.NodeExecutionLocal}) || state.Journal.Identity() != id {
		t.Fatal("legacy state lost local role or identity")
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = initializeAuthorityState(dir, "legacy-source", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
}
