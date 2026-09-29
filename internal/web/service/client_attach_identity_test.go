package service

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func attachIdentityFixture(t *testing.T, protocol model.Protocol, subID string) (*model.Inbound, *model.Inbound, model.ClientRecord) {
	t.Helper()
	setupConflictDB(t)
	client := model.Client{Email: "attach-canonical", ID: uuid.NewString(), Password: "owned-attach-password", SubID: subID, Enable: true}
	source := &model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 31820, Settings: clientsSettings(t, []model.Client{client})}
	target := &model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 31821, Settings: clientsSettings(t, nil)}
	for _, inbound := range []*model.Inbound{source, target} {
		if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
			t.Fatal(err)
		}
	}
	return source, target, lookupClientRecord(t, client.Email)
}

func TestAttachCanonicalClientWithoutSubscriptionID(t *testing.T) {
	testAttachCanonicalClientWithoutSubscriptionID(t, false)
}

func testAttachCanonicalClientWithoutSubscriptionID(t *testing.T, postgres bool) {
	t.Helper()
	for _, protocol := range []model.Protocol{model.Mieru, model.VLESS} {
		t.Run(string(protocol), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			source, target, before := attachIdentityFixture(t, protocol, "")
			if before.SubID != "" {
				t.Fatal("fixture already has a subscription ID")
			}
			clients, inbounds := &ClientService{}, &InboundService{}
			for range 2 {
				if _, err := clients.Attach(inbounds, before.Id, []int{target.Id}); err != nil {
					t.Fatalf("canonical attachment without a subscription ID failed: %v", err)
				}
			}
			after := lookupClientRecord(t, before.Email)
			if after.Id != before.Id || after.PolicyID != before.PolicyID || after.SubID != "" || after.Password != before.Password || after.UUID != before.UUID {
				t.Fatalf("attachment changed canonical identity: before=%+v after=%+v", before, after)
			}
			for _, inbound := range []*model.Inbound{source, target} {
				entry, _ := settingsClient(t, inbound.Id, before.Email)
				if entry.SubID != "" || entry.ID != before.UUID || entry.Password != before.Password {
					t.Fatalf("attachment changed stored credentials/subscription: %+v", entry)
				}
				var links int64
				if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", before.Id, inbound.Id).Count(&links).Error; err != nil || links != 1 {
					t.Fatalf("idempotent canonical membership: links=%d error=%v", links, err)
				}
			}
			data := &model.Inbound{Id: target.Id, Settings: clientsSettings(t, []model.Client{*before.ToClient()})}
			if _, err := clients.AddInboundClient(inbounds, data); err == nil || !strings.Contains(err.Error(), "Duplicate email") {
				t.Fatalf("ordinary add bypassed duplicate identity checks: %v", err)
			}
		})
	}
}

func TestAttachCanonicalClientWithoutSubscriptionID_Postgres(t *testing.T) {
	testAttachCanonicalClientWithoutSubscriptionID(t, true)
}

func TestAttachRejectsReplacedCanonicalIdentity(t *testing.T) {
	_, target, before := attachIdentityFixture(t, model.VLESS, "existing-subscription")
	db := database.GetDB()
	var replaced atomic.Bool
	newPolicy := uuid.NewString()
	callback := "test:replace-attachment-identity"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		client, ok := tx.Statement.Dest.(*model.ClientRecord)
		if !ok || client.Id != before.Id || !replaced.CompareAndSwap(false, true) {
			return
		}
		result := db.Exec("UPDATE clients SET policy_id = ? WHERE id = ?", newPolicy, before.Id)
		if result.Error != nil {
			tx.AddError(result.Error)
		} else if result.RowsAffected != 1 {
			tx.AddError(fmt.Errorf("fixture identity replacement changed %d rows", result.RowsAffected))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, err := (&ClientService{}).Attach(&InboundService{}, before.Id, []int{target.Id})
	if !replaced.Load() {
		t.Fatal("fixture failed to replace the selected identity before attachment commit")
	}
	if current := lookupClientRecord(t, before.Email); current.PolicyID != newPolicy {
		t.Fatalf("fixture did not persist the replacement identity: %s", current.PolicyID)
	}
	if !errors.Is(err, database.ErrUsageConflict) {
		t.Fatalf("attachment did not reject changed canonical identity: %v", err)
	}
	if emails := settingsClientEmails(t, target.Id); len(emails) != 0 {
		t.Fatalf("stale attachment modified target settings: %v", emails)
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", before.Id, target.Id).Count(&links).Error; err != nil || links != 0 {
		t.Fatalf("stale attachment persisted membership: links=%d error=%v", links, err)
	}
}

func TestAttachRejectsReplacedCanonicalIdentity_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestAttachRejectsReplacedCanonicalIdentity(t)
}
