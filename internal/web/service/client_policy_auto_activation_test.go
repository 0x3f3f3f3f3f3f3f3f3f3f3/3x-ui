//go:build linux

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func removeManagedTemplateOptIn(t *testing.T, svc *XrayService) {
	t.Helper()
	raw, err := svc.settingService.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var template map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &template); err != nil {
		t.Fatal(err)
	}
	delete(template, "clientPolicy")
	data, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.settingService.saveSetting("xrayTemplateConfig", string(data)); err != nil {
		t.Fatal(err)
	}
}

func TestClientPolicyAutomaticallyActivatesOwnedTunnel(t *testing.T) {
	for _, explicitPolicy := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-policy-%v", explicitPolicy), func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			removeManagedTemplateOptIn(t, svc)
			if !explicitPolicy {
				if err := database.GetDB().Model(owner).Updates(map[string]any{"policy_upload_bytes_per_second": nil, "policy_download_bytes_per_second": nil, "policy_multiplier": nil}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatalf("ordinary activation: %v", err)
			}
			if len(currentXrayProcess().GetConfig().ClientPolicy) == 0 {
				t.Fatal("ordinary owned Tunnel started without unified client policy")
			}
			flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "auto")
			if _, _, err := svc.GetXrayTraffic(); err != nil {
				t.Fatal(err)
			}
			wantBilled := int64(308)
			if explicitPolicy {
				wantBilled = 316
			}
			if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 104 || total.RawDownload != 204 || total.BilledBytes != wantBilled {
				t.Fatalf("ordinary activation bypassed accounting: %+v", total)
			}
		})
	}
}

func TestClientPolicyAutomaticSelectionUsesLocalListenerBindings(t *testing.T) {
	for _, scenario := range []string{"local-policy", "local-default-policy", "local-tunnel", "local-default-auth", "remote", "disabled-listener", "unbound"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			restore := SetXrayProcessForTest(nil)
			t.Cleanup(restore)
			db := database.GetDB()
			owner := model.ClientRecord{Email: "automatic-choice", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
			protocol := model.VLESS
			want := scenario == "local-policy" || scenario == "local-default-policy" || scenario == "local-tunnel"
			if scenario == "local-default-policy" {
				owner.Policy = &model.ClientPolicyOptions{}
			}
			if scenario == "local-default-auth" || scenario == "local-tunnel" {
				owner.Policy = nil
			}
			if scenario == "local-tunnel" {
				protocol = model.Tunnel
			}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			inbound := mkInbound(t, 45192, protocol, `{"clients":[]}`)
			if scenario == "remote" {
				if err := db.Model(inbound).Update("node_id", 7).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "disabled-listener" {
				if err := db.Model(inbound).Update("enable", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "unbound" {
				if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if got, err := (&XrayService{}).managedPolicyRequested(); err != nil || got != want {
				t.Fatalf("automatic local selection: got %v want %v err %v", got, want, err)
			}
		})
	}
}

func TestClientPolicyAutomaticEditHandsOffLiveLegacy(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "final")
	client := owner.ToClient()
	client.Policy = &model.ClientPolicyOptions{Multiplier: "0.5"}
	if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, *client, 0); err != nil {
		t.Fatalf("ordinary policy edit: %v", err)
	}
	if currentXrayProcess() == old || len(currentXrayProcess().GetConfig().ClientPolicy) == 0 || old.IsRunning() {
		t.Fatal("ordinary policy edit left the legacy child running without enforcement")
	}
	managedActivationClosed(t, flow)
	var receipt model.ClientPolicyReceipt
	if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil || receipt.SeedUpload != 105 || receipt.SeedDownload != 205 || receipt.SeedBilled != 310 {
		t.Fatalf("automatic handoff lost final legacy seed: %+v %v", receipt, err)
	}
	managed, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	managedActivationEcho(t, managed, "half")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 109 || total.RawDownload != 209 || total.BilledBytes != 314 {
		t.Fatalf("automatic edit missed current multiplier or repriced history: %+v", total)
	}
}

func TestClientPolicyAutomaticReconcileQueuesStoppedCore(t *testing.T) {
	svc, _, _, _ := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	// Stop records manual intent even when no child has been started yet.
	_ = svc.StopXray()
	if handled, err := svc.ReconcileManagedChange(context.Background()); err != nil || !handled {
		t.Fatalf("stopped owned listener fell through to legacy mutation: %v %v", handled, err)
	}
	if currentXrayProcess() != nil || !isNeedXrayRestart.Load() || !isManuallyStopped.Load() {
		t.Fatal("queued automatic activation started or un-stopped the business core")
	}
}

func TestClientPolicyAutomaticEditReportsUnmeteredLegacyRefusal(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	old := xray.NewProcess(cfg)
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	xrayState.replace(old)
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "before")
	client := owner.ToClient()
	client.Policy = &model.ClientPolicyOptions{Multiplier: "0.5"}
	if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, *client, 0); !errors.Is(err, panelruntime.ErrManagedApply) || !errors.Is(err, xray.ErrTrafficDrainCapability) {
		t.Fatalf("unmetered legacy edit reported enforcement success: %v", err)
	}
	if currentXrayProcess() != old || !old.IsRunning() || old.FinalTrafficPending() {
		t.Fatal("refused automatic activation disturbed legacy ownership")
	}
	managedActivationEcho(t, flow, "after")
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil || stored.DesiredPolicyVersion != 0 || !isNeedXrayRestart.Load() {
		t.Fatalf("unapplied policy state not preserved: %+v %v", stored, err)
	}
}
