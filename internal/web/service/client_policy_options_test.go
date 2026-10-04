package service

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientPolicyOptionsSurviveLegacyEditsAndExplicitReset(t *testing.T) {
	setupPolicyLedgerDB(t)
	record := model.ClientRecord{Email: "policy-options", Enable: true}
	if err := database.GetDB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	svc := ClientService{}
	for _, step := range []struct {
		input      string
		up, down   int64
		multiplier string
	}{
		{`{"email":"policy-options","enable":true,"policy":{"uploadBytesPerSecond":262144,"downloadBytesPerSecond":1048576,"multiplier":"0.5"}}`, 262144, 1048576, "0.5"},
		{`{"email":"policy-options","enable":true,"comment":"legacy edit"}`, 262144, 1048576, "0.5"},
		{`{"email":"policy-options","enable":true,"policy":{"uploadBytesPerSecond":0,"downloadBytesPerSecond":0,"multiplier":"1"}}`, 0, 0, "1"},
	} {
		var update model.Client
		if err := json.Unmarshal([]byte(step.input), &update); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Update(&InboundService{}, record.Id, update, 0); err != nil {
			t.Fatal(err)
		}
		stored, err := svc.GetByID(record.Id)
		if err != nil {
			t.Fatal(err)
		}
		for _, shape := range []any{stored, stored.ToClient()} {
			raw, err := json.Marshal(shape)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Policy *struct {
					Upload     int64  `json:"uploadBytesPerSecond"`
					Download   int64  `json:"downloadBytesPerSecond"`
					Multiplier string `json:"multiplier"`
				} `json:"policy"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response.Policy == nil || response.Policy.Upload != step.up || response.Policy.Download != step.down || response.Policy.Multiplier != step.multiplier {
				t.Fatalf("client policy lost across persistence/export: %s", raw)
			}
		}
		if stored.StableID != record.StableID {
			t.Fatal("policy edit changed client identity")
		}
	}
}

func TestClientPolicyOptionsRejectInvalidWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := ClientService{}
	ib := mkInbound(t, 43191, model.VLESS, `{"clients":[]}`)
	for _, policy := range []string{
		`{"multiplier":"0"}`, `{"multiplier":"1.0000001"}`, `{"multiplier":"1000.1"}`,
		`{"multiplier":"NaN"}`, `{"uploadBytesPerSecond":-1}`, `{"downloadBytesPerSecond":1099511627777}`,
		`{"scope":""}`, `{"scope":"all"}`, `{"scope":"GLOBAL"}`, `{"scope":"global "}`,
	} {
		var client model.Client
		if err := json.Unmarshal([]byte(`{"email":"invalid-policy","enable":true,"policy":`+policy+`}`), &client); err != nil {
			t.Fatal(err)
		}
		err := svc.SyncInbound(nil, ib.Id, []model.Client{client})
		if !errors.Is(err, clientpolicy.ErrInvalidPolicy) {
			t.Fatalf("invalid policy write returned %v: %s", err, policy)
		}
		var count int64
		if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", client.Email).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rejected policy changed records: %d, %v", count, err)
		}
	}
}

func TestClientPolicyOptionsRemainInAttachedClientSettings(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc, inboundSvc := ClientService{}, InboundService{}
	client := model.Client{
		ID: "9c86b631-8bb4-4789-89c9-4d4d565923f0", Email: "attached-policy", Enable: true,
		Policy: &model.ClientPolicyOptions{UploadBytesPerSecond: 262144, Multiplier: "1.5"},
	}
	ib := mkInbound(t, 43192, model.VLESS, clientsSettings(t, []model.Client{client}))
	if err := svc.SyncInbound(nil, ib.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	record, err := svc.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	client.Policy = nil
	client.Comment = "legacy metadata edit"
	if _, err := svc.Update(&inboundSvc, record.Id, client, 0); err != nil {
		t.Fatal(err)
	}
	stored, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := inboundSvc.GetClients(stored)
	if err != nil || len(clients) != 1 || clients[0].Policy == nil || clients[0].Policy.Multiplier != "1.5" || clients[0].Policy.UploadBytesPerSecond != 262144 {
		t.Fatalf("legacy edit erased policy from attached settings: %+v, %v", clients, err)
	}
	if err := database.GetDB().Model(&record).Update("policy_multiplier", "2.5").Error; err != nil {
		t.Fatal(err)
	}
	client.Comment = "edit through inbound endpoint"
	data := &model.Inbound{Id: ib.Id, Settings: clientsSettings(t, []model.Client{client})}
	if _, err := svc.UpdateInboundClient(&inboundSvc, data, client.Email); err != nil {
		t.Fatal(err)
	}
	stored, err = inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatal(err)
	}
	clients, err = inboundSvc.GetClients(stored)
	if err != nil || len(clients) != 1 || clients[0].Policy == nil || clients[0].Policy.Multiplier != "2.5" {
		t.Fatalf("stale inbound settings overwrote the authoritative policy: %+v, %v", clients, err)
	}
}

func TestClientPolicyOmissionPreservesConcurrentPolicyEdit(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	record := model.ClientRecord{Email: "concurrent-policy", Enable: true, Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	read, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	if err := db.Callback().Query().After("gorm:query").Register("test:pause-policy-read", func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" {
			once.Do(func() { close(read); <-resume })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:pause-policy-read") })
	done := make(chan error, 1)
	go func() {
		_, err := (&ClientService{}).Update(&InboundService{}, record.Id, model.Client{Email: record.Email, Enable: true, Comment: "legacy edit"}, 0)
		done <- err
	}()
	<-read
	err := db.Model(&model.ClientRecord{}).Where("id = ?", record.Id).Update("policy_multiplier", "2.5").Error
	close(resume)
	updateErr := <-done
	if err != nil || updateErr != nil {
		t.Fatalf("concurrent edits failed: %v, %v", err, updateErr)
	}
	var stored model.ClientRecord
	if err := db.First(&stored, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Policy == nil || stored.Policy.Multiplier != "2.5" || stored.Comment != "legacy edit" {
		t.Fatalf("omitted field overwrote concurrent policy: %+v", stored)
	}
}
