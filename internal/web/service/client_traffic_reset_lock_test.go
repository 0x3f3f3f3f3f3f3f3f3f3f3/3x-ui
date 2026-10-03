package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

func useProductionResetRuntime(t *testing.T) {
	t.Helper()
	previous := panelruntime.GetManager()
	svc := &XrayService{}
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{
		ManagedChange: svc.ReconcileManagedChange, SetNeedRestart: svc.SetToNeedRestart,
	}))
	t.Cleanup(func() { panelruntime.SetManager(previous) })
}

func TestTrafficResetProductionRuntimeDoesNotReenterLifecycleLock(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	ib := seedLocalDisabledClient(t, svc, 24219, "", "production-reset-lock", 1000, 0, 600, 500)
	useProductionResetRuntime(t)
	affected, err := svc.BulkResetTrafficWithRequest(context.Background(), &InboundService{}, []string{"production-reset-lock"}, "production-reset-lock-request")
	if err != nil || affected != 1 {
		t.Fatalf("public reset affected=%d err=%v", affected, err)
	}
	assertEnableEverywhere(t, svc, &InboundService{}, ib.Id, "production-reset-lock", true)
	traffic := trafficOf(t, "production-reset-lock")
	if traffic.Up != 0 || traffic.Down != 0 {
		t.Fatalf("committed reset lost: %+v", traffic)
	}
}

func TestTrafficResetConcurrentInboundEditPreservesLockOrder(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	ib := seedLocalDisabledClient(t, svc, 24220, "", "concurrent-reset-lock", 1000, 0, 600, 500)
	useProductionResetRuntime(t)
	db := database.GetDB()
	editHasInboundLock := make(chan struct{})
	resetApplied := make(chan struct{})
	var editRead atomic.Bool
	var applied sync.Once
	const queryHook = "test:reset-edit-holds-inbound"
	const updateHook = "test:reset-committed-application"
	if err := db.Callback().Query().After("gorm:query").Register(queryHook, func(tx *gorm.DB) {
		if tx.Statement.Table == "inbounds" && editRead.CompareAndSwap(false, true) {
			close(editHasInboundLock)
			<-resetApplied
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(queryHook) })
	if err := db.Callback().Update().After("gorm:update").Register(updateHook, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_traffic_reset_batches" && tx.Error == nil {
			if operation, ok := tx.Statement.Model.(*model.ClientTrafficResetBatch); ok && operation.Applied {
				applied.Do(func() { close(resetApplied) })
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(updateHook) })
	edit := *ib
	edit.Remark = "concurrent edit retained"
	editDone := make(chan error, 1)
	go func() {
		_, _, err := (&InboundService{}).UpdateInbound(&edit)
		editDone <- err
	}()
	<-editHasInboundLock
	affected, err := svc.BulkResetTrafficWithRequest(context.Background(), &InboundService{}, []string{"concurrent-reset-lock"}, "concurrent-reset-lock-request")
	if err != nil || affected != 1 {
		t.Fatalf("concurrent public reset affected=%d err=%v", affected, err)
	}
	if err := <-editDone; err != nil {
		t.Fatal(err)
	}
	var retained model.Inbound
	if err := db.First(&retained, ib.Id).Error; err != nil || retained.Remark != edit.Remark {
		t.Fatalf("concurrent edit lost: %+v / %v", retained, err)
	}
}
