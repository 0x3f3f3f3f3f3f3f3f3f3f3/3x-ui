package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestDepletionPurgeRechecksConcurrentCredit(t *testing.T) {
	for _, mode := range []string{"clients", "inbounds"} {
		for _, change := range []string{"reset", "quota", "replace"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				testDepletionPurgeConcurrentCredit(t, mode, change)
			})
		}
	}
}

func testDepletionPurgeConcurrentCredit(t *testing.T, mode, change string) {
	t.Helper()
	ledger, record, _, inboundID := managedUsageFixture(t, 2000)
	withdrawClientTombstones(record.Email)
	t.Cleanup(func() { withdrawClientTombstones(record.Email) })
	db := database.GetDB()
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
	mgr.SetLocalRuntimeOverride(&fakeNodeRuntime{})
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(nil) })
	client := record.ToClient()
	client.TotalGB = 300
	if err := db.Model(&model.Inbound{}).Where("id = ?", inboundID).
		Update("settings", clientsSettings(t, []model.Client{*client})).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&ClientService{}).SyncInbound(nil, inboundID, []model.Client{*client}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 300).Error; err != nil {
		t.Fatal(err)
	}

	selected, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	resume := func() { released.Do(func() { close(release) }) }
	t.Cleanup(resume)
	var intercepted atomic.Bool
	const callback = "test:credit_after_depletion_selection"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Error == nil && tx.RowsAffected > 0 && strings.Contains(tx.Statement.SQL.String(), "c.reset_weekday = 0") && intercepted.CompareAndSwap(false, true) {
			close(selected)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	done := make(chan error, 1)
	go func() {
		if mode == "clients" {
			_, _, err := (&ClientService{}).DelDepleted(&InboundService{})
			done <- err
		} else {
			done <- (&InboundService{}).DelDepletedClients(-1)
		}
	}()
	select {
	case <-selected:
	case err := <-done:
		t.Fatalf("purge finished before selecting depleted client: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("purge never reached candidate selection")
	}
	var mutationErr error
	switch change {
	case "reset":
		_, mutationErr = (&ClientService{}).ResetTrafficByEmail(&InboundService{}, record.Email)
	case "quota":
		_, _, mutationErr = (&ClientService{}).BulkAdjust(&InboundService{}, []string{record.Email}, 0, 300, "", nil, "")
	case "replace":
		mutationErr = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&model.ClientRecord{}, record.Id).Error; err != nil {
				return err
			}
			record.PolicyID = ""
			record.TotalGB = 300
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("policy_id", record.PolicyID).Error; err != nil {
				return err
			}
			return tx.Create(&model.ClientUsageAccount{PolicyID: record.PolicyID, Up: 150, Billed: 300, Multiplier: 2000, Revision: 1}).Error
		})
	}
	resume()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("purge after %s: %v", change, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("purge did not finish after candidate selection resumed")
	}
	if mutationErr != nil {
		t.Fatalf("concurrent %s: %v", change, mutationErr)
	}
	after := lookupClientRecord(t, record.Email)
	if after.PolicyID != record.PolicyID {
		t.Fatalf("credited identity replaced: %q, want %q", after.PolicyID, record.PolicyID)
	}
	inbound, err := (&InboundService{}).GetInbound(inboundID)
	if err != nil {
		t.Fatalf("credited client lost its inbound: %v", err)
	}
	clients, err := (&InboundService{}).GetClients(inbound)
	if err != nil || !reflect.DeepEqual(emailsOf(clients), []string{record.Email}) {
		t.Fatalf("credited client removed from settings: %v, %v", emailsOf(clients), err)
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", record.Id, inboundID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("credited client lost membership: %d, %v", links, err)
	}
	wantBilled, wantQuota := int64(0), int64(300)
	switch change {
	case "quota":
		wantBilled, wantQuota = 300, 600
	case "replace":
		wantBilled = 300
	}
	account, err := ledger.Read(context.Background(), record.PolicyID)
	if err != nil || account.Billed != wantBilled {
		t.Fatalf("credited ledger: %+v, %v; want billed %d", account, err, wantBilled)
	}
	if traffic := trafficOf(t, record.Email); traffic.Total != wantQuota {
		t.Fatalf("credited traffic quota = %d, want %d", traffic.Total, wantQuota)
	}
	if isClientEmailTombstoned(record.Email) {
		t.Fatal("surviving credited client was tombstoned")
	}
}

func TestDepletionPurgeRechecksConcurrentCredit_Postgres(t *testing.T) {
	for _, mode := range []string{"clients", "inbounds"} {
		for _, change := range []string{"reset", "quota", "replace"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				managedUsagePostgresSchema(t)
				testDepletionPurgeConcurrentCredit(t, mode, change)
			})
		}
	}
}

func TestDepletionPurgeRollsBackMembershipAndTraffic(t *testing.T) {
	for _, mode := range []string{"clients", "inbounds"} {
		t.Run(mode, func(t *testing.T) { testDepletionPurgeRollback(t, mode) })
	}
}

func testDepletionPurgeRollback(t *testing.T, mode string) {
	t.Helper()
	setupBulkDB(t)
	startSerializedWriter(t)
	db := database.GetDB()
	clients := []model.Client{{Email: "rollback-spent", SubID: "rollback-sub", Enable: true, TotalGB: 100}}
	inbound := mkInbound(t, 49105, model.VLESS, clientsSettings(t, clients))
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, clients); err != nil {
		t.Fatal(err)
	}
	record := lookupClientRecord(t, clients[0].Email)
	withdrawClientTombstones(record.Email)
	t.Cleanup(func() { withdrawClientTombstones(record.Email) })
	mkTraffic(t, inbound.Id, record.Email, 100, 0, 100, 0, true)
	seedHwids(t, db, record.SubID, 1)
	fake := &fakeNodeRuntime{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(fake)
	injected := errors.New("injected depletion traffic deletion failure")
	const callback = "test:depletion_delete_failure"
	if err := db.Callback().Delete().Before("gorm:delete").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_traffics" {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(callback) })
	var err error
	if mode == "clients" {
		_, _, err = (&ClientService{}).DelDepleted(&InboundService{})
	} else {
		err = (&InboundService{}).DelDepletedClients(-1)
	}
	if !errors.Is(err, injected) {
		t.Errorf("purge must report rolled-back deletion: %v", err)
	}
	loaded, err := (&InboundService{}).GetInbound(inbound.Id)
	if err != nil || loaded.Settings != inbound.Settings {
		t.Fatalf("failed purge changed inbound: %+v, %v", loaded, err)
	}
	linked, err := (&ClientService{}).ListForInbound(nil, inbound.Id)
	if err != nil || !reflect.DeepEqual(emailsOf(linked), []string{record.Email}) {
		t.Fatalf("failed purge detached client: %v, %v", emailsOf(linked), err)
	}
	if traffic := trafficOf(t, record.Email); traffic.Up != 100 {
		t.Fatalf("failed purge changed traffic: %+v", traffic)
	}
	assertHwidState(t, db, record.Email, 0, 1)
	if isClientEmailTombstoned(record.Email) || fake.delInbound.Load() != 0 || fake.updateInbound.Load() != 0 {
		t.Fatal("failed purge escaped the database transaction")
	}
}

func TestDepletionPurgeRollsBackMembershipAndTraffic_Postgres(t *testing.T) {
	for _, mode := range []string{"clients", "inbounds"} {
		t.Run(mode, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			testDepletionPurgeRollback(t, mode)
		})
	}
}

type parkedDepletionRuntime struct {
	fakeNodeRuntime
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *parkedDepletionRuntime) RemoveUser(context.Context, *model.Inbound, string) error {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return nil
}

func TestDepletionRuntimeWaitDoesNotBlockUnrelatedClientEdit(t *testing.T) {
	setupBulkDB(t)
	startSerializedWriter(t)
	db := database.GetDB()
	for i, email := range []string{"purge-blocked", "purge-unrelated"} {
		client := model.Client{Email: email, SubID: email, Enable: true, TotalGB: 100}
		inbound := mkInbound(t, 49106+i, model.VLESS, clientsSettings(t, []model.Client{client}))
		if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
			t.Fatal(err)
		}
		used := int64(0)
		if i == 0 {
			used = 100
		}
		mkTraffic(t, inbound.Id, email, used, 0, 100, 0, true)
	}
	withdrawClientTombstones("purge-blocked", "purge-unrelated")
	t.Cleanup(func() { withdrawClientTombstones("purge-blocked", "purge-unrelated") })
	rt := &parkedDepletionRuntime{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	resume := func() { release.Do(func() { close(rt.release) }) }
	t.Cleanup(resume)
	useTestRuntimeManager(t).SetLocalRuntimeOverride(rt)
	purgeDone := make(chan error, 1)
	go func() {
		_, _, err := (&ClientService{}).DelDepleted(&InboundService{})
		purgeDone <- err
	}()
	select {
	case <-rt.entered:
	case err := <-purgeDone:
		t.Fatalf("purge never applied runtime removal: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("runtime removal never started")
	}
	var remaining int64
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", "purge-blocked").Count(&remaining).Error; err != nil || remaining != 0 {
		t.Errorf("runtime removal began before committed deletion: count=%d err=%v", remaining, err)
	}
	editDone := make(chan error, 1)
	go func() {
		_, _, err := (&ClientService{}).BulkAdjust(&InboundService{}, []string{"purge-unrelated"}, 0, 100, "", nil, "")
		editDone <- err
	}()
	select {
	case err := <-editDone:
		if err != nil {
			t.Errorf("unrelated edit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("blocked runtime removal held an unrelated inbound or the traffic writer")
		resume()
		if err := <-editDone; err != nil {
			t.Errorf("unrelated edit after release: %v", err)
		}
	}
	resume()
	if err := <-purgeDone; err != nil {
		t.Fatal(err)
	}
	if record := lookupClientRecord(t, "purge-unrelated"); record.TotalGB != 200 || !record.Enable {
		t.Fatalf("unrelated client edit lost: %+v", record)
	}
}

func TestDepletionRuntimeWaitDoesNotBlockUnrelatedClientEdit_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestDepletionRuntimeWaitDoesNotBlockUnrelatedClientEdit(t)
}

func TestDepletionPurgeHoldsStateLocksThroughMutation_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	_, record, _, _ := managedUsageFixture(t, 2000)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("id = ?", record.Id).Update("total_gb", 300).Error; err != nil {
		t.Fatal(err)
	}
	traffic := trafficOf(t, record.Email)
	useTestRuntimeManager(t).SetLocalRuntimeOverride(&fakeNodeRuntime{})
	t.Cleanup(func() { withdrawClientTombstones(record.Email) })
	selections := 0
	var probeErr error
	const callback = "test:depletion_state_lock"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.DryRun || tx.Error != nil || tx.RowsAffected == 0 || !strings.Contains(tx.Statement.SQL.String(), "c.reset_weekday = 0") {
			return
		}
		selections++
		if selections != 2 {
			return
		}
		for _, row := range []struct {
			table string
			id    int
		}{{"clients", record.Id}, {"client_traffics", traffic.Id}} {
			err := db.Transaction(func(probe *gorm.DB) error {
				var id int
				return probe.Raw("SELECT id FROM "+row.table+" WHERE id = ? FOR UPDATE NOWAIT", row.id).Scan(&id).Error
			})
			var state interface{ SQLState() string }
			if !errors.As(err, &state) || state.SQLState() != "55P03" {
				if err == nil {
					err = errors.New("another transaction acquired the row lock")
				}
				probeErr = errors.Join(probeErr, fmt.Errorf("%s could change after eligibility check: %w", row.table, err))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	count, _, err := (&ClientService{}).DelDepleted(&InboundService{})
	if count != 1 || err != nil || probeErr != nil || selections != 2 {
		t.Fatalf("purge count=%d err=%v probes=%v selections=%d", count, err, probeErr, selections)
	}
}

func TestDepletionPurgeKeepsOtherIdentityWithCaseVariant(t *testing.T) {
	setupBulkDB(t)
	useTestRuntimeManager(t).SetLocalRuntimeOverride(&fakeNodeRuntime{})
	clients := []model.Client{
		{Email: "case-client", SubID: "lower-sub", TotalGB: 100, Enable: true},
		{Email: "CASE-CLIENT", SubID: "upper-sub", TotalGB: 100, Enable: true},
	}
	inbound := mkInbound(t, 49108, model.VLESS, clientsSettings(t, clients))
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, clients); err != nil {
		t.Fatal(err)
	}
	mkTraffic(t, inbound.Id, "case-client", 100, 0, 100, 0, true)
	mkTraffic(t, inbound.Id, "CASE-CLIENT", 0, 0, 100, 0, true)
	if err := (&InboundService{}).DelDepletedClients(-1); err != nil {
		t.Fatal(err)
	}
	loaded, err := (&InboundService{}).GetInbound(inbound.Id)
	if err != nil {
		t.Fatalf("distinct healthy identity lost its inbound: %v", err)
	}
	survivors, err := (&InboundService{}).GetClients(loaded)
	if err != nil || !reflect.DeepEqual(emailsOf(survivors), []string{"CASE-CLIENT"}) {
		t.Fatalf("distinct healthy identity removed: %v, %v", emailsOf(survivors), err)
	}
	linked, err := (&ClientService{}).ListForInbound(nil, inbound.Id)
	if err != nil || !reflect.DeepEqual(emailsOf(linked), []string{"CASE-CLIENT"}) {
		t.Fatalf("distinct healthy identity detached: %v, %v", emailsOf(linked), err)
	}
	if traffic := trafficOf(t, "CASE-CLIENT"); traffic.Total != 100 || traffic.Up != 0 {
		t.Fatalf("healthy identity traffic changed: %+v", traffic)
	}
}

func TestDepletionPurgeKeepsOtherIdentityWithCaseVariant_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestDepletionPurgeKeepsOtherIdentityWithCaseVariant(t)
}
