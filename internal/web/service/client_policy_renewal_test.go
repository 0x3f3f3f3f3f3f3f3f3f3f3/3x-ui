package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyRenewalPollingPreservesLifetimeAndRestrictions(t *testing.T) {
	for _, mode := range []string{"normal", "sql-failure", "control-loss"} {
		t.Run(mode, func(t *testing.T) { testClientPolicyRenewal(t, mode) })
	}
}

func testClientPolicyRenewal(t *testing.T, mode string) {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	dir, err := os.MkdirTemp("", "policy-renew-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).UnixMilli()
	clients := []model.Client{
		{Email: "renew-active", ID: "11111111-1111-1111-1111-111111111111", Enable: true, TotalGB: 9007199254740993, ExpiryTime: past, Reset: 1, ResetMax: 2},
		{Email: "renew-disabled", ID: "22222222-2222-2222-2222-222222222222", Enable: false, TotalGB: 1000, ExpiryTime: past, Reset: 1, ResetMax: 2},
		{Email: "renew-truncated", ID: "33333333-3333-3333-3333-333333333333", Enable: true, TotalGB: 1000, ExpiryTime: past - 3*86400000, Reset: 1, ResetMax: 1},
		{Email: "renew-spent", ID: "44444444-4444-4444-4444-444444444444", Enable: true, TotalGB: 1000, ExpiryTime: past, Reset: 1, ResetMax: 1},
	}
	rawClients, err := json.Marshal(map[string]any{"clients": clients, "testMarker": "preserve"})
	if err != nil {
		t.Fatal(err)
	}
	inbound := mkInbound(t, 24218, model.VLESS, string(rawClients))
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, clients); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(clients))
	for i, client := range clients {
		var record model.ClientRecord
		if err := db.First(&record, "email = ?", client.Email).Error; err != nil {
			t.Fatal(err)
		}
		ids[i] = record.StableID
		if err := db.Model(&record).Update("enable", client.Enable).Error; err != nil {
			t.Fatal(err)
		}
		count := 0
		if i == 3 {
			count = 1
		}
		if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbound.Id, Enable: client.Enable, Total: client.TotalGB, ExpiryTime: client.ExpiryTime, Reset: client.Reset, ResetMax: client.ResetMax, ResetCount: count, Up: 111, Down: 222}).Error; err != nil {
			t.Fatal(err)
		}
	}
	state, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	state.Policies, err = PrepareClientPolicies(ids)
	if err != nil {
		t.Fatal(err)
	}
	policyJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "control.sock")
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"stats":{},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService","StatsService"]},"clientPolicy":%s,"outbounds":[{"protocol":"freedom"}]}`, socket, policyJSON)
	var config xray.Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	process := xray.NewTestProcess(&config, filepath.Join(dir, "renew.json"))
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
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{}))
	defer func() { xrayState.replace(previousProcess); panelruntime.SetManager(previousManager) }()
	if mode == "sql-failure" {
		injected := errors.New("injected renewal commit preparation failure")
		attempts := 0
		if err := db.Callback().Create().Before("gorm:create").Register("test:renewal-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "client_policy_resets" {
				attempts++
				tx.AddError(injected)
			}
		}); err != nil {
			t.Fatal(err)
		}
		_, _, pollErr := (&XrayService{}).GetXrayTraffic()
		if err := db.Callback().Create().Remove("test:renewal-failure"); err != nil {
			t.Fatal(err)
		}
		if pollErr != nil || attempts == 0 {
			t.Fatalf("renewal failure stopped independent traffic polling or was not exercised: %v, attempts=%d", pollErr, attempts)
		}
		var count int64
		if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed renewal left quota windows: %d %v", count, err)
		}
		for i, client := range clients {
			var record model.ClientRecord
			if err := db.First(&record, "stable_id = ?", ids[i]).Error; err != nil {
				t.Fatal(err)
			}
			row := trafficOf(t, client.Email)
			wantCount := 0
			if i == 3 {
				wantCount = 1
			}
			if record.ExpiryTime != client.ExpiryTime || record.DesiredPolicyVersion != 1 || row.ExpiryTime != client.ExpiryTime || row.ResetCount != wantCount {
				t.Fatalf("failed renewal partially committed: %+v, %+v", record, row)
			}
		}
	}
	if mode == "control-loss" {
		if err := pollLocalClientPolicyLedger(ctx, process); err != nil {
			t.Fatal(err)
		}
		moved := false
		if err := db.Callback().Update().After("gorm:update").Register("test:renewal-control-loss", func(tx *gorm.DB) {
			if tx.Statement.Table == "inbounds" && !moved {
				tx.AddError(os.Rename(socket, socket+".hidden"))
				moved = true
			}
		}); err != nil {
			t.Fatal(err)
		}
		failureCtx, failureCancel := context.WithTimeout(ctx, 2*time.Second)
		renewalErr := renewLocalClientPolicies(failureCtx, process)
		failureCancel()
		if err := db.Callback().Update().Remove("test:renewal-control-loss"); err != nil {
			t.Fatal(err)
		}
		if moved {
			if err := os.Rename(socket+".hidden", socket); err != nil {
				t.Fatal(err)
			}
		}
		if !moved || renewalErr == nil || !strings.Contains(renewalErr.Error(), "application failed") {
			t.Fatalf("control loss did not leave a committed renewal: %v, moved=%v", renewalErr, moved)
		}
	}
	for range 2 {
		if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (&InboundService{}).AddTraffic(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	api, err := xray.DialClientPolicy(ctx, socket, state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for i, client := range clients {
		row := trafficOf(t, client.Email)
		wantExpiry := client.ExpiryTime
		wantCount := 1
		wantVersion := uint64(2)
		wantBaseline := uint64(0)
		if i != 3 {
			wantExpiry += 86400000
		} else {
			wantVersion = 1
		}
		if i < 2 {
			wantBaseline = 333
		}
		var record model.ClientRecord
		if err := db.First(&record, "stable_id = ?", ids[i]).Error; err != nil {
			t.Fatal(err)
		}
		if row.ExpiryTime != wantExpiry || record.ExpiryTime != wantExpiry || row.ResetCount != wantCount || row.Up != 111 || row.Down != 222 || record.Enable != client.Enable || row.Enable != client.Enable {
			t.Fatalf("renewal lost expiry, count, raw history, or manual restriction: %s record=%+v row=%+v", client.Email, record, row)
		}
		snapshot, err := api.GetClient(ctx, ids[i])
		if err != nil || snapshot.Policy.Version != wantVersion || snapshot.Policy.ExpiresAt != wantExpiry || snapshot.Policy.Enabled != client.Enable || snapshot.Policy.QuotaBaselineBytes != wantBaseline || snapshot.Usage.RawUpload != 111 || snapshot.Usage.RawDownload != 222 || snapshot.Usage.BilledBytes != 333 {
			t.Fatalf("renewal did not reach the actual core: %s %+v, %v", client.Email, snapshot, err)
		}
		wantReason := uint32(0)
		if i == 1 {
			wantReason = uint32(clientpolicy.ReasonDisabled)
		} else if i >= 2 {
			wantReason = uint32(clientpolicy.ReasonExpired)
		}
		if snapshot.Reasons != wantReason {
			t.Fatalf("renewal cleared the wrong restriction: %s reasons=%d want=%d", client.Email, snapshot.Reasons, wantReason)
		}
		projected, err := (&InboundService{}).GetClientTrafficByEmail(client.Email)
		if err != nil {
			t.Fatal(err)
		}
		wantPeriod := "333"
		if i < 2 {
			wantPeriod = "0"
		}
		if projected.Accounting == nil || projected.Accounting.ResetPending || projected.Accounting.Lifetime.Billed != "333" || projected.Accounting.Period.Billed != wantPeriod {
			t.Fatalf("renewal statistics lost the acknowledged window: %+v", projected.Accounting)
		}
		if total := policyLedgerTotal(t, ids[i]); total.RawUpload != 111 || total.RawDownload != 222 || total.BilledBytes != 333 {
			t.Fatalf("renewal erased lifetime: %+v", total)
		}
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("renewal granted duplicate or expired windows: %d, %v", count, err)
	}
	if err := db.First(inbound, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Clients []model.Client `json:"clients"`
		Marker  string         `json:"testMarker"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Marker != "preserve" || len(settings.Clients) != 4 {
		t.Fatalf("renewal lost inbound settings: %s", inbound.Settings)
	}
	for i, client := range settings.Clients {
		want := clients[i].ExpiryTime
		if i != 3 {
			want += 86400000
		}
		if client.ExpiryTime != want || client.Enable != clients[i].Enable || client.TotalGB != clients[i].TotalGB {
			t.Fatalf("renewal did not preserve exact configuration: %+v", client)
		}
	}
	if mode == "normal" {
		if err := os.Rename(socket, socket+".hidden"); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Rename(socket+".hidden", socket) }()
		idleCtx, idleCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer idleCancel()
		if err := renewLocalClientPolicies(idleCtx, process); err != nil {
			t.Fatalf("exhausted renewal allowances still required a core checkpoint: %v", err)
		}
	}
}
