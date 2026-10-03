package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// A successful SQL/core renewal without the complete original witness cannot
// recover its expiry/count/window after restoring an older SQL snapshot.
func TestManagedAuthorityRenewalRetainsCompleteOriginalWitnessAndWindow(t *testing.T) {
	svc, inbound, client, _ := setupManagedActivationService(t)
	if err := (&SettingService{}).saveSetting("timeLocation", "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	past := time.Now().Add(-time.Hour).UnixMilli()
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("stable_id = ?", client.StableID).
		Updates(map[string]any{"expiry_time": past, "reset": 1, "reset_max": 2}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	var current model.ClientRecord
	if err := database.GetDB().First(&current, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	traffic := trafficOf(t, client.Email)
	if current.ExpiryTime != past+86400000 || traffic.ExpiryTime != current.ExpiryTime || traffic.ResetCount != 1 || !current.Enable {
		t.Fatalf("owned polling lost original renewal effects: expiry=%d traffic-expiry=%d count=%d enable=%t", current.ExpiryTime, traffic.ExpiryTime, traffic.ResetCount, current.Enable)
	}
	page, err := owner.state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 || !strings.HasPrefix(page[0].RequestID, "policy-renewal:") {
		t.Fatalf("actual owned renewal has no complete original operation witness: %d/%v", len(page), err)
	}
	capture, err := owner.state.Journal.LookupResetOperation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	done, err := owner.state.Journal.LookupResetCompletion(capture.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Schema  int
		ResetAt int64
		Effects []struct {
			ClientID         string
			BeforeExpiryTime int64
			AfterExpiryTime  int64
			BeforeResetCount int
			AfterResetCount  int
		}
		Resets []model.ClientPolicyReset
	}
	if err := json.Unmarshal([]byte(prepared.Snapshot), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Schema != 1 || payload.ResetAt <= 0 || len(payload.Effects) != 1 || len(payload.Resets) != 1 {
		t.Fatalf("incomplete renewal preparation: %+v", payload)
	}
	effect, reset := payload.Effects[0], payload.Resets[0]
	if effect.ClientID != client.StableID || effect.BeforeExpiryTime != past || effect.AfterExpiryTime != past+86400000 || effect.BeforeResetCount != 0 || effect.AfterResetCount != 1 {
		t.Fatalf("original expiry/count effect not protected: %+v", effect)
	}
	if reset.Id != 0 || reset.ClientID != client.StableID || !strings.HasPrefix(reset.RequestID, "renewal:") || reset.PolicyVersion != 2 || reset.CreatedAt != payload.ResetAt || reset.RawUpload != 104 || reset.RawDownload != 204 || reset.BilledBytes != 316 {
		t.Fatalf("original renewal window not protected: %+v", reset)
	}
	managedActivationEcho(t, flow, "next")
	for range 2 {
		if _, _, err := svc.GetXrayTraffic(); err != nil {
			t.Fatal(err)
		}
	}
	retained, err := owner.state.Journal.LookupResetPreparation(capture.RequestID)
	if err != nil || retained != prepared {
		t.Fatalf("ordinary renewal retry changed preparation: %v", err)
	}
	ack, err := owner.state.Journal.LookupResetCompletion(capture.RequestID)
	if err != nil || ack != done {
		t.Fatalf("ordinary renewal retry changed completion: %v", err)
	}
	traffic = trafficOf(t, client.Email)
	if traffic.ExpiryTime != past+86400000 || traffic.ResetCount != 1 {
		t.Fatalf("ordinary retry spent another allowance: expiry=%d count=%d", traffic.ExpiryTime, traffic.ResetCount)
	}
	managedActivationEcho(t, flow, "stay")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || account.WindowBaseline != 316 || account.WindowUsed != 32 {
		t.Fatalf("renewal retry lost actual2x lifetime/original window: %+v/%v", account, err)
	}
}
