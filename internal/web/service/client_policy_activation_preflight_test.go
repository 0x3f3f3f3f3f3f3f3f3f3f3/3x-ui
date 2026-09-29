package service

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyLiveLegacyRefusalLeavesPreparationUntouched(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClientPolicy = nil
	process := xray.NewProcess(cfg)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	xrayState.replace(process)
	var flow net.Conn
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		flow, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	if err := svc.RestartXray(true); err == nil || !strings.Contains(err.Error(), "legacy traffic") {
		t.Fatalf("missing live legacy refusal: %v", err)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DesiredPolicyVersion != 0 {
		t.Errorf("rejected activation reserved policy version %d", stored.DesiredPolicyVersion)
	}
	for _, table := range []any{&model.ClientPolicySource{}, &model.ClientPolicyReceipt{}, &model.ClientPolicyTotal{}} {
		var count int64
		if err := database.GetDB().Model(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("rejected activation populated %T: %d", table, count)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XUI_DB_FOLDER"), "client-policy")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rejected activation created state: %v", err)
	}
	if !process.IsRunning() {
		t.Fatal("refusal stopped legacy process")
	}
	managedActivationEcho(t, flow, "safe")
}

func TestClientPolicyPostcommitBindConflictStopsExistingAccess(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	var listeners []*model.Inbound
	for range 2 {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := probe.Addr().(*net.TCPAddr).Port
		_ = probe.Close()
		listeners = append(listeners, mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`))
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	if err := database.GetDB().Model(listeners[1]).Update("port", listeners[0].Port).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "both bind") {
		t.Fatalf("manual restart did not refuse conflicting candidate: %v", err)
	}
	if !process.IsRunning() {
		t.Fatal("manual validation failure stopped existing process")
	}
	managedActivationEcho(t, flow, "safe")
	changed := owner.ToClient()
	changed.Enable = false
	if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, *changed, 0); err == nil || !strings.Contains(err.Error(), "both bind") {
		t.Fatalf("disable did not report conflict: %v", err)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil || stored.Enable {
		t.Fatalf("disable did not commit: %+v %v", stored, err)
	}
	if process.IsRunning() {
		t.Fatal("committed disable left old access running after bind conflict")
	}
	managedActivationClosed(t, flow)
}
