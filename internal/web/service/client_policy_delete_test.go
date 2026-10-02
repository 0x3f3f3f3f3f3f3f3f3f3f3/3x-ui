package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyPermanentDeletionRevokesIdentityAndPreservesUsage(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		for _, keepTraffic := range []bool{false, true} {
			t.Run(fmt.Sprintf("bulk-%t/keep-traffic-%t", bulk, keepTraffic), func(t *testing.T) {
				svc, tunnel, owner, _ := setupManagedActivationService(t)
				if err := svc.RestartXray(true); err != nil {
					t.Fatal(err)
				}
				flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer flow.Close()
				managedActivationEcho(t, flow, "warm")
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
				before := policyLedgerTotal(t, owner.StableID)
				if bulk {
					result, _, err := (&ClientService{}).BulkDelete(&InboundService{}, []string{owner.Email}, keepTraffic)
					if err != nil || result.Deleted != 1 || len(result.Skipped) != 0 {
						t.Fatalf("delete failed: %+v %v", result, err)
					}
				} else if _, err := (&ClientService{}).Delete(&InboundService{}, owner.Id, keepTraffic); err != nil {
					t.Fatal(err)
				}
				managedActivationClosed(t, flow)
				var count int64
				if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", owner.StableID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("canonical client survived deletion: %d %v", count, err)
				}
				for _, restart := range []bool{false, true} {
					if restart {
						if err := svc.RestartXray(true); err != nil {
							t.Fatal(err)
						}
					}
					process := currentXrayProcess()
					endpoint, err := process.GetAPIEndpoint()
					if err != nil {
						t.Fatal(err)
					}
					var config conf.ClientPolicyConfig
					if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					api, err := xray.DialClientPolicy(ctx, endpoint, config.InstanceID)
					if err != nil {
						t.Fatal(err)
					}
					defer api.Close()
					state, err := api.GetClient(ctx, owner.StableID)
					if err != nil {
						t.Fatal(err)
					}
					if state.Reasons&uint32(clientpolicy.ReasonRevoked) == 0 {
						t.Fatalf("permanently deleted identity remains reusable in core, restarted=%t: %+v", restart, state)
					}
					if _, _, err := svc.GetXrayTraffic(); err != nil {
						t.Fatal(err)
					}
					var receipt model.ClientPolicyReceipt
					if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil {
						t.Fatal(err)
					}
					if !receipt.Revoked {
						t.Fatal("deletion acknowledgement was not durably settled")
					}
					if total := policyLedgerTotal(t, owner.StableID); total != before {
						t.Fatalf("deletion changed lifetime usage: before=%+v after=%+v", before, total)
					}
					authority := managedAuthorityForProcess(process)
					if authority == nil {
						t.Fatal("deletion recovery lost its issuance authority")
					}
					account, err := authority.state.Journal.Account(owner.StableID)
					if err != nil || !account.Deleted || account.HeldCapacity != 0 || account.Usage.RawUpload != uint64(before.RawUpload) || account.Usage.RawDownload != uint64(before.RawDownload) || account.Usage.BilledBytes != uint64(before.BilledBytes) {
						t.Fatalf("deletion failed to preserve a sealed durable tombstone: %+v/%v", account, err)
					}
					stale := proto.Clone(state.Policy).(*clientpolicy.PolicyConfig)
					stale.Version++
					stale.Enabled = true
					if err := api.Apply(ctx, []*clientpolicy.PolicyConfig{stale}); err == nil {
						t.Fatal("higher policy version revived a deleted identity")
					}
				}
			})
		}
	}
}

func TestClientPolicyPermanentDeletionRejectsPreparedIdentityReplay(t *testing.T) {
	svc, _, owner, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &config); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).Delete(&InboundService{}, owner.Id, false); err != nil {
		t.Fatal(err)
	}
	replayed := *owner
	replayed.Id = 0
	if err := database.GetDB().Create(&replayed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{owner.StableID}); !errors.Is(err, clientpolicy.ErrRevoked) {
		t.Fatalf("deleted identity received a desired policy: %v", err)
	}
	if _, err := PrepareClientPolicyLedger(config.InstanceID, owner.StableID); !errors.Is(err, clientpolicy.ErrRevoked) {
		t.Fatalf("deleted identity received an initialization seed: %v", err)
	}
}

func TestClientPolicyPermanentDeletionRollsBackIntentWithRecord(t *testing.T) {
	_, email, _ := seedReverseProbeInbound(t, "delete-rollback", 50077, true)
	owner := lookupClientRecord(t, email)
	db := database.GetDB()
	injected := errors.New("canonical deletion failed")
	const callback = "test:fail-canonical-client-deletion"
	if err := db.Callback().Delete().Before("gorm:delete").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(callback) })
	if _, err := (&ClientService{}).Delete(&InboundService{}, owner.Id, true); !errors.Is(err, injected) {
		t.Fatalf("missing SQL failure: %v", err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyTombstone{}).Where("client_id = ?", owner.StableID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed deletion committed its intent: count=%d err=%v", count, err)
	}
	if _, err := (&ClientService{}).GetByID(owner.Id); err != nil {
		t.Fatalf("failed deletion removed canonical record: %v", err)
	}
}

func TestClientPolicyPermanentDeletionRecoversStoppedOrLostControl(t *testing.T) {
	for _, lostControl := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-control-%t", lostControl), func(t *testing.T) {
			svc, _, owner, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			before := policyLedgerTotal(t, owner.StableID)
			const callback = "test:lose-control-after-canonical-delete"
			if lostControl {
				if err := database.GetDB().Callback().Delete().After("gorm:delete").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table != "clients" || tx.Error != nil {
						return
					}
					config := *process.GetConfig()
					var api conf.APIConfig
					if err := json.Unmarshal(config.API, &api); err != nil {
						tx.AddError(err)
						return
					}
					api.Listen += ".unavailable"
					data, err := json.Marshal(api)
					if err != nil {
						tx.AddError(err)
						return
					}
					config.API = data
					process.SetConfig(&config)
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = database.GetDB().Callback().Delete().Remove(callback) })
			} else {
				if err := svc.StopXray(); err != nil {
					t.Fatal(err)
				}
			}
			_, deleteErr := (&ClientService{}).Delete(&InboundService{}, owner.Id, false)
			if lostControl && !errors.Is(deleteErr, panelruntime.ErrManagedApply) {
				t.Fatalf("lost revocation control was hidden: %v", deleteErr)
			}
			if !lostControl && deleteErr != nil {
				t.Fatal(deleteErr)
			}
			if process.IsRunning() {
				t.Fatal("unacknowledged permanent deletion retained a running process")
			}
			var intent model.ClientPolicyTombstone
			if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&intent).Error; err != nil {
				t.Fatal(err)
			}
			if lostControl {
				_ = database.GetDB().Callback().Delete().Remove(callback)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			if err := requireRevokedClientPolicy(owner.StableID); err != nil {
				t.Fatalf("startup opened service before recovering the deletion: %v", err)
			}
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			var receipt model.ClientPolicyReceipt
			if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil || !receipt.Revoked {
				t.Fatalf("restart failed to settle permanent revocation: %+v %v", receipt, err)
			}
			if total := policyLedgerTotal(t, owner.StableID); total != before {
				t.Fatalf("revocation retry changed usage: before=%+v after=%+v", before, total)
			}
		})
	}
}

func requireRevokedClientPolicy(id string) error {
	process := currentXrayProcess()
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		return err
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	api, err := xray.DialClientPolicy(ctx, endpoint, config.InstanceID)
	if err != nil {
		return err
	}
	defer api.Close()
	state, err := api.GetClient(ctx, id)
	if err != nil {
		return err
	}
	if state.Reasons&uint32(clientpolicy.ReasonRevoked) == 0 {
		return errors.New("identity is not revoked")
	}
	return nil
}

func TestClientPolicyPermanentDeletionFencesNeverInitializedIdentity(t *testing.T) {
	setupPolicyLedgerDB(t)
	restore := SetXrayProcessForTest(nil)
	defer restore()
	owner := model.ClientRecord{Email: "deleted-before-initialization", Enable: true}
	if err := database.GetDB().Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := BindClientPolicySource("local", "uninitialized-core", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{owner.StableID}); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicyLedger("uninitialized-core", owner.StableID); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).Delete(&InboundService{}, owner.Id, false); err != nil {
		t.Fatal(err)
	}
	owner.Id = 0
	if err := database.GetDB().Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{owner.StableID}); !errors.Is(err, clientpolicy.ErrRevoked) {
		t.Fatalf("stale preparation revived uninitialized deletion: %v", err)
	}
	if _, err := PrepareClientPolicyLedger("uninitialized-core", owner.StableID); !errors.Is(err, clientpolicy.ErrRevoked) {
		t.Fatalf("stale initialization revived deleted identity: %v", err)
	}
}

func TestClientPolicyPermanentDeletionIncludesOrphanEntrypoints(t *testing.T) {
	for _, sweep := range []string{"manual", "sync"} {
		t.Run(sweep, func(t *testing.T) {
			svc, inbound, owner, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			cs := &ClientService{}
			if _, err := cs.Detach(&InboundService{}, owner.Id, []int{inbound.Id}); err != nil {
				t.Fatal(err)
			}
			var count int
			var err error
			if sweep == "manual" {
				count, err = cs.DeleteOrphans()
			} else {
				if err := database.GetDB().Model(owner).Update("sync_orphaned_at", time.Now().Add(-2*syncOrphanReapGrace).UnixMilli()).Error; err != nil {
					t.Fatal(err)
				}
				count, err = cs.ReapSyncOrphans()
			}
			if err != nil || count != 1 {
				t.Fatalf("orphan deletion failed: count=%d err=%v", count, err)
			}
			if err := requireRevokedClientPolicy(owner.StableID); err != nil {
				t.Fatalf("orphan sweep bypassed permanent revocation: %v", err)
			}
		})
	}
}

func TestClientPolicyPermanentDeletionEmptyBulkKeepsUnrelatedPendingWork(t *testing.T) {
	svc, inbound, owner, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	cs := &ClientService{}
	if _, err := cs.Detach(&InboundService{}, owner.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	if err := runSerializedTx(func(tx *gorm.DB) error {
		if err := recordClientPolicyTombstones(tx, []int{owner.Id}); err != nil {
			return err
		}
		return tx.Delete(&model.ClientRecord{}, owner.Id).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := requireRevokedClientPolicy(owner.StableID); err == nil {
		t.Fatal("fixture had already applied unrelated pending work")
	}
	result, _, err := cs.BulkDelete(&InboundService{}, []string{"does-not-exist"}, true)
	if err != nil || result.Deleted != 0 || len(result.Skipped) != 1 {
		t.Fatalf("empty successful subset changed response: %+v %v", result, err)
	}
	if err := requireRevokedClientPolicy(owner.StableID); err == nil {
		t.Fatal("all-skipped bulk deletion swept an unrelated identity")
	}
}

func TestClientPolicyPermanentDeletionBootstrapTraversesHistory(t *testing.T) {
	setupPolicyLedgerDB(t)
	const instanceID = "large-deletion-history"
	const count = 100001
	for start := 0; start < count; start += 500 {
		var tombstones []model.ClientPolicyTombstone
		var receipts []model.ClientPolicyReceipt
		for i := start; i < min(start+500, count); i++ {
			id := fmt.Sprintf("deleted-%06d", i)
			tombstones = append(tombstones, model.ClientPolicyTombstone{ClientID: id})
			receipts = append(receipts, model.ClientPolicyReceipt{InstanceID: instanceID, ClientID: id})
		}
		if err := database.GetDB().Create(&tombstones).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.GetDB().Create(&receipts).Error; err != nil {
			t.Fatal(err)
		}
	}
	bootstrap, err := PrepareLocalClientPolicyBootstrap(&command.Capabilities{ApiVersion: 1, InstanceId: instanceID, Epoch: 1}, &conf.ClientPolicyConfig{InstanceID: instanceID, StateFile: "/unused-state"})
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	after := ""
	for {
		page, err := bootstrap.DeletedClientPage(context.Background(), after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 1000 {
			t.Fatalf("unbounded deletion page: %d", len(page))
		}
		for _, id := range page {
			if id != fmt.Sprintf("deleted-%06d", seen) {
				t.Fatalf("history skipped or repeated: index=%d id=%q", seen, id)
			}
			seen++
		}
		if err := bootstrap.ConfirmAbsentClients(context.Background(), page); err != nil {
			t.Fatal(err)
		}
		after = page[len(page)-1]
	}
	if seen != count || after != "deleted-100000" {
		t.Fatalf("startup truncated pending history: got %d, want %d", seen, count)
	}
	if page, err := bootstrap.DeletedClientPage(context.Background(), ""); err != nil || len(page) != 0 {
		t.Fatalf("confirmed absence was rescanned: %d %v", len(page), err)
	}
	var revoked int64
	if err := database.GetDB().Model(&model.ClientPolicyReceipt{}).Where("revoked = ?", true).Count(&revoked).Error; err != nil || revoked != 0 {
		t.Fatalf("absence fabricated ledger acknowledgement: %d %v", revoked, err)
	}
	if err := database.GetDB().Model(&model.ClientPolicyReceipt{}).Where("client_id = ?", "deleted-100000").Update("policy_version", 1).Error; err != nil {
		t.Fatal(err)
	}
	if page, err := bootstrap.DeletedClientPage(context.Background(), ""); err != nil || len(page) != 1 || page[0] != "deleted-100000" {
		t.Fatalf("later initialization hid pending revocation: %+v %v", page, err)
	}
}

func TestClientPolicyPermanentDeletionRetriesSQLFailureWithoutRebilling(t *testing.T) {
	for _, stage := range []string{"pending-read", "receipt-write"} {
		t.Run(stage, func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "warm")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			before := policyLedgerTotal(t, owner.StableID)
			db := database.GetDB()
			var deleted, injected atomic.Bool
			fault := errors.New("deletion SQL acknowledgement unavailable")
			const callback = "test:deletion-sql-failure"
			if err := db.Callback().Delete().After("gorm:delete").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "clients" && tx.Error == nil {
					deleted.Store(true)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if stage == "pending-read" && deleted.Load() && tx.Statement.TableExpr != nil && strings.Contains(tx.Statement.TableExpr.SQL, "client_policy_tombstones") {
					injected.Store(true)
					tx.AddError(fault)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				if stage == "receipt-write" && deleted.Load() && tx.Statement.Table == "client_policy_receipts" {
					injected.Store(true)
					tx.AddError(fault)
				}
			}); err != nil {
				t.Fatal(err)
			}
			remove := func() {
				_ = db.Callback().Delete().Remove(callback)
				_ = db.Callback().Query().Remove(callback)
				_ = db.Callback().Update().Remove(callback)
			}
			t.Cleanup(remove)
			_, err = (&ClientService{}).Delete(&InboundService{}, owner.Id, false)
			if !injected.Load() || !errors.Is(err, panelruntime.ErrManagedApply) || !errors.Is(err, fault) {
				t.Fatalf("deletion hid SQL failure: injected=%t err=%v", injected.Load(), err)
			}
			if currentXrayProcess().IsRunning() {
				t.Fatal("unacknowledged permanent deletion retained business access")
			}
			remove()
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			if err := requireRevokedClientPolicy(owner.StableID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, _, err := svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
			}
			if total := policyLedgerTotal(t, owner.StableID); total != before {
				t.Fatalf("retry rebilled lifetime usage: before=%+v after=%+v", before, total)
			}
			var receipt model.ClientPolicyReceipt
			if err := db.First(&receipt, "client_id = ?", owner.StableID).Error; err != nil || !receipt.Revoked {
				t.Fatalf("recovery did not settle the revoke: %+v %v", receipt, err)
			}
		})
	}
}

func TestClientPolicyPermanentDeletionOrphanEmailMarkerStaysWithinWriter(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback-%t", fail), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			restore := SetXrayProcessForTest(nil)
			defer restore()
			StartTrafficWriter()
			t.Cleanup(StopTrafficWriter)
			owner := model.ClientRecord{Email: "orphan-reused-email", Enable: true}
			db := database.GetDB()
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { withdrawClientTombstones(owner.Email) })
			var markedBeforeRelease atomic.Bool
			fault := errors.New("orphan deletion rollback")
			const callback = "test:orphan-deletion-marker"
			if err := db.Callback().Delete().After("gorm:delete").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "clients" {
					markedBeforeRelease.Store(isClientEmailTombstoned(owner.Email))
					if fail {
						tx.AddError(fault)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Delete().Remove(callback) })
			count, err := (&ClientService{}).DeleteOrphans()
			if !markedBeforeRelease.Load() {
				t.Fatal("email marker can arrive after a queued replacement client has committed")
			}
			if fail {
				if !errors.Is(err, fault) || count != 0 || isClientEmailTombstoned(owner.Email) {
					t.Fatalf("failed orphan deletion retained email marker: count=%d err=%v", count, err)
				}
				return
			}
			if err != nil || count != 1 {
				t.Fatalf("orphan deletion: count=%d err=%v", count, err)
			}
			ib := mkInbound(t, 40571, model.VLESS, `{"decryption":"none","clients":[]}`)
			if _, err := (&ClientService{}).Create(&InboundService{}, &ClientCreatePayload{Client: model.Client{Email: owner.Email, Enable: true, ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e"}, InboundIds: []int{ib.Id}}); err != nil {
				t.Fatal(err)
			}
			if isClientEmailTombstoned(owner.Email) {
				t.Fatal("deleted identity's email marker outlived its replacement")
			}
			current := lookupClientRecord(t, owner.Email)
			if current.StableID == owner.StableID {
				t.Fatal("replacement revived permanent identity")
			}
		})
	}
}

func TestClientPolicyPermanentDeletionPreservesSiblingAndReplacement(t *testing.T) {
	svc, tunnel, _, target := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ib := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	sibling, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()
	managedActivationEcho(t, sibling, "warm")
	cs, is := &ClientService{}, &InboundService{}
	client := model.Client{Email: "deletion-reused", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
		t.Fatal(err)
	}
	old := lookupClientRecord(t, client.Email)
	flow := managedActivationVLESS(t, port, target, client.ID)
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	oldTotal := policyLedgerTotal(t, old.StableID)
	if _, err := cs.Delete(is, old.Id, false); err != nil {
		t.Fatal(err)
	}
	managedActivationClosed(t, flow)
	if err := requireRevokedClientPolicy(old.StableID); err != nil {
		t.Fatal(err)
	}
	client.ID = "01dc4f70-3902-446a-98cb-c00992d1a6c5"
	if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
		t.Fatal(err)
	}
	replacement := lookupClientRecord(t, client.Email)
	if replacement.StableID == old.StableID {
		t.Fatal("reused email revived revoked identity")
	}
	managedActivationVLESS(t, port, target, client.ID)
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, replacement.StableID); total.RawUpload != 4 || total.RawDownload != 4 || total.BilledBytes != 16 {
		t.Fatalf("replacement inherited deleted identity's final usage: %+v", total)
	}
	if total := policyLedgerTotal(t, old.StableID); total != oldTotal {
		t.Fatalf("replacement changed deleted identity history: before=%+v after=%+v", oldTotal, total)
	}
	managedActivationEcho(t, sibling, "stay")
	if currentXrayProcess() != process {
		t.Fatal("one identity deletion restarted the unrelated live flow")
	}
}
