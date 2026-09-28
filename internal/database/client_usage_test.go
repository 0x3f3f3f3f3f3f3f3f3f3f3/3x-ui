package database

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func usageTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := gorm.Open(sqlite.Open(path+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=10000"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, mdl := range []any{&model.ClientRecord{}, &xray.ClientTraffic{}, &model.ClientUsageAccount{}, &model.ClientUsageMeter{}} {
		if err := db.AutoMigrate(mdl); err != nil {
			t.Fatal(err)
		}
	}
	handle, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return db, path
}

func usageClient(t *testing.T, db *gorm.DB, up, down int64) model.ClientRecord {
	t.Helper()
	c := model.ClientRecord{Email: uuid.NewString()}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	ct := xray.ClientTraffic{Email: c.Email, Up: up, Down: down, Total: 100 << 30, Enable: false}
	if err := db.Create(&ct).Error; err != nil {
		t.Fatal(err)
	}
	return c
}

func usageMeter(t *testing.T, ledger *ClientUsageLedger, c model.ClientRecord, source string) model.ClientUsageMeter {
	t.Helper()
	m, err := ledger.Register(context.Background(), c.PolicyID, source, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func usageRead(t *testing.T, ledger *ClientUsageLedger, c model.ClientRecord) model.ClientUsageAccount {
	t.Helper()
	a, err := ledger.Read(context.Background(), c.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestClientUsageReplayReorderAndReopen(t *testing.T) {
	db, path := usageTestDB(t)
	c := usageClient(t, db, 111, 222)
	ledger := NewClientUsageLedger(db)
	m := usageMeter(t, ledger, c, "local/xray")
	if a := usageRead(t, ledger, c); a.Up != 111 || a.Down != 222 || a.Billed != 333 || a.Multiplier != 1000 || a.Revision != 1 {
		t.Fatalf("legacy usage was lost or repriced: %+v", a)
	}
	report := ClientUsageReport{MeterID: m.ID, Sequence: 2, Up: 100, Down: 50}
	delta, err := ledger.Apply(context.Background(), report)
	if err != nil || delta.Up != 100 || delta.Down != 50 || delta.Billed != 150 {
		t.Fatalf("initial report: %+v, %v", delta, err)
	}
	for _, r := range []ClientUsageReport{report, {MeterID: m.ID, Sequence: 1, Up: 90, Down: 40}} {
		if d, err := ledger.Apply(context.Background(), r); err != nil || d != (ClientUsageDelta{}) {
			t.Fatalf("replay/reorder charged again: %+v, %v", d, err)
		}
	}
	for _, r := range []ClientUsageReport{
		{MeterID: m.ID, Sequence: 2, Up: 101, Down: 50},
		{MeterID: m.ID, Sequence: 3, Up: 99, Down: 60},
	} {
		if _, err := ledger.Apply(context.Background(), r); !errors.Is(err, ErrUsageConflict) {
			t.Fatalf("conflicting or reset counter accepted: %v", err)
		}
	}
	handle, _ := db.DB()
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := gorm.Open(sqlite.Open(path+"?_journal_mode=WAL&_busy_timeout=10000"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	reopenedHandle, _ := reopened.DB()
	t.Cleanup(func() { _ = reopenedHandle.Close() })
	ledger = NewClientUsageLedger(reopened)
	if _, err := ledger.Apply(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if a := usageRead(t, ledger, c); a.Up != 211 || a.Down != 272 || a.Billed != 483 {
		t.Fatalf("durable cursor did not survive reopen: %+v", a)
	}
	var ct xray.ClientTraffic
	if err := reopened.Where("email = ?", c.Email).First(&ct).Error; err != nil {
		t.Fatal(err)
	}
	if ct.Up != 211 || ct.Down != 272 || ct.Total != 100<<30 || ct.Enable {
		t.Fatalf("traffic projection or manual disable changed: %+v", ct)
	}
}

func TestClientUsageMultiplierRequiresCompleteBoundary(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	m := usageMeter(t, ledger, c, "local/xray")
	final := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 10 << 30}
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 2000, nil); !errors.Is(err, ErrUsageBoundary) {
		t.Fatalf("changed multiplier with an unsettled source: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Revision != 1 || a.Multiplier != 1000 || a.Billed != 0 {
		t.Fatalf("failed change mutated usage: %+v", a)
	}
	a, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 2000, []ClientUsageReport{final})
	if err != nil || a.Revision != 2 || a.Billed != 10<<30 || a.Multiplier != 2000 {
		t.Fatalf("boundary did not settle at old multiplier: %+v, %v", a, err)
	}
	if d, err := ledger.Apply(context.Background(), final); err != nil || d != (ClientUsageDelta{}) {
		t.Fatalf("closed-source replay was not harmless: %+v, %v", d, err)
	}
	final.Sequence++
	final.Up++
	if _, err := ledger.Apply(context.Background(), final); !errors.Is(err, ErrUsageClosed) {
		t.Fatalf("closed epoch accepted new traffic: %v", err)
	}
	m = usageMeter(t, ledger, c, "local/xray")
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: 1, Down: 5 << 30}); err != nil {
		t.Fatal(err)
	}
	if a := usageRead(t, ledger, c); a.Up != 10<<30 || a.Down != 5<<30 || a.Billed != 20<<30 {
		t.Fatalf("historical traffic was repriced: %+v", a)
	}
}

func TestClientUsageFractionalBatches(t *testing.T) {
	db, _ := usageTestDB(t)
	for _, tc := range []struct{ multiplier, billed, remainder int64 }{
		{500, 500, 500}, {1000, 1001, 0}, {1500, 1501, 500}, {2000, 2002, 0}, {10000, 10010, 0},
	} {
		for _, batch := range []int64{1, 13, 1001} {
			t.Run(fmt.Sprintf("m%d/batch%d", tc.multiplier, batch), func(t *testing.T) {
				c := usageClient(t, db, 0, 0)
				ledger := NewClientUsageLedger(db)
				if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, clientpolicy.Multiplier(tc.multiplier), nil); err != nil {
					t.Fatal(err)
				}
				m := usageMeter(t, ledger, c, "local/ssh")
				for n := batch; ; n += batch {
					n = min(n, 1001)
					if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: n, Up: n / 2, Down: n - n/2}); err != nil {
						t.Fatal(err)
					}
					if n == 1001 {
						break
					}
				}
				if a := usageRead(t, ledger, c); a.Up != 500 || a.Down != 501 || a.Billed != tc.billed || a.Remainder != tc.remainder {
					t.Fatalf("partitioning changed charge: %+v", a)
				}
			})
		}
	}
}

func TestClientUsageConcurrentSources(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	var meters []model.ClientUsageMeter
	for n := range 8 {
		meters = append(meters, usageMeter(t, ledger, c, fmt.Sprintf("node-%d/xray", n)))
	}
	var wg sync.WaitGroup
	for _, m := range meters {
		for seq := int64(1); seq <= 10; seq++ {
			wg.Go(func() {
				_, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: seq, Up: seq * 10, Down: seq * 20})
				if err != nil {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
	if a := usageRead(t, ledger, c); a.Up != 800 || a.Down != 1600 || a.Billed != 2400 {
		t.Fatalf("concurrent reports lost or duplicated bytes: %+v", a)
	}
}

func TestClientUsageRollbackAndOverflow(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	m := usageMeter(t, ledger, c, "local/mieru")
	if err := db.Exec("CREATE TRIGGER reject_cursor BEFORE UPDATE ON client_usage_meters BEGIN SELECT RAISE(ABORT, 'disk write failed'); END").Error; err != nil {
		t.Fatal(err)
	}
	r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 100, Down: 100}
	if _, err := ledger.Apply(context.Background(), r); err == nil {
		t.Fatal("injected database failure unexpectedly committed")
	}
	if a := usageRead(t, ledger, c); a.Up != 0 || a.Down != 0 || a.Billed != 0 {
		t.Fatalf("charge survived failed cursor write: %+v", a)
	}
	var ct xray.ClientTraffic
	if err := db.Where("email = ?", c.Email).First(&ct).Error; err != nil || ct.Up != 0 || ct.Down != 0 {
		t.Fatalf("projection survived rollback: %+v, %v", ct, err)
	}
	if err := db.Exec("DROP TRIGGER reject_cursor").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Sequence++
	r.Up = math.MaxInt64
	if _, err := ledger.Apply(context.Background(), r); !errors.Is(err, clientpolicy.ErrOverflow) {
		t.Fatalf("overflow was not rejected: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Up != 100 || a.Down != 100 || a.Billed != 200 {
		t.Fatalf("overflow partially committed: %+v", a)
	}
}

func TestClientUsageCannotAttachDeletedIdentityToRecreatedEmail(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	m := usageMeter(t, ledger, c, "local/forward")
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 100}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&c).Error; err != nil {
		t.Fatal(err)
	}
	replacement := model.ClientRecord{Email: c.Email}
	if err := db.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: 2, Up: 200}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("old report reached new client: %v", err)
	}
	if _, err := ledger.Register(context.Background(), replacement.PolicyID, "local/forward", uuid.NewString()); !errors.Is(err, ErrUsageConflict) {
		t.Fatalf("recreated label adopted an old incarnation's traffic row: %v", err)
	}
	if err := db.Where("email = ?", c.Email).Delete(&xray.ClientTraffic{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: replacement.Email}).Error; err != nil {
		t.Fatal(err)
	}
	usageMeter(t, ledger, replacement, "local/forward")
	if a := usageRead(t, ledger, replacement); a.Up != 0 || a.Billed != 0 {
		t.Fatalf("replacement inherited old usage: %+v", a)
	}
}

func TestClientUsageRejectsProjectionDriftAndDuplicateSource(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	m := usageMeter(t, ledger, c, "local/xray")
	if got, err := ledger.Register(context.Background(), c.PolicyID, m.Source, m.ID); err != nil || got.ID != m.ID {
		t.Fatalf("registration retry was not idempotent: %+v, %v", got, err)
	}
	if _, err := ledger.Register(context.Background(), c.PolicyID, m.Source, uuid.NewString()); !errors.Is(err, ErrUsageConflict) {
		t.Fatalf("two meters claimed one active source: %v", err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Update("up", 17).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 100}); !errors.Is(err, ErrUsageUntracked) {
		t.Fatalf("ledger overwrote a legacy writer: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Up != 0 || a.Billed != 0 {
		t.Fatalf("failed projection check committed usage: %+v", a)
	}
}

func TestClientUsageBoundaryIsAtomicAcrossSourcesAndPreservesFractions(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 500, nil); err != nil {
		t.Fatal(err)
	}
	meters := []model.ClientUsageMeter{usageMeter(t, ledger, c, "local/ssh"), usageMeter(t, ledger, c, "local/forward")}
	finals := []ClientUsageReport{{MeterID: meters[0].ID, Sequence: 1, Up: 1}, {MeterID: meters[1].ID, Sequence: 1, Down: 2}}
	bad := append([]ClientUsageReport(nil), finals...)
	bad[1].MeterID = uuid.NewString()
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 2, 1500, bad); !errors.Is(err, ErrUsageBoundary) {
		t.Fatalf("accepted an incomplete boundary: %v", err)
	}
	if a := usageRead(t, ledger, c); a.Billed != 0 || a.Up != 0 || a.Down != 0 || a.Revision != 2 {
		t.Fatalf("invalid boundary partially committed: %+v", a)
	}
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 2, 1500, finals); err != nil {
		t.Fatal(err)
	}
	m := usageMeter(t, ledger, c, "local/ssh")
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 1}); err != nil {
		t.Fatal(err)
	}
	if a := usageRead(t, ledger, c); a.Up != 2 || a.Down != 2 || a.Billed != 3 || a.Remainder != 0 || a.Revision != 3 {
		t.Fatalf("fractional carry was lost across the revision: %+v", a)
	}
}

func TestClientUsageBackupRestoresCursorAndFraction(t *testing.T) {
	db, path := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	ledger := NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 500, nil); err != nil {
		t.Fatal(err)
	}
	m := usageMeter(t, ledger, c, "local/mieru")
	r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 3}
	if _, err := ledger.Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "backup.sql")
	if err := DumpSQLite(path, dump); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if err := RestoreSQLite(dump, restored); err != nil {
		t.Fatal(err)
	}
	copyDB, err := gorm.Open(sqlite.Open(restored), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := copyDB.DB()
	t.Cleanup(func() { _ = handle.Close() })
	ledger = NewClientUsageLedger(copyDB)
	if d, err := ledger.Apply(context.Background(), r); err != nil || d != (ClientUsageDelta{}) {
		t.Fatalf("restore replay changed usage: %+v, %v", d, err)
	}
	r.Sequence++
	r.Up++
	if _, err := ledger.Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if a := usageRead(t, ledger, c); a.Up != 4 || a.Billed != 2 || a.Remainder != 0 {
		t.Fatalf("backup lost cursor or fraction: %+v", a)
	}
}

func TestClientUsageResetRetiresOldMetersWithoutEnablingClient(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 100, 200)
	ledger := NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 1500, nil); err != nil {
		t.Fatal(err)
	}
	m := usageMeter(t, ledger, c, "local/ssh")
	r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 1}
	if _, err := ledger.Reset(context.Background(), c.PolicyID, 2, nil); !errors.Is(err, ErrUsageBoundary) {
		t.Fatalf("reset accepted an unsettled counter: %v", err)
	}
	a, err := ledger.Reset(context.Background(), c.PolicyID, 2, []ClientUsageReport{r})
	if err != nil || a.Up != 0 || a.Down != 0 || a.Billed != 0 || a.Remainder != 0 || a.Revision != 3 || a.Multiplier != 1500 {
		t.Fatalf("reset lost policy or retained usage: %+v, %v", a, err)
	}
	if d, err := ledger.Apply(context.Background(), r); err != nil || d != (ClientUsageDelta{}) {
		t.Fatalf("old report reappeared after reset: %+v, %v", d, err)
	}
	var ct xray.ClientTraffic
	if err := db.Where("email = ?", c.Email).First(&ct).Error; err != nil || ct.Enable || ct.Up != 0 || ct.Down != 0 || ct.Total != 100<<30 {
		t.Fatalf("reset changed disable/quota or missed projection: %+v, %v", ct, err)
	}
	newMeter := usageMeter(t, ledger, c, "local/ssh")
	if _, err := ledger.Apply(context.Background(), ClientUsageReport{MeterID: newMeter.ID, Sequence: 1, Down: 1}); err != nil {
		t.Fatal(err)
	}
	if a := usageRead(t, ledger, c); a.Billed != 1 || a.Remainder != 500 || a.Down != 1 {
		t.Fatalf("reset carried old fractional usage: %+v", a)
	}
}
