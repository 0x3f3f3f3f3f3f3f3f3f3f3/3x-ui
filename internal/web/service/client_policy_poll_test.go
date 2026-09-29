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
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyPollingRetriesCommittedTraffic(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	setupPolicyLedgerDB(t)
	dir, err := os.MkdirTemp("", "policy-poll-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	client := model.ClientRecord{Email: "poll-owner", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	others := make([]model.ClientRecord, 1000)
	for i := range others {
		others[i] = model.ClientRecord{Email: fmt.Sprintf("idle-poll-%d", i), Enable: true}
	}
	if err := db.CreateInBatches(others, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, Up: 100, Down: 200}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := EnsureLocalClientPolicyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	state.Policies, err = PrepareClientPolicies([]string{client.StableID})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(others))
	for i := range others {
		ids[i] = others[i].StableID
	}
	idlePolicies, err := PrepareClientPolicies(ids)
	if err != nil {
		t.Fatal(err)
	}
	state.Policies = append(state.Policies, idlePolicies...)
	policyJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"stats":{},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService","StatsService"]},"clientPolicy":%s,"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, filepath.Join(dir, "control.sock"), policyJSON, port, target.Addr().(*net.TCPAddr).Port, client.StableID)
	var config xray.Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	process := xray.NewTestProcess(&config, filepath.Join(dir, "poll.json"))
	defer process.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
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
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := bytes.Repeat([]byte{0x61}, 1024)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("managed echo: %v", err)
	}
	before := policyLedgerTotal(t, client.StableID)
	injected := errors.New("injected poll commit failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:poll-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_sources" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, _, pollErr := (&XrayService{}).GetXrayTraffic()
	if err := db.Callback().Update().Remove("test:poll-failure"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(pollErr, injected) {
		t.Fatalf("traffic poll did not report durable ledger failure: %v", pollErr)
	}
	if got := policyLedgerTotal(t, client.StableID); got != before {
		t.Fatalf("failed poll partially committed usage: %+v", got)
	}
	if cursor, err := ClientPolicyLedgerCursor(state.InstanceID); err != nil || cursor != 0 {
		t.Fatalf("failed poll advanced cursor: %d, %v", cursor, err)
	}
	for range 2 {
		if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
			t.Fatal(err)
		}
		got := policyLedgerTotal(t, client.StableID)
		if got.RawUpload != 1124 || got.RawDownload != 1224 || got.BilledBytes != 4396 || got.UncertainBytes != 0 {
			t.Fatalf("poll retry lost or duplicated durable usage: %+v", got)
		}
	}
	var settled int64
	if err := db.Model(&model.ClientPolicyReceipt{}).Where("sequence > 0").Count(&settled).Error; err != nil || settled != 1001 {
		t.Fatalf("poll stopped at its first page: %d settled clients, %v", settled, err)
	}
	after, err := ClientPolicyLedgerCursor(state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("second managed echo: %v", err)
	}
	api, err := xray.DialClientPolicy(ctx, filepath.Join(dir, "control.sock"), state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	reserved, err := api.ReadLedger(ctx, after, 1000)
	if err != nil || len(reserved.Records) != 1 || reserved.Records[0].ReservedBytes == 0 {
		t.Fatalf("expected one pending reservation before checkpoint: %+v, %v", reserved, err)
	}
	if err := db.Model(&model.ClientPolicySource{}).Where("instance_id = ?", state.InstanceID).Update("sequence", reserved.NextSequence+1).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = (&XrayService{}).GetXrayTraffic()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("checkpoint concealed an ahead-of-core database cursor: %v", err)
	}
	if err := db.Model(&model.ClientPolicySource{}).Where("instance_id = ?", state.InstanceID).Update("sequence", after).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	if got := policyLedgerTotal(t, client.StableID); got.RawUpload != 2148 || got.RawDownload != 2248 || got.BilledBytes != 8492 {
		t.Fatalf("cursor rejection discarded recoverable traffic: %+v", got)
	}
}
