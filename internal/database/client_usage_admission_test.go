package database

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientUsageAdmissionRespectsExactBilledQuota(t *testing.T) {
	db, _ := usageTestDB(t)
	for _, tc := range []struct{ multiplier, raw, billed, remainder int64 }{
		{500, 2000, 1000, 0}, {1000, 1000, 1000, 0}, {1500, 666, 999, 0}, {2000, 500, 1000, 0}, {10000, 100, 1000, 0},
	} {
		c := usageClient(t, db, 0, 0)
		if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Updates(map[string]any{"enable": true, "total": 1000}).Error; err != nil {
			t.Fatal(err)
		}
		ledger := NewClientUsageLedger(db)
		if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, clientpolicy.Multiplier(tc.multiplier), nil); err != nil {
			t.Fatal(err)
		}
		m := usageMeter(t, ledger, c, "local/dispatch")
		r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: tc.raw / 2, Down: tc.raw - tc.raw/2}
		if _, err := ledger.Admit(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if d, err := ledger.Admit(context.Background(), r); err != nil || d != (ClientUsageDelta{}) {
			t.Fatalf("acknowledged grant retry changed accounting: %+v, %v", d, err)
		}
		r.Sequence++
		r.Up++
		if _, err := ledger.Admit(context.Background(), r); !errors.Is(err, ErrUsageQuota) {
			t.Fatalf("multiplier %d accepted a byte beyond exact allowance: %v", tc.multiplier, err)
		}
		if a := usageRead(t, ledger, c); a.Up+a.Down != tc.raw || a.Billed != tc.billed || a.Remainder != tc.remainder {
			t.Fatalf("rejected grant consumed bytes: %+v", a)
		}
	}
}

func TestClientUsageAdmissionSerializesCompetingQuotaRequests(t *testing.T) {
	db, _ := usageTestDB(t)
	testClientUsageAdmissionCompetingSources(t, db)
}

func testClientUsageAdmissionCompetingSources(t *testing.T, db *gorm.DB) {
	t.Helper()
	c := usageClient(t, db, 0, 0)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Updates(map[string]any{"enable": true, "total": 100}).Error; err != nil {
		t.Fatal(err)
	}
	ledger := NewClientUsageLedger(db)
	meters := []model.ClientUsageMeter{usageMeter(t, ledger, c, "node-1"), usageMeter(t, ledger, c, "node-2")}
	var results [2]error
	var wg sync.WaitGroup
	for i, m := range meters {
		wg.Go(func() {
			_, results[i] = ledger.Admit(context.Background(), ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 60})
		})
	}
	wg.Wait()
	accepted, rejected := 0, 0
	for i, err := range results {
		if err == nil {
			accepted++
			continue
		}
		var exhausted *UsageQuotaError
		if !errors.As(err, &exhausted) || exhausted.RawAllowance != 40 {
			t.Fatalf("competing source did not see 40 remaining raw bytes: %v", err)
		}
		rejected++
		if _, err := ledger.Admit(context.Background(), ClientUsageReport{MeterID: meters[i].ID, Sequence: 1, Down: 40}); err != nil {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("quota was spent twice: accepted=%d rejected=%d", accepted, rejected)
	}
	if a := usageRead(t, ledger, c); a.Up != 60 || a.Down != 40 || a.Billed != 100 {
		t.Fatalf("competing sources overspent quota: %+v", a)
	}
}

func TestClientUsageAdmissionChecksStateBeforeReplayingGrant(t *testing.T) {
	db, _ := usageTestDB(t)
	for _, tc := range []struct {
		name    string
		client  map[string]any
		traffic map[string]any
		want    error
	}{
		{"manual-disabled", map[string]any{"enable": false}, nil, ErrUsageDisabled},
		{"traffic-disabled", nil, map[string]any{"enable": false}, ErrUsageDisabled},
		{"expired-client", map[string]any{"expiry_time": time.Now().Add(-time.Hour).UnixMilli()}, nil, ErrUsageExpired},
		{"expired-traffic", nil, map[string]any{"expiry_time": time.Now().Add(-time.Hour).UnixMilli()}, ErrUsageExpired},
		{"pending-first-use", map[string]any{"expiry_time": -3600000}, nil, ErrUsageUnready},
		{"quota-lowered", nil, map[string]any{"total": 5}, ErrUsageQuota},
		{"canonical-quota-lowered", map[string]any{"total_gb": 5}, nil, ErrUsageQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := usageClient(t, db, 0, 0)
			if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Update("enable", true).Error; err != nil {
				t.Fatal(err)
			}
			ledger := NewClientUsageLedger(db)
			m := usageMeter(t, ledger, c, "local/dispatch")
			r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 10}
			if _, err := ledger.Admit(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if tc.client != nil {
				if err := db.Model(&model.ClientRecord{}).Where("id = ?", c.Id).Updates(tc.client).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.traffic != nil {
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Updates(tc.traffic).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ledger.Admit(context.Background(), r); !errors.Is(err, tc.want) {
				t.Fatalf("replayed grant bypassed changed policy: %v, want %v", err, tc.want)
			}
			if a := usageRead(t, ledger, c); a.Billed != 10 || a.Up != 10 {
				t.Fatalf("blocked grant changed usage: %+v", a)
			}
		})
	}
}

func TestClientUsageAdmissionBoundsCombinedRawUsage(t *testing.T) {
	db, _ := usageTestDB(t)
	for _, quota := range []int64{0, math.MaxInt64} {
		c := usageClient(t, db, 0, 0)
		if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Updates(map[string]any{"enable": true, "total": quota}).Error; err != nil {
			t.Fatal(err)
		}
		ledger := NewClientUsageLedger(db)
		if _, err := ledger.ChangeMultiplier(context.Background(), c.PolicyID, 1, 500, nil); err != nil {
			t.Fatal(err)
		}
		m := usageMeter(t, ledger, c, "local/dispatch")
		r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: math.MaxInt64}
		if _, err := ledger.Admit(context.Background(), r); err != nil {
			t.Fatalf("large representable usage was rejected: %v", err)
		}
		r.Sequence++
		r.Down = 1
		if _, err := ledger.Admit(context.Background(), r); !errors.Is(err, clientpolicy.ErrOverflow) {
			t.Fatalf("raw aggregate overflowed at 0.5x while billed usage still fit: %v", err)
		}
	}
}

func TestClientUsageAdmissionRetiresPreviouslyGrantedBytesAfterReset(t *testing.T) {
	db, _ := usageTestDB(t)
	c := usageClient(t, db, 0, 0)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", c.Email).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	ledger := NewClientUsageLedger(db)
	m := usageMeter(t, ledger, c, "local/dispatch")
	r := ClientUsageReport{MeterID: m.ID, Sequence: 1, Up: 100}
	if _, err := ledger.Admit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reset(context.Background(), c.PolicyID, 1, []ClientUsageReport{r}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(context.Background(), r); !errors.Is(err, ErrUsageClosed) {
		t.Fatalf("old grant could be forwarded after reset: %v", err)
	}
}
