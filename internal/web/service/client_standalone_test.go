package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientStandaloneCreatePreservesAccountBeforeTunnelAttachment(t *testing.T) {
	setupPolicyLedgerDB(t)
	var payload ClientCreatePayload
	if err := json.Unmarshal([]byte(`{"client":{"email":"standalone-owner","clientId":"7eb617c7-1945-4d02-a590-ab6012f410b5","enable":false,"totalGB":9007199254740993,"expiryTime":-86400000,"limitHwid":3,"comment":"Before any listener","policy":{"uploadBytesPerSecond":1024,"downloadBytesPerSecond":2048,"multiplier":"1.5"}},"inboundIds":[]}`), &payload); err != nil {
		t.Fatal(err)
	}
	clients, inbounds := &ClientService{}, &InboundService{}
	if restart, err := clients.Create(inbounds, &payload); err != nil || restart {
		t.Fatalf("standalone create: restart=%v err=%v", restart, err)
	}
	record, err := clients.GetRecordByEmail(nil, "standalone-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(record.StableID); err != nil || record.StableID == "7eb617c7-1945-4d02-a590-ab6012f410b5" {
		t.Fatalf("server did not allocate an independent stable identity: %q %v", record.StableID, err)
	}
	if _, err := uuid.Parse(record.SubID); err != nil {
		t.Fatalf("missing generated subscription ID: %q %v", record.SubID, err)
	}
	if record.Enable || record.TotalGB != 9007199254740993 || record.ExpiryTime != -86400000 || record.LimitHwid != 3 || record.Comment != "Before any listener" || record.CreatedAt == 0 || record.UpdatedAt < record.CreatedAt {
		t.Fatalf("account fields not retained: %+v", record)
	}
	if record.Policy == nil || *record.Policy != (model.ClientPolicyOptions{UploadBytesPerSecond: 1024, DownloadBytesPerSecond: 2048, Multiplier: "1.5"}) || record.DesiredPolicyVersion != 0 {
		t.Fatalf("saved policy changed or appeared applied before attachment: %+v", record)
	}
	if record.UUID != "" || record.Password != "" || record.Auth != "" || record.Secret != "" || record.PrivateKey != "" || record.PublicKey != "" {
		t.Fatal("standalone creation minted credentials without selecting a protocol")
	}
	for _, table := range []any{&model.Inbound{}, &model.ClientInbound{}, &xray.ClientTraffic{}, &model.ClientPolicyReceipt{}} {
		var count int64
		if err := database.GetDB().Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("standalone creation changed %T: count=%d err=%v", table, count, err)
		}
	}
	page, err := clients.ListPaged(inbounds, &SettingService{}, ClientPageParams{Search: record.Email})
	if err != nil || len(page.Items) != 1 || page.Items[0].StableID != record.StableID || len(page.Items[0].InboundIds) != 0 || page.Items[0].Traffic != nil {
		t.Fatalf("standalone owner unavailable in paged picker: %+v %v", page, err)
	}
	exported, err := clients.ExportAll()
	if err != nil || len(exported) != 1 || exported[0].Client.Enable || exported[0].Client.TotalGB != 9007199254740993 || exported[0].LimitHwid != 3 || len(exported[0].InboundIds) != 0 || exported[0].Client.Policy == nil || *exported[0].Client.Policy != *record.Policy {
		t.Fatalf("unattached account export lost configuration: %+v %v", exported, err)
	}
	request := tunnelOwnerRequest(t, record.StableID, nil)
	inbound, _, err := inbounds.AddInbound(request)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := clients.ListForInbound(nil, inbound.Id)
	if err != nil || len(bound) != 1 || bound[0].Email != record.Email || bound[0].Enable || bound[0].Policy == nil || *bound[0].Policy != *record.Policy {
		t.Fatalf("Tunnel did not attach the existing account intact: %+v %v", bound, err)
	}
	got, err := clients.GetRecordByEmail(nil, record.Email)
	if err != nil || got.StableID != record.StableID {
		t.Fatalf("Tunnel attachment replaced the account identity: %+v %v", got, err)
	}
}

func TestClientStandaloneCreateRejectsDuplicateIdentityWithoutOverwrite(t *testing.T) {
	setupPolicyLedgerDB(t)
	clients := &ClientService{}
	original := &model.ClientRecord{Email: "Existing-owner", SubID: "kept-sub", Comment: "keep me", Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
	if err := database.GetDB().Create(original).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ email, subID, errorPart string }{
		{"Existing-owner", "kept-sub", "email already in use"},
		{"existing-owner", "different-sub", "email already in use"},
		{"new-owner", "kept-sub", "subId already in use"},
	} {
		restart, err := clients.Create(&InboundService{}, &ClientCreatePayload{Client: model.Client{Email: tc.email, SubID: tc.subID, Comment: "overwrite"}})
		if restart || err == nil || !strings.Contains(err.Error(), tc.errorPart) {
			t.Fatalf("duplicate %q/%q: restart=%v err=%v", tc.email, tc.subID, restart, err)
		}
	}
	var rows []model.ClientRecord
	if err := database.GetDB().Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].StableID != original.StableID || rows[0].Comment != "keep me" || rows[0].Policy == nil || rows[0].Policy.Multiplier != "2" {
		t.Fatalf("rejected create changed existing account: %+v %v", rows, err)
	}
}

func TestClientStandaloneCreateRollsBackDisabledCorrection(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	injected := errors.New("standalone disabled write failed")
	const hook = "test:standalone-disabled"
	if err := db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hook) })
	restart, err := (&ClientService{}).Create(&InboundService{}, &ClientCreatePayload{Client: model.Client{Email: "rollback-owner", Enable: false}})
	if restart || !errors.Is(err, injected) {
		t.Fatalf("disabled correction failed incorrectly: restart=%v err=%v", restart, err)
	}
	var count int64
	if err := db.Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed create left a usable account: count=%d err=%v", count, err)
	}
}
