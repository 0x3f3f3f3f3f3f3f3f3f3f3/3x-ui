package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLegacyTrafficSettlementExceedsSQLParameterLimit(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	StartTrafficWriter()
	t.Cleanup(StopTrafficWriter)
	const count = 100001
	rows := make([]xray.ClientTraffic, count)
	deltas := make([]*xray.ClientTraffic, count)
	for i := range rows {
		email := fmt.Sprintf("large-%06d", i)
		rows[i] = xray.ClientTraffic{Email: email, Up: 11, Down: 19, ExpiryTime: -86400000}
		deltas[i] = &xray.ClientTraffic{Email: email, Up: 3, Down: 7}
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	batch := &xray.TrafficBatch{ProcessID: "large-child", Sequence: 1, ID: "large-final", Final: true, ClientTraffics: deltas}
	svc := &XrayService{}
	for range 2 {
		if err := svc.settleLegacyTrafficBatch(batch); err != nil {
			t.Fatalf("large final settlement: %v", err)
		}
	}
	var wrong, stored int64
	if err := db.Model(&xray.ClientTraffic{}).Where("up <> 14 OR down <> 26 OR expiry_time <> -86400000").Count(&wrong).Error; err != nil || wrong != 0 {
		t.Fatalf("large final settlement missed or duplicated %d clients: %v", wrong, err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Count(&stored).Error; err != nil || stored != 100001 {
		t.Fatalf("large settlement lost rows: %d %v", stored, err)
	}
	var receipt model.LegacyTrafficReceipt
	if err := db.First(&receipt, "process_id = ?", "large-child").Error; err != nil || receipt.Sequence != 1 || receipt.BatchID != "large-final" {
		t.Fatalf("large final receipt: %+v %v", receipt, err)
	}
}

func TestLegacyTrafficSettlementLateWriteRollsBackAllChunks(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	StartTrafficWriter()
	t.Cleanup(StopTrafficWriter)
	rows := make([]xray.ClientTraffic, 1001)
	deltas := make([]*xray.ClientTraffic, len(rows))
	for i := range rows {
		email := fmt.Sprintf("late-%06d", i)
		rows[i] = xray.ClientTraffic{Email: email, Up: 11, Down: 19}
		deltas[i] = &xray.ClientTraffic{Email: email, Up: 3, Down: 7}
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("last client write failed")
	const hook = "test:late-traffic-write"
	if err := db.Callback().Raw().Before("gorm:raw").Register(hook, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE client_traffics") && slices.ContainsFunc(tx.Statement.Vars, func(v any) bool { return v == "late-001000" }) {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Raw().Remove(hook) })
	deltas = append(deltas, &xray.ClientTraffic{Email: "late-000000", Up: 5, Down: 11})
	batch := &xray.TrafficBatch{ProcessID: "late-child", Sequence: 1, ID: "late-final", Final: true, ClientTraffics: deltas}
	svc := &XrayService{}
	if err := svc.settleLegacyTrafficBatch(batch); !errors.Is(err, injected) {
		t.Fatalf("late failure: %v", err)
	}
	if err := db.Callback().Raw().Remove(hook); err != nil {
		t.Fatal(err)
	}
	var wrong, receipts int64
	if err := db.Model(&xray.ClientTraffic{}).Where("up <> 11 OR down <> 19").Count(&wrong).Error; err != nil || wrong != 0 {
		t.Fatalf("late failure partially committed %d clients: %v", wrong, err)
	}
	if err := db.Model(&model.LegacyTrafficReceipt{}).Count(&receipts).Error; err != nil || receipts != 0 {
		t.Fatalf("late failure committed receipt: %d %v", receipts, err)
	}
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatalf("retry late batch: %v", err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email <> ? AND (up <> 14 OR down <> 26)", "late-000000").Count(&wrong).Error; err != nil || wrong != 0 {
		t.Fatalf("retry missed or duplicated %d clients: %v", wrong, err)
	}
	var duplicate xray.ClientTraffic
	if err := db.Where("email = ?", "late-000000").First(&duplicate).Error; err != nil || duplicate.Up != 16 || duplicate.Down != 30 {
		t.Fatalf("duplicate email across SQL chunks changed settlement: %+v %v", duplicate, err)
	}
}

func TestLegacyTrafficFirstUseManyInbounds(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	client := model.Client{Email: "many-inbounds", ID: "f78c22b7-04b7-4ae1-9f1c-f4644c28aa43", Enable: true, ExpiryTime: -86400000}
	record := model.ClientRecord{Email: client.Email, UUID: client.ID, Enable: true, ExpiryTime: client.ExpiryTime}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	inbounds := make([]model.Inbound, 3001)
	settings := clientsSettings(t, []model.Client{client})
	for i := range inbounds {
		inbounds[i] = model.Inbound{UserId: 1, Tag: fmt.Sprintf("first-use-%04d", i), Port: 40000 + i, Enable: true, Protocol: model.VLESS, Settings: settings}
	}
	if err := db.CreateInBatches(inbounds, 400).Error; err != nil {
		t.Fatal(err)
	}
	links := make([]model.ClientInbound, len(inbounds))
	for i := range links {
		links[i] = model.ClientInbound{ClientId: record.Id, InboundId: inbounds[i].Id}
	}
	if err := db.CreateInBatches(links, 400).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: inbounds[0].Id, Enable: true, ExpiryTime: client.ExpiryTime}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &XrayService{}
	batch := &xray.TrafficBatch{ProcessID: "many-inbounds-child", Sequence: 1, ID: "many-inbounds-final", Final: true, ClientTraffics: []*xray.ClientTraffic{{Email: client.Email, Up: 3, Down: 7}}}
	if err := svc.settleLegacyTrafficBatch(batch); err != nil {
		t.Fatalf("first use across many listeners: %v", err)
	}
	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", client.Email).First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	if traffic.Up != 3 || traffic.Down != 7 || traffic.ExpiryTime <= 0 {
		t.Fatalf("first use usage/duration: %+v", traffic)
	}
	if err := db.First(&record, record.Id).Error; err != nil || record.ExpiryTime != traffic.ExpiryTime {
		t.Fatalf("canonical first use differs: %+v %v", record, err)
	}
	var count int64
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ?", record.Id).Count(&count).Error; err != nil || count != 3001 {
		t.Fatalf("first use lost client memberships: %d %v", count, err)
	}
	for _, inbound := range inbounds {
		var stored model.Inbound
		if err := db.First(&stored, inbound.Id).Error; err != nil {
			t.Fatal(err)
		}
		clients, err := (&InboundService{}).GetClients(&stored)
		if err != nil || len(clients) != 1 || clients[0].ExpiryTime != traffic.ExpiryTime {
			t.Fatalf("inbound %d has stale first use: %+v %v", stored.Id, clients, err)
		}
	}
}
