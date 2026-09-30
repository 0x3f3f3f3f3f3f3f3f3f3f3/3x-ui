package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestClientPolicyRemoteScopeMetadataPreservesStoredPolicy(t *testing.T) {
	setupPolicyLedgerDB(t)
	mgr := useTestRuntimeManager(t)
	svc := &ClientService{}
	client := model.Client{Email: "remote-policy-metadata", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", SubID: "remote-policy-metadata", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	inbound := mkInbound(t, 24089, model.VLESS, clientsSettings(t, []model.Client{client}))
	if err := svc.SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	record := lookupClientRecord(t, client.Email)
	received := make(chan model.Client, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panel/api/inbounds/list", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": []map[string]any{{"id": 42, "tag": inbound.Tag}}})
	})
	mux.HandleFunc("POST /panel/api/clients/update/remote-policy-metadata", func(w http.ResponseWriter, r *http.Request) {
		var updated model.Client
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			http.Error(w, "bad update", http.StatusBadRequest)
			return
		}
		received <- updated
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	node := reconcileTestNode(t, server, "remote-policy-metadata", "all", nil)
	if err := database.GetDB().Model(inbound).Update("node_id", node.Id).Error; err != nil {
		t.Fatal(err)
	}
	mgr.SetRuntimeOverride(node.Id, runtime.NewRemote(node, nil))
	updated := client
	updated.Policy, updated.Comment = nil, "metadata only"
	if _, err := svc.Update(&InboundService{}, record.Id, updated, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case wire := <-received:
		if wire.Policy != nil || wire.Comment != updated.Comment {
			t.Error("metadata request replayed inherited policy or lost its edit")
		}
	default:
		t.Fatal("metadata update did not reach the remote runtime")
	}
	stored := lookupClientRecord(t, client.Email)
	if !reflect.DeepEqual(stored.Policy, client.Policy) || stored.Comment != updated.Comment {
		t.Error("wire omission changed stored policy or lost metadata")
	}
	if err := database.GetDB().First(inbound, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	storedClients, err := (&InboundService{}).GetClients(inbound)
	if err != nil || len(storedClients) != 1 || !reflect.DeepEqual(storedClients[0].Policy, client.Policy) {
		t.Fatalf("wire omission changed persisted inbound policy: %v", err)
	}
}

func TestClientPolicyRemoteScopeRejectsBeforeFanout(t *testing.T) {
	for _, operation := range []string{"create", "reuse", "update-filtered", "attach"} {
		t.Run(operation, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			local := mkInbound(t, 24083, model.VLESS, `{"decryption":"none","clients":[]}`)
			remote := mkInbound(t, 24084, model.VLESS, `{"decryption":"none","clients":[]}`)
			if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
			client := model.Client{Email: "scope-before-write", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", SubID: "scope-before-write", Enable: true}
			policy := &model.ClientPolicyOptions{Multiplier: "2"}
			svc, inbounds := &ClientService{}, &InboundService{}
			var record *model.ClientRecord
			if operation != "create" {
				record = client.ToRecord()
				if operation == "attach" {
					record.Policy = policy.Clone()
				}
				if err := db.Create(record).Error; err != nil {
					t.Fatal(err)
				}
				if operation != "attach" {
					if err := db.Create(&model.ClientInbound{ClientId: record.Id, InboundId: remote.Id}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			var err error
			switch operation {
			case "create":
				client.Policy = policy
				_, err = svc.Create(inbounds, &ClientCreatePayload{Client: client, InboundIds: []int{local.Id, remote.Id}})
			case "reuse":
				client.Policy = policy
				_, err = svc.Create(inbounds, &ClientCreatePayload{Client: client, InboundIds: []int{local.Id}})
			case "update-filtered":
				client.Policy, client.Comment = policy, "must roll back"
				_, err = svc.Update(inbounds, record.Id, client, 0, local.Id)
			case "attach":
				_, err = svc.Attach(inbounds, record.Id, []int{local.Id, remote.Id})
			}
			if !errors.Is(err, ErrClientPolicyLedger) {
				t.Errorf("%s policy operation = %v, want explicit unsupported remote scope", operation, err)
			}
			var localAfter model.Inbound
			if err := db.First(&localAfter, local.Id).Error; err != nil {
				t.Fatal(err)
			}
			if localAfter.Settings != local.Settings {
				t.Error("rejected remote policy changed a local inbound before fanout completed")
			}
			var got []model.ClientRecord
			if err := db.Where("email = ?", client.Email).Find(&got).Error; err != nil {
				t.Fatal(err)
			}
			if record == nil {
				if len(got) != 0 {
					t.Error("rejected create left a client record")
				}
			} else if len(got) != 1 || !reflect.DeepEqual(got[0].Policy, record.Policy) || got[0].Comment != record.Comment {
				t.Error("rejected request changed the shared client record")
			}
		})
	}
}

func TestClientPolicyRemoteScopeRejectsMirrorAtomically(t *testing.T) {
	for _, policyJSON := range []string{`{"multiplier":"2"}`, `{"multiplier":"0"}`, `{"uploadBytesPerSecond":"invalid"}`} {
		for _, existing := range []bool{false, true} {
			t.Run(map[bool]string{false: "new-inbound", true: "existing-inbound"}[existing]+policyJSON, func(t *testing.T) {
				setupPolicyLedgerDB(t)
				db := database.GetDB()
				seedNodeRow(t, db, &model.Node{Id: 1, Name: "scope-node", Enable: true})
				snapshot := &model.Inbound{
					Tag: "n1-scope", Enable: true, Protocol: model.VLESS, Port: 24087,
					Settings: `{"clients":[{"email":"remote-policy","enable":true,"policy":` + policyJSON + `}]}`,
				}
				if existing {
					prior := *snapshot
					id := 1
					prior.NodeID, prior.Settings = &id, `{"clients":[]}`
					if err := db.Create(&prior).Error; err != nil {
						t.Fatal(err)
					}
				}
				_, err := (&InboundService{}).setRemoteTrafficLocked(1, &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{snapshot}}, false, false)
				if err == nil {
					t.Errorf("mirror result = %v, want unsupported scope", err)
				}
				var rows []model.Inbound
				if err := db.Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				if !existing && len(rows) != 0 || existing && (len(rows) != 1 || rows[0].Settings != `{"clients":[]}`) {
					t.Error("rejected mirror committed raw inbound settings")
				}
			})
		}
	}
}

func TestClientPolicyRemoteScopeMirrorChecksBeforeIdentityFilters(t *testing.T) {
	for _, scenario := range []string{"foreign", "tombstone"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			seedNodeRow(t, db, &model.Node{Id: 1, Name: "scope-node", Enable: true})
			client := model.Client{Email: "filtered-policy", Enable: true}
			if scenario == "foreign" {
				local := mkInbound(t, 24091, model.VLESS, `{}`)
				if err := (&ClientService{}).SyncInbound(nil, local.Id, []model.Client{client}); err != nil {
					t.Fatal(err)
				}
			} else {
				tombstoneClientEmail(client.Email)
				t.Cleanup(func() { withdrawClientTombstones(client.Email) })
			}
			client.Policy = &model.ClientPolicyOptions{Multiplier: "2"}
			snapshot := &model.Inbound{Tag: "n1-filtered-policy", Enable: true, Protocol: model.VLESS, Port: 24090, Settings: clientsSettings(t, []model.Client{client})}
			_, err := (&InboundService{}).setRemoteTrafficLocked(1, &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{snapshot}}, false, false)
			if !errors.Is(err, ErrClientPolicyLedger) {
				t.Errorf("filtered raw policy was adopted: %v", err)
			}
			var count int64
			if err := db.Model(&model.Inbound{}).Where("node_id = ?", 1).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Error("filtered raw policy reached saved remote settings")
			}
		})
	}
}

func TestClientPolicyRemoteScopeUnboundUpdateSerializesWithAttachment(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("requires PostgreSQL row locking")
	}
	client := model.Client{Email: "scope-racing-update", Enable: true}
	record := client.ToRecord()
	if err := db.Create(record).Error; err != nil {
		t.Fatal(err)
	}
	remote := mkInbound(t, 24088, model.VLESS, `{}`)
	if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	const callback = "test:hold-policy-scope-attachment"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_inbounds" && paused.CompareAndSwap(false, true) {
			close(locked)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	attached, updated := make(chan error, 1), make(chan error, 1)
	go func() { attached <- (&ClientService{}).SyncInbound(nil, remote.Id, []model.Client{client}) }()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("attachment did not acquire the client lock")
	}
	go func() {
		updatedClient := client
		updatedClient.Policy = &model.ClientPolicyOptions{Multiplier: "2"}
		_, err := (&ClientService{}).Update(&InboundService{}, record.Id, updatedClient, 0)
		updated <- err
	}()
	var updateErr error
	finished := false
	select {
	case updateErr = <-updated:
		finished = true
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-attached; err != nil {
		t.Fatalf("legacy attachment: %v", err)
	}
	if !finished {
		updateErr = <-updated
	}
	if !errors.Is(updateErr, ErrClientPolicyLedger) {
		t.Fatalf("unbound update raced past remote attachment: %v", updateErr)
	}
	var current model.ClientRecord
	if err := db.First(&current, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	if current.Policy != nil {
		t.Error("racing update stored an unsupported remote policy")
	}
}

func TestClientPolicyRemoteScopeSyncBoundary(t *testing.T) {
	for _, scenario := range []string{"new-remote", "attach-unprepared", "change-local-with-remote", "change-remote", "preserve-remote", "legacy-remote"} {
		t.Run(scenario, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			local := mkInbound(t, 24085, model.VLESS, `{}`)
			remote := mkInbound(t, 24086, model.VLESS, `{}`)
			if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
			client := model.Client{Email: "scope-sync", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
			var prior *model.ClientRecord
			if scenario != "new-remote" && scenario != "legacy-remote" {
				prior = client.ToRecord()
				if err := db.Create(prior).Error; err != nil {
					t.Fatal(err)
				}
				if scenario != "attach-unprepared" {
					if err := db.Create(&model.ClientInbound{ClientId: prior.Id, InboundId: remote.Id}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			target := remote.Id
			switch scenario {
			case "change-local-with-remote":
				target = local.Id
				client.Policy.Multiplier = "3"
			case "change-remote":
				client.Policy.Multiplier = "3"
			case "attach-unprepared", "legacy-remote":
				client.Policy = nil
			}
			client.Comment = "unrelated metadata"
			err := (&ClientService{}).SyncInbound(nil, target, []model.Client{client})
			allowed := scenario == "preserve-remote" || scenario == "legacy-remote"
			if allowed && err != nil {
				t.Fatalf("existing compatible operation: %v", err)
			}
			if !allowed && !errors.Is(err, ErrClientPolicyLedger) {
				t.Errorf("scope guard = %v, want explicit unsupported remote scope", err)
			}
			var got []model.ClientRecord
			if err := db.Where("email = ?", client.Email).Find(&got).Error; err != nil {
				t.Fatal(err)
			}
			if allowed {
				if len(got) != 1 || !reflect.DeepEqual(got[0].Policy, client.Policy) || got[0].Comment != client.Comment {
					t.Error("compatible edit failed to preserve policy and metadata")
				}
			} else if prior == nil {
				if len(got) != 0 {
					t.Error("rejected sync created a record")
				}
			} else if len(got) != 1 || !reflect.DeepEqual(got[0].Policy, prior.Policy) || got[0].Comment != prior.Comment {
				t.Error("rejected sync changed the stored policy or metadata")
			}
		})
	}
}
