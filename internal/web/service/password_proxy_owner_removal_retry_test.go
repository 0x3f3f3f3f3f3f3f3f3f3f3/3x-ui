package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestPasswordProxyOwnerRemovalFinalDeletionRecheck(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "bulk"}[bulk], func(t *testing.T) {
			setupPolicyLedgerDB(t)
			owner := passwordOwner(t, "removal-late-owner")
			inbounds, clients := &InboundService{}, &ClientService{}
			_, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24537, Settings: passwordOwnerSettings(t, model.HTTP,
				map[string]any{"user": "first", "pass": "first-secret", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			later, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24538, Settings: passwordOwnerSettings(t, model.HTTP)})
			if err != nil {
				t.Fatal(err)
			}
			previous := panelruntime.GetManager()
			t.Cleanup(func() { panelruntime.SetManager(previous) })
			var inserted atomic.Bool
			// Schedule a real resource command after the first removal commits.
			// This test proves SQL interleaving; socket behavior is tested separately.
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) {
				if inserted.CompareAndSwap(false, true) {
					request := *later
					request.Settings = passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "late", "pass": "late-secret", "ownerClientId": owner.StableID})
					_, _, err := inbounds.UpdateInbound(&request)
					return true, err
				}
				return true, nil
			}}))
			remove := func() error {
				if bulk {
					_, _, err := clients.BulkDelete(inbounds, []string{owner.Email}, true)
					return err
				}
				_, err := clients.Delete(inbounds, owner.Id, true)
				return err
			}
			if err := remove(); !errors.Is(err, ErrPasswordProxyOwner) {
				t.Fatalf("late owned account did not fence canonical deletion: %v", err)
			}
			if !inserted.Load() || len(linksOf(t, later.Id)) != 1 {
				t.Fatal("recheck lost the newly attached resource")
			}
			if _, err := clients.GetByID(owner.Id); err != nil {
				t.Fatalf("recheck deleted the canonical owner: %v", err)
			}
			var tombstones int64
			if err := database.GetDB().Model(&model.ClientPolicyTombstone{}).Where("client_id = ?", owner.StableID).Count(&tombstones).Error; err != nil || tombstones != 0 {
				t.Fatalf("recheck committed revocation: %d %v", tombstones, err)
			}
			if err := remove(); err != nil {
				t.Fatalf("retry did not remove the newly discovered resource: %v", err)
			}
			if len(linksOf(t, later.Id)) != 0 {
				t.Fatal("retry retained late membership")
			}
		})
	}
}
