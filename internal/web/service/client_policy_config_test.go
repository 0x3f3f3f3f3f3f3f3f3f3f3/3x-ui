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
	"sort"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func policyConfigState(t *testing.T) *conf.ClientPolicyConfig {
	t.Helper()
	dir, err := os.MkdirTemp("", "policy-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return &conf.ClientPolicyConfig{InstanceID: "compiler-test", StateFile: filepath.Join(dir, "state.db")}
}

func TestClientPolicyConfigFeedsRealTunnelLedger(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	dir := filepath.Dir(policyConfigState(t).StateFile)
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	state, err := EnsureLocalClientPolicyState(dir)
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
	udpTarget, err := net.ListenPacket("udp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	go func() {
		payload := make([]byte, 1024)
		for {
			n, peer, err := udpTarget.ReadFrom(payload)
			if err != nil {
				return
			}
			_, _ = udpTarget.WriteTo(payload[:n], peer)
		}
	}()
	owner := model.ClientRecord{Email: "compiled-live-owner", SubID: "compiled-live", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := database.GetDB().Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: owner.Email, Enable: true, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	var probes []net.Listener
	var inbounds []*model.Inbound
	for range 2 {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		probes = append(probes, probe)
		inbound := mkInbound(t, probe.Addr().(*net.TCPAddr).Port, model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp,udp","clientId":"untrusted-template-value","clients":[]}`, target.Addr().(*net.TCPAddr).Port))
		if _, err := (&ClientService{}).Attach(&InboundService{}, owner.Id, []int{inbound.Id}); err != nil {
			t.Fatal(err)
		}
		inbounds = append(inbounds, inbound)
	}
	cfg, err := (&XrayService{}).GetManagedXrayConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	var generated conf.ClientPolicyConfig
	if err := json.Unmarshal(cfg.ClientPolicy, &generated); err != nil {
		t.Fatal(err)
	}
	for _, probe := range probes {
		_ = probe.Close()
	}
	process := xray.NewTestProcess(cfg, filepath.Join(dir, "compiled.json"))
	defer process.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
	if err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
		return PrepareLocalClientPolicyBootstrap(caps, &generated)
	}); err != nil {
		t.Fatal(err)
	}
	previousProcess, _ := xrayState.snapshot()
	previousManager := panelruntime.GetManager()
	xrayState.replace(process)
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{}))
	defer func() {
		xrayState.replace(previousProcess)
		panelruntime.SetManager(previousManager)
	}()
	for _, transfer := range []struct {
		network     string
		rule, count int
	}{{"tcp", 0, 55}, {"tcp", 1, 17}, {"udp", 0, 13}} {
		conn, err := net.DialTimeout(transfer.network, fmt.Sprintf("127.0.0.1:%d", inbounds[transfer.rule].Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		payload := bytes.Repeat([]byte{0x5a}, transfer.count)
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
			t.Fatalf("compiled %s rule %d did not echo its payload: %v", transfer.network, transfer.rule, err)
		}
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	total := policyLedgerTotal(t, owner.StableID)
	if total.RawUpload != 185 || total.RawDownload != 285 || total.BilledBytes != 640 {
		t.Fatalf("generated listener identity lost or duplicated historical/current usage: %+v", total)
	}
}

func policyConfigTemplate(t *testing.T) {
	t.Helper()
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"log":{"loglevel":"error"},"api":{"tag":"api","services":["HandlerService","StatsService","RoutingService"]},"inbounds":[{"tag":"api","listen":"127.0.0.1","port":62789,"protocol":"tunnel","settings":{"address":"127.0.0.1"}}],"outbounds":[{"protocol":"freedom","tag":"direct","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]},"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}}}}`); err != nil {
		t.Fatal(err)
	}
}

func TestClientPolicyConfigBindsAuthoritativeOwners(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	cs := &ClientService{}
	client := model.Client{
		Email: "compiled-owner@example.test", ID: "93f92da6-afd1-49d4-bdb1-7db92c576647", Enable: true, SubID: "compiled-owner", TotalGB: 10000,
		Policy: &model.ClientPolicyOptions{UploadBytesPerSecond: 12345, DownloadBytesPerSecond: 54321, Multiplier: "1.5"},
	}
	tunnel := mkInbound(t, 24101, model.Tunnel, `{"address":"127.0.0.1","port":9001,"network":"tcp,udp","clientId":"forged-owner","clients":[]}`)
	vless := mkInbound(t, 24102, model.VLESS, `{"clients":[],"decryption":"none"}`)
	for _, inbound := range []*model.Inbound{tunnel, vless} {
		if err := cs.SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
			t.Fatal(err)
		}
	}
	record, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	state := policyConfigState(t)
	cfg, err := (&XrayService{}).GetManagedXrayConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	var generated conf.ClientPolicyConfig
	if err := json.Unmarshal(cfg.ClientPolicy, &generated); err != nil {
		t.Fatal(err)
	}
	if len(generated.Policies) != 1 || generated.Policies[0].ClientID != record.StableID || generated.Policies[0].Multiplier != 1500000 || generated.Policies[0].UploadRate != 12345 || generated.Policies[0].DownloadRate != 54321 || generated.Policies[0].QuotaBytes != 10000 {
		t.Fatalf("generated policy differs from the authoritative account: %+v", generated.Policies)
	}
	if len(cfg.InboundConfigs) != 2 {
		t.Fatalf("legacy API listener was retained: %+v", cfg.InboundConfigs)
	}
	for _, inbound := range cfg.InboundConfigs {
		var settings struct {
			ClientID string `json:"clientId"`
			Email    string `json:"email"`
			Clients  []struct {
				ClientID string `json:"clientId"`
				Email    string `json:"email"`
				ID       string `json:"id"`
			} `json:"clients"`
		}
		if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
			t.Fatal(err)
		}
		switch inbound.Tag {
		case tunnel.Tag:
			if settings.ClientID != record.StableID || settings.Email != record.Email || len(settings.Clients) != 0 {
				t.Fatalf("Tunnel did not bind the stored listener owner: %+v", settings)
			}
		case vless.Tag:
			if len(settings.Clients) != 1 || settings.Clients[0].ClientID != record.StableID || settings.Clients[0].Email != record.Email || settings.Clients[0].ID != client.ID {
				t.Fatalf("authenticated account identity or credential changed: %+v", settings)
			}
		default:
			t.Fatalf("unexpected generated listener %q", inbound.Tag)
		}
	}
	var api conf.APIConfig
	if err := json.Unmarshal(cfg.API, &api); err != nil || api.Listen != filepath.Join(filepath.Dir(state.StateFile), "control.sock") {
		t.Fatalf("managed control is not private: %+v, %v", api, err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var coreConfig conf.Config
	if err := json.Unmarshal(raw, &coreConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := coreConfig.Build(); err != nil {
		t.Fatalf("generated candidate does not build against the managed core: %v", err)
	}
	var preserved model.Inbound
	if err := database.GetDB().First(&preserved, tunnel.Id).Error; err != nil || preserved.Settings != tunnel.Settings {
		t.Fatalf("candidate compilation rewrote the saved rule: %v", err)
	}
	if len(state.Policies) != 0 {
		t.Fatal("candidate compilation modified caller state")
	}
}

func TestClientPolicyConfigRejectsUnresolvedBusinessIdentity(t *testing.T) {
	for _, scenario := range []string{"unowned", "ambiguous", "unsupported", "remote-budget", "template-listener", "unconfigured-api-tag", "invalid-control-listener"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			inbound := mkInbound(t, 24103, model.Tunnel, `{"address":"127.0.0.1","port":9001,"network":"tcp"}`)
			owner := model.ClientRecord{Email: "candidate-owner@example.test", Enable: true}
			if err := database.GetDB().Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			if scenario != "unowned" {
				if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "ambiguous":
				other := model.ClientRecord{Email: "other-candidate@example.test", Enable: true}
				if err := database.GetDB().Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Create(&model.ClientInbound{ClientId: other.Id, InboundId: inbound.Id}).Error; err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				if err := database.GetDB().Model(inbound).Update("protocol", model.Hysteria).Error; err != nil {
					t.Fatal(err)
				}
			case "remote-budget":
				nodeID := 7
				remote := mkInbound(t, 24104, model.Tunnel, inbound.Settings)
				if err := database.GetDB().Model(remote).Update("node_id", nodeID).Error; err != nil {
					t.Fatal(err)
				}
				if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: remote.Id}).Error; err != nil {
					t.Fatal(err)
				}
			case "template-listener":
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"inbounds":[{"tag":"unknown-business","protocol":"tunnel","listen":"127.0.0.1","port":24105,"settings":{"address":"127.0.0.1","port":9001}}],"outbounds":[{"protocol":"freedom"}]}`); err != nil {
					t.Fatal(err)
				}
			case "unconfigured-api-tag":
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"inbounds":[{"tag":"api","protocol":"tunnel","listen":"127.0.0.1","port":24105,"settings":{"address":"127.0.0.1","port":9001}}],"outbounds":[{"protocol":"freedom"}]}`); err != nil {
					t.Fatal(err)
				}
			case "invalid-control-listener":
				if err := (&SettingService{}).saveSetting("xrayTemplateConfig", `{"api":{"tag":"api"},"inbounds":[{"tag":"api","protocol":"http","listen":"127.0.0.1","port":24105,"settings":{}}],"outbounds":[{"protocol":"freedom"}]}`); err != nil {
					t.Fatal(err)
				}
			}
			state := policyConfigState(t)
			cfg, err := (&XrayService{}).GetManagedXrayConfig(state)
			if cfg != nil || !errors.Is(err, xray.ErrClientPolicyCapability) {
				t.Fatalf("%s candidate was not rejected: config=%v err=%v", scenario, cfg != nil, err)
			}
			if err := database.GetDB().First(&owner, owner.Id).Error; err != nil || owner.DesiredPolicyVersion != 0 {
				t.Fatalf("rejected candidate changed desired policy version: %d, %v", owner.DesiredPolicyVersion, err)
			}
		})
	}
}

func TestClientPolicyConfigKeepsCredentialsBehindCoreRestrictions(t *testing.T) {
	for _, manualDisable := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual-disable-%t", manualDisable), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			policyConfigTemplate(t)
			client := model.Client{Email: "restricted-owner@example.test", ID: "34b02cc8-8318-4491-81d5-9aa23b3d261d", Enable: !manualDisable, TotalGB: 100}
			inbound := mkInbound(t, 24106, model.VLESS, `{"clients":[],"decryption":"none"}`)
			if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: client.Email, Enable: false, Up: 90, Down: 20, Total: 100}).Error; err != nil {
				t.Fatal(err)
			}
			cfg, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t))
			if err != nil {
				t.Fatal(err)
			}
			var settings struct {
				Clients []json.RawMessage `json:"clients"`
			}
			if len(cfg.InboundConfigs) != 1 {
				t.Fatalf("managed candidate listener count = %d", len(cfg.InboundConfigs))
			}
			if err := json.Unmarshal(cfg.InboundConfigs[0].Settings, &settings); err != nil || len(settings.Clients) != 1 {
				t.Fatalf("policy re-enable would require credential reinstallation: clients=%d err=%v", len(settings.Clients), err)
			}
			var policies conf.ClientPolicyConfig
			if err := json.Unmarshal(cfg.ClientPolicy, &policies); err != nil || len(policies.Policies) != 1 || policies.Policies[0].Enabled != !manualDisable || policies.Policies[0].QuotaBytes != 100 {
				t.Fatalf("retaining credentials removed restrictions: %+v, %v", policies, err)
			}
		})
	}
}

func TestClientPolicyConfigPreparesEveryPolicyBatch(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	inbound := mkInbound(t, 24107, model.VLESS, `{"clients":[],"decryption":"none"}`)
	records := make([]model.ClientRecord, 1001)
	for i := range records {
		records[i] = model.ClientRecord{Email: fmt.Sprintf("batch-owner-%d@example.test", i), UUID: fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1), Enable: true}
	}
	if err := database.GetDB().CreateInBatches(&records, 200).Error; err != nil {
		t.Fatal(err)
	}
	links := make([]model.ClientInbound, len(records))
	for i, record := range records {
		links[i] = model.ClientInbound{ClientId: record.Id, InboundId: inbound.Id}
	}
	if err := database.GetDB().CreateInBatches(links, 200).Error; err != nil {
		t.Fatal(err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StableID < records[j].StableID })
	last := records[len(records)-1]
	state := policyConfigState(t)
	started := time.Now()
	for version := uint64(1); version <= 2; version++ {
		if version == 2 {
			if err := database.GetDB().Model(&last).Update("policy_multiplier", "2").Error; err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := (&XrayService{}).GetManagedXrayConfig(state)
		if err != nil {
			t.Fatal(err)
		}
		var policies conf.ClientPolicyConfig
		if err := json.Unmarshal(cfg.ClientPolicy, &policies); err != nil || len(policies.Policies) != len(records) {
			t.Fatalf("candidate omitted a policy batch: count=%d err=%v", len(policies.Policies), err)
		}
		for i, policy := range policies.Policies {
			wantVersion, wantMultiplier := uint64(1), uint64(1000000)
			if policy.ClientID == last.StableID {
				wantVersion, wantMultiplier = version, version*1000000
			}
			if policy.ClientID != records[i].StableID || policy.Version != wantVersion || policy.Multiplier != wantMultiplier {
				t.Fatalf("batch-boundary policy lost its identity or version: %+v", policy)
			}
		}
	}
	t.Logf("two complete 1001-client candidate compilations: %s", time.Since(started))
}
