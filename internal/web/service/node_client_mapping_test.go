package service

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/xtls/xray-core/app/clientpolicy"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func nodeMappingRequest(t *testing.T, f *nodeControlFixture) panelruntime.NodeClientMappingRequest {
	t.Helper()
	state, err := f.owner.api.GetClient(context.Background(), f.client.StableID)
	if err != nil || state.Policy == nil || state.Policy.Version != 1 || state.Policy.MultiplierMicros != 2000000 {
		t.Fatalf("fresh actual2x policy missing: %+v/%v", state, err)
	}
	digest, err := panelruntime.EffectiveClientPolicyDigest(state.Policy)
	if err != nil {
		t.Fatal(err)
	}
	return panelruntime.NodeClientMappingRequest{Binding: f.binding, GlobalClientID: "11111111-1111-4111-8111-111111111111", LocalClientID: f.client.StableID, GlobalPolicyVersion: 7, LocalPolicyVersion: 1, ExpectedPolicyDigest: digest}
}

func TestNodeClientMappingRequiresOwnedFreshPolicy(t *testing.T) {
	t.Run("native-local-issuer", func(t *testing.T) {
		setupPolicyLedgerDB(t)
		svc, _, client, _ := setupManagedActivationServiceWithUsage(t, 0, 0)
		if err := svc.RestartXray(true); err != nil {
			t.Fatal(err)
		}
		owner := managedAuthorityForProcess(currentXrayProcess())
		if owner == nil || owner.controller == nil || owner.delegated() {
			t.Fatal("fixture lacks actual native local issuer")
		}
		journal := createServiceFixtureGrantJournal(t, nil)
		id, caps := journal.Identity(), owner.api.Capabilities()
		f := &nodeControlFixture{node: &ClientPolicyNodeService{}, owner: owner, client: client, binding: panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: caps.InstanceId, ExpectedBootID: caps.BootId, AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: "local-refusal-node"}}
		r := nodeMappingRequest(t, f)
		if r.Validate() != nil {
			t.Fatal("local refusal request is not complete and valid")
		}
		if result, err := f.node.EnrollClientMapping(context.Background(), r); err == nil || result != nil {
			t.Fatal("actual local issuer admitted remote enrollment")
		}
	})
	for _, fault := range []string{"sealed-zero-grant", "sql-first-use", "sql-uncertain", "sql-zero-reset", "foreign-source", "sql-policy", "missing-history-capability"} {
		t.Run(fault, func(t *testing.T) {
			if fault == "missing-history-capability" && os.Getenv("XRAY_E2E_BINARY") != "" {
				old := os.Getenv("XRAY_PRE_MAPPING_BINARY")
				if _, err := os.Stat(old); err != nil {
					t.Fatal("retained actual older core required", err)
				}
				t.Setenv("XRAY_E2E_BINARY", old)
			}
			f := newNodeControlFixture(t)
			r := nodeMappingRequest(t, f)
			db := database.GetDB()
			var err error
			switch fault {
			case "sealed-zero-grant":
				if _, err := f.node.InstallAuthorityGrant(context.Background(), panelruntime.NodeAuthorityInstallRequest{Binding: f.binding, Grant: f.grant}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.node.SealAuthorityGrant(context.Background(), panelruntime.NodeAuthorityGrantRequest{Binding: f.binding, ClientID: f.client.StableID, GrantID: f.grant.GrantId}); err != nil {
					t.Fatal(err)
				}
			case "sql-first-use":
				err = db.Model(&model.ClientPolicyReceipt{}).Where("client_id = ?", f.client.StableID).Update("first_used_at", 1).Error
			case "sql-uncertain":
				err = db.Model(&model.ClientPolicyTotal{}).Where("client_id = ?", f.client.StableID).Update("uncertain_bytes", 1).Error
			case "sql-zero-reset":
				err = db.Create(&model.ClientPolicyReset{ClientID: f.client.StableID, RequestID: "hidden-zero-reset", InstanceID: f.binding.ExpectedInstanceID, Epoch: 1, Sequence: 1, PolicyVersion: 1}).Error
			case "foreign-source":
				err = db.Create(&model.ClientPolicyReceipt{InstanceID: "another-source", ClientID: f.client.StableID, Epoch: 1, Sequence: 1, PolicyVersion: 1}).Error
			case "sql-policy":
				err = db.Model(&model.ClientRecord{}).Where("stable_id = ?", f.client.StableID).Update("total_gb", 9999).Error
			case "missing-history-capability":
				if slices.Contains(f.owner.api.Capabilities().Capabilities, "client-authority-history-v1") {
					t.Fatal("older actual core unexpectedly contains new capability")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if result, err := f.node.EnrollClientMapping(context.Background(), r); err == nil || result != nil {
				t.Fatal("hidden history or unproven policy admitted")
			}
			if _, err := f.owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, r.Binding.ExpectedInstanceID, r.LocalClientID); err != policyauthority.ErrNotFound {
				t.Fatal("failed fresh enrollment committed evidence", err)
			}
		})
	}
	f := newNodeControlFixture(t)
	t.Logf("node client mapping backend: %s", database.GetDB().Dialector.Name())
	r := nodeMappingRequest(t, f)
	for _, ctx := range []context.Context{nil, func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }()} {
		if result, err := f.node.EnrollClientMapping(ctx, r); err == nil || result != nil {
			t.Fatal("invalid context admitted")
		}
	}
	f.owner.mu.Lock()
	result, busyErr := f.node.EnrollClientMapping(context.Background(), r)
	f.owner.mu.Unlock()
	if busyErr == nil || result != nil {
		t.Fatal("busy owner admitted")
	}
	lease, restoreErr := database.BeginRestore()
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	result, restoreErr = f.node.EnrollClientMapping(context.Background(), r)
	lease.Close()
	if restoreErr == nil || result != nil {
		t.Fatal("restore admitted mapping")
	}
	for _, change := range []string{"unknown", "version", "digest", "boot", "role"} {
		t.Run(change, func(t *testing.T) {
			bad := r
			switch change {
			case "unknown":
				bad.LocalClientID = "33333333-3333-4333-8333-333333333333"
			case "version":
				bad.LocalPolicyVersion++
			case "digest":
				bad.ExpectedPolicyDigest = strings.Repeat("a", 64)
			case "boot":
				bad.Binding.ExpectedBootID = strings.Repeat("a", 32)
			case "role":
				bad.Binding.NodeID += "-foreign"
			}
			if proof, err := f.node.EnrollClientMapping(context.Background(), bad); err == nil || proof != nil {
				t.Fatal("unproven mapping accepted")
			}
		})
	}
	proof, err := f.node.EnrollClientMapping(context.Background(), r)
	if err != nil || proof.Validate(r) != nil || proof.Mapping.NodeAnchor != f.owner.state.Journal.Identity() {
		t.Fatalf("actual owned fresh mapping not committed: %+v/%v", proof, err)
	}
	if proof.Mapping.GlobalClientID == proof.Mapping.LocalClientID || proof.Mapping.GlobalPolicyVersion == proof.Mapping.LocalPolicyVersion {
		t.Fatal("fixture did not exercise distinct canonical/local identities and versions")
	}
	state, err := f.owner.api.GetClient(context.Background(), f.client.StableID)
	if err != nil || state.AuthorityGrantHistory || state.Usage.BilledBytes != 0 || state.ActiveSessions != 0 {
		t.Fatal("enrollment changed real policy consumption")
	}
}

func TestNodeClientMappingPreservesOriginalEvidence(t *testing.T) {
	for _, change := range []string{"deletion", "policy"} {
		t.Run("journal-"+change, func(t *testing.T) {
			f := newNodeControlFixture(t)
			r := nodeMappingRequest(t, f)
			proof, err := f.node.EnrollClientMapping(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if change == "deletion" {
				if err := f.owner.state.Journal.Tombstone(r.LocalClientID); err != nil {
					t.Fatal(err)
				}
			} else {
				account, err := f.owner.state.Journal.Account(r.LocalClientID)
				if err != nil {
					t.Fatal(err)
				}
				policy := account.Policy
				policy.Version++
				if _, err := f.owner.state.Journal.ChangePolicy(policyauthority.ChangeRequest{Identity: f.owner.state.Journal.Identity(), ClientID: r.LocalClientID, RequestID: "journal-policy-ahead", ExpectedVersion: account.Policy.Version, Policy: policy}); err != nil {
					t.Fatal(err)
				}
			}
			if result, err := f.node.EnrollClientMapping(context.Background(), r); err == nil || result != nil {
				t.Fatal("original journal deletion/version lost through unchanged SQL and actual core")
			}
			stored, err := f.owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, r.Binding.ExpectedInstanceID, r.LocalClientID)
			if err != nil || stored != proof.Mapping {
				t.Fatal("historical journal proof was erased")
			}
		})
	}
	t.Run("deleted-existing", func(t *testing.T) {
		f := newNodeControlFixture(t)
		r := nodeMappingRequest(t, f)
		proof, err := f.node.EnrollClientMapping(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.GetDB().Create(&model.ClientPolicyTombstone{ClientID: f.client.StableID}).Error; err != nil {
			t.Fatal(err)
		}
		if result, err := f.node.EnrollClientMapping(context.Background(), r); err == nil || result != nil {
			t.Fatal("deleted client admitted with unchanged actual policy")
		}
		stored, err := f.owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, r.Binding.ExpectedInstanceID, r.LocalClientID)
		if err != nil || stored != proof.Mapping {
			t.Fatal("deletion erased original evidence")
		}
	})
	for _, fault := range []string{"role", "socket"} {
		t.Run("post-commit-"+fault, func(t *testing.T) {
			f := newNodeControlFixture(t)
			r := nodeMappingRequest(t, f)
			var committed *panelruntime.NodeClientMappingResult
			result, err := ownedNodeClientMapping(context.Background(), r, func(ctx context.Context, owner *managedAuthority, connection *gorm.DB, identity panelruntime.NodeAuthorityControlIdentity, request panelruntime.NodeClientMappingRequest) (*panelruntime.NodeClientMappingResult, error) {
				var err error
				committed, err = enrollOwnedNodeClientMapping(ctx, owner, connection, identity, request)
				if err != nil {
					return nil, err
				}
				if fault == "role" {
					role := owner.state.Role
					owner.state.Role.NodeID += "-lost"
					t.Cleanup(func() { owner.state.Role = role })
				} else {
					retained := owner.socketPath + ".mapping-post-commit"
					if err := os.Rename(owner.socketPath, retained); err != nil {
						return nil, err
					}
					listener, err := net.Listen("unix", owner.socketPath)
					if err != nil {
						_ = os.Rename(retained, owner.socketPath)
						return nil, err
					}
					t.Cleanup(func() {
						_ = listener.Close()
						if err := os.Rename(retained, owner.socketPath); err != nil {
							t.Error(err)
						}
					})
				}
				return committed, nil
			})
			if committed == nil || committed.Validate(r) != nil {
				t.Fatal("fault was not injected after actual policy proof and journal commit")
			}
			if err == nil || result != nil {
				t.Fatal("lost post-commit ownership acknowledged binding")
			}
			stored, err := f.owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, r.Binding.ExpectedInstanceID, r.LocalClientID)
			if err != nil || stored != committed.Mapping {
				t.Fatal("uncertain acknowledgement erased original evidence")
			}
		})
	}
	for _, action := range []string{"restore", "close"} {
		t.Run("retained-sql-"+action, func(t *testing.T) {
			f := newNodeControlFixture(t)
			r := nodeMappingRequest(t, f)
			reached, release := make(chan struct{}), make(chan struct{})
			operationDone, replacementDone := make(chan error, 1), make(chan error, 1)
			var once sync.Once
			operationJoined, replacementStarted, replacementJoined := false, false, false
			defer func() {
				once.Do(func() { close(release) })
				for _, pending := range []struct {
					join bool
					done <-chan error
				}{{!operationJoined, operationDone}, {replacementStarted && !replacementJoined, replacementDone}} {
					if pending.join {
						select {
						case <-pending.done:
						case <-time.After(5 * time.Second):
							t.Error("retained callback did not exit")
						}
					}
				}
			}()
			go func() {
				_, err := ownedNodeClientMapping(context.Background(), r, func(ctx context.Context, owner *managedAuthority, connection *gorm.DB, identity panelruntime.NodeAuthorityControlIdentity, request panelruntime.NodeClientMappingRequest) (*panelruntime.NodeClientMappingResult, error) {
					result, err := enrollOwnedNodeClientMapping(ctx, owner, connection, identity, request)
					if err != nil {
						return nil, err
					}
					close(reached)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return result, nil
				})
				operationDone <- err
			}()
			waitTrafficWriterSignal(t, reached, "real enrollment did not reach retained callback")
			replacementStarted = true
			go func() {
				if action == "restore" {
					lease, err := database.BeginRestore()
					if lease != nil {
						lease.Close()
					}
					replacementDone <- err
				} else {
					replacementDone <- database.CloseDB()
				}
			}()
			if action == "restore" {
				deadline := time.Now().Add(time.Second)
				for {
					ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
					err := f.owner.db.WithContext(ctx).Exec("SELECT 1").Error
					cancel()
					if errors.Is(err, database.ErrRestoreInProgress) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("restore admission not published", err)
					}
					time.Sleep(time.Millisecond)
				}
			}
			select {
			case err := <-replacementDone:
				replacementJoined = true
				t.Fatal("SQL replacement crossed retained enrollment", err)
			case <-time.After(100 * time.Millisecond):
			}
			once.Do(func() { close(release) })
			err := waitTrafficWriterErr(t, operationDone)
			operationJoined = true
			if err != nil {
				t.Fatal(err)
			}
			err = waitTrafficWriterErr(t, replacementDone)
			replacementJoined = true
			if err != nil {
				t.Fatal(err)
			}
			stored, err := f.owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, r.Binding.ExpectedInstanceID, r.LocalClientID)
			if err != nil || stored.GlobalClientID != r.GlobalClientID {
				t.Fatal("replacement erased original mapping")
			}
		})
	}
	f := newNodeControlFixture(t)
	r := nodeMappingRequest(t, f)
	proof, err := f.node.EnrollClientMapping(context.Background(), r)
	if err != nil || proof == nil {
		t.Fatalf("fresh enrollment unavailable: %v", err)
	}
	if _, err := f.node.InstallAuthorityGrant(context.Background(), panelruntime.NodeAuthorityInstallRequest{Binding: f.binding, Grant: f.grant}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.node.SealAuthorityGrant(context.Background(), panelruntime.NodeAuthorityGrantRequest{Binding: f.binding, ClientID: f.client.StableID, GrantID: f.grant.GrantId}); err != nil {
		t.Fatal(err)
	}
	retry, err := f.node.EnrollClientMapping(context.Background(), r)
	if err != nil || retry == nil || retry.Mapping != proof.Mapping {
		t.Fatalf("committed proof lost after real sealed grant: %+v/%v", retry, err)
	}
	conflict := r
	conflict.GlobalClientID = "33333333-3333-4333-8333-333333333333"
	if result, err := f.node.EnrollClientMapping(context.Background(), conflict); err == nil || result != nil {
		t.Fatal("original node evidence retargeted")
	}
	state, err := f.owner.api.GetClient(context.Background(), f.client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	changed := proto.Clone(state.Policy).(*clientpolicy.PolicyConfig)
	changed.Version++
	changed.MultiplierMicros++
	if err := f.owner.api.Apply(context.Background(), []*clientpolicy.PolicyConfig{changed}); err != nil {
		t.Fatal(err)
	}
	if result, err := f.node.EnrollClientMapping(context.Background(), r); err == nil || result != nil {
		t.Fatal("changed real policy reused stale proof")
	}
	stored, err := f.owner.state.Journal.LookupClientMapping(policyauthority.ClientMappingNode, f.binding.ExpectedInstanceID, f.client.StableID)
	if err != nil || stored != proof.Mapping {
		t.Fatal("policy change erased original proof")
	}
}
