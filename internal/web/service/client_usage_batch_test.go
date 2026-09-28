package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedUsageBulkResetKeepsLegacyBatching(t *testing.T) {
	ledger, managed, _, _ := managedUsageFixture(t, 1500)
	db := database.GetDB()
	emails := []string{managed.Email}
	var records []model.ClientRecord
	var traffic []xray.ClientTraffic
	for n := range 2*sqlInChunk + 37 {
		email := fmt.Sprintf("legacy-reset-%d", n)
		emails = append(emails, email)
		records = append(records, model.ClientRecord{Email: email, Enable: true})
		traffic = append(traffic, xray.ClientTraffic{Email: email, Enable: true, Up: 1, Down: 2})
	}
	if err := db.CreateInBatches(records, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInBatches(traffic, 100).Error; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	count := func(_ *gorm.DB) { calls.Add(1) }
	if err := db.Callback().Query().Before("gorm:query").Register("managed-reset-cost", count); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("managed-reset-cost") })
	if err := db.Callback().Update().Before("gorm:update").Register("managed-reset-cost", count); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove("managed-reset-cost") })
	affected, err := (&ClientService{}).BulkResetTraffic(&InboundService{}, emails)
	if err != nil || affected != len(emails) {
		t.Fatalf("mixed bulk reset failed: affected=%d want=%d err=%v", affected, len(emails), err)
	}
	if got := calls.Load(); got > 100 {
		t.Fatalf("bulk reset lost batching: %d query/update calls for %d clients (budget 100)", got, len(emails))
	}
	var remaining int64
	if err := db.Model(&xray.ClientTraffic{}).Where("up <> 0 OR down <> 0").Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("batching left stale raw traffic: rows=%d err=%v", remaining, err)
	}
	account, err := ledger.Read(context.Background(), managed.PolicyID)
	if err != nil || account.Up != 0 || account.Billed != 0 || account.Revision != 3 {
		t.Fatalf("mixed batching skipped the durable boundary: %+v, %v", account, err)
	}
	t.Logf("mixed bulk reset: %d clients, %d query/update calls", len(emails), calls.Load())
}
