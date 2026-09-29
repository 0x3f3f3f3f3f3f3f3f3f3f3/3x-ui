package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestClientPolicyMutationReportsManagedApplyFailure(t *testing.T) {
	for _, operation := range []string{"add-user", "attach-disabled", "detach", "detach-disabled", "bulk-detach-disabled", "bulk-disable", "bulk-enable", "bulk-adjust", "bulk-delete", "bulk-delete-stale-settings", "add-inbound", "update-inbound", "disable-inbound", "delete-inbound"} {
		t.Run(operation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			previous := panelruntime.GetManager()
			t.Cleanup(func() { panelruntime.SetManager(previous) })
			panelruntime.SetManager(nil)
			cs, is := &ClientService{}, &InboundService{}
			ib := mkInbound(t, 24561, model.VLESS, `{"decryption":"none","clients":[]}`)
			client := model.Client{Email: "mutation-result", SubID: "mutation-sub", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", Enable: !strings.Contains(operation, "disabled") && operation != "bulk-enable", TotalGB: 10000}
			if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
				t.Fatal(err)
			}
			record, err := cs.GetRecordByEmail(nil, client.Email)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("managed application deliberately unavailable")
			var attempts, restarts atomic.Int32
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{
				ManagedChange:  func(context.Context) (bool, error) { attempts.Add(1); return true, injected },
				SetNeedRestart: func() { restarts.Add(1) },
			}))
			var needRestart bool
			bulk := false
			switch operation {
			case "add-user":
				added := model.Client{Email: "added-user", SubID: "added-sub", ID: "01dc4f70-3902-446a-98cb-c00992d1a6c5", Enable: true}
				needRestart, err = cs.AddInboundClient(is, &model.Inbound{Id: ib.Id, Settings: clientsSettings(t, []model.Client{added})})
				if _, savedErr := cs.GetRecordByEmail(nil, added.Email); savedErr != nil {
					t.Fatalf("add did not commit before runtime application: %v", savedErr)
				}
			case "attach-disabled":
				other := mkInbound(t, 24562, model.VLESS, `{"decryption":"none","clients":[]}`)
				needRestart, err = cs.Attach(is, record.Id, []int{other.Id})
				if len(linksOf(t, other.Id)) != 1 {
					t.Fatal("disabled attachment was not committed")
				}
			case "detach", "detach-disabled":
				needRestart, err = cs.Detach(is, record.Id, []int{ib.Id})
				if len(linksOf(t, ib.Id)) != 0 {
					t.Fatal("detach was not committed")
				}
			case "bulk-detach-disabled":
				bulk = true
				result, restart, applyErr := cs.BulkDetach(is, []string{record.Email}, []int{ib.Id})
				needRestart, err = restart, applyErr
				if err == nil && len(result.Errors) > 0 {
					err = errors.New(strings.Join(result.Errors, "; "))
				}
				if len(result.Detached) != 0 || len(linksOf(t, ib.Id)) != 0 {
					t.Errorf("failed runtime detach response does not match committed links: %+v", result)
				}
			case "bulk-disable", "bulk-enable":
				bulk = true
				result, restart, applyErr := cs.BulkSetEnable(is, []string{record.Email}, operation == "bulk-enable")
				needRestart, err = restart, applyErr
				if err == nil && len(result.Skipped) > 0 {
					err = errors.New(result.Skipped[0].Reason)
				}
				if result.Changed != 0 {
					t.Errorf("failed runtime toggle reported success: %+v", result)
				}
			case "bulk-adjust":
				bulk = true
				result, restart, applyErr := cs.BulkAdjust(is, []string{record.Email}, 0, -100, "", nil, "")
				needRestart, err = restart, applyErr
				if err == nil && len(result.Skipped) > 0 {
					err = errors.New(result.Skipped[0].Reason)
				}
				if result.Adjusted != 0 {
					t.Errorf("failed runtime quota adjustment reported success: %+v", result)
				}
			case "bulk-delete", "bulk-delete-stale-settings":
				if operation == "bulk-delete-stale-settings" {
					if err := database.GetDB().Model(ib).Update("settings", `{"decryption":"none","clients":[]}`).Error; err != nil {
						t.Fatal(err)
					}
				}
				bulk = true
				result, restart, applyErr := cs.BulkDelete(is, []string{record.Email}, true)
				needRestart, err = restart, applyErr
				if err == nil && len(result.Skipped) > 0 {
					err = errors.New(result.Skipped[0].Reason)
				}
				if result.Deleted != 0 {
					t.Errorf("failed runtime deletion reported success: %+v", result)
				}
			case "add-inbound":
				added := &model.Inbound{Tag: "added-managed", Port: 24562, Listen: "127.0.0.1", Protocol: model.VLESS, Enable: true, Settings: `{"decryption":"none","clients":[]}`}
				_, needRestart, err = is.AddInbound(added)
				if _, savedErr := is.GetInbound(added.Id); savedErr != nil {
					t.Fatalf("inbound add did not commit: %v", savedErr)
				}
			case "update-inbound":
				ib.Remark = "committed mutation"
				_, needRestart, err = is.UpdateInbound(ib)
				stored, savedErr := is.GetInbound(ib.Id)
				if savedErr != nil || stored.Remark != ib.Remark {
					t.Fatalf("inbound update did not commit: %+v %v", stored, savedErr)
				}
			case "disable-inbound":
				needRestart, err = is.SetInboundEnable(ib.Id, false)
			case "delete-inbound":
				needRestart, err = is.DelInbound(ib.Id)
				var count int64
				if savedErr := database.GetDB().Model(&model.Inbound{}).Where("id = ?", ib.Id).Count(&count).Error; savedErr != nil || count != 0 {
					t.Fatalf("inbound deletion did not commit: %d %v", count, savedErr)
				}
			}
			if err == nil || !strings.Contains(err.Error(), injected.Error()) || !strings.Contains(err.Error(), panelruntime.ErrManagedApply.Error()) {
				t.Errorf("saved mutation hid managed runtime failure: %v", err)
			} else if !bulk && !errors.Is(err, panelruntime.ErrManagedApply) {
				t.Errorf("single mutation lost typed runtime failure: %v", err)
			}
			if attempts.Load() != 1 || restarts.Load() != 1 || !needRestart {
				t.Errorf("mutation did not reconcile once and queue recovery: attempts=%d restarts=%d needRestart=%t", attempts.Load(), restarts.Load(), needRestart)
			}
		})
	}
}
