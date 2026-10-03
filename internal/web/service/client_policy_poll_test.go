package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
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
	t.Logf("retained polling fixture: %s", dir)
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
	grantAPI, err := xray.DialClientPolicy(ctx, filepath.Join(dir, "control.sock"), state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer grantAPI.Close()
	journal := createServiceFixtureGrantJournal(t, []policyauthority.Seed{serviceFixtureGrantSeed(t, ctx, grantAPI, client.StableID)})
	execution, err := newAuthorityExecution(ctx, db, journal, "local", grantAPI)
	if err != nil {
		t.Fatal(err)
	}
	grant := authorizeServiceFixtureGrant(t, ctx, execution, client.StableID, "two-poll-exchanges", 16384)
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
	if err := execution.Settle(ctx, grant.GrantID, true, false); err != nil {
		t.Fatalf("seal both admitted exchanges before reset-only checks: %v", err)
	}
	account, err := journal.Account(client.StableID)
	if err != nil || account.Usage.RawUpload != 2148 || account.Usage.RawDownload != 2248 || account.Usage.BilledBytes != 8492 || account.HeldCapacity != 0 {
		t.Fatalf("restricted grant charged capacity instead of exact admitted usage: %+v, %v", account, err)
	}
	lastID := state.Policies[len(state.Policies)-1].ClientID
	if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", lastID).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClientPolicyReset(state.InstanceID, lastID, "last-batch-reset"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	last, err := api.GetClient(ctx, lastID)
	if err != nil || last.Policy.Version != 2 || last.Policy.Enabled || last.Usage.BilledBytes != 0 {
		t.Fatalf("polling skipped the pending reset in its last policy batch: %+v, %v", last, err)
	}
	testManagedResetBatch(t, ctx, process, state.InstanceID, client.StableID, lastID, append(ids, client.StableID))
	testManagedScheduledResetRecovery(t, ctx, process, client.StableID, lastID)
}

func testManagedScheduledResetRecovery(t *testing.T, ctx context.Context, process *xray.Process, activeID, disabledID string) {
	t.Helper()
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("stable_id IN ?", []string{activeID, disabledID}).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().AddDate(0, 0, 2)
	now := time.Date(future.Year(), future.Month(), future.Day(), 0, 0, 0, 0, time.UTC)
	xrayState.replace(nil)
	olderErr := applyRestrictedPollingCalendar(t, ctx, "daily", now.AddDate(0, 0, -1))
	newerErr := applyRestrictedPollingCalendar(t, ctx, "daily", now)
	xrayState.replace(process)
	for _, err := range []error{olderErr, newerErr} {
		if err == nil || !strings.Contains(err.Error(), "managed core is not ready") {
			t.Fatalf("offline calendar reset did not retain its pending intent: %v", err)
		}
	}
	unconfigured := model.ClientRecord{Email: "calendar-unconfigured", Enable: true, TrafficReset: "weekly"}
	if err := db.Create(&unconfigured).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientPolicyTotal{ClientID: unconfigured.StableID}).Error; err != nil {
		t.Fatal(err)
	}
	var badOperation model.ClientTrafficResetBatch
	for i := 1; i <= 9; i++ {
		var err error
		badOperation, err = captureRestrictedPollingCalendar(ctx, "weekly", now.AddDate(0, 0, 7*i))
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
			t.Fatalf("unconfigured calendar member stopped ordinary traffic collection: %v", err)
		}
		// Public resumption correctly rejects this fixture's unowned projection.
		// Drive the same bounded scheduler with its restricted private pipeline;
		// malformed/unconfigured intents remain pending while collection works.
		_ = resumeScheduledTrafficResetsWithApplication(ctx, applyRestrictedPollingOperation)
	}
	var after int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&after).Error; err != nil || after != before+2 {
		t.Fatalf("restricted polling did not recover only the newest scheduled window: before=%d, after=%d, %v", before, after, err)
	}
	var pending int64
	if err := db.Model(&model.ClientTrafficResetBatch{}).Where("scheduled_at > 0 AND applied = ?", false).Count(&pending).Error; err != nil || pending != 9 {
		t.Fatalf("calendar recovery left stale pending work: %d, %v", pending, err)
	}
	if err := db.First(&badOperation, "request_id = ?", badOperation.RequestID).Error; err != nil || badOperation.Applied {
		t.Fatalf("recovery hid the unconfigured calendar failure: %+v, %v", badOperation, err)
	}
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		t.Fatal(err)
	}
	api, err := xray.DialClientPolicy(ctx, endpoint, config.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	state, err := api.GetClient(ctx, disabledID)
	if err != nil || state.Policy.Enabled {
		t.Fatalf("scheduled reset cleared manual disable: %+v, %v", state, err)
	}
}

type measuredResetRuntime struct {
	panelruntime.Runtime
	panelruntime.ManagedProcessRuntime
	checkpoints, applications int
	failApplyAt               int
	failure                   error
	beforeApply               func()
}

func (r *measuredResetRuntime) ReadManagedLedger(ctx context.Context, process *xray.Process, after uint64, checkpoint bool) (*command.Capabilities, *command.LedgerPage, error) {
	if checkpoint {
		r.checkpoints++
	}
	return r.ManagedProcessRuntime.ReadManagedLedger(ctx, process, after, checkpoint)
}

func (r *measuredResetRuntime) ApplyManagedPolicies(ctx context.Context, process *xray.Process, policies []clientpolicy.Policy) error {
	r.applications++
	if r.beforeApply != nil {
		r.beforeApply()
	}
	if r.applications == r.failApplyAt {
		return r.failure
	}
	return r.ManagedProcessRuntime.ApplyManagedPolicies(ctx, process, policies)
}

func testManagedResetBatch(t *testing.T, ctx context.Context, process *xray.Process, instanceID, activeID, disabledID string, ids []string) {
	t.Helper()
	manager := panelruntime.GetManager()
	measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime)}
	manager.SetLocalRuntimeOverride(measured)
	defer manager.SetLocalRuntimeOverride(nil)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("1 = 1").Update("total_gb", 9000).Error; err != nil {
		t.Fatal(err)
	}
	var priorResets int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&priorResets).Error; err != nil {
		t.Fatal(err)
	}
	if err := ResetLocalClientPolicies(ctx, ids, "unowned-direct-refusal"); !errors.Is(err, ErrAuthorityNotInitialized) || measured.checkpoints != 0 || measured.applications != 0 {
		t.Fatalf("unowned public direct reset reached the restricted core: checkpoints=%d, applies=%d, %v", measured.checkpoints, measured.applications, err)
	}
	var afterRefusal int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&afterRefusal).Error; err != nil || afterRefusal != priorResets {
		t.Fatalf("unowned public direct reset changed SQL resets: before=%d, after=%d, %v", priorResets, afterRefusal, err)
	}
	if err := applyRestrictedPollingDirectReset(ctx, append(slices.Clone(ids), "missing-client"), "unknown-member"); !errors.Is(err, clientpolicy.ErrUnknownClient) || measured.checkpoints != 0 {
		t.Fatalf("unknown batch member reached the data plane: checkpoints=%d, %v", measured.checkpoints, err)
	}
	injected := errors.New("last reset insert failed")
	insertions := 0
	if err := db.Callback().Create().Before("gorm:create").Register("test:batch-last-insert", func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_resets" {
			insertions++
			if insertions == 1001 {
				tx.AddError(injected)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	resetErr := applyRestrictedPollingDirectReset(ctx, ids, "all-clients")
	if err := db.Callback().Create().Remove("test:batch-last-insert"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(resetErr, injected) || measured.checkpoints != 1 || measured.applications != 0 {
		t.Fatalf("failed SQL batch escaped before commit: checkpoints=%d, applies=%d, error=%v", measured.checkpoints, measured.applications, resetErr)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("second SQL chunk failure committed earlier resets: count=%d, %v", count, err)
	}
	measured.checkpoints, measured.applications = 0, 0
	measured.failure, measured.failApplyAt = errors.New("second Runtime batch unavailable"), 2
	if err := applyRestrictedPollingDirectReset(ctx, ids, "all-clients"); !errors.Is(err, measured.failure) || measured.checkpoints != 1 || measured.applications != 2 {
		t.Fatalf("batch repeated checkpoints or concealed partial application: checkpoints=%d, applies=%d, error=%v", measured.checkpoints, measured.applications, err)
	}
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 1002 {
		t.Fatalf("Runtime failure lost committed retry boundaries: count=%d, %v", count, err)
	}
	if _, _, err := (&XrayService{}).GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	measured.checkpoints, measured.applications, measured.failApplyAt = 0, 0, 0
	if err := applyRestrictedPollingDirectReset(ctx, ids, "all-clients"); err != nil || measured.checkpoints != 1 {
		t.Fatalf("batch retry: checkpoints=%d, %v", measured.checkpoints, err)
	}
	if err := db.Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 1002 {
		t.Fatalf("batch retry created additional windows: count=%d, %v", count, err)
	}
	endpoint, err := process.GetAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	api, err := xray.DialClientPolicy(ctx, endpoint, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for _, id := range ids {
		state, err := api.GetClient(ctx, id)
		if err != nil || state.Policy.QuotaBytes != 9000 || state.Policy.Enabled != (id != disabledID) {
			t.Fatalf("batch member was omitted or enabled: %s, %+v, %v", id, state, err)
		}
		if id == activeID && (state.Policy.QuotaBaselineBytes != 8492 || state.Usage.BilledBytes != 8492) {
			t.Fatalf("batch lost the active client's committed boundary: %+v", state)
		}
	}
	legacy := model.ClientRecord{Email: "mixed-reset-legacy", UUID: "f540e6e3-4bab-461a-b1a0-9c403c2a4d98"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&legacy).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: legacy.Email, Up: 11, Down: 22, Enable: false}).Error; err != nil {
		t.Fatal(err)
	}
	var emails []string
	if err := db.Model(&model.ClientRecord{}).Pluck("email", &emails).Error; err != nil {
		t.Fatal(err)
	}
	measured.checkpoints, measured.applications, measured.failApplyAt = 0, 0, 2
	svc := &ClientService{}
	if _, err := svc.BulkResetTrafficWithRequest(ctx, &InboundService{}, emails, "unowned-public-refusal"); !errors.Is(err, ErrAuthorityNotInitialized) || measured.checkpoints != 0 || measured.applications != 0 {
		t.Fatalf("unowned public reset reached the restricted core: %v", err)
	}
	if row := trafficOf(t, legacy.Email); row.Up != 11 || row.Down != 22 || row.Enable {
		t.Fatalf("unowned public refusal changed legacy traffic: %+v", row)
	}
	var unowned model.ClientTrafficResetBatch
	rawTargets, err := json.Marshal([]clientResetTarget{{ClientID: legacy.StableID, Email: legacy.Email}})
	if err != nil {
		t.Fatal(err)
	}
	unowned = model.ClientTrafficResetBatch{RequestID: "unowned-application-refusal", Scope: "bulk", TargetsJSON: string(rawTargets), ManagedIDsJSON: "[]", InboundIDsJSON: "[]"}
	if _, _, err := svc.applyTrafficResetBatch(ctx, &InboundService{}, unowned); !errors.Is(err, ErrAuthorityNotInitialized) || measured.checkpoints != 0 || measured.applications != 0 {
		t.Fatalf("unowned application reached restricted core: %v", err)
	}
	if row := trafficOf(t, legacy.Email); row.Up != 11 || row.Down != 22 || row.Enable {
		t.Fatalf("unowned application changed legacy traffic: %+v", row)
	}
	if _, err := applyRestrictedPollingBatch(ctx, emails, "mixed-public"); !errors.Is(err, measured.failure) {
		t.Fatalf("restricted batch concealed Runtime failure: %v", err)
	}
	if err := db.First(&legacy, legacy.Id).Error; err != nil || !legacy.Enable {
		t.Fatalf("committed legacy reset lost its re-enable after another member's Runtime failure: %+v, %v", legacy, err)
	}
	if row := trafficOf(t, legacy.Email); row.Up != 0 || row.Down != 0 || !row.Enable {
		t.Fatalf("mixed batch lost the committed legacy reset: %+v", row)
	}
	if err := db.Model(&legacy).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", legacy.Email).Update("up", 13).Error; err != nil {
		t.Fatal(err)
	}
	if affected, err := applyRestrictedPollingBatch(ctx, emails, "mixed-public"); err != nil || affected != 1002 {
		t.Fatalf("restricted mixed retry lost original result: affected=%d, %v", affected, err)
	}
	if err := db.First(&legacy, legacy.Id).Error; err != nil || legacy.Enable {
		t.Fatalf("duplicate batch cleared a later manual disable: %+v, %v", legacy, err)
	}
	if row := trafficOf(t, legacy.Email); row.Up != 13 {
		t.Fatalf("duplicate mixed batch zeroed new legacy traffic: %+v", row)
	}
	var enabledAtCommit bool
	measured.beforeApply = func() {
		measured.beforeApply = nil
		if err := db.First(&legacy, legacy.Id).Error; err != nil {
			t.Fatal(err)
		}
		enabledAtCommit = legacy.Enable
		if err := db.Model(&legacy).Updates(map[string]any{"enable": false, "total_gb": 1700}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var active model.ClientRecord
	if err := db.First(&active, "stable_id = ?", activeID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := applyRestrictedPollingBatch(ctx, []string{legacy.Email, active.Email}, "disable-after-commit"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacy, legacy.Id).Error; err != nil || !enabledAtCommit || legacy.Enable || legacy.TotalGB != 1700 {
		t.Fatalf("post-commit reset overwrote a newer edit: enabledAtCommit=%v, record=%+v, %v", enabledAtCommit, legacy, err)
	}
	var original model.ClientRecord
	if err := db.First(&original, "stable_id = ?", activeID).Error; err != nil {
		t.Fatal(err)
	}
	oldEmail := original.Email
	xrayState.replace(nil)
	_, stoppedErr := applyRestrictedPollingBatch(ctx, []string{oldEmail}, "before-core-recovery")
	xrayState.replace(process)
	if stoppedErr == nil || !strings.Contains(stoppedErr.Error(), "managed core is not ready") {
		t.Fatalf("stopped batch did not stay pending: %v", stoppedErr)
	}
	if err := db.Model(&original).Update("email", "renamed-after-failure").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientRecord{Email: oldEmail}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", oldEmail).Updates(map[string]any{"up": 37, "down": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if affected, err := applyRestrictedPollingBatch(ctx, []string{oldEmail}, "before-core-recovery"); err != nil || affected != 1 {
		t.Fatalf("renamed pending member did not recover: affected=%d, %v", affected, err)
	}
	if row := trafficOf(t, oldEmail); row.Up != 37 {
		t.Fatalf("pending batch rebound a recycled email on recovery: %+v", row)
	}
}

// This fixture has a SQL grant projection but no production owner. Keep the
// direct reset SQL/checkpoint/application checks on the private lower pipeline;
// the public call above must refuse this incomplete ownership before mutation.
func applyRestrictedPollingDirectReset(ctx context.Context, ids []string, request string) error {
	return applyLocalClientPolicyReset(ctx, ids, func(instanceID string) ([]clientpolicy.Policy, error) {
		return PrepareClientPolicyResets(instanceID, ids, request)
	})
}

// This deliberately restricted fixture tests legacy cursor/SQL/application
// transactions without a production authority owner. Public reset admission is
// tested separately above and by source-owned live service fixtures. Reuse the
// actual SQL selectors rather than inventing a second selector in test code.
func applyRestrictedPollingBatch(ctx context.Context, emails []string, request string) (int, error) {
	emails = trimmedUniqueEmails(emails)
	slices.Sort(emails)
	raw, err := json.Marshal(emails)
	if err != nil {
		return 0, err
	}
	digest := sha256.Sum256(raw)
	var operation model.ClientTrafficResetBatch
	if err := runSerializedTxContextForDatabase(ctx, database.GetDB(), func(tx *gorm.DB) error {
		return selectClientTrafficResetBatchTx(tx, &operation, "bulk", emails, request, hex.EncodeToString(digest[:]))
	}); err != nil {
		return 0, err
	}
	affected, _, err := applyRestrictedPollingOperation(ctx, operation)
	return affected, err
}

func captureRestrictedPollingCalendar(ctx context.Context, period string, now time.Time) (model.ClientTrafficResetBatch, error) {
	var operation model.ClientTrafficResetBatch
	at, err := trafficResetCalendarWindow(period, now)
	if err != nil {
		return operation, err
	}
	zone := sha256.Sum256([]byte(now.Location().String()))
	scope := fmt.Sprintf("calendar:%s:%x", period, zone[:8])
	err = runSerializedTxContextForDatabase(ctx, database.GetDB(), func(tx *gorm.DB) error {
		return selectScheduledTrafficResetTx(tx, &operation, period, now, scope, at)
	})
	return operation, err
}

func applyRestrictedPollingCalendar(t *testing.T, ctx context.Context, period string, now time.Time) error {
	t.Helper()
	operation, err := captureRestrictedPollingCalendar(ctx, period, now)
	if err != nil {
		return err
	}
	_, _, err = applyRestrictedPollingOperation(ctx, operation)
	return err
}

// The fixture's grant settlement creates a real SQL projection but deliberately
// retains no production owner/manifest. Exercise its existing lower pipeline
// directly; both public capture and application must reject that ownership.
func applyRestrictedPollingOperation(ctx context.Context, operation model.ClientTrafficResetBatch) (int, bool, error) {
	affected, needRestart, effects, err := func() (int, bool, *trafficResetLegacyEffects, error) {
		lock.Lock()
		defer lock.Unlock()
		return (&ClientService{}).applyTrafficResetBatchLocked(ctx, operation, database.GetDB(), nil, nil)
	}()
	if effects != nil {
		needRestart = effects.apply(ctx, &InboundService{}) || needRestart
	}
	return affected, needRestart, err
}
