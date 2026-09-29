package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

func TestTrafficRuntimeBatchBoundsReadsAndManagedReconciliation(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	inbound := mkInbound(t, 24793, model.VLESS, `{"decryption":"none","clients":[]}`)
	const count = 1001
	clients := make([]model.Client, count)
	batch := newTrafficMutationBatch()
	for i := range clients {
		clients[i] = model.Client{Email: fmt.Sprintf("batch-%d", i), ID: fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", i), Enable: true}
		batch.localPlans = append(batch.localPlans, trafficLocalApplyPlan{action: trafficAddUser, inbound: *inbound, client: map[string]any{"email": clients[i].Email}})
	}
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, clients); err != nil {
		t.Fatal(err)
	}
	var reads int
	const callback = "test:traffic-batch-read-count"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "inbounds" || tx.Statement.Table == "clients" {
			reads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	probe := &trafficAddProbe{}
	manager := useTestRuntimeManager(t)
	manager.SetLocalRuntimeOverride(probe)
	if restart := (&InboundService{}).applyTrafficMutationBatch(batch); restart || probe.added != count {
		t.Fatalf("batch did not apply every enabled linked client: restart=%t added=%d", restart, probe.added)
	}
	if reads > 20 {
		t.Errorf("batch performed %d reads for one inbound; reads must be bounded in batches", reads)
	}
	var reconciliations int
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) {
		reconciliations++
		return true, nil
	}}))
	if restart := (&InboundService{}).applyTrafficMutationBatch(batch); restart || reconciliations != 1 {
		t.Fatalf("one committed batch reconciled the managed configuration %d times, restart=%t", reconciliations, restart)
	}
}

type trafficAddProbe struct {
	fakeNodeRuntime
	added  int
	lastID string
	users  map[int]map[string]any
}

type trafficBlockingAddProbe struct {
	trafficAddProbe
	entered chan struct{}
	release chan struct{}
}

func (p *trafficBlockingAddProbe) AddUser(ctx context.Context, inbound *model.Inbound, user map[string]any) error {
	close(p.entered)
	<-p.release
	return p.trafficAddProbe.AddUser(ctx, inbound, user)
}

func TestTrafficRuntimeReadAndApplySharesInboundMutationLock(t *testing.T) {
	for _, mutation := range []string{"client-disable", "inbound-disable", "inbound-delete", "inbound-update"} {
		t.Run(mutation, func(t *testing.T) {
			setupConflictDB(t)
			setRestartOnClientDisable(t, false)
			probe := &trafficBlockingAddProbe{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(probe.release) }) }
			t.Cleanup(release)
			useTestRuntimeManager(t).SetLocalRuntimeOverride(probe)
			inbound := seedRenewableNeighbour(t, 24792, nil)
			svc := &InboundService{}
			_, _, _, batch, err := svc.addTrafficLocked(nil, nil)
			if err != nil || batch == nil || len(batch.localPlans) != 1 {
				t.Fatalf("missing renewal plan: %v", err)
			}
			applied := make(chan bool, 1)
			go func() { applied <- svc.applyTrafficMutationBatch(batch) }()
			select {
			case <-probe.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("runtime call was not reached")
			}
			started, changed := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				var err error
				switch mutation {
				case "client-disable":
					_, _, err = (&ClientService{}).SetClientEnableByEmail(svc, "y@stale", false)
				case "inbound-disable":
					_, err = svc.SetInboundEnable(inbound.Id, false)
				case "inbound-delete":
					_, err = svc.DelInbound(inbound.Id)
				case "inbound-update":
					inbound.Enable = false
					_, _, err = svc.UpdateInbound(inbound)
				}
				changed <- err
			}()
			<-started
			select {
			case err := <-changed:
				release()
				<-applied
				t.Fatalf("%s overtook a runtime apply after its configuration read: %v", mutation, err)
			case <-time.After(100 * time.Millisecond):
			}
			release()
			if restart := <-applied; restart {
				t.Fatal("runtime apply failed")
			}
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mutation did not resume after runtime apply")
			}
		})
	}
}

func (p *trafficAddProbe) AddUser(_ context.Context, inbound *model.Inbound, user map[string]any) error {
	p.added++
	p.lastID, _ = user["id"].(string)
	if p.users != nil {
		p.users[inbound.Id] = user
	}
	return nil
}

func TestTrafficConfigExportLeavesMaintenanceForPoll(t *testing.T) {
	setupSettingTestDB(t)
	seedRenewableNeighbour(t, 24794, nil)
	probe := &trafficAddProbe{}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(probe)
	if _, err := (&ServerService{}).GetConfigJson(); err != nil {
		t.Fatal(err)
	}
	record, err := (&ClientService{}).GetRecordByEmail(nil, "y@stale")
	if err != nil {
		t.Fatal(err)
	}
	if record.Enable || record.ExpiryTime > time.Now().UnixMilli() || probe.added != 0 {
		t.Fatal("configuration export consumed a pending lifecycle transition")
	}
	if _, _, err := (&InboundService{}).AddTraffic(nil, nil); err != nil {
		t.Fatal(err)
	}
	if probe.added != 1 {
		t.Fatalf("ordinary poll lost the runtime renewal after export: %d additions", probe.added)
	}
}

func TestTrafficRestartMaintenanceDoesNotHoldRestartLock(t *testing.T) {
	setupSettingTestDB(t)
	seedRenewableNeighbour(t, 24797, nil)
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", "invalid-json"); err != nil {
		t.Fatal(err)
	}
	previous := panelruntime.GetManager()
	t.Cleanup(func() { panelruntime.SetManager(previous) })
	var calls int
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) {
		calls++
		if !lock.TryLock() {
			t.Error("restart held its global lock while entering runtime reconciliation")
		} else {
			lock.Unlock()
		}
		return true, nil
	}}))
	if err := (&XrayService{}).RestartXray(false); err == nil {
		t.Fatal("fixture requires invalid candidate to stop before starting a child")
	}
	if calls != 1 {
		t.Fatalf("restart did not apply lifecycle maintenance outside its lock: %d calls", calls)
	}
}

func TestTrafficRenewalPreservesPerInboundWireguardPeer(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	past := time.Now().Add(-time.Hour).UnixMilli()
	want := make(map[int]model.Client)
	for i := 0; i < 2; i++ {
		client := model.Client{Email: "shared-wg", Enable: false, Reset: 30, ExpiryTime: past, PublicKey: wgTestSecretKey(), AllowedIPs: []string{fmt.Sprintf("10.%d.0.2/32", i)}, PreSharedKey: fmt.Sprintf("psk-%d", i)}
		inbound := mkInbound(t, 24795+i, model.WireGuard, clientsSettings(t, []model.Client{client}))
		if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
			t.Fatal(err)
		}
		want[inbound.Id] = client
	}
	if err := db.Create(&xray.ClientTraffic{Email: "shared-wg", Enable: false, Reset: 30, ExpiryTime: past}).Error; err != nil {
		t.Fatal(err)
	}
	probe := &trafficAddProbe{users: make(map[int]map[string]any)}
	useTestRuntimeManager(t).SetLocalRuntimeOverride(probe)
	if _, _, err := (&InboundService{}).AddTraffic(nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(probe.users) != len(want) {
		t.Fatalf("renewal updated %d inbounds, want %d", len(probe.users), len(want))
	}
	for id, client := range want {
		raw, err := json.Marshal(probe.users[id])
		if err != nil {
			t.Fatal(err)
		}
		var got model.Client
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.AllowedIPs, client.AllowedIPs) || got.PreSharedKey != client.PreSharedKey {
			t.Errorf("inbound %d inherited another inbound's peer address or key", id)
		}
	}
}

func TestTrafficRenewalDoesNotReplayOverLaterMutation(t *testing.T) {
	for _, mutation := range []string{"client-disable", "inbound-disable", "inbound-delete", "credential-rotation"} {
		t.Run(mutation, func(t *testing.T) {
			setupConflictDB(t)
			setRestartOnClientDisable(t, false)
			probe := &trafficAddProbe{}
			useTestRuntimeManager(t).SetLocalRuntimeOverride(probe)
			inbound := seedRenewableNeighbour(t, 24791, nil)
			svc, clients := &InboundService{}, &ClientService{}
			_, _, _, batch, err := svc.addTrafficLocked(nil, nil)
			if err != nil || batch == nil || len(batch.localPlans) != 1 {
				t.Fatalf("renewal did not create one delayed runtime operation: %+v, %v", batch, err)
			}
			switch mutation {
			case "client-disable":
				_, _, err = clients.SetClientEnableByEmail(svc, "y@stale", false)
			case "inbound-disable":
				_, err = svc.SetInboundEnable(inbound.Id, false)
			case "inbound-delete":
				_, err = svc.DelInbound(inbound.Id)
			case "credential-rotation":
				var record *model.ClientRecord
				record, err = clients.GetRecordByEmail(nil, "y@stale")
				if err == nil {
					updated := *record.ToClient()
					updated.ID = "aaaaaaaa-0000-0000-0000-00000000000c"
					_, err = clients.Update(svc, record.Id, updated, 0)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			probe.added = 0
			svc.applyTrafficMutationBatch(batch)
			if mutation == "credential-rotation" {
				if probe.added > 0 && probe.lastID != "aaaaaaaa-0000-0000-0000-00000000000c" {
					t.Fatal("delayed renewal restored the revoked credential")
				}
			} else if probe.added != 0 {
				t.Fatalf("delayed renewal replayed %d stale AddUser calls after %s", probe.added, mutation)
			}
		})
	}
}
