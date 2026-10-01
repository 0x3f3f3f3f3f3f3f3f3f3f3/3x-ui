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

func TestLegacyUnassignedTrafficRealCollectorRetryAndGrowth(t *testing.T) {
	legacyRealCollectorRetryAndGrowth(t, false)
}

func TestLegacyTrafficConfigProofRealCollectorRetryAndGrowth(t *testing.T) {
	legacyRealCollectorRetryAndGrowth(t, true)
}

func legacyRealCollectorRetryAndGrowth(t *testing.T, checkProof bool) {
	t.Helper()
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		for _, failure := range []string{"bucket-write", "commit-ack"} {
			t.Run(string(protocol)+"/"+failure, func(t *testing.T) {
				// Legacy splice publishes download counters only when the copy ends.
				// This fixture verifies polling and growth while the flows remain open.
				t.Setenv("XRAY_BUF_SPLICE", "disable")
				svc, tunnel, owner, _ := setupManagedActivationService(t)
				target, received := passwordRemovalTarget(t, false)
				targetPort := target.Addr().(*net.TCPAddr).Port
				probe, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := probe.Addr().(*net.TCPAddr).Port
				_ = probe.Close()
				settings := passwordOwnerSettings(t, protocol,
					map[string]any{"user": "alice", "pass": "wire-secret"},
					map[string]any{"user": "ALICE", "pass": "alias-secret"},
					map[string]any{"user": "legacy\nusername", "pass": "newline-secret"},
					map[string]any{"user": "legacy\x00username", "pass": "nul-secret"})
				password := model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: port, Tag: "retained-password", Enable: true, Settings: settings}
				if err := database.GetDB().Create(&password).Error; err != nil {
					t.Fatal(err)
				}
				controlDir, err := os.MkdirTemp("", "retained-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(controlDir) })
				cfgPath := filepath.Join(controlDir, "legacy-retention.json")
				if err := os.Chmod(filepath.Dir(cfgPath), 0o700); err != nil {
					t.Fatal(err)
				}
				raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"api","listen":%q,"services":["HandlerService","StatsService"]},"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}},"system":{"statsInboundUplink":true,"statsInboundDownlink":true,"statsOutboundUplink":true,"statsOutboundDownlink":true}},"inbounds":[{"tag":%q,"listen":"127.0.0.1","port":%d,"protocol":%q,"settings":%s},{"tag":%q,"listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"address":"127.0.0.1","port":%d,"network":"tcp","email":%q}}],"outbounds":[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, cfgPath+".sock", password.Tag, port, protocol, settings, tunnel.Tag, tunnel.Port, targetPort, owner.Email)
				var cfg xray.Config
				if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
					t.Fatal(err)
				}
				process := xray.NewTestProcess(&cfg, cfgPath)
				t.Cleanup(func() { _ = process.Stop() })
				if err := process.Start(); err != nil {
					t.Fatalf("legacy core startup: %v; result=%s", err, process.GetResult())
				}
				readyBy := time.Now().Add(3 * time.Second)
				for {
					if err := os.Chmod(cfgPath+".sock", 0o600); err == nil {
						break
					} else if time.Now().After(readyBy) {
						t.Fatalf("legacy control socket unavailable: %v; result=%s", err, process.GetResult())
					}
					time.Sleep(10 * time.Millisecond)
				}
				xrayState.replace(process)
				mode := "http"
				if protocol == model.Mixed {
					mode = "socks"
				}
				dial := func(user, pass string) net.Conn {
					t.Helper()
					deadline := time.Now().Add(3 * time.Second)
					for {
						flow, err := passwordConfigDial(mode, port, target.Addr().String(), user, pass)
						if err == nil {
							t.Cleanup(func() { _ = flow.Close() })
							return flow
						}
						if time.Now().After(deadline) {
							t.Fatal(err)
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				first, alias := dial("alice", "wire-secret"), dial("ALICE", "alias-secret")
				multiline := dial("legacy\nusername", "newline-secret")
				nul := dial("legacy\x00username", "nul-secret")
				known, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = known.Close() })
				for _, flow := range []net.Conn{first, alias, multiline, nul, known} {
					managedActivationEcho(t, flow, "hello!")
				}
				injected := errors.New("legacy retention response unavailable")
				var sourceID, pendingID string
				startupProof := process.NativeTrafficConfigProof()
				if checkProof && (startupProof == nil || !startupProof.ConfigStable) {
					t.Fatal("real legacy child lacks stable startup evidence before collection")
				}
				settle := func(batch *xray.TrafficBatch) error {
					if checkProof {
						sourceID, pendingID = batch.ProcessID, batch.ID
						if batch.ConfigProof == nil || *batch.ConfigProof != *startupProof {
							t.Fatal("actual first native batch lost startup evidence")
						}
					}
					return svc.settleLegacyTrafficBatch(batch)
				}
				if failure == "bucket-write" {
					const hook = "test-retained-real-bucket-failure"
					if err := database.GetDB().Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
						if tx.Statement.Table == "legacy_unassigned_traffics" {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = database.GetDB().Callback().Create().Remove(hook) })
					var err error
					if checkProof {
						_, _, err = process.SettleTraffic(settle)
					} else {
						_, err = svc.CollectAndSettleTraffic()
					}
					if !errors.Is(err, injected) {
						t.Fatalf("real late write failure=%v", err)
					}
					if checkProof {
						var count int64
						if err := database.GetDB().Model(&model.LegacyTrafficConfigSource{}).Count(&count).Error; err != nil || count != 0 {
							t.Fatalf("real failed bucket write committed startup evidence: %d/%v", count, err)
						}
					}
					if history := trafficOf(t, owner.Email); history.Up != 100 || history.Down != 200 {
						t.Fatalf("failed bucket write committed known usage: %+v", history)
					}
					_ = database.GetDB().Callback().Create().Remove(hook)
				} else {
					if _, _, err := process.SettleTraffic(func(batch *xray.TrafficBatch) error {
						if err := settle(batch); err != nil {
							return err
						}
						return injected
					}); !errors.Is(err, injected) {
						t.Fatalf("committed lost response=%v", err)
					}
				}
				if checkProof {
					changed := *process.GetConfig()
					changed.LogConfig = []byte(`{"loglevel":"warning"}`)
					process.SetConfig(&changed)
					if _, _, err := process.SettleTraffic(func(batch *xray.TrafficBatch) error {
						if batch.ID != pendingID || batch.ConfigProof == nil || *batch.ConfigProof != *startupProof {
							t.Fatal("SQL retry replaced immutable proof with changed current configuration")
						}
						return svc.settleLegacyTrafficBatch(batch)
					}); err != nil {
						t.Fatal(err)
					}
					if source := configSourceOf(t, sourceID); !source.ConfigStable || source.ConfigDigest != startupProof.ConfigDigest || source.EffectiveConfigDigest != startupProof.EffectiveConfigDigest {
						t.Fatalf("real original retry lost its captured evidence: %+v", source)
					}
				}
				managedActivationEcho(t, first, "growth")
				for range 3 {
					if _, err := svc.CollectAndSettleTraffic(); err != nil {
						t.Fatal(err)
					}
				}
				if checkProof {
					if source := configSourceOf(t, sourceID); source.ConfigStable || source.ConfigDigest != startupProof.ConfigDigest || source.EffectiveConfigDigest != startupProof.EffectiveConfigDigest {
						t.Fatalf("new real poll erased drift or changed startup identity: %+v", source)
					}
				}
				var retained []model.LegacyUnassignedTraffic
				if err := database.GetDB().Order("label").Find(&retained).Error; err != nil {
					t.Fatal(err)
				}
				want := map[string]int64{"alice": 12, "ALICE": 6, "legacy\nusername": 6, "legacy\x00username": 6}
				if len(retained) != 4 || received.Load() != 36 {
					t.Fatalf("real label/target conservation: %+v target=%d", retained, received.Load())
				}
				for _, row := range retained {
					if row.SourceMode != "legacy" || row.SourceInstanceID != "" || row.RawUpload != want[row.Label] || row.RawDownload != want[row.Label] {
						t.Fatalf("lost or replayed retained native usage: %+v", row)
					}
				}
				if history := trafficOf(t, owner.Email); history.Up != 106 || history.Down != 206 {
					t.Fatalf("aliases were assigned to same-IP known client: %+v", history)
				}
				var totals int64
				if err := database.GetDB().Model(&model.ClientPolicyTotal{}).Count(&totals).Error; err != nil || totals != 0 {
					t.Fatalf("retention invented managed owner billing: %d/%v", totals, err)
				}
			})
		}
	}
}
