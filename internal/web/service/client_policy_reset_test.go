package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func settleResetEngine(t *testing.T, e *clientpolicy.Engine) {
	t.Helper()
	after, err := ClientPolicyLedgerCursor("core-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	records, err := e.ReadLedger(after, 1000)
	if err != nil {
		t.Fatal(err)
	}
	page := &command.LedgerPage{NextSequence: after}
	for _, r := range records {
		page.Records = append(page.Records, &command.LedgerRecord{InstanceId: r.InstanceID, Epoch: r.Epoch, Sequence: r.Sequence, ClientId: r.ClientID, PolicyVersion: r.PolicyVersion, UncertainBytes: r.UncertainBytes, ReservedBytes: r.ReservedBytes, Revoked: r.Revoked, Usage: &command.Usage{RawUpload: r.Usage.RawUpload, RawDownload: r.Usage.RawDownload, BilledBytes: r.Usage.BilledBytes, Remainder: r.Usage.Remainder}})
		page.NextSequence = r.Sequence
	}
	if err := SettleClientPolicyLedger("core-a", e.Capabilities().Epoch, after, page); err != nil {
		t.Fatal(err)
	}
}

func TestClientPolicyResetRetriesKeepTheLatestWindowAndLifetime(t *testing.T) {
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "reset-owner", 0, 0)
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", id).Updates(map[string]any{"policy_multiplier": "0.5", "total_gb": 2}).Error; err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{id})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "core.db")
	if err := clientpolicy.CreateStore(path, "core-a"); err != nil {
		t.Fatal(err)
	}
	e, err := clientpolicy.OpenPersistentEngine(path, "core-a")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Initialize(policies[0], clientpolicy.Usage{}); err != nil {
		t.Fatal(err)
	}
	session, err := e.Open(context.Background(), clientpolicy.Metadata{ClientID: id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Admit(clientpolicy.Upload, 3); err != nil {
		t.Fatal(err)
	}
	settleResetEngine(t, e)
	first, err := PrepareClientPolicyReset("core-a", id, "reset-one")
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 2 || first.QuotaBaselineBytes != 1 || first.QuotaBaselineRemainder != 500000 || !first.Enabled {
		t.Fatalf("reset did not preserve exact historical boundary: %+v", first)
	}
	if err := e.Apply(first); err != nil {
		t.Fatal(err)
	}
	if err := session.Admit(clientpolicy.Download, 4); err != nil {
		t.Fatal(err)
	}
	settleResetEngine(t, e)
	retry, err := PrepareClientPolicyReset("core-a", id, "reset-one")
	if err != nil || retry != first {
		t.Fatalf("retry captured new traffic as a fresh window: %+v %v", retry, err)
	}
	second, err := PrepareClientPolicyReset("core-a", id, "reset-two")
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 3 || second.QuotaBaselineBytes != 3 || second.QuotaBaselineRemainder != 500000 {
		t.Fatalf("second boundary: %+v", second)
	}
	if err := e.Apply(second); err != nil {
		t.Fatal(err)
	}
	retry, err = PrepareClientPolicyReset("core-a", id, "reset-one")
	if err != nil || retry != second {
		t.Fatalf("late retry replaced newer reset: %+v %v", retry, err)
	}
	total := policyLedgerTotal(t, id)
	if total.RawUpload != 3 || total.RawDownload != 4 || total.BilledBytes != 3 || total.UncertainBytes != 0 {
		t.Fatalf("reset erased lifetime ledger: %+v", total)
	}
	var receipt model.ClientPolicyReceipt
	if err := database.GetDB().First(&receipt, "instance_id = ? AND client_id = ?", "core-a", id).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Remainder != 500000 || receipt.RawUpload != 3 || receipt.RawDownload != 4 {
		t.Fatalf("reset changed receipt: %+v", receipt)
	}
}

func resetLedgerFixture(t *testing.T) string {
	t.Helper()
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "reset-fixture", 0, 0)
	if _, err := PrepareClientPolicies([]string{id}); err != nil {
		t.Fatal(err)
	}
	page := policyLedgerPage(id, 1, 3, 0, 1)
	page.Records[0].Usage.Remainder = 500000
	page.Records[0].UncertainBytes, page.Records[0].ReservedBytes = 7, 10
	if err := SettleClientPolicyLedger("core-a", 1, 0, page); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClientPolicyResetConcurrentRetriesPreserveRestrictions(t *testing.T) {
	id := resetLedgerFixture(t)
	expired := time.Now().Add(-time.Hour).UnixMilli()
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", id).Updates(map[string]any{"enable": false, "expiry_time": expired, "total_gb": 2}).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			policy, err := PrepareClientPolicyReset("core-a", id, "same-request")
			if err != nil || policy.Version != 2 || policy.QuotaBaselineBytes != 8 || policy.QuotaBaselineRemainder != 500000 || policy.Enabled || policy.ExpiresAt != expired {
				t.Errorf("concurrent reset granted duplicate quota or cleared restrictions: %+v %v", policy, err)
			}
		})
	}
	wg.Wait()
	var count int64
	if err := database.GetDB().Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate reset records: %d %v", count, err)
	}
	policies, err := PrepareClientPolicies([]string{id})
	if err != nil || len(policies) != 1 || policies[0].Version != 2 || policies[0].QuotaBaselineBytes != 8 || policies[0].QuotaBaselineRemainder != 500000 {
		t.Fatalf("normal reconciliation lost the persisted reset: %+v %v", policies, err)
	}
}

func TestClientPolicyResetInsertionFailureRollsBackVersion(t *testing.T) {
	id := resetLedgerFixture(t)
	db := database.GetDB()
	injected := errors.New("injected reset insertion failure")
	name := "test:reset-insert"
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_resets" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(name) })
	if _, err := PrepareClientPolicyReset("core-a", id, "failed-request"); !errors.Is(err, injected) {
		t.Fatalf("lost insertion failure: %v", err)
	}
	var record model.ClientRecord
	if err := db.First(&record, "stable_id = ?", id).Error; err != nil || record.DesiredPolicyVersion != 1 {
		t.Fatalf("failed reset committed a policy version: %+v %v", record, err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed reset left intent: %d %v", count, err)
	}
	if err := db.Callback().Create().Remove(name); err != nil {
		t.Fatal(err)
	}
	policy, err := PrepareClientPolicyReset("core-a", id, "failed-request")
	if err != nil || policy.Version != 2 || policy.QuotaBaselineBytes != 8 {
		t.Fatalf("retry skipped a version or lost boundary: %+v %v", policy, err)
	}
}

func TestClientPolicyResetRejectsSharedRemoteBudget(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepared bool
		version  int64
	}{
		{"new-request", false, 1}, {"retry-after-attachment", true, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := resetLedgerFixture(t)
			if test.prepared {
				if _, err := PrepareClientPolicyReset("core-a", id, "remote-request"); err != nil {
					t.Fatal(err)
				}
			}
			var client model.ClientRecord
			if err := database.GetDB().First(&client, "stable_id = ?", id).Error; err != nil {
				t.Fatal(err)
			}
			remote := mkInbound(t, 24199, model.Tunnel, `{ "address":"127.0.0.1", "port":9001 }`)
			if err := database.GetDB().Model(remote).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Create(&model.ClientInbound{ClientId: client.Id, InboundId: remote.Id}).Error; err != nil {
				t.Fatal(err)
			}
			policy, err := PrepareClientPolicyReset("core-a", id, "remote-request")
			if !errors.Is(err, ErrClientPolicyLedger) || policy != (clientpolicy.Policy{}) {
				t.Fatalf("reset a global client without coordinated node budgets: %+v %v", policy, err)
			}
			if err := database.GetDB().First(&client, client.Id).Error; err != nil || client.DesiredPolicyVersion != test.version {
				t.Fatalf("rejected reset changed version: %+v %v", client, err)
			}
		})
	}
}

func TestClientPolicyResetFutureVersionFailsReconciliation(t *testing.T) {
	id := resetLedgerFixture(t)
	reset := model.ClientPolicyReset{ClientID: id, RequestID: "future-version", InstanceID: "core-a", Epoch: 1, Sequence: 1, RawUpload: 3, BilledBytes: 1, Remainder: 500000, UncertainBytes: 7, PolicyVersion: 2}
	if err := database.GetDB().Create(&reset).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{id}); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("policy version rollback concealed by reset: %v", err)
	}
}

func TestClientPolicyResetPendingIgnoresUnrelatedRevokedClients(t *testing.T) {
	for _, withReset := range []bool{false, true} {
		t.Run(fmt.Sprintf("previous-reset-%t", withReset), func(t *testing.T) {
			id := resetLedgerFixture(t)
			if _, err := PrepareClientPolicyReset("core-a", id, "pending"); err != nil {
				t.Fatal(err)
			}
			revokedID := policyLedgerClient(t, "unrelated-revoked", 0, 0)
			if _, err := PrepareClientPolicies([]string{revokedID}); err != nil {
				t.Fatal(err)
			}
			page := policyLedgerPage(revokedID, 2, 0, 0, 0)
			if err := SettleClientPolicyLedger("core-a", 1, 1, page); err != nil {
				t.Fatal(err)
			}
			if withReset {
				if _, err := PrepareClientPolicyReset("core-a", revokedID, "already-applied"); err != nil {
					t.Fatal(err)
				}
			}
			page = policyLedgerPage(revokedID, 3, 0, 0, 0)
			page.Records[0].PolicyVersion = 2
			page.Records[0].Revoked = true
			if err := SettleClientPolicyLedger("core-a", 1, 2, page); err != nil {
				t.Fatal(err)
			}
			pending, err := pendingClientPolicyResetIDs(database.GetDB(), "core-a", []clientpolicy.Policy{
				{ClientID: id, Version: 1}, {ClientID: revokedID, Version: 2},
			})
			if err != nil || len(pending) != 1 || pending[0] != id {
				t.Fatalf("unrelated revoked client blocked a pending reset: %v, %v", pending, err)
			}
		})
	}
}

func TestClientPolicyResetPostgresCommitFailureKeepsPriorPolicy(t *testing.T) {
	if os.Getenv("XUI_DB_TYPE") != "postgres" || os.Getenv("XUI_DB_DSN") == "" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	id := resetLedgerFixture(t)
	db := database.GetDB()
	installDeferredCommitFailure(t, db, "create", "test:reset-commit", "client_policy_resets", "reset_fail_parent", "reset_fail_child")
	policy, err := PrepareClientPolicyReset("core-a", id, "commit-failure")
	var sqlError interface{ SQLState() string }
	if !errors.As(err, &sqlError) || sqlError.SQLState() != "23503" || policy.Version != 0 {
		t.Fatalf("expected deferred constraint failure without an applicable policy: %+v %v", policy, err)
	}
	var client model.ClientRecord
	if err := db.First(&client, "stable_id = ?", id).Error; err != nil || client.DesiredPolicyVersion != 1 {
		t.Fatalf("failed commit changed policy version: %+v %v", client, err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed commit retained reset request: %d %v", count, err)
	}
	if err := db.Callback().Create().Remove("test:reset-commit"); err != nil {
		t.Fatal(err)
	}
	policy, err = PrepareClientPolicyReset("core-a", id, "commit-failure")
	if err != nil || policy.Version != 2 || policy.QuotaBaselineBytes != 8 || policy.QuotaBaselineRemainder != 500000 {
		t.Fatalf("post-failure retry: %+v %v", policy, err)
	}
}

func TestClientPolicyResetRejectsUnsettledRevokedAndMultipleSources(t *testing.T) {
	for _, scenario := range []string{"unsettled", "revoked", "multiple-sources"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			if err := BindClientPolicySource("local", "core-a", 1); err != nil {
				t.Fatal(err)
			}
			id := policyLedgerClient(t, "guarded-reset", 0, 0)
			if _, err := PrepareClientPolicies([]string{id}); err != nil {
				t.Fatal(err)
			}
			if scenario != "unsettled" {
				page := policyLedgerPage(id, 1, 10, 20, 30)
				page.Records[0].Revoked = scenario == "revoked"
				if err := SettleClientPolicyLedger("core-a", 1, 0, page); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "multiple-sources" {
				if err := BindClientPolicySource("remote", "core-b", 1); err != nil {
					t.Fatal(err)
				}
				if _, err := PrepareClientPolicyLedger("core-b", id); err != nil {
					t.Fatal(err)
				}
			}
			if policy, err := PrepareClientPolicyReset("core-a", id, "forbidden"); !errors.Is(err, ErrClientPolicyLedger) || policy.Version != 0 {
				t.Fatalf("accepted %s reset: %+v %v", scenario, policy, err)
			}
			var count int64
			if err := database.GetDB().Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected reset persisted an intent: %d %v", count, err)
			}
		})
	}
}
