package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruBulkLifecyclePreservesOwnedUsage(t *testing.T) {
	setupConflictDB(t)
	clients, inbounds := &ClientService{}, &InboundService{}
	var ids []int
	for i, network := range []string{"tcp", "udp"} {
		inbound := &model.Inbound{Protocol: model.Mieru, Listen: "127.0.0.1", Port: 31400 + i, Settings: `{"network":"` + network + `","clients":[]}`}
		if _, _, err := inbounds.AddInbound(inbound); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, inbound.Id)
	}
	emails := []string{"bulk-mieru-alice", "bulk-mieru-bob"}
	payloads := make([]ClientCreatePayload, len(emails))
	for i, email := range emails {
		payloads[i] = ClientCreatePayload{Client: model.Client{Email: email, Password: "native-" + email, Enable: true, TotalGB: 1000}, InboundIds: ids}
	}
	created, _, err := clients.BulkCreate(inbounds, payloads)
	if err != nil || created.Created != 2 || len(created.Skipped) != 0 {
		t.Fatalf("bulk native create: %+v err=%v", created, err)
	}
	alice := lookupClientRecord(t, emails[0])
	bob := lookupClientRecord(t, emails[1])
	if alice.PolicyID == "" || bob.PolicyID == "" || alice.PolicyID == bob.PolicyID {
		t.Fatal("bulk creation did not assign independent canonical policy identities")
	}
	policy, err := clients.GetPolicy(t.Context(), alice.Email)
	if err != nil || !policy.Supported {
		t.Fatalf("bulk-created native policy is unsupported: %+v err=%v", policy, err)
	}
	policy.UploadBps, policy.DownloadBps, policy.Multiplier = 32768, 65536, "1.5"
	if _, err := clients.UpdatePolicy(t.Context(), alice.Email, policy.ClientPolicyUpdate); err != nil {
		t.Fatal(err)
	}
	ledger := database.NewClientUsageLedger(database.GetDB())
	meter, err := ledger.ClaimAdmissionSource(t.Context(), alice.PolicyID, "bulk/mieru-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(t.Context(), database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 3, Down: 2}); err != nil {
		t.Fatal(err)
	}
	assertUsage := func(supported bool, quota string) {
		t.Helper()
		current, err := clients.GetPolicy(t.Context(), alice.Email)
		if err != nil || current.Supported != supported || current.PolicyID != alice.PolicyID || current.Usage.Up != "3" || current.Usage.Down != "2" || current.Usage.Billed != "7" || current.Usage.Remainder != 500 || current.Usage.Quota != quota || current.UploadBps != 32768 || current.DownloadBps != 65536 || current.Multiplier != "1.5" {
			t.Fatalf("bulk operation changed native policy/history: %+v err=%v", current, err)
		}
	}
	assertUsage(true, "1000")
	unmanaged := mkInbound(t, 31402, model.VLESS, clientsSettings(t, nil))
	rejected, _, err := clients.BulkAttach(inbounds, emails, []int{unmanaged.Id})
	if err != nil || len(rejected.Attached) != 0 || len(rejected.Errors) != 1 {
		t.Fatalf("bulk attachment allowed an owned account onto an unenforced path: %+v err=%v", rejected, err)
	}
	var unsupportedLinks int64
	if err := database.GetDB().Model(&model.ClientInbound{}).Where("inbound_id = ?", unmanaged.Id).Count(&unsupportedLinks).Error; err != nil || unsupportedLinks != 0 {
		t.Fatal("rejected bulk attachment persisted unmanaged links")
	}
	assertUsage(true, "1000")
	disabled, _, err := clients.BulkSetEnable(inbounds, emails, false)
	if err != nil || disabled.Changed != 2 || len(disabled.Skipped) != 0 {
		t.Fatalf("bulk native disable: %+v err=%v", disabled, err)
	}
	adjusted, _, err := clients.BulkAdjust(inbounds, emails, 0, 500, "", nil, "")
	if err != nil || adjusted.Adjusted != 2 || len(adjusted.Skipped) != 0 {
		t.Fatalf("bulk native quota change: %+v err=%v", adjusted, err)
	}
	for _, id := range ids {
		attached, err := clients.ListForInbound(nil, id)
		if err != nil || len(attached) != 2 {
			t.Fatalf("bulk attachment lost clients: %+v err=%v", attached, err)
		}
		for _, client := range attached {
			if client.Enable || client.TotalGB != 1500 || client.Password != "native-"+client.Email {
				t.Fatal("bulk adjustment lost native credentials or cleared manual disable")
			}
		}
	}
	assertUsage(true, "1500")
	detached, _, err := clients.BulkDetach(inbounds, emails, ids)
	if err != nil || len(detached.Detached) != 2 || len(detached.Errors) != 0 {
		t.Fatalf("bulk native detach: %+v err=%v", detached, err)
	}
	assertUsage(false, "1500")
	attached, _, err := clients.BulkAttach(inbounds, emails, ids)
	if err != nil || len(attached.Attached) != 4 || len(attached.Errors) != 0 {
		t.Fatalf("bulk native reattach: %+v err=%v", attached, err)
	}
	assertUsage(true, "1500")
	again, _, err := clients.BulkAttach(inbounds, emails, ids)
	if err != nil || len(again.Attached) != 0 || len(again.Skipped) != 4 || len(again.Errors) != 0 {
		t.Fatalf("native reattachment is not idempotent: %+v err=%v", again, err)
	}
	deleted, _, err := clients.BulkDelete(inbounds, emails, false)
	if err != nil || deleted.Deleted != 2 || len(deleted.Skipped) != 0 {
		t.Fatalf("bulk native delete: %+v err=%v", deleted, err)
	}
	created, _, err = clients.BulkCreate(inbounds, payloads)
	if err != nil || created.Created != 2 || len(created.Skipped) != 0 {
		t.Fatalf("bulk native recreate: %+v err=%v", created, err)
	}
	fresh, err := clients.GetPolicy(t.Context(), alice.Email)
	if err != nil || !fresh.Supported || fresh.PolicyID == alice.PolicyID || fresh.Usage.Up != "0" || fresh.Usage.Down != "0" || fresh.Usage.Billed != "0" || fresh.Multiplier != "1" {
		t.Fatalf("recreated native client inherited an old owner's usage or policy: %+v err=%v", fresh, err)
	}
}

func TestMieruBulkLifecyclePreservesOwnedUsage_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestMieruBulkLifecyclePreservesOwnedUsage(t)
}

func TestMieruBulkCreateRejectsInvalidCredentialsAtomically(t *testing.T) {
	setupConflictDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	inbound := &model.Inbound{Protocol: model.Mieru, Listen: "127.0.0.1", Port: 31400, Settings: `{"network":"tcp","clients":[]}`}
	if _, _, err := inbounds.AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	result, _, err := clients.BulkCreate(inbounds, []ClientCreatePayload{
		{Client: model.Client{Email: "bulk-valid", Password: "native-password", Enable: true}, InboundIds: []int{inbound.Id}},
		{Client: model.Client{Email: "bulk-invalid", Password: strings.Repeat("界", 22), Enable: true}, InboundIds: []int{inbound.Id}},
	})
	if err != nil || result.Created != 0 || len(result.Skipped) != 2 {
		t.Fatalf("invalid native batch was not rejected: %+v err=%v", result, err)
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed native batch persisted canonical client records")
	}
	stored, err := inbounds.GetInbound(inbound.Id)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := inbounds.GetClients(stored)
	if err != nil || len(bound) != 0 {
		t.Fatal("failed native batch changed listener credentials")
	}
}
