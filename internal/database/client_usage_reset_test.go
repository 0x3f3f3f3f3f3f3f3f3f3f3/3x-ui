package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestAdmissionResetSerializesWithActiveSources(t *testing.T) {
	db, _ := usageTestDB(t)
	testAdmissionResetSerializesWithActiveSources(t, db)
}

func TestAdmissionReset_Postgres(t *testing.T) {
	testAdmissionResetSerializesWithActiveSources(t, usagePostgresDB(t))
}

func testAdmissionResetSerializesWithActiveSources(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	client := usageClient(t, db, 0, 0)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	ledger := NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(ctx, client.PolicyID, 1, 1500, nil); err != nil {
		t.Fatal(err)
	}
	var meters []model.ClientUsageMeter
	for n := range 4 {
		meter, err := ledger.ClaimAdmissionSource(ctx, client.PolicyID, fmt.Sprintf("node-%d/admission", n))
		if err != nil {
			t.Fatal(err)
		}
		meters = append(meters, meter)
		if _, err := ledger.Admit(ctx, ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 1}); err != nil {
			t.Fatal(err)
		}
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for _, meter := range meters {
		workers.Go(func() {
			<-start
			for seq := int64(2); seq <= 50; seq++ {
				_, err := ledger.Admit(ctx, ClientUsageReport{MeterID: meter.ID, Sequence: seq, Up: seq})
				if errors.Is(err, ErrUsageClosed) {
					return
				}
				if err != nil {
					t.Errorf("racing admission failed outside the reset fence: %v", err)
					return
				}
			}
		})
	}
	close(start)
	_, resetErr := ledger.ResetAdmitted(ctx, client.PolicyID)
	workers.Wait()
	if resetErr != nil {
		t.Fatal(resetErr)
	}
	account := usageRead(t, ledger, client)
	if account.Up != 0 || account.Down != 0 || account.Billed != 0 || account.Remainder != 0 || account.Revision != 3 || account.Multiplier != 1500 {
		t.Fatalf("a racing old source escaped the reset boundary: %+v", account)
	}
	for _, meter := range meters {
		if err := ledger.CheckAdmissionSource(ctx, meter.ID); !errors.Is(err, ErrUsageClosed) {
			t.Fatalf("old source remained usable: %v", err)
		}
	}
	fresh, err := ledger.ClaimAdmissionSource(ctx, client.PolicyID, "node-0/admission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, ClientUsageReport{MeterID: fresh.ID, Sequence: 1, Down: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Register(ctx, client.PolicyID, "observed/unsettled", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ResetAdmitted(ctx, client.PolicyID); !errors.Is(err, ErrUsageBoundary) {
		t.Fatalf("admission reset discarded an observed source: %v", err)
	}
	account = usageRead(t, ledger, client)
	if account.Up != 0 || account.Down != 3 || account.Billed != 4 || account.Remainder != 500 || account.Revision != 3 {
		t.Fatalf("fresh traffic or rejected reset lost its exact billing: %+v", account)
	}
}
