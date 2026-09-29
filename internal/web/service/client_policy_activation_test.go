package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/vless"
	vlessencoding "github.com/xtls/xray-core/proxy/vless/encoding"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyNormalUserMutationPreservesIdentityAndOtherFlows(t *testing.T) {
	svc, tunnel, _, target := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ib := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	other, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	managedActivationEcho(t, other, "warm")
	cs, is := &ClientService{}, &InboundService{}
	client := model.Client{Email: "hot-user", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", SubID: "hot-user-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
		t.Fatal(err)
	}
	flow := managedActivationVLESS(t, port, target, client.ID)
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	record, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, record.StableID); total.RawUpload != 4 || total.RawDownload != 4 || total.BilledBytes != 16 {
		t.Fatalf("ordinary user creation bypassed stable identity accounting: %+v", total)
	}
	client.Policy = &model.ClientPolicyOptions{Multiplier: "0.5"}
	if _, err := cs.Update(is, record.Id, client, 0); err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "half")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, record.StableID); total.RawUpload != 8 || total.RawDownload != 8 || total.BilledBytes != 20 {
		t.Fatalf("policy edit repriced history or lost a live stream: %+v", total)
	}
	client.ID = "01dc4f70-3902-446a-98cb-c00992d1a6c5"
	if _, err := cs.Update(is, record.Id, client, 0); err != nil {
		t.Fatal(err)
	}
	managedActivationClosed(t, flow)
	rt, err := panelruntime.GetManager().RuntimeFor(nil)
	if err != nil {
		t.Fatal(err)
	}
	stale := client
	stale.ID = "936997e1-3b0c-4de9-9eea-047ee5829d3e"
	if err := rt.UpdateUser(context.Background(), ib, client.Email, stale); err != nil {
		t.Fatalf("delayed runtime application failed to reconcile current credentials: %v", err)
	}
	if old, err := dialManagedActivationVLESS(t, port, target, stale.ID); err == nil {
		_ = old.Close()
		t.Fatal("delayed runtime callback restored a revoked credential")
	} else if os.IsTimeout(err) {
		t.Fatalf("revoked credential was left waiting instead of rejected: %v", err)
	}
	rotated := managedActivationVLESS(t, port, target, client.ID)
	managedActivationEcho(t, other, "stay")
	if currentXrayProcess() != process {
		t.Fatal("ordinary client mutation restarted unrelated clients")
	}
	if _, err := cs.Detach(is, record.Id, []int{ib.Id}); err != nil {
		t.Fatal(err)
	}
	managedActivationClosed(t, rotated)
	managedActivationEcho(t, other, "last")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, record.StableID); total.RawUpload != 12 || total.RawDownload != 12 || total.BilledBytes != 24 {
		t.Fatalf("credential rotation or detach reset the stable lifetime: %+v", total)
	}
	retained, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil || retained.StableID != record.StableID {
		t.Fatalf("detach destroyed reusable client identity: %+v %v", retained, err)
	}
}

func managedActivationEcho(t *testing.T, flow net.Conn, payload string) {
	t.Helper()
	_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := flow.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(flow, reply); err != nil || string(reply) != payload {
		t.Fatalf("managed payload: %q %v", reply, err)
	}
}

func TestClientPolicyDetachPreservesSiblingInboundFlow(t *testing.T) {
	svc, tunnel, owner, target := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ib := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	cs, is := &ClientService{}, &InboundService{}
	if _, err := cs.Attach(is, owner.Id, []int{ib.Id}); err != nil {
		t.Fatal(err)
	}
	record, err := cs.GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	auth := managedActivationVLESS(t, port, target, record.UUID)
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	managedActivationEcho(t, flow, "warm")
	if _, err := cs.Detach(is, owner.Id, []int{ib.Id}); err != nil {
		t.Fatal(err)
	}
	managedActivationClosed(t, auth)
	managedActivationEcho(t, flow, "stay")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 112 || total.RawDownload != 212 || total.BilledBytes != 348 {
		t.Fatalf("partial detach lost shared lifetime usage: %+v", total)
	}
}

func TestClientPolicyDisableWithLostControlStopsExistingAccess(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	managedActivationEcho(t, flow, "warm")
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(endpoint, endpoint+".offline"); err != nil {
		t.Fatal(err)
	}
	changed := owner.ToClient()
	changed.Enable = false
	_, updateErr := (&ClientService{}).Update(&InboundService{}, owner.Id, *changed, 0)
	if updateErr == nil {
		t.Fatal("lost control was reported as an applied disable")
	}
	_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = flow.Write([]byte("lost"))
	if _, err := flow.Read(make([]byte, 4)); err == nil || os.IsTimeout(err) {
		t.Fatalf("failed disable retained old access: %v", err)
	}
	if process.IsRunning() {
		t.Fatal("unreachable managed core was left serving outdated permissions")
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("recover latest disabled state: %v", err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 104 || total.RawDownload != 204 || total.BilledBytes != 316 {
		t.Fatalf("control failure recovery lost durable traffic: %+v", total)
	}
	var state conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Policies) != 1 || state.Policies[0].Enabled {
		t.Fatal("control recovery reopened an administratively disabled client")
	}
}

func TestClientPolicyStoppedServiceQueuesNewIdentity(t *testing.T) {
	svc, _, _, target := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ib := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	cs := &ClientService{}
	client := model.Client{Email: "created-while-stopped", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", Enable: true, SubID: "stopped-sub", Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if _, err := cs.Create(&InboundService{}, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
		t.Fatal(err)
	}
	svc.ApplyPendingRestart()
	if svc.IsXrayRunning() || !isManuallyStopped.Load() {
		t.Fatal("saving a managed identity restarted a manually stopped core")
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow := managedActivationVLESS(t, port, target, client.ID)
	_ = flow.Close()
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	record, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, record.StableID); total.RawUpload != 4 || total.RawDownload != 4 || total.BilledBytes != 16 {
		t.Fatalf("explicit start lost the queued identity policy: %+v", total)
	}
}

func managedActivationClosed(t *testing.T, flow net.Conn) {
	t.Helper()
	_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := flow.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
		t.Fatalf("removed credential retained its existing stream: %v", err)
	}
}

func managedActivationVLESS(t *testing.T, port, target int, id string) net.Conn {
	t.Helper()
	flow, err := dialManagedActivationVLESS(t, port, target, id)
	if err != nil {
		t.Fatal(err)
	}
	return flow
}

func dialManagedActivationVLESS(t *testing.T, port, target int, id string) (net.Conn, error) {
	t.Helper()
	user, err := (&protocol.User{Account: serial.ToTypedMessage(&vless.Account{Id: id})}).ToMemoryUser()
	if err != nil {
		return nil, err
	}
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = flow.Close() })
	_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
	request := &protocol.RequestHeader{User: user, Command: protocol.RequestCommandTCP, Address: xnet.LocalHostIP, Port: xnet.Port(target)}
	if err := vlessencoding.EncodeRequestHeader(flow, request, &vlessencoding.Addons{}); err != nil {
		return flow, err
	}
	if _, err := flow.Write([]byte("data")); err != nil {
		return flow, err
	}
	if _, err := vlessencoding.DecodeResponseHeader(flow, request); err != nil {
		return flow, err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(flow, reply); err != nil {
		return flow, err
	}
	if string(reply) != "data" {
		return flow, fmt.Errorf("authenticated managed payload changed: %q", reply)
	}
	return flow, nil
}

func TestClientPolicyRestartActivatesNormalService(t *testing.T) {
	svc, inbound, owner, _ := setupManagedActivationService(t)
	db, port := database.GetDB(), inbound.Port
	var priorEpoch uint64
	for iteration := range 2 {
		if err := svc.RestartXray(true); err != nil {
			t.Fatalf("ordinary managed restart: %v", err)
		}
		process := currentXrayProcess()
		if process == nil || !process.IsControlReady() {
			t.Fatal("ordinary restart left managed core unready")
		}
		var state conf.ClientPolicyConfig
		if err := json.Unmarshal(process.GetConfig().ClientPolicy, &state); err != nil {
			t.Fatal(err)
		}
		endpoint, err := process.GetAPIEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		api, err := xray.DialClientPolicy(ctx, endpoint, state.InstanceID)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if api.Capabilities().Epoch <= priorEpoch {
			t.Fatal("restart did not reopen persistent state")
		}
		priorEpoch = api.Capabilities().Epoch
		live, err := api.GetClient(ctx, owner.StableID)
		_ = api.Close()
		cancel()
		if err != nil || live.Usage.BilledBytes != uint64(300+16*iteration) {
			t.Fatalf("ordinary startup lost historical billing: %+v %v", live, err)
		}
		if iteration == 1 {
			if err := db.First(inbound, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			var edited map[string]json.RawMessage
			if err := json.Unmarshal([]byte(inbound.Settings), &edited); err != nil {
				t.Fatal(err)
			}
			edited["network"] = json.RawMessage(`"tcp,udp"`)
			updated, err := json.Marshal(edited)
			if err != nil {
				t.Fatal(err)
			}
			inbound.Settings = string(updated)
			if _, _, err := (&InboundService{}).UpdateInbound(inbound); err != nil {
				t.Fatalf("ordinary managed inbound edit: %v", err)
			}
			if currentXrayProcess() != process {
				t.Fatal("editing one managed listener restarted the entire core")
			}
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 4)
		if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "data" {
			t.Fatalf("ordinary managed echo: %q %v", reply, err)
		}
		_ = conn.Close()
		if _, _, err := svc.GetXrayTraffic(); err != nil {
			t.Fatal(err)
		}
		total := policyLedgerTotal(t, owner.StableID)
		if total.RawUpload != int64(104+4*iteration) || total.RawDownload != int64(204+4*iteration) || total.BilledBytes != int64(316+16*iteration) {
			t.Fatalf("ordinary startup/polling lost identity or repriced history: %+v", total)
		}
	}
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	_ = flow.SetDeadline(time.Now().Add(time.Second))
	if _, err := flow.Write([]byte("warm")); err != nil {
		t.Fatal(err)
	}
	warm := make([]byte, 4)
	if _, err := io.ReadFull(flow, warm); err != nil || string(warm) != "warm" {
		t.Fatalf("owner deletion fixture did not establish a real stream: %q %v", warm, err)
	}
	injected := errors.New("owner disable transaction failed")
	if err := db.Callback().Update().Before("gorm:update").Register("test:owner-disable-failure", func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]any); ok && tx.Statement.Table == "inbounds" && values["enable"] == false {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, deleteErr := (&ClientService{}).Delete(&InboundService{}, owner.Id, true)
	if err := db.Callback().Update().Remove("test:owner-disable-failure"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(deleteErr, injected) {
		t.Fatalf("owner removal did not report its transaction failure: %v", deleteErr)
	}
	if err := db.First(inbound, inbound.Id).Error; err != nil || !inbound.Enable || len(linksOf(t, inbound.Id)) != 1 {
		t.Fatalf("failed owner removal partially changed the resource: %+v %v", inbound, err)
	}
	if _, err := flow.Write([]byte("stay")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(flow, warm); err != nil || string(warm) != "stay" {
		t.Fatalf("failed owner removal changed the live stream: %q %v", warm, err)
	}
	if restart, err := (&ClientService{}).Delete(&InboundService{}, owner.Id, true); err != nil {
		t.Fatal(err)
	} else if restart {
		svc.SetToNeedRestart()
		svc.ApplyPendingRestart()
	}
	_ = flow.SetDeadline(time.Now().Add(time.Second))
	_, _ = flow.Write([]byte("gone"))
	var timed net.Error
	if _, err := flow.Read(make([]byte, 4)); err == nil {
		t.Fatal("deleted Tunnel owner retained a live forwarding stream")
	} else if errors.As(err, &timed) && timed.Timeout() {
		t.Fatal("deleted Tunnel owner stream was not closed")
	}
	if err := db.First(inbound, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	if inbound.Enable {
		t.Fatal("removing the owner left an enabled unowned Tunnel resource")
	}
	if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 116 || total.RawDownload != 216 || total.BilledBytes != 364 {
		t.Fatalf("owner deletion lost committed lifetime usage: %+v", total)
	}
}

func setupManagedActivationService(t *testing.T) (*XrayService, *model.Inbound, *model.ClientRecord, int) {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	StartTrafficWriter()
	t.Cleanup(StopTrafficWriter)
	policyConfigTemplate(t)
	dir, err := os.MkdirTemp("", "policy-restart-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_DB_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	settings := &SettingService{}
	template, err := settings.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var templateConfig map[string]json.RawMessage
	if err := json.Unmarshal([]byte(template), &templateConfig); err != nil {
		t.Fatal(err)
	}
	templateConfig["clientPolicy"] = json.RawMessage(`{}`)
	raw, err := json.Marshal(templateConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.saveSetting("xrayTemplateConfig", string(raw)); err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	owner := model.ClientRecord{Email: "ordinary-managed", SubID: "ordinary-managed-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	db := database.GetDB()
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	inbound := mkInbound(t, port, model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target.Addr().(*net.TCPAddr).Port))
	if _, err := (&ClientService{}).Attach(&InboundService{}, owner.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	restore := SetXrayProcessForTest(nil)
	previousManager := panelruntime.GetManager()
	svc := &XrayService{}
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{APIEndpoint: svc.GetXrayAPIEndpoint, SetNeedRestart: svc.SetToNeedRestart, ManagedChange: svc.ReconcileManagedChange}))
	t.Cleanup(func() {
		if process := currentXrayProcess(); process != nil {
			_ = process.Stop()
		}
		restore()
		panelruntime.SetManager(previousManager)
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	return svc, inbound, &owner, target.Addr().(*net.TCPAddr).Port
}

func TestClientPolicyDisableWithCompilerFailureStopsExistingAccess(t *testing.T) {
	svc, tunnel, owner, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	managedActivationEcho(t, flow, "warm")
	db := database.GetDB()
	injected := errors.New("candidate inbound read failed")
	const callback = "test:fail-managed-candidate-inbounds"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "inbounds" && tx.Statement.Clauses["WHERE"].Expression == nil {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	changed := owner.ToClient()
	changed.Enable = false
	_, updateErr := (&ClientService{}).Update(&InboundService{}, owner.Id, *changed, 0)
	if !errors.Is(updateErr, injected) {
		t.Fatalf("compiler failure not reported: %v", updateErr)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, owner.Email)
	if err != nil || stored.Enable {
		t.Fatalf("disable was not committed before compilation failed: %+v %v", stored, err)
	}
	managedActivationClosed(t, flow)
	if process.IsRunning() {
		t.Fatal("failed compilation left outdated permissions active")
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 104 || total.RawDownload != 204 || total.BilledBytes != 316 {
		t.Fatalf("compiler failure recovery changed lifetime usage: %+v", total)
	}
	var state conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Policies) != 1 || state.Policies[0].Enabled {
		t.Fatal("recovery restored revoked access")
	}
}

func TestClientPolicyCollectorPreservesOperationalCountersAndLedger(t *testing.T) {
	svc, _, _, target := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	inbound := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	cs := &ClientService{}
	client := model.Client{Email: "collector-owner", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", SubID: "collector-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if _, err := cs.Create(&InboundService{}, &ClientCreatePayload{Client: client, InboundIds: []int{inbound.Id}}); err != nil {
		t.Fatal(err)
	}
	managedActivationVLESS(t, port, target, client.ID)
	record, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	for poll := range 2 {
		settlement, err := svc.CollectAndSettleTraffic()
		if err != nil {
			t.Fatal(err)
		}
		if poll == 0 {
			found := false
			for _, raw := range settlement.ClientTraffics {
				if raw.Email == client.Email && raw.Up == 4 && raw.Down == 4 {
					found = true
				}
			}
			if !found {
				t.Fatalf("fixture did not produce legacy payload counters: %+v", settlement.ClientTraffics)
			}
		}
		var row xray.ClientTraffic
		if err := database.GetDB().Where("email = ?", client.Email).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Up != 4 || row.Down != 4 {
			t.Fatalf("managed polling lost or duplicated operational counters: %d/%d", row.Up, row.Down)
		}
		var receipt model.ClientPolicyReceipt
		if err := database.GetDB().Where("client_id = ?", record.StableID).First(&receipt).Error; err != nil {
			t.Fatal(err)
		}
		if receipt.SeedUpload != 0 || receipt.SeedDownload != 0 || receipt.SeedBilled != 0 {
			t.Fatal("operational stats changed the captured migration seed")
		}
		if total := policyLedgerTotal(t, record.StableID); total.RawUpload != 4 || total.RawDownload != 4 || total.BilledBytes != 16 {
			t.Fatalf("managed settlement mismatch: %+v", total)
		}
	}
}
