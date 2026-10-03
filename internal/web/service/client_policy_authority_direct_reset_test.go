package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
)

func TestManagedAuthorityDirectResetRetainsOriginalWitnessAndWindow(t *testing.T) {
	for _, publicSingle := range []bool{true, false} {
		t.Run(fmt.Sprintf("public-single-%t", publicSingle), func(t *testing.T) {
			svc, inbound, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			managedActivationEcho(t, flow, "warm")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			const request = "original-direct-policy-request"
			reset := func() error {
				if publicSingle {
					_, err := (&ClientService{}).ResetTrafficByEmailWithRequest(ctx, &InboundService{}, client.Email, ClientTrafficResetRequest{ClientID: client.StableID, RequestID: request})
					return err
				}
				return ResetLocalClientPolicies(ctx, []string{client.StableID}, request)
			}
			if err := reset(); err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 1 {
				t.Fatalf("actual direct reset lacks original operation capture: got%d err=%v", len(page), err)
			}
			capture, err := owner.state.Journal.LookupResetOperation(page[0].RequestID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			done, err := owner.state.Journal.LookupResetCompletion(capture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Schema    int
				RequestID string
				Resets    []model.ClientPolicyReset
			}
			if err := json.Unmarshal([]byte(prepared.Snapshot), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Schema != 1 || payload.RequestID != request || len(payload.Resets) != 1 {
				t.Fatalf("direct request identity changed: %+v", payload)
			}
			r := payload.Resets[0]
			if r.Id != 0 || r.RequestID != request || r.ClientID != client.StableID || r.RawUpload != 104 || r.RawDownload != 204 || r.BilledBytes != 316 || r.PolicyVersion != 2 || r.CreatedAt <= 0 {
				t.Fatalf("lost original direct semantic boundary: %+v", r)
			}
			var stamp model.ClientTrafficResetTime
			if err := database.GetDB().First(&stamp, "client_id = ?", client.StableID).Error; err != nil || stamp.EffectiveAt != r.CreatedAt {
				t.Fatalf("protected direct time differs from actual effect time: %+v %+v %v", stamp, r, err)
			}
			managedActivationEcho(t, flow, "next")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Where("client_id = ? AND request_id = ?", client.StableID, request).Delete(&model.ClientPolicyReset{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := reset(); err != nil {
				t.Fatal(err)
			}
			retained, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
			if err != nil || retained != prepared {
				t.Fatalf("SQL-row loss retry changed original preparation: %v", err)
			}
			ack, err := owner.state.Journal.LookupResetCompletion(capture.RequestID)
			if err != nil || ack != done {
				t.Fatalf("SQL-row loss retry changed original completion: %v", err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || before.WindowUsed != after.WindowUsed {
				t.Fatalf("direct retry moved account/window: %v", err)
			}
			managedActivationEcho(t, flow, "stay")
			if err := owner.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
			after, err = owner.state.Journal.Account(client.StableID)
			if err != nil || after.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || after.WindowBaseline != 316 || after.WindowUsed != 32 {
				t.Fatalf("direct retry lost actual2x payload billing: %+v %v", after, err)
			}
		})
	}
}

func TestManagedAuthorityDirectResetCoreFailureRetainsUnacknowledgedPreparation(t *testing.T) {
	svc, _, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	manager := panelruntime.GetManager()
	injected := errors.New("direct reset core reply failed")
	measured := &measuredResetRuntime{Runtime: manager.Local(), ManagedProcessRuntime: manager.Local().(panelruntime.ManagedProcessRuntime), failApplyAt: 1, failure: injected}
	var observed error
	measured.beforeApply = func() {
		page, err := owner.state.Journal.ResetOperationPage("", 128)
		if err != nil || len(page) != 1 {
			observed = fmt.Errorf("direct core RPC without durable capture: %d/%v", len(page), err)
			return
		}
		if _, err := owner.state.Journal.LookupResetPreparation(page[0].RequestID); err != nil {
			observed = err
		}
		if _, err := owner.state.Journal.LookupResetCompletion(page[0].RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
			observed = fmt.Errorf("direct completion before actual core reply: %v", err)
		}
	}
	manager.SetLocalRuntimeOverride(measured)
	err := ResetLocalClientPolicy(context.Background(), client.StableID, "direct-failed-core")
	manager.SetLocalRuntimeOverride(nil)
	if !errors.Is(err, injected) || observed != nil || process.IsRunning() || measured.applications != 1 {
		t.Fatalf("direct failure boundary lost: err=%v observed=%v running=%t applies=%d", err, observed, process.IsRunning(), measured.applications)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(owner.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	key := authorityDirectResetKey("direct-failed-core", []string{client.StableID})
	if _, err := state.Journal.LookupResetPreparation(key); err != nil {
		t.Fatalf("core failure lost original preparation after reopen: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("unacknowledged core gained completion after reopen: %v", err)
	}
}

func TestManagedAuthorityDirectResetRefusesCancelledOrForeignOwner(t *testing.T) {
	for _, kind := range []string{"cancelled", "lost-owner", "foreign-database"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			owner := managedAuthorityForProcess(process)
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ErrClientPolicyLedger
			restore := func() {}
			switch kind {
			case "cancelled":
				cancel()
				want = context.Canceled
			case "lost-owner":
				localAuthority.Lock()
				localAuthority.process, localAuthority.authority = nil, nil
				localAuthority.Unlock()
				restore = func() {
					localAuthority.Lock()
					localAuthority.process, localAuthority.authority = process, owner
					localAuthority.Unlock()
				}
				want = ErrAuthorityNotInitialized
			case "foreign-database":
				db := owner.db
				owner.mu.Lock()
				owner.db = db.Session(&gorm.Session{NewDB: true})
				owner.mu.Unlock()
				restore = func() { owner.mu.Lock(); owner.db = db; owner.mu.Unlock() }
			}
			err = ResetLocalClientPolicy(ctx, client.StableID, "direct-refused-owner")
			restore()
			if !errors.Is(err, want) {
				t.Fatalf("direct %s not refused: %v", kind, err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before != after {
				t.Fatalf("direct refusal changed full account: %v", err)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 0 {
				t.Fatalf("direct refusal captured effects: %d/%v", len(page), err)
			}
			var rows int64
			if err := database.GetDB().Model(&model.ClientPolicyReset{}).Count(&rows).Error; err != nil || rows != 0 {
				t.Fatalf("direct refusal prepared SQL: %d/%v", rows, err)
			}
		})
	}
}

func TestManagedAuthorityDirectResetHistoricalCompatibilityRecoversRawRequest(t *testing.T) {
	for _, loseSQL := range []bool{false, true} {
		t.Run(fmt.Sprintf("sql-row-loss-%t", loseSQL), func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			const request = "direct-historical-original-request"
			// Establish the prior per-client-only implementation's real core ack.
			if err := applyLocalClientPolicyReset(ctx, []string{client.StableID}, func(instance string) ([]clientpolicy.Policy, error) {
				return PrepareClientPolicyResets(instance, []string{client.StableID}, request)
			}); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			var original model.ClientPolicyReset
			if err := db.First(&original, "client_id = ? AND request_id = ?", client.StableID, request).Error; err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			before, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			if loseSQL {
				if err := db.Delete(&original).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := ResetLocalClientPolicy(ctx, client.StableID, request); err != nil {
				t.Fatal(err)
			}
			var retained model.ClientPolicyReset
			if err := db.First(&retained, "client_id = ? AND request_id = ?", client.StableID, request).Error; err != nil {
				t.Fatal(err)
			}
			retained.Id = original.Id // SQL numeric identities may regenerate.
			if retained != original {
				t.Fatalf("historical direct request recaptured its quota boundary: %+v %+v", original, retained)
			}
			page, err := owner.state.Journal.ResetOperationPage("", 128)
			if err != nil || len(page) != 0 {
				t.Fatalf("historical direct ack acquired invented operation witness: %d/%v", len(page), err)
			}
			after, err := owner.state.Journal.Account(client.StableID)
			if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline {
				t.Fatalf("historical direct retry moved business state: %v", err)
			}
		})
	}
}

func TestManagedAuthorityDirectResetPartialHistoricalOverlapPreservesBothWindows(t *testing.T) {
	svc, firstInbound, first, target := setupManagedActivationService(t)
	db := database.GetDB()
	second := model.ClientRecord{Email: "direct-second-owner", SubID: "direct-second-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: second.Email, Enable: true, Up: 7, Down: 11}).Error; err != nil {
		t.Fatal(err)
	}
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	secondInbound := mkInbound(t, port, model.Tunnel, fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, target))
	if _, err := (&ClientService{}).Attach(&InboundService{}, second.Id, []int{secondInbound.Id}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	firstFlow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", firstInbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer firstFlow.Close()
	secondFlow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", secondInbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFlow.Close()
	managedActivationEcho(t, firstFlow, "warm")
	managedActivationEcho(t, secondFlow, "two")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const request = "direct-overlapping-original-request"
	if err := applyLocalClientPolicyReset(ctx, []string{first.StableID}, func(instance string) ([]clientpolicy.Policy, error) {
		return PrepareClientPolicyResets(instance, []string{first.StableID}, request)
	}); err != nil {
		t.Fatal(err)
	}
	var original model.ClientPolicyReset
	if err := db.First(&original, "client_id = ? AND request_id = ?", first.StableID, request).Error; err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, firstFlow, "next")
	owner := managedAuthorityForProcess(currentXrayProcess())
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := owner.state.Journal.Account(first.StableID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{second.StableID, first.StableID}
	if err := ResetLocalClientPolicies(ctx, ids, request); err != nil {
		t.Fatal(err)
	}
	var retained model.ClientPolicyReset
	if err := db.First(&retained, "client_id = ? AND request_id = ?", first.StableID, request).Error; err != nil || retained != original {
		t.Fatalf("partial overlap recaptured original first boundary: %+v %+v %v", original, retained, err)
	}
	after, err := owner.state.Journal.Account(first.StableID)
	if err != nil || before.Policy != after.Policy || before.Usage != after.Usage || before.WindowBaseline != after.WindowBaseline || after.WindowUsed != 16 {
		t.Fatalf("partial overlap changed first original window: %+v %v", after, err)
	}
	newWindow, err := owner.state.Journal.Account(second.StableID)
	if err != nil || newWindow.Usage.BilledBytes != 30 || newWindow.WindowBaseline != 30 || newWindow.WindowUsed != 0 {
		t.Fatalf("partial overlap lost new second window: %+v %v", newWindow, err)
	}
	page, err := owner.state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 {
		t.Fatalf("partial overlap missing exact direct cohort: %d/%v", len(page), err)
	}
	prepared, err := owner.state.Journal.LookupResetPreparation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.state.Journal.LookupResetCompletion(page[0].RequestID); err != nil {
		t.Fatal(err)
	}
	var snapshot authorityDirectResetPreparationSnapshot
	if err := json.Unmarshal([]byte(prepared.Snapshot), &snapshot); err != nil || len(snapshot.Resets) != 2 {
		t.Fatalf("partial overlap lost original/new semantic rows: %+v/%v", snapshot, err)
	}
	for _, reset := range snapshot.Resets {
		if reset.RequestID != request || reset.Id != 0 || reset.ClientID == first.StableID && reset.CreatedAt != original.CreatedAt {
			t.Fatalf("partial overlap replaced old request/time: %+v", reset)
		}
	}
}
