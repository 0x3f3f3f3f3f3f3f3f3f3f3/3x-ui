package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyResetBulkCommitsLegacyEnableWithItsCounters(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	email := "atomic-legacy-reset"
	inbound := seedLocalDisabledClient(t, svc, 24213, "", email, 1000, 0, 600, 500)
	db := database.GetDB()
	injected := errors.New("reject reset completion before commit")
	var boundaryErr error
	if err := db.Callback().Update().After("gorm:update").Register("test:reset-enable-boundary", func(tx *gorm.DB) {
		if tx.Statement.Table != "client_traffic_reset_batches" {
			return
		}
		var record model.ClientRecord
		var stored model.Inbound
		query := tx.Session(&gorm.Session{NewDB: true})
		if err := query.First(&record, "email = ?", email).Error; err != nil {
			boundaryErr = err
		} else if !record.Enable {
			boundaryErr = errors.New("reset completion preceded durable client enable")
		}
		if err := query.First(&stored, inbound.Id).Error; err != nil {
			boundaryErr = err
		} else {
			clients, err := (&InboundService{}).GetClients(&stored)
			if err != nil {
				boundaryErr = err
			} else if len(clients) != 1 || !clients[0].Enable {
				boundaryErr = errors.New("reset completion preceded durable inbound enable")
			}
		}
		tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.BulkResetTraffic(&InboundService{}, []string{email})
	if removeErr := db.Callback().Update().Remove("test:reset-enable-boundary"); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !errors.Is(err, injected) || boundaryErr != nil {
		t.Fatalf("reset cannot recover a crash after SQL commit: boundary=%v, error=%v", boundaryErr, err)
	}
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, "email = ?", email).Error; err != nil || traffic.Up != 600 || traffic.Down != 500 || traffic.Enable {
		t.Fatalf("failed commit did not roll back counters: %+v, %v", traffic, err)
	}
	assertEnableEverywhere(t, svc, &InboundService{}, inbound.Id, email, false)
}

func TestClientPolicyResetBulkPreservesRemoteInboundFields(t *testing.T) {
	setupPolicyLedgerDB(t)
	mgr := useTestRuntimeManager(t)
	svc := &ClientService{}
	inbound := seedLocalDisabledClient(t, svc, 24214, "", "remote-reset-fields", 1000, 0, 600, 500)
	const exactQuota int64 = 9007199254740993
	clients := []model.Client{{Email: "remote-reset-fields", ID: "11111111-1111-1111-1111-111111111111", SubID: "inbound-sub", TotalGB: exactQuota, ExpiryTime: 1900000000000, Enable: false}}
	raw, err := json.Marshal(map[string][]model.Client{"clients": clients})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(inbound).Update("settings", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	received := make(chan model.Client, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panel/api/inbounds/list", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": []map[string]any{{"id": 42, "tag": inbound.Tag}}})
	})
	mux.HandleFunc("POST /panel/api/clients/update/remote-reset-fields", func(w http.ResponseWriter, r *http.Request) {
		var client model.Client
		if err := json.NewDecoder(r.Body).Decode(&client); err != nil || r.URL.Query().Get("inboundIds") != "42" {
			http.Error(w, "bad client update", http.StatusBadRequest)
			return
		}
		received <- client
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	node := reconcileTestNode(t, server, "reset-fields", "all", nil)
	if err := database.GetDB().Model(inbound).Update("node_id", node.Id).Error; err != nil {
		t.Fatal(err)
	}
	mgr.SetRuntimeOverride(node.Id, runtime.NewRemote(node, nil))
	if affected, err := svc.BulkResetTrafficWithRequest(context.Background(), &InboundService{}, []string{clients[0].Email}, "remote-fields"); err != nil || affected != 1 {
		t.Fatalf("remote reset: affected=%d, %v", affected, err)
	}
	select {
	case client := <-received:
		if !client.Enable || client.TotalGB != exactQuota || client.ExpiryTime != 1900000000000 || client.SubID != "inbound-sub" {
			t.Fatalf("reset replayed stale record fields to remote: %+v", client)
		}
	default:
		t.Fatal("committed reset did not reach remote Runtime")
	}
	if err := database.GetDB().First(node, node.Id).Error; err != nil || !node.ConfigDirty {
		t.Fatalf("reset lost durable node reconciliation: %+v, %v", node, err)
	}
	if err := database.GetDB().First(inbound, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := (&InboundService{}).GetClients(inbound)
	if err != nil || len(stored) != 1 || stored[0].TotalGB != exactQuota || !stored[0].Enable {
		t.Fatalf("reset corrupted stored inbound fields: %+v, %v", stored, err)
	}
}
