package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestPortableMieruPreservesPolicyAndSharedAttachments(t *testing.T) {
	for _, mode := range []string{"mieru", "ssh-mieru", "detached", "legacy-mieru"} {
		t.Run(mode, func(t *testing.T) {
			portableTestSchema(t)
			clients, inbounds := &ClientService{}, &InboundService{}
			var record model.ClientRecord
			if mode == "ssh-mieru" {
				record = clientPolicyFixture(t)
				updated := *record.ToClient()
				updated.Password = "portable-native-password"
				if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
					t.Fatal(err)
				}
			} else {
				setupConflictDB(t)
			}
			inbound := &model.Inbound{Protocol: model.Mieru, Listen: "127.0.0.1", Port: 31281, Settings: `{"network":"both","clients":[]}`}
			if mode != "ssh-mieru" {
				settings, err := json.Marshal(map[string]any{"network": "both", "clients": []model.Client{{Email: "portable-mieru", Password: "portable-native-password", Enable: true, TotalGB: 1000}}})
				if err != nil {
					t.Fatal(err)
				}
				inbound.Settings = string(settings)
			}
			if _, _, err := inbounds.AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			if mode == "ssh-mieru" {
				if _, err := clients.Attach(inbounds, record.Id, []int{inbound.Id}); err != nil {
					t.Fatal(err)
				}
			} else {
				record = lookupClientRecord(t, "portable-mieru")
			}
			policy, err := clients.GetPolicy(t.Context(), record.Email)
			if err != nil {
				t.Fatal(err)
			}
			policy.UploadBps, policy.DownloadBps, policy.Multiplier = 32768, 65536, "1.5"
			if _, err := clients.UpdatePolicy(t.Context(), record.Email, policy.ClientPolicyUpdate); err != nil {
				t.Fatal(err)
			}
			ledger := database.NewClientUsageLedger(database.GetDB())
			meter, err := ledger.ClaimAdmissionSource(t.Context(), record.PolicyID, "portable/mieru-test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.Admit(t.Context(), database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 3, Down: 2}); err != nil {
				t.Fatal(err)
			}
			record = lookupClientRecord(t, record.Email)
			updated := *record.ToClient()
			updated.Enable, updated.ExpiryTime, updated.ResetWeekday = false, -86400000, 3
			if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			items := portableBrowserRoundTrip(t, clients)
			if len(items) != 1 || items[0].Policy == nil {
				t.Fatal("portable export lost the native client's accounting snapshot")
			}
			if mode == "legacy-mieru" {
				items[0].Policy = nil
			}
			wantLinks := len(items[0].InboundIds)
			if mode == "detached" {
				items[0].InboundIds = nil
				wantLinks = 0
			}
			if _, err := clients.Delete(inbounds, record.Id, false); err != nil {
				t.Fatal(err)
			}
			result, _, err := clients.ImportClients(inbounds, items)
			if err != nil || result.Created != 1 || len(result.Skipped) != 0 {
				t.Fatalf("native portable import failed: result=%+v err=%v", result, err)
			}
			got, err := clients.GetPolicy(t.Context(), record.Email)
			if err != nil || got.Supported != (mode != "detached") || got.PolicyID == record.PolicyID || got.Usage.Up != "3" || got.Usage.Down != "2" || got.Usage.Quota != "1000" {
				t.Fatalf("restoration changed native policy/history: %+v err=%v", got, err)
			}
			if mode == "legacy-mieru" {
				if got.UploadBps != 0 || got.DownloadBps != 0 || got.Multiplier != "1" || got.Usage.Billed != "5" || got.Usage.Remainder != 0 {
					t.Fatalf("legacy restoration did not initialize native usage from raw traffic: %+v", got)
				}
			} else if got.UploadBps != 32768 || got.DownloadBps != 65536 || got.Multiplier != "1.5" || got.Usage.Billed != "7" || got.Usage.Remainder != 500 {
				t.Fatalf("restoration changed exact native policy: %+v", got)
			}
			restored := lookupClientRecord(t, record.Email)
			if restored.Password != "portable-native-password" || restored.Enable || restored.ExpiryTime != -86400000 || restored.ResetWeekday != 3 {
				t.Fatal("restoration lost native credentials or independent lifecycle restrictions")
			}
			var links int64
			if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ?", restored.Id).Count(&links).Error; err != nil || links != int64(wantLinks) {
				t.Fatalf("restored attachment count=%d want=%d err=%v", links, wantLinks, err)
			}
			if mode == "detached" {
				if _, err := clients.Attach(inbounds, restored.Id, []int{inbound.Id}); err != nil {
					t.Fatal(err)
				}
				attached, err := clients.GetPolicy(t.Context(), record.Email)
				if err != nil || !attached.Supported || attached.PolicyID != got.PolicyID || attached.Usage != got.Usage {
					t.Fatalf("reattaching a restored native owner changed its usage: %+v err=%v", attached, err)
				}
			}
		})
	}
}

func TestPortableMieruPreservesPolicyAndSharedAttachments_Postgres(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "postgres")
	TestPortableMieruPreservesPolicyAndSharedAttachments(t)
}
