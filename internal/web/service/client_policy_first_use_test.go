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

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyFirstUseSettlementPreservesNewerEditsAndRetries(t *testing.T) {
	for _, mode := range []string{"normal", "rollback", "newer-edit"} {
		t.Run(mode, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			client := model.Client{Email: "first-use-settlement", ID: "11111111-1111-1111-1111-111111111111", Enable: true, ExpiryTime: -86400000, TotalGB: 9007199254740993}
			raw, err := json.Marshal(map[string]any{"clients": []model.Client{client}})
			if err != nil {
				t.Fatal(err)
			}
			inbound := mkInbound(t, 24220, model.VLESS, string(raw))
			if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			var record model.ClientRecord
			if err := db.First(&record, "email = ?", client.Email).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbound.Id, Enable: true, ExpiryTime: client.ExpiryTime, Total: client.TotalGB}).Error; err != nil {
				t.Fatal(err)
			}
			if err := BindClientPolicySource("local", "first-use-core", 1); err != nil {
				t.Fatal(err)
			}
			seed, err := PrepareClientPolicyLedger("first-use-core", record.StableID)
			if err != nil {
				t.Fatal(err)
			}
			policies, err := PrepareClientPolicies([]string{record.StableID})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "state.db")
			if err := clientpolicy.CreateStore(path, "first-use-core"); err != nil {
				t.Fatal(err)
			}
			engine, err := clientpolicy.OpenPersistentEngine(path, "first-use-core")
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if err := engine.Initialize(policies[0], clientpolicy.Usage{RawUpload: seed.RawUpload, RawDownload: seed.RawDownload, BilledBytes: seed.BilledBytes}); err != nil {
				t.Fatal(err)
			}
			session, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: record.StableID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if err := session.Admit(clientpolicy.Upload, 3); err != nil {
				t.Fatal(err)
			}
			if err := engine.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			events, err := engine.ReadLedger(0, 1000)
			if err != nil || len(events) != 1 || events[0].FirstUsedAt <= 0 {
				t.Fatalf("missing actual activation receipt: %+v %v", events, err)
			}
			event := events[0]
			page := &command.LedgerPage{NextSequence: event.Sequence, Records: []*command.LedgerRecord{{InstanceId: event.InstanceID, Epoch: event.Epoch, Sequence: event.Sequence, ClientId: event.ClientID, PolicyVersion: event.PolicyVersion, FirstUsedAt: event.FirstUsedAt, Usage: &command.Usage{RawUpload: event.Usage.RawUpload, RawDownload: event.Usage.RawDownload, BilledBytes: event.Usage.BilledBytes, Remainder: event.Usage.Remainder}}}}
			wantExpiry := event.FirstUsedAt + 86400000
			if err := db.Model(&record).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			client.Enable = false
			raw, err = json.Marshal(map[string]any{"clients": []model.Client{client}})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(inbound).Update("settings", string(raw)).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "newer-edit" {
				wantExpiry += 86400000
				if err := db.Model(&record).Update("expiry_time", wantExpiry).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("expiry_time", wantExpiry).Error; err != nil {
					t.Fatal(err)
				}
				client.ExpiryTime = wantExpiry
				client.Enable = false
				raw, err := json.Marshal(map[string]any{"clients": []model.Client{client}})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Model(inbound).Update("settings", string(raw)).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := PrepareClientPolicies([]string{record.StableID}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rollback" {
				injected := errors.New("first-use receipt commit rejected")
				if err := db.Callback().Update().Before("gorm:update").Register("test:first-use-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "client_policy_sources" {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				settleErr := SettleClientPolicyLedger(event.InstanceID, event.Epoch, 0, page)
				if err := db.Callback().Update().Remove("test:first-use-failure"); err != nil {
					t.Fatal(err)
				}
				if !errors.Is(settleErr, injected) {
					t.Fatalf("expected atomic activation failure: %v", settleErr)
				}
				if err := db.First(&record, record.Id).Error; err != nil {
					t.Fatal(err)
				}
				if record.ExpiryTime != -86400000 || record.DesiredPolicyVersion != 1 {
					t.Fatalf("failed receipt partially activated the client: %+v", record)
				}
			}
			for range 2 {
				if err := SettleClientPolicyLedger(event.InstanceID, event.Epoch, 0, page); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.First(&record, record.Id).Error; err != nil {
				t.Fatal(err)
			}
			traffic := trafficOf(t, client.Email)
			if record.ExpiryTime != wantExpiry || record.Enable || record.DesiredPolicyVersion != 2 || traffic.ExpiryTime != wantExpiry || traffic.Enable {
				t.Fatalf("first-use settlement failed to retain activation, manual disable or newer edit: record=%+v traffic=%+v", record, traffic)
			}
			if err := db.First(inbound, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			clients, err := ParseInboundSettingsClients(inbound.Settings)
			if err != nil || len(clients) != 1 || clients[0].ExpiryTime != wantExpiry || clients[0].TotalGB != 9007199254740993 || clients[0].Enable {
				t.Fatalf("activation corrupted attached settings: %s %v", inbound.Settings, err)
			}
			var stamp int64
			if err := db.Model(&model.ClientPolicyReceipt{}).Select("first_used_at").Where("client_id = ?", record.StableID).Scan(&stamp).Error; err != nil || stamp != event.FirstUsedAt {
				t.Fatalf("receipt lost durable first use: %d %v", stamp, err)
			}
			if total := policyLedgerTotal(t, record.StableID); total.RawUpload != 3 || total.BilledBytes != 3 {
				t.Fatalf("activation replay changed billing: %+v", total)
			}
		})
	}
}

func TestClientPolicyFirstUseCompilerPollingAndChildRestart(t *testing.T) {
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
	owner := model.ClientRecord{Email: "first-use-runtime", SubID: "first-use-sub", Enable: true, ExpiryTime: -2000, TotalGB: 10000}
	db := database.GetDB()
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Enable: true, ExpiryTime: owner.ExpiryTime, Total: owner.TotalGB}).Error; err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	inbound := mkInbound(t, probe.Addr().(*net.TCPAddr).Port, model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target.Addr().(*net.TCPAddr).Port))
	if _, err := (&ClientService{}).Attach(&InboundService{}, owner.Id, []int{inbound.Id}); err != nil {
		t.Fatal(err)
	}
	_ = probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var process *xray.Process
	defer func() {
		if process != nil {
			_ = process.Stop()
		}
	}()
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
	startChild := func() {
		t.Helper()
		cfg, err := (&XrayService{}).GetManagedXrayConfig(state)
		if err != nil {
			t.Fatal(err)
		}
		var generated conf.ClientPolicyConfig
		if err := json.Unmarshal(cfg.ClientPolicy, &generated); err != nil {
			t.Fatal(err)
		}
		process = xray.NewTestProcess(cfg, filepath.Join(dir, "first-use.json"))
		if err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
			return PrepareLocalClientPolicyBootstrap(caps, &generated)
		}); err != nil {
			t.Fatal(err)
		}
	}
	startChild()
	previousProcess, _ := xrayState.snapshot()
	previousManager := panelruntime.GetManager()
	xrayState.replace(process)
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{}))
	defer func() { xrayState.replace(previousProcess); panelruntime.SetManager(previousManager) }()
	dialAPI := func() *xray.ClientPolicyAPI {
		t.Helper()
		var apiConfig conf.APIConfig
		if err := json.Unmarshal(process.GetConfig().API, &apiConfig); err != nil {
			t.Fatal(err)
		}
		api, err := xray.DialClientPolicy(ctx, apiConfig.Listen, state.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		return api
	}
	api := dialAPI()
	unused, err := api.GetClient(ctx, owner.StableID)
	if err != nil || unused.FirstUsedAt != 0 || unused.Policy.ExpiresAt != -2000 {
		t.Fatalf("compiler prematurely activated first-use expiry: %+v, %v", unused, err)
	}
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	_ = flow.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := flow.Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(flow, reply); err != nil || string(reply) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("first-use child traffic: %x %v", reply, err)
	}
	used, err := api.GetClient(ctx, owner.StableID)
	if err != nil || used.FirstUsedAt <= 0 {
		t.Fatalf("child did not persist first use: %+v %v", used, err)
	}
	stamp := used.FirstUsedAt
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&owner, owner.Id).Error; err != nil {
		t.Fatal(err)
	}
	if owner.ExpiryTime != stamp+2000 || owner.DesiredPolicyVersion != 2 {
		t.Fatalf("polling lost activated expiry: %+v", owner)
	}
	acknowledged, err := api.GetClient(ctx, owner.StableID)
	if err != nil || acknowledged.Policy.Version != 2 || acknowledged.Policy.ExpiresAt != stamp+2000 || acknowledged.FirstUsedAt != stamp {
		t.Fatalf("polling did not acknowledge activated expiry: %+v %v", acknowledged, err)
	}
	_ = api.Close()
	if err := process.Stop(); err != nil {
		t.Fatal(err)
	}
	startChild()
	xrayState.replace(process)
	api = dialAPI()
	defer api.Close()
	if wait := time.Until(time.UnixMilli(stamp + 2100)); wait > 0 {
		time.Sleep(wait)
	}
	expired, err := api.GetClient(ctx, owner.StableID)
	if err != nil || expired.Reasons&uint32(clientpolicy.ReasonExpired) == 0 || expired.FirstUsedAt != stamp || expired.Usage.BilledBytes != 8 {
		t.Fatalf("child restart renewed first-use time or lost usage: %+v %v", expired, err)
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if total := policyLedgerTotal(t, owner.StableID); total.BilledBytes != 8 || total.RawUpload != 4 || total.RawDownload != 4 {
		t.Fatalf("first-use restart repriced traffic: %+v", total)
	}
}
