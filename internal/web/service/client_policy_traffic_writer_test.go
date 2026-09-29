package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestTrafficManagedReconciliationRunsOutsideSerialWriter(t *testing.T) {
	for _, mode := range []struct {
		name    string
		restart bool
	}{{"restart-off", false}, {"restart-on", true}} {
		t.Run(mode.name, func(t *testing.T) { checkTrafficManagedReconciliation(t, mode.restart) })
	}
}

func checkTrafficManagedReconciliation(t *testing.T, restart bool) {
	t.Helper()
	setupPolicyLedgerDB(t)
	setRestartOnClientDisable(t, restart)
	StartTrafficWriter()
	t.Cleanup(StopTrafficWriter)
	previous := panelruntime.GetManager()
	panelruntime.SetManager(nil)
	t.Cleanup(func() { panelruntime.SetManager(previous) })
	cs, is := &ClientService{}, &InboundService{}
	ib := mkInbound(t, 24563, model.VLESS, `{"decryption":"none","clients":[]}`)
	client := model.Client{Email: "traffic-reentry", SubID: "reentry-sub", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", Enable: true, TotalGB: 100}
	if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
		t.Fatal(err)
	}
	record, err := cs.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var blocked atomic.Bool
	nested := make(chan error, 1)
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{
		ManagedChange: func(context.Context) (bool, error) {
			calls.Add(1)
			go func() {
				policies, err := PrepareClientPolicies([]string{record.StableID})
				if err == nil && (len(policies) != 1 || policies[0].Enabled) {
					err = errors.New("runtime observed policy before disable committed")
				}
				nested <- err
			}()
			select {
			case err := <-nested:
				return true, err
			case <-time.After(time.Second):
				blocked.Store(true)
				return true, errors.New("runtime callback waits on its own SQL writer")
			}
		},
	}))
	_, disabled, err := is.AddTraffic(nil, []*xray.ClientTraffic{{Email: client.Email, Up: 100}})
	if blocked.Load() {
		select {
		case <-nested:
		case <-time.After(5 * time.Second):
			t.Fatal("queued preparation did not recover after callback returned")
		}
		t.Error("managed runtime callback occupied the serial writer while waiting for policy preparation")
	}
	if err != nil || !disabled || calls.Load() != 1 {
		t.Fatalf("traffic disable: disabled=%t calls=%d err=%v", disabled, calls.Load(), err)
	}
	var total xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", client.Email).First(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total.Up != 100 || total.Enable {
		t.Fatalf("traffic or disable did not commit: %+v", total)
	}
}

func TestTrafficResetManagedReconciliationRunsOutsideSerialWriter(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint("inbound-enabled-", enabled), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			StartTrafficWriter()
			t.Cleanup(StopTrafficWriter)
			previous := panelruntime.GetManager()
			panelruntime.SetManager(nil)
			t.Cleanup(func() { panelruntime.SetManager(previous) })
			cs, is := &ClientService{}, &InboundService{}
			ib := mkInbound(t, 24563, model.VLESS, `{"decryption":"none","clients":[]}`)
			client := model.Client{Email: "reset-reentry", SubID: "reset-sub", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", Enable: true, TotalGB: 100}
			if _, err := cs.Create(is, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}}); err != nil {
				t.Fatal(err)
			}
			record, err := cs.GetRecordByEmail(nil, client.Email)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(ib).Update("enable", enabled).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Updates(map[string]any{"enable": false, "up": 100}).Error; err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var blocked atomic.Bool
			nested := make(chan error, 1)
			panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) {
				calls.Add(1)
				go func() {
					_, err := PrepareClientPolicies([]string{record.StableID})
					nested <- err
				}()
				select {
				case err := <-nested:
					return true, err
				case <-time.After(time.Second):
					blocked.Store(true)
					return true, errors.New("reset callback waits on its own SQL writer")
				}
			}}))
			_, err = cs.ResetTrafficByEmailWithRequest(context.Background(), is, client.Email, ClientTrafficResetRequest{})
			if blocked.Load() {
				select {
				case <-nested:
				case <-time.After(5 * time.Second):
					t.Fatal("queued reset preparation did not recover")
				}
				t.Error("reset runtime callback ran inside serial writer")
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := int32(0)
			if enabled {
				expected = 1
			}
			if calls.Load() != expected {
				t.Errorf("reset reconciled %d times, want %d", calls.Load(), expected)
			}
			var traffic xray.ClientTraffic
			if err := database.GetDB().Where("email = ?", client.Email).First(&traffic).Error; err != nil {
				t.Fatal(err)
			}
			if traffic.Up != 0 || traffic.Down != 0 || !traffic.Enable {
				t.Fatalf("reset did not commit: %+v", traffic)
			}
		})
	}
}
