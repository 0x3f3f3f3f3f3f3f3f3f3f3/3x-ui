package database

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestAdmissionSourceClaimFencesPreviousOwner(t *testing.T) {
	db, _ := usageTestDB(t)
	testAdmissionSourceClaimFencesPreviousOwner(t, db)
}

func testAdmissionSourceClaimFencesPreviousOwner(t *testing.T, db *gorm.DB) {
	t.Helper()
	c := usageClient(t, db, 0, 0)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Updates(map[string]any{"enable": true, "total": 100}).Error; err != nil {
		t.Fatal(err)
	}
	ledger := NewClientUsageLedger(db)
	old, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, "node-a/controller")
	if err != nil {
		t.Fatal(err)
	}
	r := ClientUsageReport{MeterID: old.ID, Sequence: 1, Up: 20}
	if _, err := ledger.Admit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	current, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, old.Source)
	if err != nil || current.ID == old.ID {
		t.Fatalf("new controller did not get a fresh incarnation: %+v, %v", current, err)
	}
	if _, err := ledger.Admit(context.Background(), r); !errors.Is(err, ErrUsageClosed) {
		t.Fatalf("old controller could replay a forwarding grant: %v", err)
	}
	if err := ledger.CheckAdmissionSource(context.Background(), old.ID); !errors.Is(err, ErrUsageClosed) {
		t.Fatalf("idle old controller did not observe fencing: %v", err)
	}
	if _, err := ledger.Admit(context.Background(), ClientUsageReport{MeterID: current.ID, Sequence: 1, Down: 80}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CheckAdmissionSource(context.Background(), current.ID); !errors.Is(err, ErrUsageQuota) {
		t.Fatalf("exhausted client could open a new idle flow: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Up != 20 || a.Down != 80 || a.Billed != 100 {
		t.Fatalf("ownership transfer changed billed history: %+v", a)
	}
}

func TestAdmissionSourceCannotSettleUnadmittedBytes(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	meter, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, "node-a/controller")
	if err != nil {
		t.Fatal(err)
	}
	r := ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 10}
	if _, err := ledger.Apply(context.Background(), r); !errors.Is(err, ErrUsageConflict) {
		t.Fatalf("admission-only source bypassed quota via observed reports: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Up != 0 || a.Billed != 0 {
		t.Fatalf("unadmitted bytes were accepted: %+v", a)
	}
}

func TestAdmissionSourceCannotStealUnsettledObservedMeter(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	old, err := ledger.Register(context.Background(), c.PolicyID, "node-a/legacy", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, old.Source); !errors.Is(err, ErrUsageConflict) {
		t.Fatalf("retired an observed counter without its final snapshot: %v", err)
	}
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: old.ID, Sequence: 1, Up: 13}); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionRejectsNonDurableSQLiteSettings(t *testing.T) {
	for _, setting := range []string{
		"PRAGMA synchronous = OFF", "PRAGMA synchronous = NORMAL",
		"PRAGMA journal_mode = MEMORY", "PRAGMA journal_mode = OFF",
		"PRAGMA journal_mode = DELETE", "PRAGMA journal_mode = TRUNCATE", "PRAGMA journal_mode = PERSIST",
	} {
		t.Run(setting, func(t *testing.T) {
			db, _ := usageTestDB(t)
			testAdmissionRejectsNonDurableSettings(t, db, setting)
		})
	}
}

func testAdmissionRejectsNonDurableSettings(t *testing.T, db *gorm.DB, setting string) {
	t.Helper()
	handle, _ := db.DB()
	handle.SetMaxOpenConns(1)
	c := usageClient(t, db, 0, 0)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	ledger := NewClientUsageLedger(db)
	meter, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, "node-a/durable")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(setting).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(context.Background(), ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 10}); !errors.Is(err, ErrUsageDurability) {
		t.Fatalf("non-durable database granted forwarding rights: %v", err)
	}
	if err := ledger.CheckAdmissionSource(context.Background(), meter.ID); !errors.Is(err, ErrUsageDurability) {
		t.Fatalf("non-durable database allowed a new connection: %v", err)
	}
	if _, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, meter.Source); !errors.Is(err, ErrUsageDurability) {
		t.Fatalf("non-durable source could be reactivated: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Up != 0 || a.Billed != 0 {
		t.Fatalf("failed admission changed durable usage: %+v", a)
	}
}

func TestAdmissionSourceMigrationPreservesObservedCounters(t *testing.T) {
	db, _ := usageTestDB(t)
	testAdmissionSourceMigrationPreservesObservedCounters(t, db)
}

func testAdmissionSourceMigrationPreservesObservedCounters(t *testing.T, db *gorm.DB) {
	t.Helper()
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	meter := usageMeter(t, ledger, c, "node-a/observed")
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 13}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.ClientUsageMeter{}, "AdmissionOnly"); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ClientUsageMeter{}); err != nil {
		t.Fatal(err)
	}
	var migrated model.ClientUsageMeter
	if err := db.Where("meter_id = ?", meter.ID).First(&migrated).Error; err != nil {
		t.Fatal(err)
	}
	if migrated.AdmissionOnly || migrated.Closed || migrated.Sequence != 1 || migrated.Up != 13 {
		t.Fatalf("legacy counter lost its observed-source semantics: %+v", migrated)
	}
	if _, err := ledger.ClaimAdmissionSource(context.Background(), c.PolicyID, meter.Source); !errors.Is(err, ErrUsageConflict) {
		t.Fatalf("migration allowed discarding an unsettled observed counter: %v", err)
	}
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: meter.ID, Sequence: 2, Up: 21}); err != nil {
		t.Fatal(err)
	}
	if account := usageRead(t, ledger, c); account.Up != 21 || account.Billed != 21 {
		t.Fatalf("migration replayed or lost old usage: %+v", account)
	}
}
