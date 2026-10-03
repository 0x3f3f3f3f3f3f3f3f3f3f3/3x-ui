package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

func TestManagedAuthorityCalendarNoOpResumesOriginalFlow(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	if err := db.Model(client).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ResetLocalClientPolicy(ctx, client.StableID, "recent-manual"); err != nil {
		t.Fatal(err)
	}
	var before model.ClientRecord
	if err := db.First(&before, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "before")
	cs := &ClientService{}
	if err := cs.RunScheduledTrafficReset(ctx, "daily", now); err != nil {
		t.Fatal(err)
	}
	var batch model.ClientTrafficResetBatch
	if err := db.First(&batch, "scheduled_at > 0").Error; err != nil {
		t.Fatal(err)
	}
	var targets []clientResetTarget
	if err := json.Unmarshal([]byte(batch.TargetsJSON), &targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ClientID != client.StableID || !batch.Applied || batch.Affected != 0 {
		t.Fatal("calendar operation did not retain the original client as a completed no-op")
	}
	managedActivationEcho(t, flow, "after")
	if err := cs.RunScheduledTrafficReset(ctx, "daily", now); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() != process {
		t.Fatal("calendar no-op restarted the business core")
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Policy.Version != uint64(before.DesiredPolicyVersion) || a.Usage != (policyauthority.Usage{RawUpload: 115, RawDownload: 215, BilledBytes: 360}) || a.WindowBaseline != 316 || a.WindowUsed != 44 || a.HeldCapacity != 0 {
		t.Fatalf("calendar no-op changed policy, lifetime billing, or reset window: version=%d usage=%+v baseline=%d used=%d held=%d", a.Policy.Version, a.Usage, a.WindowBaseline, a.WindowUsed, a.HeldCapacity)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ?", client.StableID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("calendar no-op created extra policy resets: %d", count)
	}
}

func TestManagedAuthorityCalendarPartialResetResumesBothFlows(t *testing.T) {
	svc, firstTunnel, first, target := setupManagedActivationService(t)
	db := database.GetDB()
	if err := db.Model(first).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	second := model.ClientRecord{Email: "calendar-eligible", SubID: "calendar-eligible-sub", Enable: true, TotalGB: 10000, TrafficReset: "daily", Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: second.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	secondTunnel := mkInbound(t, port, model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target))
	cs := &ClientService{}
	if _, err := cs.Attach(&InboundService{}, second.Id, []int{secondTunnel.Id}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	flows := make([]net.Conn, 2)
	for i, inbound := range []*model.Inbound{firstTunnel, secondTunnel} {
		flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer flow.Close()
		flows[i] = flow
		managedActivationEcho(t, flow, "warm")
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ResetLocalClientPolicy(ctx, first.StableID, "recent-manual"); err != nil {
		t.Fatal(err)
	}
	for _, flow := range flows {
		managedActivationEcho(t, flow, "before")
	}
	if err := cs.RunScheduledTrafficReset(ctx, "daily", now); err != nil {
		t.Fatal(err)
	}
	var batch model.ClientTrafficResetBatch
	if err := db.First(&batch, "scheduled_at > 0").Error; err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(batch.ManagedIDsJSON), &ids); err != nil {
		t.Fatal(err)
	}
	if !batch.Applied || batch.Affected != 1 || len(ids) != 1 || ids[0] != second.StableID {
		t.Fatal("calendar reset did not select only its eligible managed client")
	}
	for _, flow := range flows {
		managedActivationEcho(t, flow, "after")
	}
	if err := cs.RunScheduledTrafficReset(ctx, "daily", now); err != nil {
		t.Fatal(err)
	}
	if currentXrayProcess() != process {
		t.Fatal("partial calendar reset restarted the business core")
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	for i, client := range []*model.ClientRecord{first, &second} {
		a, err := state.Journal.Account(client.StableID)
		if err != nil {
			t.Fatal(err)
		}
		baseline, used := uint64(316), uint64(44)
		if i == 1 {
			baseline, used = 340, 20
		}
		if a.Policy.Version != 2 || a.Usage != (policyauthority.Usage{RawUpload: 115, RawDownload: 215, BilledBytes: 360}) || a.WindowBaseline != baseline || a.WindowUsed != used || a.HeldCapacity != 0 {
			t.Fatalf("partial calendar reset changed client %d policy/billing/window: version=%d usage=%+v baseline=%d used=%d held=%d", i, a.Policy.Version, a.Usage, a.WindowBaseline, a.WindowUsed, a.HeldCapacity)
		}
		var count int64
		if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ?", client.StableID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("partial calendar reset created extra resets for client %d: %d", i, count)
		}
	}
}

func TestManagedAuthorityCalendarPreparationFailureStopsOriginalFlow(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	if err := db.Model(client).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	batch, err := captureScheduledTrafficReset(ctx, "daily", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("calendar preparation read failed")
	const callback = "test:fail-calendar-preparation"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_traffic_reset_batches" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, _, err = (&ClientService{}).applyTrafficResetBatch(ctx, &InboundService{}, batch)
	if !errors.Is(err, injected) {
		t.Fatalf("calendar preparation failure was not reported: %v", err)
	}
	if process.IsRunning() || !isNeedXrayRestart.Load() {
		t.Fatal("failed calendar preparation retained active core permissions")
	}
	managedActivationClosed(t, flow)
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&batch, "request_id = ?", batch.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if batch.Applied {
		t.Fatal("failed calendar preparation acknowledged a reset")
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	// Migration seeds a baseline of 300 and retains those 300 bytes in the window.
	if a.Policy.Version != 1 || a.Usage != (policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316}) || a.WindowBaseline != 300 || a.WindowUsed != 316 || a.HeldCapacity != 0 {
		t.Fatalf("failed calendar preparation changed billing/reset state: version=%d usage=%+v baseline=%d used=%d held=%d", a.Policy.Version, a.Usage, a.WindowBaseline, a.WindowUsed, a.HeldCapacity)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ?", client.StableID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed calendar preparation committed policy resets: %d", count)
	}
}
