package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyResetRuntimePollingRetriesTheCommittedBoundary(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	dir, err := os.MkdirTemp("", "policy-reset-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	client := model.ClientRecord{Email: "reset-runtime-owner", Enable: true, TotalGB: 1000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Total: 1000}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	state.Policies, err = PrepareClientPolicies([]string{client.StableID})
	if err != nil {
		t.Fatal(err)
	}
	policyJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	socket := filepath.Join(dir, "control.sock")
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"stats":{},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService","StatsService"]},"clientPolicy":%s,"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, policyJSON, port, target.Addr().(*net.TCPAddr).Port, client.StableID)
	var config xray.Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	process := xray.NewTestProcess(&config, filepath.Join(dir, "reset.json"))
	defer process.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
	if err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
		return PrepareLocalClientPolicyBootstrap(caps, state)
	}); err != nil {
		t.Fatal(err)
	}
	previousProcess, _ := xrayState.snapshot()
	previousManager := panelruntime.GetManager()
	xrayState.replace(process)
	manager := panelruntime.NewManager(panelruntime.LocalDeps{})
	panelruntime.SetManager(manager)
	defer func() {
		xrayState.replace(previousProcess)
		panelruntime.SetManager(previousManager)
	}()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echo := func(n int) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		payload := bytes.Repeat([]byte{0x71}, n)
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, n)
		if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(payload, reply) {
			t.Fatalf("managed echo: %v, %x", err, reply)
		}
	}
	echo(8)
	injected := errors.New("reset insert failed")
	var checkpointErr error
	if err := db.Callback().Create().Before("gorm:create").Register("test:reset-insert-failure", func(tx *gorm.DB) {
		if tx.Statement.Table != "client_policy_resets" {
			return
		}
		done := make(chan error, 1)
		go func() {
			_, page, err := manager.Local().(panelruntime.ManagedProcessRuntime).ReadManagedLedger(ctx, process, 0, false)
			if err == nil && (len(page.Records) != 1 || page.Records[0].Usage.BilledBytes != 32) {
				err = fmt.Errorf("reset did not checkpoint before SQL: %+v", page)
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
		t.Fatalf("reset lost its database error or held a Runtime mutex during SQL: %v, %v", resetErr, checkpointErr)
	}
	var resetCount int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&resetCount).Error; err != nil || resetCount != 0 {
		t.Fatalf("failed reset left a request behind: %d, %v", resetCount, err)
	}
	if err := db.Callback().Create().After("gorm:create").Register("test:reset-control-loss", func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_resets" {
			tx.AddError(os.Rename(socket, socket+".hidden"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	failureCtx, failureCancel := context.WithTimeout(ctx, 2*time.Second)
	_, resetErr = (&ClientService{}).ResetTrafficByEmailWithRequest(failureCtx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-a", ClientID: client.StableID})
	failureCancel()
	if err := db.Callback().Create().Remove("test:reset-control-loss"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(socket+".hidden", socket); err != nil {
		t.Fatal(err)
	}
	if resetErr == nil || !strings.Contains(resetErr.Error(), "reset saved but core application failed") {
		t.Fatalf("lost control connection did not report the committed pending reset: %v", resetErr)
	}
	var reset model.ClientPolicyReset
	if err := db.Where("client_id = ?", client.StableID).First(&reset).Error; err != nil {
		t.Fatal(err)
	}
	if reset.PolicyVersion != 2 || reset.BilledBytes != 32 || reset.RawUpload != 8 || reset.RawDownload != 8 {
		t.Fatalf("reset did not checkpoint before persisting its boundary: %+v", reset)
	}
	assertAccounting := func(period, lifetime, applied string, pending bool) {
		t.Helper()
		traffic, err := (&InboundService{}).GetClientTrafficByEmail(client.Email)
		if err != nil || traffic == nil || traffic.Accounting == nil {
			t.Fatalf("read real core accounting: %+v %v", traffic, err)
		}
		if got := traffic.Accounting; got.ClientID != client.StableID || got.Period.Billed != period || got.Lifetime.Billed != lifetime || got.AppliedVersion != applied || got.ResetPending != pending {
			t.Fatalf("real core receipt projected the wrong window: %+v", got)
		}
	}
	assertAccounting("32", "32", "1", true)
	echo(4)
	remote := mkInbound(t, 24198, model.Tunnel, `{}`)
	if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	link := model.ClientInbound{ClientId: client.Id, InboundId: remote.Id}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("pending reset was applied after the client acquired an uncoordinated remote budget: %v", err)
	}
	if err := db.Delete(&link).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	api, err := xray.DialClientPolicy(ctx, socket, state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	assertCore := func(version, baseline, billed uint64) {
		t.Helper()
		current, err := api.GetClient(ctx, client.StableID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Policy.Version != version || current.Policy.QuotaBaselineBytes != baseline || current.Usage.BilledBytes != billed {
			t.Fatalf("core reset captured later usage or lost its pending application: %+v", current)
		}
		var saved conf.ClientPolicyConfig
		if err := json.Unmarshal(process.GetConfig().ClientPolicy, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Policies[0].Version != version || saved.Policies[0].QuotaBaselineBytes != baseline {
			t.Fatalf("runtime restart configuration lost the applied reset: %+v", saved.Policies)
		}
	}
	assertCore(2, 32, 48)
	assertAccounting("16", "48", "2", false)
	echo(8)
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-a", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	assertCore(2, 32, 80)
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-b", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	echo(3)
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-a", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	assertCore(3, 80, 92)
	assertAccounting("12", "92", "3", false)
	if got := policyLedgerTotal(t, client.StableID); got.RawUpload != 23 || got.RawDownload != 23 || got.BilledBytes != 92 {
		t.Fatalf("reset cleared or duplicated lifetime history: %+v", got)
	}
	if err := db.Model(&model.ClientRecord{}).Where("id = ?", client.Id).Updates(map[string]any{"enable": false, "expiry_time": time.Now().Add(-time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "request-c", ClientID: client.StableID}); err != nil {
		t.Fatal(err)
	}
	current, err := api.GetClient(ctx, client.StableID)
	if err != nil || current.Policy.Enabled || current.Policy.ExpiresAt <= 0 || current.ActiveSessions != 0 {
		t.Fatalf("reset removed other restrictions or kept their stream alive: %+v, %v", current, err)
	}
	var deadline net.Error
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("reset preserved a disabled client's existing TCP stream")
	} else if errors.As(err, &deadline) && deadline.Timeout() {
		t.Fatalf("disabled stream remained open: %v", err)
	}
	if err := process.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{RequestID: "while-stopped", ClientID: client.StableID}); err == nil || !strings.Contains(err.Error(), "managed core is not ready") {
		t.Fatalf("stopped managed core accepted a reset: %v", err)
	}
	if err := db.Model(&model.ClientPolicyReset{}).Count(&resetCount).Error; err != nil || resetCount != 3 {
		t.Fatalf("stopped core recorded an uncheckpointed reset: %d, %v", resetCount, err)
	}
	restarted := xray.NewTestProcess(process.GetConfig(), filepath.Join(dir, "restarted.json"))
	defer restarted.Stop()
	var recovered conf.ClientPolicyConfig
	if err := json.Unmarshal(restarted.GetConfig().ClientPolicy, &recovered); err != nil {
		t.Fatal(err)
	}
	if err := local.StartManagedProcess(ctx, restarted, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
		return PrepareLocalClientPolicyBootstrap(caps, &recovered)
	}); err != nil {
		t.Fatal(err)
	}
	process = restarted
	xrayState.replace(process)
	api.Close()
	api, err = xray.DialClientPolicy(ctx, socket, state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	assertCore(4, 92, 92)
	assertAccounting("0", "92", "4", false)
	current, err = api.GetClient(ctx, client.StableID)
	if err != nil || current.Policy.Enabled || current.Policy.ExpiresAt <= 0 || current.ActiveSessions != 0 {
		t.Fatalf("restart cleared reset restrictions: %+v, %v", current, err)
	}
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
		t.Fatalf("active managed identity fell back to legacy reset after losing its SQL ledger: %v", err)
	}
	var legacy xray.ClientTraffic
	if err := db.First(&legacy, "email = ?", client.Email).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&client, client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Up != 111 || legacy.Down != 222 || legacy.Enable || client.Enable {
		t.Fatalf("missing managed ledger cleared legacy usage or manual disable: %+v, enabled=%t", legacy, client.Enable)
	}
}
