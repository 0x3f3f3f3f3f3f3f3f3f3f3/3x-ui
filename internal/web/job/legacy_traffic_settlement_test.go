package job

import (
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

	"github.com/op/go-logging"
	statscommand "github.com/xtls/xray-core/app/stats/command"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/vless"
	vlessencoding "github.com/xtls/xray-core/proxy/vless/encoding"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestXrayTrafficJobRetriesUncommittedCountersAtomically(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the custom core")
	}
	for _, failedTable := range []string{"inbounds", "client_traffics", "outbound_traffics", "legacy_traffic_receipts", "first_poll", "new_job", "commit_ack", "commit_ack_growth"} {
		t.Run(failedTable, func(t *testing.T) {
			xuilogger.InitLogger(logging.ERROR)
			cleanup, err := testpg.IsolatePackage("traffic_settlement_" + t.Name())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			dir, err := os.MkdirTemp("", "legacy-settlement-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			t.Setenv("XUI_BIN_FOLDER", dir)
			t.Setenv("XUI_LOG_FOLDER", dir)
			t.Setenv("XUI_DB_FOLDER", dir)
			dbtest.InitDB(t, filepath.Join(dir, "panel.db"))
			service.StartTrafficWriter()
			t.Cleanup(service.StopTrafficWriter)
			if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
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
			probe, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			const id = "936997e1-3b0c-4de9-9eea-047ee5829d3e"
			const email = "legacy-settlement"
			settings := fmt.Sprintf(`{"decryption":"none","clients":[{"id":%q,"email":%q,"level":0}]}`, id, email)
			db := database.GetDB()
			inbound := model.Inbound{Tag: "settle-in", Protocol: model.VLESS, Listen: "127.0.0.1", Port: port, Enable: true, Settings: settings}
			client := model.ClientRecord{Email: email, SubID: "settlement-sub", UUID: id, Enable: true}
			if err := db.Create(&inbound).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&client).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: email, InboundId: inbound.Id, Enable: true, Up: 100, Down: 200}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.OutboundTraffics{Tag: "settle-out", Up: 300, Down: 400, Total: 700}).Error; err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["HandlerService","StatsService"]},"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}},"system":{"statsInboundUplink":true,"statsInboundDownlink":true,"statsOutboundUplink":true,"statsOutboundDownlink":true}},"inbounds":[{"tag":"settle-in","listen":"127.0.0.1","port":%d,"protocol":"vless","settings":%s}],"outbounds":[{"tag":"settle-out","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, filepath.Join(dir, "control.sock"), port, settings)
			var cfg xray.Config
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatal(err)
			}
			process := xray.NewTestProcess(&cfg, filepath.Join(dir, "config.json"))
			t.Cleanup(service.SetXrayProcessForTest(process))
			t.Cleanup(func() { _ = process.Stop() })
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			readyBy := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "control.sock")); err == nil {
					if err := os.Chmod(filepath.Join(dir, "control.sock"), 0o600); err != nil {
						t.Fatal(err)
					}
					if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
						_ = conn.Close()
						break
					}
				}
				if time.Now().After(readyBy) {
					t.Fatalf("legacy listener did not become ready: running=%t", process.IsRunning())
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Prime the existing polling cursor before any business payload.
			job := NewXrayTrafficJob()
			if failedTable != "first_poll" {
				job.Run()
			}
			flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = flow.Close() })
			_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
			user, err := (&protocol.User{Account: serial.ToTypedMessage(&vless.Account{Id: id})}).ToMemoryUser()
			if err != nil {
				t.Fatal(err)
			}
			request := &protocol.RequestHeader{User: user, Command: protocol.RequestCommandTCP, Address: xnet.LocalHostIP, Port: xnet.Port(target.Addr().(*net.TCPAddr).Port)}
			if err := vlessencoding.EncodeRequestHeader(flow, request, &vlessencoding.Addons{}); err != nil {
				t.Fatal(err)
			}
			if _, err := flow.Write([]byte("data")); err != nil {
				t.Fatal(err)
			}
			if _, err := vlessencoding.DecodeResponseHeader(flow, request); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, 4)
			if _, err := io.ReadFull(flow, reply); err != nil || string(reply) != "data" {
				t.Fatalf("echo: %q %v", reply, err)
			}
			var api xray.XrayAPI
			if err := api.InitProcess(process); err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			readCounters := func(payloadBytes int64) map[string]int64 {
				var counters map[string]int64
				deadline := time.Now().Add(2 * time.Second)
				for {
					page, err := (*api.StatsServiceClient).QueryStats(context.Background(), &statscommand.QueryStatsRequest{Reset_: false})
					if err != nil {
						t.Fatal(err)
					}
					counters = make(map[string]int64)
					for _, stat := range page.Stat {
						counters[stat.Name] = stat.Value
					}
					// This IPv4 VLESS request has a 26-byte header and a 2-byte response header.
					// Network counters settle after user counters, so wait for all accounting layers.
					if counters["user>>>"+email+">>>traffic>>>uplink"] == payloadBytes &&
						counters["user>>>"+email+">>>traffic>>>downlink"] == payloadBytes &&
						counters["inbound>>>settle-in>>>traffic>>>uplink"] == payloadBytes+26 &&
						counters["inbound>>>settle-in>>>traffic>>>downlink"] == payloadBytes+2 &&
						counters["outbound>>>settle-out>>>traffic>>>uplink"] == payloadBytes &&
						counters["outbound>>>settle-out>>>traffic>>>downlink"] == payloadBytes {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("payload counters did not settle: %v", counters)
					}
					time.Sleep(time.Millisecond)
				}
				return counters
			}
			payloadBytes := int64(4)
			counters := readCounters(payloadBytes)
			injected := errors.New("traffic settlement transaction failed")
			const callback = "test:traffic-settlement-failure"
			register := db.Callback().Update().Before("gorm:update").Register
			unregister := db.Callback().Update().Remove
			if failedTable == "client_traffics" {
				register = db.Callback().Raw().Before("gorm:raw").Register
				unregister = db.Callback().Raw().Remove
			}
			if err := register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == failedTable || failedTable == "client_traffics" && strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE client_traffics SET up =") {
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unregister(callback) })
			if failedTable == "new_job" {
				job = NewXrayTrafficJob()
			}
			if strings.HasPrefix(failedTable, "commit_ack") {
				loseNextTrafficCommitAcknowledgement(t, db)
			}
			job.Run()
			check := func(applied bool) {
				t.Helper()
				var stored xray.ClientTraffic
				var in model.Inbound
				var out model.OutboundTraffics
				if err := db.Where("email = ?", email).First(&stored).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.First(&in, inbound.Id).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("tag = ?", "settle-out").First(&out).Error; err != nil {
					t.Fatal(err)
				}
				up, down, iu, idn, ou, od := int64(100), int64(200), int64(0), int64(0), int64(300), int64(400)
				if applied {
					up += payloadBytes
					down += payloadBytes
					iu = counters["inbound>>>settle-in>>>traffic>>>uplink"]
					idn = counters["inbound>>>settle-in>>>traffic>>>downlink"]
					ou += counters["outbound>>>settle-out>>>traffic>>>uplink"]
					od += counters["outbound>>>settle-out>>>traffic>>>downlink"]
				}
				if stored.Up != up || stored.Down != down || in.Up != iu || in.Down != idn || out.Up != ou || out.Down != od {
					t.Errorf("settlement applied=%t: client=%d/%d inbound=%d/%d outbound=%d/%d; want %d/%d %d/%d %d/%d", applied, stored.Up, stored.Down, in.Up, in.Down, out.Up, out.Down, up, down, iu, idn, ou, od)
				}
			}
			check(failedTable == "first_poll" || failedTable == "new_job" || strings.HasPrefix(failedTable, "commit_ack"))
			if err := unregister(callback); err != nil {
				t.Fatal(err)
			}
			if failedTable == "commit_ack_growth" {
				_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := flow.Write([]byte("more")); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(flow, reply); err != nil || string(reply) != "more" {
					t.Fatalf("second echo: %q %v", reply, err)
				}
				_ = readCounters(8)
			}

			job.Run()
			check(true)
			if failedTable == "new_job" {
				job = NewXrayTrafficJob()
			}
			if failedTable == "commit_ack_growth" {
				payloadBytes = 8
				counters = readCounters(payloadBytes)
			}

			job.Run()
			check(true)
		})
	}
}
