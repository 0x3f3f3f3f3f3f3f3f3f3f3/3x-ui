package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestManagedAuthorityPostMigrationAllRetryRetainsOriginalSelection(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
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
	cs := &ClientService{}
	if _, err := cs.ResetAllTrafficsWithRequest(ctx, "post-migration-original-all"); err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "next")
	owner := managedAuthorityForProcess(currentXrayProcess())
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := owner.state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	later := model.ClientRecord{Email: "created-after-original-all", Enable: true}
	if err := db.Create(&later).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: later.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
		t.Fatal(err)
	}
	// Lose only the disposable SQL operation projection; retain the authority and reset evidence.
	if err := db.Where("request_id = ?", "post-migration-original-all").Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ResetAllTrafficsWithRequest(ctx, "post-migration-original-all"); err != nil {
		t.Fatal(err)
	}
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, "email = ?", later.Email).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.Up != 7 || traffic.Down != 11 {
		t.Fatalf("retry recaptured a new client's traffic after SQL operation loss: up=%d down=%d", traffic.Up, traffic.Down)
	}
	after, err := owner.state.Journal.Account(client.StableID)
	if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || before.WindowBaselineRemainder != after.WindowBaselineRemainder || before.WindowUsed != after.WindowUsed {
		t.Fatalf("original retry changed the protected reset boundary: %v", err)
	}
	digest := sha256.Sum256([]byte("post-migration-original-all"))
	capture, err := owner.state.Journal.LookupResetOperation("traffic-reset:" + hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Schema             int
		Operation          model.ClientTrafficResetBatch
		OriginalManagedIDs []string
	}
	if err := json.Unmarshal([]byte(capture.Snapshot), &snapshot); err != nil || snapshot.Schema != 1 || snapshot.Operation.RequestID != "post-migration-original-all" || len(snapshot.OriginalManagedIDs) != 1 || snapshot.OriginalManagedIDs[0] != client.StableID {
		t.Fatalf("original protected capture was lost: %v", err)
	}
	managedActivationEcho(t, flow, "stay")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = owner.state.Journal.Account(client.StableID)
	if err != nil || after.Usage.RawUpload != 112 || after.Usage.RawDownload != 212 || after.Usage.BilledBytes != 348 || after.WindowBaseline != 316 || after.WindowUsed != 32 {
		t.Fatalf("flow did not retain exact 2x accounting: %v", err)
	}
}

func TestManagedAuthorityPostMigrationManualScopesRetainCanonicalSelection(t *testing.T) {
	for _, bulk := range []bool{true, false} {
		t.Run(map[bool]string{true: "bulk-renamed-original", false: "inbound-all"}[bulk], func(t *testing.T) {
			svc, tunnel, client, _ := setupManagedActivationService(t)
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
			request := "post-migration-scoped-reset"
			originalEmail := client.Email
			reset := func() error {
				cs := &ClientService{}
				if bulk {
					_, err := cs.BulkResetTrafficWithRequest(ctx, &InboundService{}, []string{originalEmail}, request)
					return err
				}
				return cs.ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, -1, request)
			}
			if err := reset(); err != nil {
				t.Fatal(err)
			}
			managedActivationEcho(t, flow, "next")
			owner := managedAuthorityForProcess(currentXrayProcess())
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			original, err := owner.state.Journal.LookupResetOperation(authorityResetRequestKey(request))
			if err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			laterEmail := "created-after-inbound-all"
			if bulk {
				if err := db.Model(client).Update("email", "renamed-original-owner").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", originalEmail).Update("email", "renamed-original-owner").Error; err != nil {
					t.Fatal(err)
				}
				laterEmail = originalEmail
			}
			later := model.ClientRecord{Email: laterEmail, Enable: true}
			if err := db.Create(&later).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: later.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("request_id = ?", request).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := reset(); err != nil {
				t.Fatal(err)
			}
			var traffic xray.ClientTraffic
			if err := db.First(&traffic, "email = ?", later.Email).Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
				t.Fatalf("scoped retry reset later identity: up=%d down=%d err=%v", traffic.Up, traffic.Down, err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || before.WindowUsed != after.WindowUsed {
				t.Fatalf("scoped retry changed original policy/window: %v", err)
			}
			retained, err := owner.state.Journal.LookupResetOperation(original.RequestID)
			if err != nil || retained != original {
				t.Fatalf("scoped retry changed original capture: %v", err)
			}
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			after, err = owner.state.Journal.Account(client.StableID)
			if err != nil || after.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || after.WindowBaseline != 316 || after.WindowUsed != 32 {
				t.Fatalf("scoped flow lost exact original accounting: %v", err)
			}
		})
	}
}

func TestManagedAuthorityResetCaptureRefusesConflictsAndAmbiguousLegacy(t *testing.T) {
	for _, fault := range []string{"selection", "immutable-sql", "source", "cancelled", "mixed-pending"} {
		t.Run(fault, func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			if fault == "mixed-pending" {
				legacy := model.ClientRecord{Email: "unprepared-legacy", Enable: true}
				if err := db.Create(&legacy).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&xray.ClientTraffic{Email: legacy.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
					t.Fatal(err)
				}
			}
			request := "captured-pending-fault"
			operation, err := captureClientTrafficResetBatch(context.Background(), "all", nil, request)
			if err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			capture, err := owner.state.Journal.LookupResetOperation(authorityResetRequestKey(request))
			if err != nil {
				t.Fatal(err)
			}
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			scope := "all"
			switch fault {
			case "selection":
				scope = "bulk"
			case "immutable-sql":
				if err := db.Table("client_traffic_reset_batches").Where("request_id = ?", operation.RequestID).Update("targets_json", "[]").Error; err != nil {
					t.Fatal(err)
				}
				var damaged model.ClientTrafficResetBatch
				if err := db.First(&damaged, "request_id = ?", request).Error; err != nil || damaged.TargetsJSON != "[]" {
					t.Fatalf("immutable conflict fixture was not injected: %v", err)
				}
			case "source":
				if err := db.Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Update("instance_id", "wrong-capture-source").Error; err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					db.Model(&model.ClientPolicySource{}).Where("node_key = ?", "local").Update("instance_id", owner.config.InstanceID)
				})
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "mixed-pending":
				if err := db.Where("request_id = ?", request).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := captureClientTrafficResetBatch(ctx, scope, nil, request); err == nil || fault == "cancelled" && !errors.Is(err, context.Canceled) || fault != "selection" && fault != "cancelled" && !errors.Is(err, ErrClientPolicyLedger) {
				t.Fatalf("unsafe original capture accepted (%s): %v", fault, err)
			}
			retained, err := owner.state.Journal.LookupResetOperation(capture.RequestID)
			if err != nil || retained != capture {
				t.Fatalf("failed capture changed original witness: %v", err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || after != before {
				t.Fatalf("failed capture changed full protected account: %v", err)
			}
			if fault == "mixed-pending" {
				var traffic xray.ClientTraffic
				if err := db.First(&traffic, "email = ?", "unprepared-legacy").Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
					t.Fatalf("ambiguous legacy effects were replayed: up=%d down=%d err=%v", traffic.Up, traffic.Down, err)
				}
			}
		})
	}
}

func TestManagedAuthorityResetCaptureDoesNotRecreateMissingJournal(t *testing.T) {
	previousManual, previousRestart := isManuallyStopped.Load(), isNeedXrayRestart.Load()
	t.Cleanup(func() { isManuallyStopped.Store(previousManual); isNeedXrayRestart.Store(previousRestart) })
	svc, _, _, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	path := filepath.Join(filepath.Dir(owner.config.StateFile), "authority", "journal.db")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	retained := path + ".retained-missing-capture-fixture"
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Rename(retained, path); err != nil {
			t.Error(err)
		}
	})
	if _, err := captureClientTrafficResetBatch(context.Background(), "all", nil, "missing-owned-journal"); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("missing owned journal was accepted: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture recreated missing owned journal: %v", err)
	}
	after, err := os.ReadFile(retained)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("missing-journal refusal changed retained original: %v", err)
	}
}

func TestManagedAuthorityPostMigrationCalendarRetryRetainsOriginalSelection(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-managed", true: "original-empty"}[empty], func(t *testing.T) {
			svc, tunnel, client, _ := setupManagedActivationService(t)
			db := database.GetDB()
			if !empty {
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
			cs := &ClientService{}
			if err := cs.RunScheduledTrafficReset(ctx, "daily", now); err != nil {
				t.Fatal(err)
			}
			var original model.ClientTrafficResetBatch
			if err := db.First(&original, "scheduled_at > 0").Error; err != nil {
				t.Fatal(err)
			}
			managedActivationEcho(t, flow, "next")
			owner := managedAuthorityForProcess(currentXrayProcess())
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			later := model.ClientRecord{Email: "created-after-original-calendar", Enable: true, TrafficReset: "daily"}
			if err := db.Create(&later).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: later.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("request_id = ?", original.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := cs.RunScheduledTrafficReset(ctx, "daily", now); err != nil {
				t.Fatal(err)
			}
			var traffic xray.ClientTraffic
			if err := db.First(&traffic, "email = ?", later.Email).Error; err != nil || traffic.Up != 7 || traffic.Down != 11 {
				t.Fatalf("calendar retry reset later client: up=%d down=%d err=%v", traffic.Up, traffic.Down, err)
			}
			var restored model.ClientTrafficResetBatch
			if err := db.First(&restored, "request_id = ?", original.RequestID).Error; err != nil || restored.TargetsJSON != original.TargetsJSON || restored.InboundIDsJSON != original.InboundIDsJSON || restored.Scope != original.Scope || restored.ScheduledAt != original.ScheduledAt || restored.CreatedAt != original.CreatedAt {
				t.Fatalf("calendar retry changed immutable original operation: %v", err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || before.WindowUsed != after.WindowUsed {
				t.Fatalf("calendar retry changed protected accounting: %v", err)
			}
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			after, err = owner.state.Journal.Account(client.StableID)
			window := uint64(32)
			if empty {
				window = 348
			}
			if err != nil || after.Usage.RawUpload != 112 || after.Usage.RawDownload != 212 || after.Usage.BilledBytes != 348 || after.WindowUsed != window {
				t.Fatalf("calendar flow lost exact 2x usage: raw=%d/%d billed=%d window=%d err=%v", after.Usage.RawUpload, after.Usage.RawDownload, after.Usage.BilledBytes, after.WindowUsed, err)
			}
		})
	}
}
