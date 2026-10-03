package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// Missing manual-inbound preparation or updating a stamp on retry loses the
// original business boundary even while the per-client reset row survives.
func TestManagedAuthorityInboundResetRetainsOriginalStamp(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const request = "inbound-original-stamp"
	reset := func() error {
		return (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, tunnel.Id, request)
	}
	if err := reset(); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	key := authorityResetRequestKey(request)
	prepared, err := owner.state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatalf("successful managed inbound reset lacks original stamp preparation: %v", err)
	}
	done, err := owner.state.Journal.LookupResetCompletion(key)
	if err != nil {
		t.Fatalf("actual core execution lacks completion: %v", err)
	}
	var snapshot struct {
		Schema        int
		ResetAt       int64
		InboundStamps []struct {
			StableID string
			ResetAt  int64
		}
		Resets []model.ClientPolicyReset
	}
	if err := json.Unmarshal([]byte(prepared.Snapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Schema != 2 || len(snapshot.InboundStamps) != 1 || snapshot.InboundStamps[0].StableID != tunnel.StableID || snapshot.InboundStamps[0].ResetAt != snapshot.ResetAt || snapshot.ResetAt <= 0 {
		t.Fatalf("lost original inbound stamp: %+v", snapshot)
	}
	if len(snapshot.Resets) != 1 || snapshot.Resets[0].RawUpload != 104 || snapshot.Resets[0].RawDownload != 204 || snapshot.Resets[0].BilledBytes != 316 {
		t.Fatalf("wrong original 2x boundary: %+v", snapshot.Resets)
	}
	db := database.GetDB()
	var original model.Inbound
	if err := db.First(&original, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	if original.LastTrafficResetTime != snapshot.ResetAt {
		t.Fatalf("SQL stamp differs from original preparation: %d/%d", original.LastTrafficResetTime, snapshot.ResetAt)
	}
	managedActivationEcho(t, flow, "next")
	if err := db.Where("request_id = ?", request).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := reset(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&original, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	if original.LastTrafficResetTime != snapshot.ResetAt {
		t.Fatal("retry changed original inbound stamp")
	}
	retained, err := owner.state.Journal.LookupResetPreparation(key)
	if err != nil || retained != prepared {
		t.Fatalf("retry rewrote preparation: %v", err)
	}
	completed, err := owner.state.Journal.LookupResetCompletion(key)
	if err != nil || completed != done {
		t.Fatalf("retry rewrote completion: %v", err)
	}
	managedActivationEcho(t, flow, "stay")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) || account.WindowBaseline != 316 || account.WindowUsed != 32 {
		t.Fatalf("retry changed exact billed boundary: %+v/%v", account, err)
	}
}

// Reselecting numeric IDs after capture transfers the original stamp to a new
// resource and lets newly added inbounds join an old all-inbound operation.
func TestManagedAuthorityInboundResetCaptureRetryPreservesResourceSet(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("all-%t", all), func(t *testing.T) {
			svc, tunnel, client, _ := setupManagedActivationService(t)
			db := database.GetDB()
			original := *tunnel
			original.Id, original.StableID, original.Enable, original.Port = 0, "", false, tunnel.Port+1
			original.Tag, original.LastTrafficResetTime = "captured-disabled-resource", 17
			if err := db.Create(&original).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: original.Id}).Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			scope := "inbound:" + strconv.Itoa(original.Id)
			if all {
				scope = "inbound:-1"
			}
			const request = "inbound-capture-only-original-set"
			if _, err := captureClientTrafficResetBatch(ctx, scope, nil, request); err != nil {
				t.Fatal(err)
			}
			if err := db.Delete(&original).Error; err != nil {
				t.Fatal(err)
			}
			replacement := original
			replacement.StableID, replacement.Remark, replacement.LastTrafficResetTime = "", "new resource at reused ID", 23
			if err := db.Create(&replacement).Error; err != nil {
				t.Fatal(err)
			}
			added := replacement
			added.Id, added.StableID, added.Tag, added.LastTrafficResetTime = 0, "", "added-after-capture", 29
			if err := db.Create(&added).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(tunnel).Update("remark", "later original remark").Error; err != nil {
				t.Fatal(err)
			}
			id := original.Id
			if all {
				id = -1
			}
			if err := (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, id, request); err != nil {
				t.Fatal(err)
			}
			var current model.Inbound
			if err := db.First(&current, replacement.Id).Error; err != nil {
				t.Fatal(err)
			}
			if current.LastTrafficResetTime != 23 || current.StableID != replacement.StableID || current.Remark != "new resource at reused ID" {
				t.Fatalf("old operation changed replacement: %+v", current)
			}
			current = model.Inbound{}
			if err := db.First(&current, added.Id).Error; err != nil {
				t.Fatal(err)
			}
			if current.LastTrafficResetTime != 29 {
				t.Fatal("old operation expanded to a newly added resource")
			}
			current = model.Inbound{}
			if err := db.First(&current, tunnel.Id).Error; err != nil {
				t.Fatal(err)
			}
			if current.Remark != "later original remark" || all && current.LastTrafficResetTime <= 0 || !all && current.LastTrafficResetTime != 0 {
				t.Fatalf("wrong original resource projection: %+v", current)
			}
		})
	}
}
