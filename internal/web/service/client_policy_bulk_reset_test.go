package service

import (
	"context"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyResetBulkPinsMembershipBeforeCoreRecovery(t *testing.T) {
	ids := resetBatchFixture(t)
	db := database.GetDB()
	previous, _ := xrayState.snapshot()
	xrayState.replace(nil)
	defer xrayState.replace(previous)
	svc := &ClientService{}
	if _, err := svc.BulkResetTrafficWithRequest(context.Background(), &InboundService{}, []string{"batch-a", "batch-b"}, "pinned-members"); err == nil || !strings.Contains(err.Error(), "managed core is not ready") {
		t.Fatalf("stopped managed batch fell back to counter zeroing: %v", err)
	}
	var operation model.ClientTrafficResetBatch
	if err := db.First(&operation, "request_id = ?", "pinned-members").Error; err != nil || operation.Applied {
		t.Fatalf("failed operation lost its pending membership: %+v, %v", operation, err)
	}
	var owner model.ClientRecord
	if err := db.First(&owner, "email = ?", "batch-a").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&owner).Update("email", "renamed-batch-a").Error; err != nil {
		t.Fatal(err)
	}
	replacement := model.ClientRecord{Email: "batch-a"}
	if err := db.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "batch-a").Updates(map[string]any{"up": 123, "down": 456}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BulkResetTrafficWithRequest(context.Background(), &InboundService{}, []string{"batch-b", "batch-a"}, "pinned-members"); err == nil || !strings.Contains(err.Error(), "managed core is not ready") {
		t.Fatalf("retry rebound the original email: %v", err)
	}
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, "email = ?", "batch-a").Error; err != nil || traffic.Up != 123 || traffic.Down != 456 {
		t.Fatalf("retry reset replacement identity usage: %+v, %v", traffic, err)
	}
	if _, err := svc.BulkResetTrafficWithRequest(context.Background(), &InboundService{}, []string{"batch-a"}, "pinned-members"); err == nil || !strings.Contains(err.Error(), "different reset selection") {
		t.Fatalf("request key accepted a different explicit selection: %v", err)
	}
	for _, id := range ids {
		if total := policyLedgerTotal(t, id); total.RawUpload != 3 || total.BilledBytes != 1 || total.UncertainBytes != 7 {
			t.Fatalf("failed batch changed lifetime: %+v", total)
		}
	}
}

func TestClientPolicyResetAllRetryExcludesNewClientsAndNewUsage(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	first := model.ClientRecord{Email: "original-reset-client"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: first.Email, Up: 11, Down: 22}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &ClientService{}
	if _, err := svc.ResetAllTrafficsWithRequest(context.Background(), "reset-all-retry"); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", first.Email).Updates(map[string]any{"up": 33, "down": 44}).Error; err != nil {
		t.Fatal(err)
	}
	second := model.ClientRecord{Email: "created-after-reset"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: second.Email, Up: 55, Down: 66}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetAllTrafficsWithRequest(context.Background(), "reset-all-retry"); err != nil {
		t.Fatal(err)
	}
	var traffic []xray.ClientTraffic
	if err := db.Order("email").Find(&traffic).Error; err != nil || len(traffic) != 2 {
		t.Fatalf("read retried reset: %+v, %v", traffic, err)
	}
	if traffic[0].Up != 55 || traffic[0].Down != 66 || traffic[1].Up != 33 || traffic[1].Down != 44 {
		t.Fatalf("reset-all retry recaptured membership or new usage: %+v", traffic)
	}
	if _, err := svc.ResetAllTrafficsWithRequest(context.Background(), "reset-all-new"); err != nil {
		t.Fatal(err)
	}
	if err := db.Find(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range traffic {
		if row.Up != 0 || row.Down != 0 {
			t.Fatalf("a genuinely new reset omitted an existing client: %+v", row)
		}
	}
}

func TestClientPolicyResetInboundBatchUsesLinksAndKeepsItsOriginalMembers(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owned := mkInbound(t, 24211, model.Tunnel, `{}`)
	other := mkInbound(t, 24212, model.Tunnel, `{}`)
	client := model.ClientRecord{Email: "linked-inbound-reset"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: owned.Id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, InboundId: other.Id, Up: 11, Down: 22}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: "unrelated-traffic", InboundId: owned.Id, Up: 33}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &ClientService{}
	if err := svc.ResetAllClientTrafficsWithRequest(context.Background(), &InboundService{}, owned.Id, "inbound-retry"); err != nil {
		t.Fatal(err)
	}
	if row := trafficOf(t, client.Email); row.Up != 0 || row.Down != 0 {
		t.Fatalf("inbound reset trusted stale traffic linkage: %+v", row)
	}
	if row := trafficOf(t, "unrelated-traffic"); row.Up != 33 {
		t.Fatalf("inbound reset changed an unlinked client: %+v", row)
	}
	newClient := model.ClientRecord{Email: "attached-after-reset"}
	if err := db.Create(&newClient).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: newClient.Id, InboundId: owned.Id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: newClient.Email, InboundId: owned.Id, Up: 29}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("up", 13).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(owned).Update("last_traffic_reset_time", 123).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetAllClientTrafficsWithRequest(context.Background(), &InboundService{}, owned.Id, "inbound-retry"); err != nil {
		t.Fatal(err)
	}
	if row := trafficOf(t, client.Email); row.Up != 13 {
		t.Fatalf("inbound retry cleared later usage: %+v", row)
	}
	if row := trafficOf(t, newClient.Email); row.Up != 29 {
		t.Fatalf("inbound retry included a later attachment: %+v", row)
	}
	if err := db.First(owned, owned.Id).Error; err != nil || owned.LastTrafficResetTime != 123 {
		t.Fatalf("inbound retry rewrote its last reset time: %+v, %v", owned, err)
	}
}
