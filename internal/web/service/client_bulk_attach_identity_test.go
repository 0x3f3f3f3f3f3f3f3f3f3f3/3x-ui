package service

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func bulkAttachIdentityFixture(t *testing.T, protocol model.Protocol, subID string) (*model.Inbound, []model.ClientRecord) {
	t.Helper()
	_, target, first := attachIdentityFixture(t, protocol, subID)
	second := model.Client{Email: "attach-second", ID: uuid.NewString(), Password: "second-attach-password", SubID: subID, Enable: true}
	source := &model.Inbound{Protocol: protocol, Listen: "127.0.0.1", Port: 31822, Settings: clientsSettings(t, []model.Client{second})}
	if _, _, err := (&InboundService{}).AddInbound(source); err != nil {
		t.Fatal(err)
	}
	return target, []model.ClientRecord{first, lookupClientRecord(t, second.Email)}
}

func TestBulkAttachCanonicalClientsWithoutSubscriptionID(t *testing.T) {
	testBulkAttachCanonicalClientsWithoutSubscriptionID(t, false)
}

func TestBulkAttachCanonicalClientsWithoutSubscriptionID_Postgres(t *testing.T) {
	testBulkAttachCanonicalClientsWithoutSubscriptionID(t, true)
}

func testBulkAttachCanonicalClientsWithoutSubscriptionID(t *testing.T, postgres bool) {
	t.Helper()
	for _, protocol := range []model.Protocol{model.Mieru, model.VLESS} {
		t.Run(string(protocol), func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			target, before := bulkAttachIdentityFixture(t, protocol, "")
			clients, inbounds := &ClientService{}, &InboundService{}
			emails := []string{before[0].Email, before[1].Email}
			sourceIDs, err := clients.GetInboundIdsForRecord(before[0].Id)
			if err != nil || len(sourceIDs) != 1 {
				t.Fatalf("fixture source attachments: %v error=%v", sourceIDs, err)
			}
			for _, want := range []struct{ attached, skipped int }{{3, 1}, {0, 4}} {
				result, _, err := clients.BulkAttach(inbounds, emails, []int{target.Id, sourceIDs[0], target.Id})
				if err != nil || len(result.Errors) != 0 {
					t.Fatalf("bulk canonical attachment failed: result=%+v error=%v", result, err)
				}
				if len(result.Attached) != want.attached || len(result.Skipped) != want.skipped {
					t.Fatalf("bulk attachment retry result: %+v", result)
				}
			}
			for _, owner := range before {
				after := lookupClientRecord(t, owner.Email)
				if after.Id != owner.Id || after.PolicyID != owner.PolicyID || after.SubID != "" || after.UUID != owner.UUID || after.Password != owner.Password {
					t.Fatalf("bulk attachment changed identity: before=%+v after=%+v", owner, after)
				}
				entry, _ := settingsClient(t, target.Id, owner.Email)
				if entry.SubID != "" || entry.ID != owner.UUID || entry.Password != owner.Password {
					t.Fatalf("bulk attachment changed credentials: %+v", entry)
				}
				var count int64
				if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", owner.Id, target.Id).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("bulk membership count=%d error=%v", count, err)
				}
			}
		})
	}
}

func TestBulkAttachRejectsReplacedCanonicalIdentity(t *testing.T) {
	target, before := bulkAttachIdentityFixture(t, model.VLESS, "existing-subscription")
	db := database.GetDB()
	var replaced atomic.Bool
	newPolicy := uuid.NewString()
	callback := "test:replace-bulk-attachment-identity"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		client, ok := tx.Statement.Dest.(*model.ClientRecord)
		if !ok || client.Id != before[1].Id || !replaced.CompareAndSwap(false, true) {
			return
		}
		result := db.Exec("UPDATE clients SET policy_id = ? WHERE id = ?", newPolicy, before[1].Id)
		if result.Error != nil {
			tx.AddError(result.Error)
		} else if result.RowsAffected != 1 {
			tx.AddError(fmt.Errorf("fixture identity replacement changed %d rows", result.RowsAffected))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	result, _, err := (&ClientService{}).BulkAttach(&InboundService{}, []string{before[0].Email, before[1].Email}, []int{target.Id})
	if !replaced.Load() || lookupClientRecord(t, before[1].Email).PolicyID != newPolicy {
		t.Fatal("fixture failed to persist replacement identity before attachment commit")
	}
	if err != nil || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], database.ErrUsageConflict.Error()) || len(result.Attached) != 0 {
		t.Fatalf("bulk attachment did not reject changed identity: result=%+v error=%v", result, err)
	}
	if emails := settingsClientEmails(t, target.Id); len(emails) != 0 {
		t.Fatalf("failed batch modified target settings: %v", emails)
	}
	var count int64
	if err := db.Model(&model.ClientInbound{}).Where("inbound_id = ?", target.Id).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed batch persisted membership: count=%d error=%v", count, err)
	}
}

func TestBulkAttachRejectsReplacedCanonicalIdentity_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestBulkAttachRejectsReplacedCanonicalIdentity(t)
}
