package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

func TestManagedAuthorityResetExecutionRetainsPreparedAndCompletedBoundary(t *testing.T) {
	for _, calendarNoOp := range []bool{false, true} {
		t.Run(fmt.Sprintf("calendar-noop-%t", calendarNoOp), func(t *testing.T) {
			svc, tunnel, client, _ := setupManagedActivationService(t)
			db := database.GetDB()
			if calendarNoOp {
				if err := db.Model(client).Update("traffic_reset", "daily").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "warm")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			now := time.Now().UTC()
			request := "execution-original-all"
			if calendarNoOp {
				if err := ResetLocalClientPolicy(ctx, client.StableID, "execution-recent-manual"); err != nil {
					t.Fatal(err)
				}
			}
			reset := func() error {
				if calendarNoOp {
					return (&ClientService{}).RunScheduledTrafficReset(ctx, "daily", now)
				}
				_, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request)
				return err
			}
			if err := reset(); err != nil {
				t.Fatal(err)
			}
			var operation model.ClientTrafficResetBatch
			if err := db.First(&operation).Error; err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			capture, err := owner.state.Journal.LookupResetOperation(authorityResetRequestKey(operation.RequestID))
			if err != nil {
				t.Fatal(err)
			}
			preparation, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil {
				t.Fatalf("public successful reset lacks durable prepared boundary: %v", err)
			}
			completion, err := owner.state.Journal.LookupResetCompletion(capture.RequestID)
			if err != nil {
				t.Fatalf("core acknowledged reset lacks durable completion: %v", err)
			}
			digest := sha256.Sum256([]byte(capture.Snapshot))
			if preparation.Identity != capture.Identity || preparation.SourceID != capture.SourceID || preparation.CaptureDigest != hex.EncodeToString(digest[:]) {
				t.Fatal("preparation is not bound to original capture")
			}
			digest = sha256.Sum256([]byte(preparation.Snapshot))
			if completion.Identity != preparation.Identity || completion.SourceID != preparation.SourceID || completion.PreparationDigest != hex.EncodeToString(digest[:]) {
				t.Fatal("completion is not bound to original preparation")
			}
			var prepared struct {
				Schema           int
				RequestID        string
				ResetAt          int64
				ActiveManagedIDs []string
				Affected         int
				Resets           []model.ClientPolicyReset
			}
			if err := json.Unmarshal([]byte(preparation.Snapshot), &prepared); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if calendarNoOp {
				expected = 0
			}
			if prepared.Schema != 1 || prepared.RequestID != operation.RequestID || prepared.ResetAt <= 0 || prepared.Affected != expected || len(prepared.ActiveManagedIDs) != expected || len(prepared.Resets) != expected {
				t.Fatalf("wrong original execution boundary: %+v", prepared)
			}
			if !calendarNoOp {
				r := prepared.Resets[0]
				if r.ClientID != client.StableID || r.RawUpload != 104 || r.RawDownload != 204 || r.BilledBytes != 316 || r.PolicyVersion != 2 {
					t.Fatalf("prepared reset lost checkpointed original usage: %+v", r)
				}
			}
			managedActivationEcho(t, flow, "next")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := reset(); err != nil {
				t.Fatal(err)
			}
			retained, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil || retained != preparation {
				t.Fatalf("retry changed exact preparation: %v", err)
			}
			done, err := owner.state.Journal.LookupResetCompletion(capture.RequestID)
			if err != nil || done != completion {
				t.Fatalf("retry changed exact completion: %v", err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || before.WindowUsed != after.WindowUsed {
				t.Fatalf("retry moved quota boundary or billed later traffic: %v", err)
			}
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			after, err = owner.state.Journal.Account(client.StableID)
			if err != nil || after.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || after.WindowBaseline != 316 || after.WindowUsed != 32 {
				t.Fatalf("live exact 2x billing after acknowledged retry lost: %+v %v", after, err)
			}
		})
	}
}

func TestManagedAuthorityResetExecutionCoreFailureHasNoCompletion(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request := "execution-core-failure"
	manager := panelruntime.GetManager()
	injected := errors.New("actual managed application reply unavailable")
	measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime), failApplyAt: 1, failure: injected}
	var beforeReply error
	measured.beforeApply = func() {
		key := authorityResetRequestKey(request)
		if _, err := owner.state.Journal.LookupResetPreparation(key); err != nil {
			beforeReply = fmt.Errorf("core called without durable preparation: %w", err)
		}
		if _, err := owner.state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
			beforeReply = fmt.Errorf("completion preceded core acknowledgement: %v", err)
		}
	}
	manager.SetLocalRuntimeOverride(measured)
	_, resetErr := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request)
	manager.SetLocalRuntimeOverride(nil)
	if !errors.Is(resetErr, injected) || beforeReply != nil || measured.applications != 1 {
		t.Fatalf("wrong actual application failure boundary: %v/%v/%d", resetErr, beforeReply, measured.applications)
	}
	if process.IsRunning() {
		t.Fatal("unacknowledged core retained business access")
	}
	state, err := openAuthorityState(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	key := authorityResetRequestKey(request)
	if _, err := state.Journal.LookupResetPreparation(key); err != nil {
		t.Fatalf("lost original preparation after failed application: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("failed application has false completion: %v", err)
	}
	account, err := state.Journal.Account(client.StableID)
	if err != nil || account.Usage.BilledBytes != 316 || account.WindowBaseline != 316 {
		t.Fatalf("failed core reply lost protected exact boundary: %+v/%v", account, err)
	}
}

func TestManagedAuthorityResetExecutionRefusesLostOwnerAfterCapture(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	operation, err := captureClientTrafficResetBatch(ctx, "all", nil, "execution-owner-lost")
	if err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	localAuthority.Lock()
	localAuthority.process, localAuthority.authority = nil, nil
	localAuthority.Unlock()
	_, _, applyErr := (&ClientService{}).applyTrafficResetBatch(ctx, &InboundService{}, operation)
	localAuthority.Lock()
	localAuthority.process, localAuthority.authority = process, owner
	localAuthority.Unlock()
	if !errors.Is(applyErr, ErrAuthorityNotInitialized) {
		t.Fatalf("captured operation applied after retained owner loss: %v", applyErr)
	}
	after, err := owner.state.Journal.Account(client.StableID)
	if err != nil || before != after {
		t.Fatalf("owner-loss refusal changed account: %v", err)
	}
	var current model.ClientTrafficResetBatch
	if err := database.GetDB().First(&current, "request_id = ?", operation.RequestID).Error; err != nil || current.Applied {
		t.Fatalf("owner-loss refusal prepared SQL: %+v/%v", current, err)
	}
}

func TestManagedAuthorityResetExecutionDoesNotAcknowledgeOtherEffects(t *testing.T) {
	for _, effect := range []string{"legacy-client", "calendar-inbound", "manual-inbound"} {
		t.Run(effect, func(t *testing.T) {
			svc, tunnel, _, _ := setupManagedActivationService(t)
			db := database.GetDB()
			if effect == "legacy-client" {
				legacy := model.ClientRecord{Email: "execution-unprepared-legacy", Enable: true}
				if err := db.Create(&legacy).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&xray.ClientTraffic{Email: legacy.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if effect == "calendar-inbound" {
				if err := db.Model(tunnel).Update("traffic_reset", "daily").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			request := "execution-unprepared-effects"
			switch effect {
			case "calendar-inbound":
				if err := (&ClientService{}).RunScheduledTrafficReset(ctx, "daily", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "manual-inbound":
				if err := (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, tunnel.Id, request); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, request); err != nil {
					t.Fatal(err)
				}
			}
			var operation model.ClientTrafficResetBatch
			if err := db.First(&operation).Error; err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			key := authorityResetRequestKey(operation.RequestID)
			if _, err := owner.state.Journal.LookupResetPreparation(key); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("unprepared %s effects gained managed witness: %v", effect, err)
			}
			if _, err := owner.state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("unconfirmed %s effects gained false completion: %v", effect, err)
			}
		})
	}
}

func TestManagedAuthorityResetExecutionCancelledApplicationPreservesSelection(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	request := "execution-cancelled-after-capture"
	operation, err := captureClientTrafficResetBatch(context.Background(), "all", nil, request)
	if err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (&ClientService{}).applyTrafficResetBatch(ctx, &InboundService{}, operation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled operation applied: %v", err)
	}
	after, err := owner.state.Journal.Account(client.StableID)
	if err != nil || before != after {
		t.Fatalf("cancelled operation changed account: %v", err)
	}
	key := authorityResetRequestKey(request)
	if _, err := owner.state.Journal.LookupResetPreparation(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("cancelled operation prepared effects: %v", err)
	}
	if _, err := owner.state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("cancelled operation acknowledged effects: %v", err)
	}
}

func TestManagedAuthorityResetExecutionRejectsForeignPoolAndSource(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	operation, err := captureClientTrafficResetBatch(ctx, "all", nil, "execution-foreign-owner")
	if err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	lock.Lock()
	_, _, poolErr := authorityResetExecutionStateLocked(ctx, db.Session(&gorm.Session{NewDB: true}))
	lock.Unlock()
	if !errors.Is(poolErr, ErrClientPolicyLedger) {
		t.Fatalf("different SQL handle admitted under original owner: %v", poolErr)
	}
	state := *owner.state
	state.SourceID = "foreign-operation-source"
	if _, err := authorityResetExecutionCapture(&state, operation); !errors.Is(err, ErrClientPolicyLedger) {
		t.Fatalf("foreign source admitted original capture: %v", err)
	}
	after, err := owner.state.Journal.Account(client.StableID)
	if err != nil || before != after {
		t.Fatalf("foreign owner refusal changed account: %v", err)
	}
	key := authorityResetRequestKey(operation.RequestID)
	if _, err := owner.state.Journal.LookupResetPreparation(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("foreign owner refusal prepared effects: %v", err)
	}
}
