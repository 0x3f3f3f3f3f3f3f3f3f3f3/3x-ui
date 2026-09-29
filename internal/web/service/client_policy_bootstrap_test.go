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
	"runtime"
	"strings"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyRuntimeBootstrapPreservesLedgerAcrossChildRestarts(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	dir, err := os.MkdirTemp("", "runtime-bootstrap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	client := model.ClientRecord{Email: "bootstrap-owner", Enable: true, TotalGB: 100000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: client.Email, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{client.StableID})
	if err != nil {
		t.Fatal(err)
	}
	state.Policies = policies
	policyConfig, err := json.Marshal(state)
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
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	socket := filepath.Join(dir, "control.sock")
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":%s,"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, policyConfig, port, target.Addr().(*net.TCPAddr).Port, client.StableID)
	var config xray.Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
	for round := int64(1); round <= 2; round++ {
		process := xray.NewTestProcess(&config, filepath.Join(dir, "bootstrap.json"))
		t.Cleanup(func() { _ = process.Stop() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		if err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
			done := make(chan error, 1)
			go func() { done <- local.DelInbound(ctx, &model.Inbound{Tag: "not-ready", Protocol: model.Tunnel}) }()
			select {
			case err := <-done:
				if err == nil || err.Error() != "local xray is not running" {
					return nil, fmt.Errorf("unexpected concurrent runtime result: %w", err)
				}
			case <-time.After(2 * time.Second):
				return nil, fmt.Errorf("Runtime held its RPC mutex during database preparation")
			}
			return PrepareLocalClientPolicyBootstrap(caps, state)
		}); err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		payload := bytes.Repeat([]byte{0x43}, 1024)
		if _, err := conn.Write(payload); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		reply := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
			conn.Close()
			t.Fatalf("managed Runtime traffic: %v", err)
		}
		conn.Close()
		api, err := xray.DialClientPolicy(ctx, socket, state.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = api.Close() })
		if err := api.Checkpoint(ctx); err != nil {
			t.Fatal(err)
		}
		after, err := ClientPolicyLedgerCursor(state.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		page, err := api.ReadLedger(ctx, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := SettleClientPolicyLedger(state.InstanceID, api.Capabilities().Epoch, after, page); err != nil {
				t.Fatal(err)
			}
		}
		total := policyLedgerTotal(t, client.StableID)
		if total.RawUpload != 100+round*1024 || total.RawDownload != 200+round*1024 || total.BilledBytes != 300+round*4096 || total.UncertainBytes != 0 {
			t.Fatalf("bootstrap reset or double-counted usage: %+v", total)
		}
		api.Close()
		if err := process.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("database-policy-hot-update", func(t *testing.T) {
		process := xray.NewTestProcess(&config, filepath.Join(dir, "hot-policy.json"))
		t.Cleanup(func() { _ = process.Stop() })
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
			return PrepareLocalClientPolicyBootstrap(caps, state)
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
		svc := ClientService{}
		apply := func(edit func(*model.Client)) {
			t.Helper()
			record, err := svc.GetByID(client.Id)
			if err != nil {
				t.Fatal(err)
			}
			update := record.ToClient()
			edit(update)
			if _, err := svc.Update(&InboundService{}, client.Id, *update, 0); err != nil {
				t.Fatal(err)
			}
		}
		apply(func(c *model.Client) { c.TotalGB = 1000000; c.Policy.UploadBytesPerSecond = 1 })
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		echo := func(n int) error {
			payload := bytes.Repeat([]byte{0x62}, n)
			if _, err := conn.Write(payload); err != nil {
				return err
			}
			reply := make([]byte, n)
			if _, err := io.ReadFull(conn, reply); err != nil {
				return err
			}
			if !bytes.Equal(reply, payload) {
				return fmt.Errorf("echo payload changed")
			}
			return nil
		}
		if err := echo(65536); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- echo(1024) }()
		select {
		case err := <-done:
			t.Fatalf("upload limit did not hold the existing stream: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		updateDeadline := time.Now().Add(2 * time.Second)
		apply(func(c *model.Client) { c.Policy.UploadBytesPerSecond = 0; c.Policy.Multiplier = "0.5" })
		if time.Until(updateDeadline) <= 0 {
			t.Fatal("client edit exceeded the two-second policy update deadline")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Until(updateDeadline)):
			t.Fatal("rate update did not release the existing stream within two seconds")
		}
		api, err := xray.DialClientPolicy(ctx, socket, state.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		defer api.Close()
		if err := api.Checkpoint(ctx); err != nil {
			t.Fatal(err)
		}
		after, err := ClientPolicyLedgerCursor(state.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		page, err := api.ReadLedger(ctx, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		if err := SettleClientPolicyLedger(state.InstanceID, api.Capabilities().Epoch, after, page); err != nil {
			t.Fatal(err)
		}
		total := policyLedgerTotal(t, client.StableID)
		if total.RawUpload != 68708 || total.RawDownload != 68808 || total.BilledBytes != 271660 {
			t.Fatalf("hot update repriced history or duplicated payload: %+v", total)
		}
		disableDeadline := time.Now().Add(2 * time.Second)
		apply(func(c *model.Client) { c.Enable = false })
		_ = conn.SetReadDeadline(disableDeadline)
		var netErr net.Error
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("disabled stream remained readable")
		} else if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatal("disable did not close the existing stream")
		}
		apply(func(c *model.Client) { c.TotalGB = 2000000 })
		current, err := api.GetClient(ctx, client.StableID)
		if err != nil || current.Policy.Enabled || current.Policy.Version != 5 {
			t.Fatalf("quota change lifted manual disable: %+v, %v", current, err)
		}
		if err := process.Stop(); err != nil {
			t.Fatal(err)
		}
		var restoredState conf.ClientPolicyConfig
		if err := json.Unmarshal(process.GetConfig().ClientPolicy, &restoredState); err != nil {
			t.Fatal(err)
		}
		restarted := xray.NewTestProcess(process.GetConfig(), filepath.Join(dir, "hot-policy-restart.json"))
		t.Cleanup(func() { _ = restarted.Stop() })
		if err := local.StartManagedProcess(ctx, restarted, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
			return PrepareLocalClientPolicyBootstrap(caps, &restoredState)
		}); err != nil {
			t.Fatalf("restart lost acknowledged hot policies: %v", err)
		}
		runtime.GC()
		runtime.GC()
		recoveredAPI, err := xray.DialClientPolicy(ctx, socket, state.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		defer recoveredAPI.Close()
		recovered, err := recoveredAPI.GetClient(ctx, client.StableID)
		if err != nil || recovered.Policy.Version != 5 || recovered.Policy.Enabled || recovered.Usage.BilledBytes != 271660 {
			t.Fatalf("hot policies or usage lost across restart: %+v, %v", recovered, err)
		}
	})
	t.Run("core-behind-panel-cursor", func(t *testing.T) {
		before := policyLedgerTotal(t, client.StableID)
		if err := database.GetDB().Model(&model.ClientPolicySource{}).Where("instance_id = ?", state.InstanceID).Update("sequence", 1000000).Error; err != nil {
			t.Fatal(err)
		}
		process := xray.NewTestProcess(&config, filepath.Join(dir, "rollback-bootstrap.json"))
		t.Cleanup(func() { _ = process.Stop() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := local.StartManagedProcess(ctx, process, func(_ context.Context, caps *command.Capabilities) (*panelruntime.ManagedPolicyBootstrap, error) {
			return PrepareLocalClientPolicyBootstrap(caps, state)
		})
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "core is behind the panel ledger") {
			t.Fatalf("core ledger rollback was not rejected: %v", err)
		}
		if process.IsRunning() || process.IsControlReady() {
			t.Fatal("core ledger rollback left the child available")
		}
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
			conn.Close()
			t.Fatal("core ledger rollback reopened the business listener")
		}
		total := policyLedgerTotal(t, client.StableID)
		if total != before {
			t.Fatalf("rejected rollback changed committed totals: %+v", total)
		}
	})
}
