package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"gorm.io/gorm"
)

func authorityProjectionFixture(t *testing.T) (*policyauthority.Journal, policyauthority.Request) {
	t.Helper()
	setupPolicyLedgerDB(t)
	client := model.ClientRecord{Email: "authority-projection", Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "issuer")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	unlimited := policyauthority.Direction{Unlimited: true}
	j, id, err := policyauthority.Create(filepath.Join(dir, "journal.db"), []policyauthority.Seed{{ClientID: client.StableID, Policy: policyauthority.Policy{WindowID: "window-1", Version: 1, QuotaBytes: 100, Upload: unlimited, Download: unlimited}, Usage: policyauthority.Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 10, Remainder: 100000}, WindowUsed: 10, WindowRemainder: 100000}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: "source-1", BootID: "fresh-boot-1"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	r := policyauthority.Request{Binding: policyauthority.Binding{Identity: id, NodeBoot: boot, ClientID: client.StableID, WindowID: "window-1", PolicyVersion: 1}, RequestID: "request-1", ChallengeID: "challenge-1", Capacity: 40, Upload: unlimited, Download: unlimited, LeaseDuration: time.Second}
	return j, r
}

func authorityProjection(t *testing.T, id string) (model.ClientPolicyAuthorityProjection, policyauthority.Account) {
	t.Helper()
	var row model.ClientPolicyAuthorityProjection
	if err := database.GetDB().First(&row, "client_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	var account policyauthority.Account
	if err := json.Unmarshal([]byte(row.AccountJSON), &account); err != nil {
		t.Fatal(err)
	}
	return row, account
}

func TestAuthorityProjectionLostSQLCommitDoesNotReissueGrant(t *testing.T) {
	j, r := authorityProjectionFixture(t)
	db := database.GetDB()
	injected := errors.New("projection SQL failed after journal commit")
	callback := "authority_projection_fault"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_authority_projections" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	g, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if !errors.Is(err, injected) || g != (policyauthority.Grant{}) {
		t.Fatalf("uncertain SQL reply exposed grant: %+v/%v", g, err)
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	a, err := j.Account(r.Binding.ClientID)
	if err != nil || a.HeldCapacity != 40 || a.Revision != 2 {
		t.Fatalf("SQL failure rolled back journal: %+v/%v", a, err)
	}
	first, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if err != nil || first != retry {
		t.Fatalf("retry issued another grant: %+v/%v", retry, err)
	}
	row, projected := authorityProjection(t, r.Binding.ClientID)
	if row.Revision != 2 || projected != a || row.AuthorityID != j.Identity().AuthorityID {
		t.Fatalf("wrong projection: %+v/%+v", row, projected)
	}
}

func TestAuthorityProjectionRepairsOldSQLWithoutRecreatingBudget(t *testing.T) {
	j, r := authorityProjectionFixture(t)
	db := database.GetDB()
	if err := projectClientPolicyAuthority(context.Background(), db, j, r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	old, _ := authorityProjection(t, r.Binding.ClientID)
	g, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(policyauthority.Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: policyauthority.Usage{RawUpload: 2, BilledBytes: 4, Remainder: 200000}}); err != nil {
		t.Fatal(err)
	}
	if err := projectClientPolicyAuthority(context.Background(), db, j, r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientPolicyAuthorityProjection{}).Where("client_id = ?", r.Binding.ClientID).Updates(map[string]any{"revision": old.Revision, "account_json": old.AccountJSON}).Error; err != nil {
		t.Fatal(err)
	}
	retry, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if err != nil || retry.GrantID != g.GrantID {
		t.Fatalf("old SQL changed issuance: %+v/%v", retry, err)
	}
	row, a := authorityProjection(t, r.Binding.ClientID)
	if row.Revision != 3 || a.Usage.BilledBytes != 14 || a.Usage.Remainder != 300000 || a.HeldCapacity != 35 || a.HeldRemainder != 800000 {
		t.Fatalf("old SQL/aggregation changed committed counters: %+v/%+v", row, a)
	}
	oversized := r
	oversized.RequestID, oversized.Capacity = "extra", 50
	if _, err := issueClientPolicyAuthority(context.Background(), db, j, oversized); !errors.Is(err, policyauthority.ErrCapacity) {
		t.Fatalf("projection restore recreated capacity: %v", err)
	}
}

func TestAuthorityProjectionRejectsStalePoolAndRestoreMutations(t *testing.T) {
	j, r := authorityProjectionFixture(t)
	old := database.GetDB()
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := issueClientPolicyAuthority(context.Background(), old, j, r); !errors.Is(err, database.ErrDatabaseReplaced) {
		t.Fatalf("old pool issued authority capacity: %v", err)
	}
	lease, err := database.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := issueClientPolicyAuthority(context.Background(), database.GetDB(), j, r); !errors.Is(err, database.ErrRestoreInProgress) {
		t.Fatalf("unowned restore mutation issued grant: %v", err)
	}
	a, err := j.Account(r.Binding.ClientID)
	if err != nil || a.HeldCapacity != 0 || a.Revision != 1 {
		t.Fatalf("rejected lifecycle allocated capacity: %+v/%v", a, err)
	}
	if _, err := issueClientPolicyAuthority(lease.Context(context.Background()), database.GetDB(), j, r); err != nil {
		t.Fatalf("owned recovery could not project: %v", err)
	}
}

func TestAuthorityProjectionContradictionRejectsBeforeIssuance(t *testing.T) {
	for _, fault := range []string{"identity", "future-revision", "same-revision-payload"} {
		t.Run(fault, func(t *testing.T) {
			j, r := authorityProjectionFixture(t)
			db := database.GetDB()
			if err := projectClientPolicyAuthority(context.Background(), db, j, r.Binding.ClientID); err != nil {
				t.Fatal(err)
			}
			column, value := "authority_id", any("wrong-authority")
			if fault == "future-revision" {
				column, value = "revision", int64(4)
			}
			if fault == "same-revision-payload" {
				_, a := authorityProjection(t, r.Binding.ClientID)
				a.Usage.RawUpload++
				b, err := json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				column, value = "account_json", string(b)
			}
			if err := db.Exec("UPDATE client_policy_authority_projections SET "+column+" = ? WHERE client_id = ?", value, r.Binding.ClientID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := issueClientPolicyAuthority(context.Background(), db, j, r); !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("contradictory projection accepted: %v", err)
			}
			a, err := j.Account(r.Binding.ClientID)
			if err != nil || a.Revision != 1 || a.HeldCapacity != 0 {
				t.Fatalf("contradictory SQL allocated new capacity: %+v/%v", a, err)
			}
		})
	}
}

func TestAuthoritySurvivesActualSQLiteSnapshotRestore(t *testing.T) {
	j, r := authorityProjectionFixture(t)
	if database.IsPostgres() {
		t.Skip("SQLite backup route; PostgreSQL projection uses its private integration schema")
	}
	db := database.GetDB()
	if err := projectClientPolicyAuthority(context.Background(), db, j, r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "old-sql.db")
	if err := database.BackupSQLite(snapshot); err != nil {
		t.Fatal(err)
	}
	g, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Report(policyauthority.Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: policyauthority.Usage{RawUpload: 2, BilledBytes: 4, Remainder: 200000}}); err != nil {
		t.Fatal(err)
	}
	lease, err := database.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := database.InitDB(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := projectClientPolicyAuthority(lease.Context(context.Background()), database.GetDB(), j, r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	row, account := authorityProjection(t, r.Binding.ClientID)
	if row.Revision != 3 || account.Usage.BilledBytes != 14 || account.Usage.Remainder != 300000 || account.HeldCapacity != 35 || account.HeldRemainder != 800000 {
		t.Fatalf("actual restored SQL lost authority: %+v/%+v", row, account)
	}
	retry, err := issueClientPolicyAuthority(context.Background(), database.GetDB(), j, r)
	if err != nil || retry.GrantID != g.GrantID {
		t.Fatalf("actual restore issued another allocation: %+v/%v", retry, err)
	}
	r.RequestID, r.Capacity = "new-after-restore", 50
	if _, err := issueClientPolicyAuthority(context.Background(), database.GetDB(), j, r); !errors.Is(err, policyauthority.ErrCapacity) {
		t.Fatalf("actual restore refunded allocated budget: %v", err)
	}
}

func TestAuthorityProjectionDeletionRetainsHistoryAndRejectsCancelledWork(t *testing.T) {
	j, r := authorityProjectionFixture(t)
	db := database.GetDB()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := issueClientPolicyAuthority(ctx, db, j, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled work issued grant: %v", err)
	}
	account, err := j.Account(r.Binding.ClientID)
	if err != nil || account.Revision != 1 || account.HeldCapacity != 0 {
		t.Fatalf("cancelled work changed journal: %+v/%v", account, err)
	}
	g, err := issueClientPolicyAuthority(context.Background(), db, j, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Tombstone(r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.ClientRecord{}, "stable_id = ?", r.Binding.ClientID).Error; err != nil {
		t.Fatal(err)
	}
	if err := j.Report(policyauthority.Report{Binding: g.Request.Binding, GrantID: g.GrantID, Sequence: 1, Usage: policyauthority.Usage{RawUpload: 3, BilledBytes: 6}, Seal: true}); err != nil {
		t.Fatal(err)
	}
	if err := projectClientPolicyAuthority(context.Background(), db, j, r.Binding.ClientID); err != nil {
		t.Fatal(err)
	}
	var tombstone model.ClientPolicyTombstone
	if err := db.First(&tombstone, "client_id = ?", r.Binding.ClientID).Error; err != nil {
		t.Fatal(err)
	}
	_, account = authorityProjection(t, r.Binding.ClientID)
	if !account.Deleted || account.Usage.RawUpload != 6 || account.Usage.BilledBytes != 16 || account.HeldCapacity != 0 {
		t.Fatalf("deleted projection lost final history: %+v", account)
	}
	if _, err := issueClientPolicyAuthority(context.Background(), db, j, r); !errors.Is(err, policyauthority.ErrDeleted) {
		t.Fatalf("old issuance resurrected deletion: %v", err)
	}
}
