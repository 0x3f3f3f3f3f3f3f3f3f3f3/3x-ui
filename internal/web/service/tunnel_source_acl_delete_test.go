package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestTunnelSourceACLDeleteRechecksConcurrentAttachment(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			startSerializedWriter(t)
			db := database.GetDB()
			svc, clients := &InboundService{}, &ClientService{}
			if _, err := clients.Create(svc, &ClientCreatePayload{Client: model.Client{Email: "late-acl-owner", Enable: true}}); err != nil {
				t.Fatal(err)
			}
			owner, err := clients.GetRecordByEmail(nil, "late-acl-owner")
			if err != nil {
				t.Fatal(err)
			}
			var reconciles atomic.Int32
			previous := panelruntime.GetManager()
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) { reconciles.Add(1); return true, nil }}))
			t.Cleanup(func() { panelruntime.SetManager(previous) })
			captured, release := make(chan struct{}), make(chan struct{})
			var intercepted atomic.Bool
			var released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			const callback = "test:acl-delete-membership-snapshot"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "client_inbounds" {
					return
				}
				_, single := tx.Statement.Dest.(*[]int)
				_, many := tx.Statement.Dest.(*[]model.ClientInbound)
				if bulk && !many || !bulk && !single || !intercepted.CompareAndSwap(false, true) {
					return
				}
				close(captured)
				select {
				case <-release:
				case <-time.After(5 * time.Second):
					tx.AddError(fmt.Errorf("attachment barrier timed out"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if bulk {
					_, _, err := clients.BulkDelete(svc, []string{owner.Email}, false)
					done <- err
				} else {
					_, err := clients.Delete(svc, owner.Id, false)
					done <- err
				}
			}()
			finished := false
			t.Cleanup(func() {
				unblock()
				if !finished {
					select {
					case <-done:
					case <-time.After(6 * time.Second):
						t.Error("delete did not stop")
					}
				}
				_ = db.Callback().Query().Remove(callback)
			})
			select {
			case <-captured:
			case err := <-done:
				finished = true
				t.Fatalf("delete bypassed snapshot barrier: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("delete snapshot not reached")
			}
			request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": true})
			request.Settings = sourceACLSettings(t, []string{"127.0.0.1/32"})
			added, _, err := svc.AddInbound(request)
			if err != nil {
				t.Fatal(err)
			}
			reconciles.Store(0)
			unblock()
			select {
			case err := <-done:
				finished = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delete did not finish")
			}
			var saved model.Inbound
			if err := db.First(&saved, added.Id).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Enable || !strings.Contains(saved.Settings, "127.0.0.1/32") {
				t.Fatalf("late attachment escaped deletion: %+v", saved)
			}
			var links int64
			if err := db.Model(&model.ClientInbound{}).Where("inbound_id = ?", added.Id).Count(&links).Error; err != nil || links != 0 {
				t.Fatalf("leftover links: %d %v", links, err)
			}
			if reconciles.Load() != 1 {
				t.Fatalf("final cleanup reconciled %d times, want once", reconciles.Load())
			}
		})
	}
}

func TestTunnelSourceACLConcurrentEnableAndDetach(t *testing.T) {
	setupPolicyLedgerDB(t)
	startSerializedWriter(t)
	svc, clients := &InboundService{}, &ClientService{}
	if _, err := clients.Create(svc, &ClientCreatePayload{Client: model.Client{Email: "acl-race-owner", Enable: true}}); err != nil {
		t.Fatal(err)
	}
	owner, err := clients.GetRecordByEmail(nil, "acl-race-owner")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		request := tunnelOwnerRequest(t, owner.StableID, map[string]any{"enable": false})
		request.Port += i
		request.Settings = sourceACLSettings(t, []string{"127.0.0.1/32"})
		added, _, err := svc.AddInbound(request)
		if err != nil {
			t.Fatal(err)
		}
		start, enabled, detached := make(chan struct{}), make(chan error, 1), make(chan error, 1)
		go func() { <-start; _, err := svc.SetInboundEnable(added.Id, true); enabled <- err }()
		go func() { <-start; _, err := clients.Detach(svc, owner.Id, []int{added.Id}); detached <- err }()
		close(start)
		if err := <-enabled; err != nil && !strings.Contains(err.Error(), "owner") {
			t.Fatal(err)
		}
		if err := <-detached; err != nil {
			t.Fatal(err)
		}
		var saved model.Inbound
		if err := database.GetDB().First(&saved, added.Id).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Enable {
			t.Fatal("concurrent enable outlived final-owner detach")
		}
	}
}
