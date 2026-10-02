package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"
)

func TestClientPolicyResetRuntimePollingRetriesTheCommittedBoundary(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var conn net.Conn
	dial := func() {
		t.Helper()
		if conn != nil {
			_ = conn.Close()
		}
		var err error
		conn, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	dial()
	managedActivationEcho(t, conn, "warmwarm")
	process := currentXrayProcess()
	injected := errors.New("reset insert failed")
	var checkpointErr error
	if err := db.Callback().Create().Before("gorm:create").Register("test:reset-insert-failure", func(tx *gorm.DB) {
		if tx.Statement.Table != "client_policy_resets" {
			return
		}
		done := make(chan error, 1)
		go func() {
			runtime, err := localManagedPolicyRuntime()
			if err == nil {
				_, page, readErr := runtime.ReadManagedLedger(ctx, process, 0, false)
				err = readErr
				if err == nil && (len(page.Records) != 1 || page.Records[0].Usage.BilledBytes != 332) {
					err = fmt.Errorf("reset did not checkpoint before SQL: %+v", page)
				}
			}
			done <- err
		}()
		select {
		case err := <-done:
			checkpointErr = err
			tx.AddError(err)
		case <-time.After(2 * time.Second):
			checkpointErr = errors.New("Runtime RPC mutex held during SQL reset preparation")
			tx.AddError(checkpointErr)
		}
		tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	_, resetErr := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "failed-insert", ClientID: client.StableID})
	if err := db.Callback().Create().Remove("test:reset-insert-failure"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(resetErr, injected) || checkpointErr != nil {
		t.Fatalf("reset lost database error or held Runtime mutex: %v/%v", resetErr, checkpointErr)
	}
	var resetCount int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&resetCount).Error; err != nil || resetCount != 0 {
		t.Fatalf("failed reset left request behind: %d/%v", resetCount, err)
	}
	if process.IsRunning() {
		t.Fatal("owned failed reset retained an unconfirmed business process")
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	dial()
	owner := managedAuthorityForProcess(currentXrayProcess())
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	// A committed SQL reset whose application reply was lost remains pending.
	// Subsequent payload must belong to that original committed boundary.
	if _, err := PrepareClientPolicyReset(owner.config.InstanceID, client.StableID, "request-a"); err != nil {
		t.Fatal(err)
	}
	assertAccounting := func(period, lifetime, applied string, pending bool) {
		t.Helper()
		traffic, err := (&InboundService{}).GetClientTrafficByEmail(client.Email)
		if err != nil || traffic == nil || traffic.Accounting == nil {
			t.Fatalf("read core accounting: %+v/%v", traffic, err)
		}
		got := traffic.Accounting
		if got.Period.Billed != period || got.Lifetime.Billed != lifetime || got.AppliedVersion != applied || got.ResetPending != pending {
			t.Fatalf("wrong committed window: %+v", got)
		}
	}
	assertAccounting("332", "332", "1", true)
	managedActivationEcho(t, conn, "post")
	remote := mkInbound(t, 24198, model.Tunnel, `{}`)
	if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	link := model.ClientInbound{ClientId: client.Id, InboundId: remote.Id}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("pending reset acquired uncoordinated remote budget: %v", err)
	}
	if err := db.Delete(&link).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	dial()
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	assertCore := func(version, baseline, billed uint64) {
		t.Helper()
		process := currentXrayProcess()
		endpoint, err := process.GetAPIEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		owner := managedAuthorityForProcess(process)
		api, err := xray.DialClientPolicy(ctx, endpoint, owner.config.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		defer api.Close()
		current, err := api.GetClient(ctx, client.StableID)
		if err != nil || current.Policy.Version != version || current.Policy.QuotaBaselineBytes != baseline || current.Usage.BilledBytes != billed {
			t.Fatalf("core recaptured pending boundary: %+v/%v", current, err)
		}
		if version == 4 && (current.Policy.Enabled || current.Policy.ExpiresAt <= 0 || current.ActiveSessions != 0) {
			t.Fatalf("reset/restart cleared manual restrictions: %+v", current)
		}
		var saved conf.ClientPolicyConfig
		if err := json.Unmarshal(process.GetConfig().ClientPolicy, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Policies[0].Version != version || saved.Policies[0].QuotaBaselineBytes != baseline {
			t.Fatalf("restart config lost reset: %+v", saved.Policies)
		}
	}
	assertCore(2, 332, 348)
	assertAccounting("16", "348", "2", false)
	owner = managedAuthorityForProcess(currentXrayProcess())
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.WindowUsed != 16 {
		t.Fatalf("pending reset refunded consumed capacity: %+v/%v", account, err)
	}
	managedActivationEcho(t, conn, "nextnext")
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-a", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	assertCore(2, 332, 380)
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-b", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, conn, "end")
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-a", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	assertCore(3, 380, 392)
	assertAccounting("12", "392", "3", false)
	if got := policyLedgerTotal(t, client.StableID); got.RawUpload != 123 || got.RawDownload != 223 || got.BilledBytes != 392 {
		t.Fatalf("reset cleared/duplicated lifetime: %+v", got)
	}
	if err := db.Model(&model.ClientRecord{}).Where("id = ?", client.Id).Updates(map[string]any{"enable": false, "expiry_time": time.Now().Add(-time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-c", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("disabled stream stayed open")
	} else if deadline, ok := err.(net.Error); ok && deadline.Timeout() {
		t.Fatalf("disabled stream remained idle: %v", err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "while-stopped", ClientID: client.StableID}); err == nil || !strings.Contains(err.Error(), "managed core is not ready") {
		t.Fatalf("stopped reset: %v", err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	assertCore(4, 392, 392)
	assertAccounting("0", "392", "4", false)
	if err := db.Where("client_id = ?", client.StableID).Delete(&model.ClientPolicyReceipt{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", client.StableID).Delete(&model.ClientPolicyTotal{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Updates(map[string]any{"up": 111, "down": 222, "enable": false}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "missing-ledger", ClientID: client.StableID}); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("missing managed ledger fell back to legacy reset: %v", err)
	}
	var legacy xray.ClientTraffic
	if err := db.First(&legacy, "email = ?", client.Email).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&client, client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Up != 111 || legacy.Down != 222 || legacy.Enable || client.Enable {
		t.Fatalf("missing managed ledger cleared history/restrictions: %+v/%t", legacy, client.Enable)
	}
}
