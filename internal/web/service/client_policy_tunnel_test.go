package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyLedgerRealTunnelAndRestart(t *testing.T) {
	setupPolicyLedgerDB(t)
	dir, err := os.MkdirTemp("", "panel-policy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained in-process tunnel fixture: %s", dir)
	state, socket := filepath.Join(dir, "state.db"), filepath.Join(dir, "core.sock")
	if err := clientpolicy.CreateStore(state, "core-a"); err != nil {
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
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := port.Addr().(*net.TCPAddr).Port
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	client := model.ClientRecord{Email: "legacy-tunnel", TotalGB: 100000, Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: client.Email, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"stateFile":%q,"instanceId":"core-a","policies":[]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, state, listen, target.Addr().(*net.TCPAddr).Port, client.StableID)
	var journal *policyauthority.Journal
	for round := uint64(1); round <= 2; round++ {
		var cfg conf.Config
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		pb, err := cfg.Build()
		if err != nil {
			t.Fatal(err)
		}
		instance, err := core.New(pb)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = instance.Close() })
		if err := instance.Start(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		api, err := xray.DialClientPolicy(ctx, socket, "core-a")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = api.Close() })
		caps := api.Capabilities()
		if caps.Epoch != round {
			t.Fatalf("core epoch=%d, want %d", caps.Epoch, round)
		}
		if err := BindClientPolicySource("local", caps.InstanceId, caps.Epoch); err != nil {
			t.Fatal(err)
		}
		seed, err := PrepareClientPolicyLedger(caps.InstanceId, client.StableID)
		if err != nil {
			t.Fatal(err)
		}
		policy := &clientpolicy.PolicyConfig{ClientId: client.StableID, Version: 1, Enabled: true, MultiplierMicros: 2000000, QuotaBytes: uint64(client.TotalGB), BurstBytes: 65536}
		if err := api.Initialize(ctx, policy, seed); err != nil {
			t.Fatal(err)
		}
		if journal == nil {
			journal = createServiceFixtureGrantJournal(t, []policyauthority.Seed{serviceFixtureGrantSeed(t, ctx, api, client.StableID)})
		}
		execution, err := newAuthorityExecution(ctx, database.GetDB(), journal, "local", api)
		if err != nil {
			t.Fatal(err)
		}
		grant := authorizeServiceFixtureGrant(t, ctx, execution, client.StableID, fmt.Sprintf("tunnel-round-%d", round), 8192)
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listen), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte{byte(round)}, 1024)
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
			t.Fatalf("tunnel round trip: %v", err)
		}
		if err := api.Checkpoint(ctx); err != nil {
			t.Fatal(err)
		}
		after, err := ClientPolicyLedgerCursor(caps.InstanceId)
		if err != nil {
			t.Fatal(err)
		}
		page, err := api.ReadLedger(ctx, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := SettleClientPolicyLedger(caps.InstanceId, caps.Epoch, after, page); err != nil {
				t.Fatal(err)
			}
		}
		total := policyLedgerTotal(t, client.StableID)
		if total.RawUpload != 100+int64(round)*1024 || total.RawDownload != 200+int64(round)*1024 || total.BilledBytes != 300+int64(round)*4096 || total.UncertainBytes != 0 {
			t.Fatalf("socket observation disagrees with durable panel ledger: %+v", total)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		if err := execution.Settle(ctx, grant.GrantID, true, false); err != nil {
			t.Fatalf("seal exact tunnel usage before child restart: %v", err)
		}
		account, err := journal.Account(client.StableID)
		if err != nil || account.Usage.RawUpload != uint64(100+int64(round)*1024) || account.Usage.RawDownload != uint64(200+int64(round)*1024) || account.Usage.BilledBytes != uint64(300+int64(round)*4096) || account.HeldCapacity != 0 {
			t.Fatalf("restricted grant disagrees with the independently checked ledger: %+v, %v", account, err)
		}
		if err := api.Close(); err != nil {
			t.Fatal(err)
		}
		if err := instance.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
