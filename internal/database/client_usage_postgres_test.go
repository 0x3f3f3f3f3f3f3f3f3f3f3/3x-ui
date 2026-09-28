package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func usagePostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("XUI_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set XUI_TEST_PG_DSN to an isolated PostgreSQL instance")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	schema := fmt.Sprintf("usage_ledger_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	db, err := gorm.Open(postgres.Open(scoped), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, m := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}, &model.ClientUsageAccount{}, &model.ClientUsageMeter{}} {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestClientUsageLedger_Postgres(t *testing.T) {
	db := usagePostgresDB(t)
	c := usageClient(t, db, 111, 222)
	ledger := NewClientUsageLedger(db)
	var meters []model.ClientUsageMeter
	for n := range 8 {
		meters = append(meters, usageMeter(t, ledger, c, fmt.Sprintf("node-%d/xray", n)))
	}
	var wg sync.WaitGroup
	for _, meter := range meters {
		for seq := int64(1); seq <= 10; seq++ {
			wg.Go(func() {
				if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: meter.ID, Sequence: seq, Up: seq * 10, Down: seq * 20}); err != nil {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
	if a := usageRead(t, ledger, c); a.Up != 911 || a.Down != 1822 || a.Billed != 2733 {
		t.Fatalf("PostgreSQL concurrent accounting: %+v", a)
	}
	var finals []ClientUsageReport
	for _, meter := range meters {
		finals = append(finals, ClientUsageReport{MeterID: meter.ID, Sequence: 10, Up: 100, Down: 200})
	}
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 1500, finals); err != nil {
		t.Fatal(err)
	}
	meter := usageMeter(t, ledger, c, meters[0].Source)
	r := ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 1}
	if _, err := ledger.Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if d, err := NewClientUsageLedger(db).Apply(context.Background(), r); err != nil || d != (ClientUsageDelta{}) {
		t.Fatalf("new ledger instance double-billed a retry: %+v, %v", d, err)
	}
	if a := usageRead(t, ledger, c); a.Up != 912 || a.Down != 1822 || a.Billed != 2734 || a.Remainder != 500 || a.Revision != 2 {
		t.Fatalf("PostgreSQL multiplier boundary: %+v", a)
	}
	duplicate := model.ClientUsageMeter{ID: uuid.NewString(), PolicyID: c.PolicyID, Source: meter.Source}
	if err := db.Create(&duplicate).Error; err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("database allowed two open epochs for one source: %v", err)
	}
}

func TestClientUsageAdmission_Postgres(t *testing.T) {
	testClientUsageAdmissionCompetingSources(t, usagePostgresDB(t))
}

func TestAdmissionSourceOwnership_Postgres(t *testing.T) {
	testAdmissionSourceClaimFencesPreviousOwner(t, usagePostgresDB(t))
}

func TestAdmissionDurability_Postgres(t *testing.T) {
	testAdmissionRejectsNonDurableSettings(t, usagePostgresDB(t), "SET synchronous_commit = off")
}

func TestAdmissionSourceMigration_Postgres(t *testing.T) {
	testAdmissionSourceMigrationPreservesObservedCounters(t, usagePostgresDB(t))
}
