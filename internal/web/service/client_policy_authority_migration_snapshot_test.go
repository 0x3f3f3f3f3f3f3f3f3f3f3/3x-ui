package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/common"
)

func authorityMigrationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	previousManual, previousRestart := isManuallyStopped.Load(), isNeedXrayRestart.Load()
	t.Cleanup(func() {
		isManuallyStopped.Store(previousManual)
		isNeedXrayRestart.Store(previousRestart)
	})
	setupPolicyLedgerDB(t)
	prior, _ := xrayState.snapshot()
	xrayState.replace(nil)
	t.Cleanup(func() { xrayState.replace(prior) })
	c := model.ClientRecord{Email: "migration-owner", Enable: true, TotalGB: 100, SnellPSK: "fixture-psk-must-not-enter-accounting-evidence", Policy: &model.ClientPolicyOptions{Multiplier: "1.5", UploadBytesPerSecond: 8192, DownloadBytesPerSecond: 4096}}
	if err := database.GetDB().Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&panelxray.ClientTraffic{Email: c.Email, Up: 3, Down: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := BindClientPolicySource("local", "migration-source", 1); err != nil {
		t.Fatal(err)
	}
	seed, err := PrepareClientPolicyLedger("migration-source", c.StableID)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{c.StableID})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "authority-migration-source-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained stopped legacy execution fixture: %s", dir)
	path := filepath.Join(dir, "state.db")
	if err := clientpolicy.CreateStore(path, "migration-source"); err != nil {
		t.Fatal(err)
	}
	e, err := clientpolicy.OpenPersistentEngine(path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.Initialize(policies[0], clientpolicy.Usage{RawUpload: seed.RawUpload, RawDownload: seed.RawDownload, BilledBytes: seed.BilledBytes}); err != nil {
		t.Fatal(err)
	}
	s, err := e.Open(context.Background(), clientpolicy.Metadata{ClientID: c.StableID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(clientpolicy.Upload, 3); err != nil {
		t.Fatal(err)
	}
	if err := e.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	rows, err := e.ReadLedger(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	page := &command.LedgerPage{}
	for _, r := range rows {
		page.Records = append(page.Records, &command.LedgerRecord{InstanceId: r.InstanceID, ClientId: r.ClientID, Epoch: r.Epoch, Sequence: r.Sequence, PolicyVersion: r.PolicyVersion, FirstUsedAt: r.FirstUsedAt, Usage: &command.Usage{RawUpload: r.Usage.RawUpload, RawDownload: r.Usage.RawDownload, BilledBytes: r.Usage.BilledBytes, Remainder: r.Usage.Remainder}, UncertainBytes: r.UncertainBytes, ReservedBytes: r.ReservedBytes, Revoked: r.Revoked})
		page.NextSequence = r.Sequence
	}
	if err := SettleClientPolicyLedger("migration-source", 1, 0, page); err != nil {
		t.Fatal(err)
	}
	reset, err := PrepareClientPolicyReset("migration-source", c.StableID, "original-reset")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(reset); err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(clientpolicy.Download, 2); err != nil {
		t.Fatal(err)
	}
	deleted := uuid.NewString()
	if err := database.GetDB().Create(&model.ClientPolicyTombstone{ClientID: deleted}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientPolicyTotal{ClientID: deleted, RawUpload: 7, RawDownload: 3, BilledBytes: 20}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientPolicyReceipt{InstanceID: "migration-source", ClientID: deleted, RawUpload: 7, RawDownload: 3, BilledBytes: 20, SeedUpload: 7, SeedDownload: 3, SeedBilled: 20}).Error; err != nil {
		t.Fatal(err)
	}
	dp := policies[0]
	dp.ClientID = deleted
	if err := e.Initialize(dp, clientpolicy.Usage{RawUpload: 7, RawDownload: 3, BilledBytes: 20, Remainder: 200000}); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return path, c.StableID, deleted
}

func TestAuthorityMigrationResetHistoryRecoversOlderDesiredSQLWithoutReopeningWindow(t *testing.T) {
	path, clientID, _ := authorityMigrationFixture(t)
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := policyauthority.CreateWithMigration(filepath.Join(filepath.Dir(path), "recovery-journal.db"), snapshot.Seeds, snapshot.Deleted, snapshot.Records)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	account, err := j.Account(clientID)
	if err != nil {
		t.Fatal(err)
	}
	var client model.ClientRecord
	ctx := owner.lease.Context(context.Background())
	if err := owner.source.WithContext(ctx).First(&client, "stable_id = ?", clientID).Error; err != nil {
		t.Fatal(err)
	}
	_, originalFingerprint, err := fingerprintClientPolicy(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runSerializedTxContextForDatabase(ctx, owner.source, func(tx *gorm.DB) error {
		if err := tx.Where("client_id = ?", clientID).Delete(&model.ClientPolicyReset{}).Error; err != nil {
			return err
		}
		return tx.Table("clients").Where("stable_id = ?", clientID).Updates(map[string]any{"desired_policy_version": 1, "policy_fingerprint": originalFingerprint}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityAccount(ctx, owner.source, j, "migration-source", account); err != nil {
		t.Fatal(err)
	}
	policies, err := prepareClientPoliciesContextForDatabase(ctx, owner.source, []string{clientID}, nil)
	if err != nil || len(policies) != 1 || policies[0].Version != account.Policy.Version || policies[0].QuotaBaselineBytes != 9 || policies[0].QuotaBaselineRemainder != 500000 {
		t.Fatalf("original migrated reset was lost or reopened: %+v/%v", policies, err)
	}
	reset, err := authorityProtectedReset(j, account)
	if err != nil || reset == nil || reset.RequestID != "original-reset" {
		t.Fatalf("original migration reset lacks protected acknowledgement: %+v/%v", reset, err)
	}
	after, err := j.Account(clientID)
	if err != nil || after != account {
		t.Fatalf("SQL history recovery changed journal usage/window: %+v/%+v/%v", account, after, err)
	}
}

func TestAuthorityMigrationFreezesAbruptReservationWithoutInventingDeliveredBytes(t *testing.T) {
	setupPolicyLedgerDB(t)
	prior, _ := xrayState.snapshot()
	xrayState.replace(nil)
	t.Cleanup(func() { xrayState.replace(prior) })
	c := model.ClientRecord{Email: "migration-crash", Enable: true, TotalGB: 100, Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}}
	if err := database.GetDB().Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&panelxray.ClientTraffic{Email: c.Email, Up: 3, Down: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := BindClientPolicySource("local", "migration-source", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicyLedger("migration-source", c.StableID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicies([]string{c.StableID}); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "authority-migration-crash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained abrupt migration fixture: %s", dir)
	path := filepath.Join(dir, "state.db")
	if err := clientpolicy.CreateStore(path, "migration-source"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityMigrationCrashHelper$")
	cmd.Env = append(os.Environ(), "AUTHORITY_MIGRATION_CRASH_PATH="+path, "AUTHORITY_MIGRATION_CLIENT_ID="+c.StableID)
	out, err := cmd.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 29 {
		t.Fatalf("helper did not reach abrupt reservation boundary: %v/%s", err, out)
	}
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Seeds) != 1 {
		t.Fatalf("crashed migration account count: %d", len(snapshot.Seeds))
	}
	seed := snapshot.Seeds[0]
	if seed.Usage != (policyauthority.Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 5}) || seed.WindowUsed != 5 || seed.FrozenBilled != 95 {
		t.Fatalf("crash migration credited reservation or invented raw delivery: %+v", seed)
	}
	j, _, err := policyauthority.CreateWithMigration(filepath.Join(dir, "journal.db"), snapshot.Seeds, snapshot.Deleted, snapshot.Records)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: "migration-source", BootID: "new-boot"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: c.StableID, WindowID: seed.Policy.WindowID, PolicyVersion: seed.Policy.Version}, RequestID: "after-crash", ChallengeID: "fresh-challenge", Capacity: 1, Upload: policyauthority.Direction{Unlimited: true}, Download: policyauthority.Direction{Unlimited: true}, LeaseDuration: time.Second}
	if _, err := j.Issue(request); !errors.Is(err, policyauthority.ErrCapacity) {
		t.Fatalf("unconfirmed crash reservation became available: %v", err)
	}
}

func TestAuthorityMigrationRejectsReorderedLegacyResetVersionTie(t *testing.T) {
	path, clientID, _ := authorityMigrationFixture(t)
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	// Older writers allowed two distinct requests at unchanged usage to share a
	// version. The original journal window identifies the second request, while
	// key-order insertion into restored SQL would give the first a newer row ID.
	var earlier policyauthority.MigrationRecord
	for i, record := range snapshot.Records {
		if record.Kind != "resets" {
			continue
		}
		var reset model.ClientPolicyReset
		if err := json.Unmarshal(record.Value, &reset); err != nil {
			t.Fatal(err)
		}
		if reset.ClientID != clientID {
			continue
		}
		reset.Id = 2
		encoded, err := json.Marshal(reset)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records[i].Value = encoded
		reset.Id, reset.RequestID = 1, "zz-earlier-idle-reset"
		encoded, err = json.Marshal(reset)
		if err != nil {
			t.Fatal(err)
		}
		earlier = policyauthority.MigrationRecord{Kind: "resets", Key: clientID + "/" + reset.RequestID, Value: encoded}
		break
	}
	if earlier.Key == "" {
		t.Fatal("fixture lacks original reset history")
	}
	snapshot.Records = append(snapshot.Records, earlier)
	j, _, err := policyauthority.CreateWithMigration(filepath.Join(filepath.Dir(path), "legacy-tie-journal.db"), snapshot.Seeds, snapshot.Deleted, snapshot.Records)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	account, err := j.Account(clientID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := owner.lease.Context(context.Background())
	if err := runSerializedTxContextForDatabase(ctx, owner.source, func(tx *gorm.DB) error {
		return tx.Where("client_id = ?", clientID).Delete(&model.ClientPolicyReset{}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityAccount(ctx, owner.source, j, "migration-source", account); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("restored SQL row IDs silently selected a different legacy window: %v", err)
	}
	var rows []model.ClientPolicyReset
	if err := owner.source.WithContext(ctx).Where("client_id = ?", clientID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rejected recovery published contradictory reset history: %+v", rows)
	}
	after, err := j.Account(clientID)
	if err != nil || after != account {
		t.Fatalf("rejected recovery changed protected usage: %+v %v", after, err)
	}
}

func TestAuthorityMigrationCrashHelper(t *testing.T) {
	path := os.Getenv("AUTHORITY_MIGRATION_CRASH_PATH")
	if path == "" {
		return
	}
	client := os.Getenv("AUTHORITY_MIGRATION_CLIENT_ID")
	e, err := clientpolicy.OpenPersistentEngine(path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	p := clientpolicy.Policy{ClientID: client, Version: 1, Enabled: true, Multiplier: 1500000, QuotaBytes: 100, BurstBytes: 65536}
	if err := e.Initialize(p, clientpolicy.Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 5}); err != nil {
		t.Fatal(err)
	}
	s, err := e.Open(context.Background(), clientpolicy.Metadata{ClientID: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Admit(clientpolicy.Upload, 1); err != nil {
		t.Fatal(err)
	}
	os.Exit(29)
}

func TestAuthorityMigrationRejectsPreviouslyFundedExecutionOrSQL(t *testing.T) {
	for _, origin := range []string{"execution", "SQL"} {
		t.Run(origin, func(t *testing.T) {
			path, client, _ := authorityMigrationFixture(t)
			if origin == "SQL" {
				if err := database.GetDB().Create(&model.ClientPolicyAuthorityProjection{ClientID: client, AuthorityID: "existing-authority", Generation: 1, Revision: 1, AccountJSON: "{}"}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				object, err := common.CreateObject(context.Background(), &clientpolicy.Config{StateFile: path, InstanceId: "migration-source"})
				if err != nil {
					t.Fatal(err)
				}
				e := object.(*clientpolicy.Engine)
				t.Cleanup(func() { _ = e.Close() })
				if err := e.Start(); err != nil {
					t.Fatal(err)
				}
				caps := e.Capabilities()
				binding := clientpolicy.AuthorityBinding{AuthorityID: "existing-authority", Generation: 1, NodeID: "local"}
				if err := e.BindAuthority(caps.BootID, binding); err != nil {
					t.Fatal(err)
				}
				challenge, err := e.BeginAuthorityChallenge(caps.BootID)
				if err != nil {
					t.Fatal(err)
				}
				grant := clientpolicy.ExecutionGrant{Authority: binding, InstanceID: caps.InstanceID, BootID: caps.BootID, ClientID: client, WindowID: "existing-window", PolicyVersion: 2, GrantID: "already-issued", Sequence: 1, ChallengeID: challenge.ChallengeID, Capacity: 40, Upload: clientpolicy.AuthorityShare{Rate: 8192, Burst: 65536}, Download: clientpolicy.AuthorityShare{Rate: 4096, Burst: 65536}, LeaseDuration: time.Second}
				if _, err := e.InstallAuthorityGrant(grant); err != nil {
					t.Fatal(err)
				}
				if err := e.Close(); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := acquireDatabaseRestore()
			if err != nil {
				t.Fatal(err)
			}
			defer owner.release()
			if err := owner.fenceDatabase(); err != nil {
				t.Fatal(err)
			}
			if snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source"); !errors.Is(err, ErrClientPolicyLedger) || snapshot.Seeds != nil {
				t.Fatalf("funded %s reinitialized a new authority: %+v/%v", origin, snapshot.Seeds, err)
			}
		})
	}
}

func TestAuthorityMigrationRejectsMissingReceiptInsteadOfCreditingUnsettledUsage(t *testing.T) {
	path, client, _ := authorityMigrationFixture(t)
	// Retain the stopped core's 12.5 billed bytes while SQL still has 9 bytes;
	// without the old receipt there is no trustworthy cumulative delta.
	if err := database.GetDB().Where("client_id = ?", client).Delete(&model.ClientPolicyReceipt{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Where("client_id = ?", client).Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source"); !errors.Is(err, ErrClientPolicyLedger) || snapshot.Seeds != nil {
		t.Fatalf("missing receipt recreated an older billing baseline: %+v/%v", snapshot.Seeds, err)
	}
}

func TestAuthorityMigrationRejectsRegressedGlobalTotalsAgainstSourceReceipt(t *testing.T) {
	path, client, _ := authorityMigrationFixture(t)
	if err := database.GetDB().Model(&model.ClientPolicyTotal{}).Where("client_id = ?", client).Updates(map[string]any{"raw_upload": 0, "raw_download": 0, "billed_bytes": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Where("client_id = ?", client).Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source"); !errors.Is(err, ErrClientPolicyLedger) || snapshot.Seeds != nil {
		t.Fatalf("regressed SQL totals became a new allowance: %+v/%v", snapshot.Seeds, err)
	}
}

func TestAuthorityMigrationSQLFailureRollsBackFinalSettlementAndExposesNoSnapshot(t *testing.T) {
	path, client, _ := authorityMigrationFixture(t)
	before := policyLedgerTotal(t, client)
	db := database.GetDB()
	injected := errors.New("migration SQL source commit failed")
	name := "authority_migration_source_fault"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_sources" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source")
	if !errors.Is(err, injected) || snapshot.Seeds != nil || snapshot.Records != nil {
		t.Fatalf("failed settlement exposed partial migration data: %+v/%v", snapshot, err)
	}
	var after model.ClientPolicyTotal
	if err := owner.currentDatabase().First(&after, "client_id = ?", client).Error; err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("failed capture committed only part of the final counters: %+v/%+v", before, after)
	}
}

func TestAuthorityMigrationCapturesStoppedCoreAndOriginalSQLHistory(t *testing.T) {
	path, client, deleted := authorityMigrationFixture(t)
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	seeds := make(map[string]policyauthority.Seed)
	for _, s := range snapshot.Seeds {
		seeds[s.ClientID] = s
	}
	s := seeds[client]
	if s.Usage != (policyauthority.Usage{RawUpload: 6, RawDownload: 4, BilledBytes: 12, Remainder: 500000}) || s.WindowUsed != 3 || s.WindowRemainder != 0 || s.FrozenBilled != 0 || s.Policy.Version != 2 || s.Policy.QuotaBytes != 100 || s.Policy.Upload.Rate != 8192 || s.Policy.Download.Rate != 4096 {
		t.Fatalf("migration changed historical multiplier/reset boundary: %+v", s)
	}
	if len(snapshot.Deleted) != 1 || snapshot.Deleted[0] != deleted || seeds[deleted].Usage != (policyauthority.Usage{RawUpload: 7, RawDownload: 3, BilledBytes: 20, Remainder: 200000}) {
		t.Fatalf("migration lost terminal history: %+v/%+v", snapshot.Deleted, seeds[deleted])
	}
	var resetFound, receiptFound, tombstoneFound bool
	for _, record := range snapshot.Records {
		if bytes.Contains(record.Value, []byte("fixture-psk")) {
			t.Fatal("business credential entered accounting migration evidence")
		}
		if record.Kind == "resets" {
			var r model.ClientPolicyReset
			if err := json.Unmarshal(record.Value, &r); err != nil {
				t.Fatal(err)
			}
			resetFound = r.RequestID == "original-reset" && r.BilledBytes == 9 && r.Remainder == 500000
		}
		if record.Kind == "receipts" {
			var r model.ClientPolicyReceipt
			if err := json.Unmarshal(record.Value, &r); err != nil {
				t.Fatal(err)
			}
			if r.ClientID == client {
				receiptFound = r.BilledBytes == 12 && r.Remainder == 500000
			}
		}
		if record.Kind == "tombstones" {
			tombstoneFound = true
		}
	}
	if !resetFound || !receiptFound || !tombstoneFound {
		t.Fatalf("missing original accounting evidence: reset=%v receipt=%v tombstone=%v", resetFound, receiptFound, tombstoneFound)
	}
	dir, err := os.MkdirTemp("", "authority-migration-target-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained authority migration target: %s", dir)
	j, _, err := policyauthority.CreateWithMigration(filepath.Join(dir, "journal.db"), snapshot.Seeds, snapshot.Deleted, snapshot.Records)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	a, err := j.Account(client)
	if err != nil || a.Seed != s {
		t.Fatalf("atomic migration changed captured seed: %+v/%v", a, err)
	}
}

func TestAuthorityMigrationRejectsUnownedCancelledAndRestoredOldExecution(t *testing.T) {
	path, _, _ := authorityMigrationFixture(t)
	if _, err := captureAuthorityMigration(context.Background(), nil, path, "migration-source"); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("migration accepted an unfenced snapshot: %v", err)
	}
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := captureAuthorityMigration(ctx, owner, path, "migration-source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled migration captured state: %v", err)
	}
	if err := owner.currentDatabase().Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Update("sequence", 999999).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := captureAuthorityMigration(context.Background(), owner, path, "migration-source"); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("migration accepted execution behind SQL: %v", err)
	}
}
