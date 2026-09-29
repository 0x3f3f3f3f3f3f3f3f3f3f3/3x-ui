//go:build linux

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func startLegacyHandoff(t *testing.T, svc *XrayService, tunnel *model.Inbound, email string) (*xray.Process, net.Conn) {
	t.Helper()
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClientPolicy = nil
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag == tunnel.Tag {
			var settings map[string]any
			if err := json.Unmarshal(cfg.InboundConfigs[i].Settings, &settings); err != nil {
				t.Fatal(err)
			}
			settings["email"] = email
			cfg.InboundConfigs[i].Settings, err = json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	p := xray.NewProcess(cfg)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	xrayState.replace(p)
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flow.Close() })
	return p, flow
}

func TestClientPolicyLiveLegacyHandoffSettlesBeforeSeed(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "first")
	if _, err := svc.CollectAndSettleTraffic(); err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "second")
	lost := errors.New("legacy commit acknowledgement lost")
	if _, _, err := old.SettleTraffic(func(batch *xray.TrafficBatch) error {
		if err := svc.settleLegacyTrafficBatch(batch); err != nil {
			return err
		}
		return lost
	}); !errors.Is(err, lost) {
		t.Fatalf("lost commit fixture: %v", err)
	}
	managedActivationEcho(t, flow, "last")
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("healthy legacy handoff: %v", err)
	}
	if old.IsRunning() || currentXrayProcess() == old || !currentXrayProcess().IsControlReady() {
		t.Fatal("handoff did not replace the drained child")
	}
	managedActivationClosed(t, flow)
	var receipt model.ClientPolicyReceipt
	if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.SeedUpload != 115 || receipt.SeedDownload != 215 || receipt.SeedBilled != 330 {
		t.Fatalf("seed missed or duplicated legacy final delta: %+v", receipt)
	}
	managedFlow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer managedFlow.Close()
	managedActivationEcho(t, managedFlow, "next")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 119 || total.RawDownload != 219 || total.BilledBytes != 346 {
		t.Fatalf("handoff repriced history or lost managed accounting: %+v", total)
	}
}

func TestClientPolicyLiveLegacyHandoffRetriesFailedFinalSQL(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "final")
	db := database.GetDB()
	injected := errors.New("final receipt unavailable")
	const hook = "test:fail-final-legacy-receipt"
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_traffic_receipts" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(hook) })
	if err := svc.RestartXray(true); !errors.Is(err, injected) {
		t.Fatalf("final SQL failure not returned: %v", err)
	}
	if currentXrayProcess() != old || !old.IsRunning() {
		t.Fatal("failed final SQL discarded the old process receipt state")
	}
	managedActivationClosed(t, flow)
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DesiredPolicyVersion != 0 {
		t.Fatal("failed final settlement reserved managed policy versions")
	}
	var usage xray.ClientTraffic
	if err := db.Where("email = ?", owner.Email).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Up != 100 || usage.Down != 200 {
		t.Fatalf("failed final settlement partially committed usage: %+v", usage)
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("retry final settlement: %v", err)
	}
	var receipt model.ClientPolicyReceipt
	if err := db.Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.SeedUpload != 105 || receipt.SeedDownload != 205 || receipt.SeedBilled != 310 {
		t.Fatalf("retry missed or duplicated final counters: %+v", receipt)
	}
}

func TestClientPolicyLiveLegacyHandoffRejectsConflictBeforePreparation(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "before")
	mkInbound(t, tunnel.Port, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := svc.RestartXray(true); err == nil {
		t.Fatal("conflicting candidate was accepted")
	}
	if !old.IsRunning() || currentXrayProcess() != old || old.FinalTrafficPending() {
		t.Fatal("candidate rejection changed live legacy traffic")
	}
	managedActivationEcho(t, flow, "after")
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DesiredPolicyVersion != 0 {
		t.Fatalf("rejected candidate reserved version: %d", stored.DesiredPolicyVersion)
	}
	var receipts int64
	if err := database.GetDB().Model(&model.LegacyTrafficReceipt{}).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatal("rejected candidate settled traffic")
	}
}

func TestClientPolicyLiveLegacyHandoffRejectsUnmeteredConfig(t *testing.T) {
	for _, mode := range []string{"missing-email", "missing-stats"} {
		t.Run(mode, func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			email := owner.Email
			if mode == "missing-email" {
				email = ""
			} else {
				raw, err := svc.settingService.GetXrayConfigTemplate()
				if err != nil {
					t.Fatal(err)
				}
				var template map[string]any
				if err := json.Unmarshal([]byte(raw), &template); err != nil {
					t.Fatal(err)
				}
				template["policy"] = map[string]any{}
				data, err := json.Marshal(template)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.settingService.saveSetting("xrayTemplateConfig", string(data)); err != nil {
					t.Fatal(err)
				}
			}
			old, flow := startLegacyHandoff(t, svc, tunnel, email)
			managedActivationEcho(t, flow, "unmetered")
			if err := svc.RestartXray(true); err == nil {
				t.Fatal("unmetered legacy traffic accepted as zero")
			}
			if !old.IsRunning() || old.FinalTrafficPending() {
				t.Fatal("unmetered rejection drained old traffic")
			}
			managedActivationEcho(t, flow, "still serving")
		})
	}
}

func TestClientPolicyLiveLegacyHandoffRejectsChangedBinary(t *testing.T) {
	upstream := os.Getenv("XRAY_UPSTREAM_E2E_BINARY")
	if upstream == "" {
		t.Skip("set XRAY_UPSTREAM_E2E_BINARY")
	}
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "before")
	path := filepath.Join(os.Getenv("XUI_BIN_FOLDER"), xray.GetBinaryName())
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(upstream, path); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err == nil {
		t.Fatal("unsupported replacement core accepted")
	}
	if !old.IsRunning() || old.FinalTrafficPending() {
		t.Fatal("replacement capability rejection drained old traffic")
	}
	managedActivationEcho(t, flow, "after")
}

func TestClientPolicyLiveLegacyHandoffFencesReceiptIdentity(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "tail")
	db := database.GetDB()
	const hook = "test:rename-during-final-receipt"
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "legacy_traffic_receipts" {
			return
		}
		tx.AddError(tx.Exec("UPDATE clients SET email = ? WHERE stable_id = ?", "changed-owner", owner.StableID).Error)
		tx.AddError(tx.Exec("UPDATE client_traffics SET email = ? WHERE email = ?", "changed-owner", owner.Email).Error)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(hook) })
	if err := svc.RestartXray(true); !errors.Is(err, ErrManagedConfigStale) {
		t.Fatalf("final SQL accepted stale ownership: %v", err)
	}
	if !old.IsRunning() || !old.FinalTrafficPending() {
		t.Fatal("stale ownership discarded final snapshot")
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil || stored.Email != owner.Email {
		t.Fatalf("failed ownership check did not roll back: %v", err)
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var receipt model.ClientPolicyReceipt
	if err := db.Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.SeedBilled != 308 {
		t.Fatalf("identity retry final seed: %+v", receipt)
	}
}

func TestClientPolicyLiveLegacyHandoffPendingOutlivesOptIn(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "final")
	db := database.GetDB()
	injected := errors.New("pending final SQL")
	const hook = "test:pending-final-opt-in"
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_traffic_receipts" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(hook) })
	if err := svc.RestartXray(true); !errors.Is(err, injected) {
		t.Fatal(err)
	}
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
	if err := old.Stop(); err != nil {
		t.Fatal(err)
	}
	if requested, err := svc.managedPolicyRequested(); err != nil || !requested {
		t.Fatalf("pending receipt lost to opt-out: %v %v", requested, err)
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var receipt model.ClientPolicyReceipt
	if err := db.Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.SeedUpload != 105 || receipt.SeedDownload != 205 {
		t.Fatalf("dead child pending snapshot was lost: %+v", receipt)
	}
}

func TestClientPolicyLiveLegacyHandoffPinsIdentityAcrossRetry(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "original owner")
	db := database.GetDB()
	injected := errors.New("pending original owner's final SQL")
	const hook = "test:pending-original-owner"
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_traffic_receipts" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(hook) })
	if err := svc.RestartXray(true); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	replacement := *owner
	replacement.Id = 0
	replacement.StableID = ""
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("client_id = ?", owner.Id).Delete(&model.ClientInbound{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(owner).Error; err != nil {
			return err
		}
		if err := tx.Create(&replacement).Error; err != nil {
			return err
		}
		return tx.Create(&model.ClientInbound{ClientId: replacement.Id, InboundId: tunnel.Id}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if replacement.StableID == owner.StableID {
		t.Fatal("replacement did not get a new identity")
	}
	if err := svc.RestartXray(true); err == nil {
		t.Fatal("cached traffic rebound to a replacement identity")
	}
	if currentXrayProcess() != old || !old.FinalTrafficPending() {
		t.Fatal("owner mismatch discarded original pending snapshot")
	}
	var receipts int64
	if err := db.Model(&model.LegacyTrafficReceipt{}).Count(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatal("replacement received old owner's final receipt")
	}
}

func TestClientPolicyLiveLegacyHandoffUsesVerifiedExecutable(t *testing.T) {
	upstream := os.Getenv("XRAY_UPSTREAM_E2E_BINARY")
	if upstream == "" {
		t.Skip("set XRAY_UPSTREAM_E2E_BINARY")
	}
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	_, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "pinned")
	db := database.GetDB()
	const hook = "test:replace-executable-during-final-sql"
	changed := false
	if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "legacy_traffic_receipts" || changed {
			return
		}
		changed = true
		path := filepath.Join(os.Getenv("XUI_BIN_FOLDER"), xray.GetBinaryName())
		if err := os.Remove(path); err != nil {
			tx.AddError(err)
			return
		}
		tx.AddError(os.Symlink(upstream, path))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(hook) })
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("handoff executed a replacement binary after validation: %v", err)
	}
	if !changed || !currentXrayProcess().IsControlReady() {
		t.Fatal("executable replacement fixture did not complete")
	}
}

func TestClientPolicyLiveLegacyHandoffDoesNotTrustAPITag(t *testing.T) {
	svc, tunnel, owner, target := setupManagedActivationService(t)
	raw, err := svc.settingService.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var template map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &template); err != nil {
		t.Fatal(err)
	}
	template["routing"] = json.RawMessage(`{"rules":[]}`)
	template["inbounds"] = json.RawMessage(fmt.Sprintf(`[{"tag":"api","listen":"127.0.0.1","port":62789,"protocol":"tunnel","settings":{"address":"127.0.0.1","port":%d,"network":"tcp"}}]`, target))
	data, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.settingService.saveSetting("xrayTemplateConfig", string(data)); err != nil {
		t.Fatal(err)
	}
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	business, err := net.DialTimeout("tcp", "127.0.0.1:62789", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close()
	managedActivationEcho(t, business, "unmetered api-tag business")
	if err := svc.RestartXray(true); err == nil {
		t.Fatal("business listener was trusted by API tag alone")
	}
	if !old.IsRunning() || old.FinalTrafficPending() {
		t.Fatal("API-tag refusal drained old traffic")
	}
	managedActivationEcho(t, flow, "still live")
}

func TestClientPolicyLiveLegacyHandoffInterruptedPanelRefusesStaleSeed(t *testing.T) {
	for _, optOut := range []bool{false, true} {
		t.Run(fmt.Sprintf("opt-out-%v", optOut), func(t *testing.T) {
			svc, tunnel, owner, _ := setupManagedActivationService(t)
			old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
			managedActivationEcho(t, flow, "final")
			db := database.GetDB()
			injected := errors.New("final SQL interrupted")
			const hook = "test:interrupted-handoff"
			if err := db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
				if tx.Statement.Table == "legacy_traffic_receipts" {
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Callback().Create().Remove(hook) })
			if err := svc.RestartXray(true); !errors.Is(err, injected) {
				t.Fatalf("failure fixture: %v", err)
			}
			if err := db.Callback().Create().Remove(hook); err != nil {
				t.Fatal(err)
			}
			if err := old.Stop(); err != nil {
				t.Fatal(err)
			}
			xrayState.replace(nil)
			if optOut {
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
			if err := svc.RestartXray(true); err == nil {
				t.Fatal("new panel silently restarted from stale usage after losing final snapshot")
			}
			if currentXrayProcess() != nil {
				t.Fatal("interrupted handoff created a replacement business process")
			}
			stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
			if err != nil || stored.DesiredPolicyVersion != 0 {
				t.Fatalf("interrupted handoff prepared a policy: %+v %v", stored, err)
			}
			var receipts int64
			if err := db.Model(&model.ClientPolicyReceipt{}).Count(&receipts).Error; err != nil || receipts != 0 {
				t.Fatalf("interrupted handoff seeded stale usage: %d %v", receipts, err)
			}
		})
	}
}

func TestClientPolicyLiveLegacyHandoffCompletionRollsBackWithFinalReceipt(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "final")
	db := database.GetDB()
	injected := errors.New("final receipt update failed")
	const hook = "test:handoff-receipt-rollback"
	if err := db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "legacy_traffic_receipts" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(hook) })
	if err := svc.RestartXray(true); !errors.Is(err, injected) {
		t.Fatalf("receipt failure: %v", err)
	}
	var source model.ClientPolicySource
	if err := db.Where("node_key = ?", "local").First(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.HandoffBootID != old.TrafficDrainBootID() || source.HandoffBatchID != "" || source.HandoffProcessID != "" {
		t.Fatalf("failed final SQL left a completion marker: %+v", source)
	}
	if err := BindClientPolicySource("local", source.InstanceID, 1); err == nil {
		t.Fatal("incomplete handoff allowed activation through the bootstrap boundary")
	}
	if _, err := PrepareClientPolicyLedger(source.InstanceID, owner.StableID); err == nil {
		t.Fatal("incomplete handoff allowed a stale usage seed")
	}
	if err := db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("owned retry: %v", err)
	}
}

func TestClientPolicyLiveLegacyHandoffCompletedSurvivesPanelRestart(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	old, flow := startLegacyHandoff(t, svc, tunnel, owner.Email)
	managedActivationEcho(t, flow, "final")
	db := database.GetDB()
	injected := errors.New("policy preparation interrupted after settlement")
	const hook = "test:handoff-after-completion"
	if err := db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" {
			if updates, ok := tx.Statement.Dest.(map[string]any); ok {
				if _, preparing := updates["desired_policy_version"]; preparing {
					tx.AddError(injected)
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(hook) })
	if err := svc.RestartXray(true); !errors.Is(err, injected) {
		t.Fatalf("post-settlement failure: %v", err)
	}
	if old.IsRunning() {
		t.Fatal("post-settlement fixture did not stop the old child")
	}
	if err := db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	xrayState.replace(nil)
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("committed final settlement could not resume: %v", err)
	}
	var receipt model.ClientPolicyReceipt
	if err := db.Where("client_id = ?", owner.StableID).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.SeedUpload != 105 || receipt.SeedDownload != 205 || receipt.SeedBilled != 310 {
		t.Fatalf("resumed handoff lost final traffic: %+v", receipt)
	}
	if err := currentXrayProcess().Stop(); err != nil {
		t.Fatal(err)
	}
	xrayState.replace(nil)
	if err := db.Where("1 = 1").Delete(&model.LegacyTrafficReceipt{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); !errors.Is(err, ErrLegacyHandoffInterrupted) {
		t.Fatalf("completion marker accepted without its committed final receipt: %v", err)
	}
}
