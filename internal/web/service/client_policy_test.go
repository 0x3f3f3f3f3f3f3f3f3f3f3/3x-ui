package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func clientPolicyFixture(t *testing.T) model.ClientRecord {
	t.Helper()
	setupConflictDB(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(public)
	client := model.Client{Email: "policy-managed", Enable: true, TotalGB: 1000, SSH: &model.SSHClient{PublicKeys: []string{string(ssh.MarshalAuthorizedKey(key))}, Targets: []model.SSHTarget{{Host: "route.invalid", Port: 443}}}}
	encoded, _ := json.Marshal(map[string]any{"clients": []model.Client{client}})
	inbound := &model.Inbound{Protocol: model.SSH, Listen: "127.0.0.1", Port: 31280, Settings: string(encoded)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	return lookupClientRecord(t, client.Email)
}

func TestClientPolicyPersistsRatesAndUsesExactMultiplierBoundary(t *testing.T) {
	record := clientPolicyFixture(t)
	svc, ctx := &ClientService{}, context.Background()
	before, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || before.Version != 0 || before.UploadBps != 0 || before.DownloadBps != 0 || before.Multiplier != "1" || before.Scope != "local" || !before.Supported {
		t.Fatalf("unexpected default policy: %+v, %v", before, err)
	}
	request := ClientPolicyUpdate{PolicyID: record.PolicyID, Version: 0, UploadBps: 65536, DownloadBps: 131072, Multiplier: "0.5", Scope: "local"}
	first, err := svc.UpdatePolicy(ctx, record.Email, request)
	if err != nil || first.Version != 1 || first.UploadBps != 65536 || first.Multiplier != "0.5" {
		t.Fatalf("policy update failed: %+v, %v", first, err)
	}
	ledger := database.NewClientUsageLedger(database.GetDB())
	meter, err := ledger.ClaimAdmissionSource(ctx, record.PolicyID, "local/policy-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Up: 3}); err != nil {
		t.Fatal(err)
	}
	request.Version, request.Multiplier = first.Version, "2"
	after, err := svc.UpdatePolicy(ctx, record.Email, request)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != 2 || after.Usage.Up != "3" || after.Usage.Billed != "1" || after.Usage.Remainder != 500 {
		t.Fatalf("multiplier edit repriced history: %+v", after)
	}
	if err := ledger.CheckAdmissionSource(ctx, meter.ID); !errors.Is(err, database.ErrUsageClosed) {
		t.Fatalf("old multiplier source still admits: %v", err)
	}
	meter, err = ledger.ClaimAdmissionSource(ctx, record.PolicyID, "local/policy-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ctx, database.ClientUsageReport{MeterID: meter.ID, Sequence: 1, Down: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || got.Usage.Up != "3" || got.Usage.Down != "2" || got.Usage.Billed != "5" || got.Usage.Remaining != "995" || got.Usage.Remainder != 500 {
		t.Fatalf("segment accounting/display mismatch: %+v, %v", got, err)
	}
	if _, err := svc.UpdatePolicy(ctx, record.Email, request); !errors.Is(err, ErrClientPolicyConflict) {
		t.Fatalf("stale edit was accepted: %v", err)
	}
	if _, err := svc.ResetTrafficByEmail(&InboundService{}, record.Email); err != nil {
		t.Fatal(err)
	}
	got, err = svc.GetPolicy(ctx, record.Email)
	if err != nil || got.UploadBps != 65536 || got.DownloadBps != 131072 || got.Multiplier != "2" || got.Usage.Billed != "0" || got.Version != 2 {
		t.Fatalf("traffic reset erased policy: %+v, %v", got, err)
	}
}

func TestClientPolicyRejectsUnsupportedAndInvalidChangesAtomically(t *testing.T) {
	record := clientPolicyFixture(t)
	svc, ctx := &ClientService{}, context.Background()
	for _, request := range []ClientPolicyUpdate{
		{UploadBps: -1, Multiplier: "1", Scope: "local"},
		{UploadBps: 1<<40 + 1, Multiplier: "1", Scope: "local"},
		{Multiplier: "0", Scope: "local"},
		{Multiplier: "1.0001", Scope: "local"},
		{Multiplier: "NaN", Scope: "local"},
		{Multiplier: "1", Scope: "global"},
	} {
		request.PolicyID = record.PolicyID
		if _, err := svc.UpdatePolicy(ctx, record.Email, request); err == nil {
			t.Fatalf("invalid or unsupported policy accepted: %+v", request)
		}
	}
	ledger := database.NewClientUsageLedger(database.GetDB())
	if _, err := ledger.Register(ctx, record.PolicyID, "unsettled-observed", "a2222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdatePolicy(ctx, record.Email, ClientPolicyUpdate{PolicyID: record.PolicyID, UploadBps: 123, Multiplier: "2", Scope: "local"}); !errors.Is(err, database.ErrUsageBoundary) {
		t.Fatalf("unsettled billing boundary was ignored: %v", err)
	}
	got, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || got.Version != 0 || got.UploadBps != 0 || got.Multiplier != "1" {
		t.Fatalf("failed boundary partially changed rate policy: %+v, %v", got, err)
	}
}

func TestClientPolicyLegacyReadDoesNotActivateOrReprice(t *testing.T) {
	setupConflictDB(t)
	client := model.Client{Email: "legacy-policy", Enable: true, TotalGB: 1000}
	inbound := mkInbound(t, 31281, model.VLESS, clientsSettings(t, []model.Client{client}))
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Up: 11, Down: 13, Total: 1000}).Error; err != nil {
		t.Fatal(err)
	}
	svc, ctx := &ClientService{}, context.Background()
	got, err := svc.GetPolicy(ctx, client.Email)
	if err != nil || got.Supported || got.Multiplier != "1" || got.Usage.Billed != "24" || got.Usage.Remaining != "976" {
		t.Fatalf("legacy defaults lost historical use: %+v, %v", got, err)
	}
	if _, err := svc.UpdatePolicy(ctx, client.Email, ClientPolicyUpdate{PolicyID: got.PolicyID, UploadBps: 65536, Multiplier: "2", Scope: "local"}); !errors.Is(err, ErrClientPolicyUnsupported) {
		t.Fatal("unenforced native policy was accepted")
	}
	var accounts int64
	if err := database.GetDB().Model(&model.ClientUsageAccount{}).Count(&accounts).Error; err != nil || accounts != 0 {
		t.Fatal("reading/rejecting legacy policy activated accounting")
	}
}

func TestClientPolicy_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"billing-boundary", TestClientPolicyPersistsRatesAndUsesExactMultiplierBoundary},
		{"rollback", TestClientPolicyRejectsUnsupportedAndInvalidChangesAtomically},
		{"legacy", TestClientPolicyLegacyReadDoesNotActivateOrReprice},
		{"recreated-label", TestClientPolicyStaleEditorCannotChangeRecreatedLabel},
		{"concurrent-edit", TestClientPolicyConcurrentEditorsCannotOverwriteEachOther},
	} {
		t.Run(test.name, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			test.run(t)
		})
	}
}

func TestClientPolicyConcurrentEditorsCannotOverwriteEachOther(t *testing.T) {
	record := clientPolicyFixture(t)
	svc, ctx := &ClientService{}, context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, rate := range []int64{65536, 131072} {
		go func() {
			<-start
			_, err := svc.UpdatePolicy(ctx, record.Email, ClientPolicyUpdate{PolicyID: record.PolicyID, UploadBps: rate, Multiplier: "1", Scope: "local"})
			results <- err
		}()
	}
	close(start)
	wins, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			wins++
		case errors.Is(err, ErrClientPolicyConflict):
			conflicts++
		default:
			t.Fatalf("concurrent policy edit failed unexpectedly: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("lost concurrent update: successes=%d conflicts=%d", wins, conflicts)
	}
	got, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || got.Version != 1 || (got.UploadBps != 65536 && got.UploadBps != 131072) {
		t.Fatalf("concurrent edit result: %+v, %v", got, err)
	}
}

func TestClientPolicyStaleEditorCannotChangeRecreatedLabel(t *testing.T) {
	record := clientPolicyFixture(t)
	svc, ctx := &ClientService{}, context.Background()
	before, err := svc.GetPolicy(ctx, record.Email)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := svc.GetInboundIdsForRecord(record.Id)
	if err != nil || len(ids) != 1 {
		t.Fatalf("fixture attachment: %v, %v", ids, err)
	}
	if _, err := svc.Delete(&InboundService{}, record.Id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOne(&InboundService{}, ids[0], *record.ToClient()); err != nil {
		t.Fatal(err)
	}
	request := before.ClientPolicyUpdate
	request.UploadBps = 65536
	if _, err := svc.UpdatePolicy(ctx, record.Email, request); !errors.Is(err, ErrClientPolicyConflict) {
		t.Fatalf("stale editor changed the replacement client: %v", err)
	}
	after, err := svc.GetPolicy(ctx, record.Email)
	if err != nil || after.PolicyID == before.PolicyID || after.Version != 0 || after.UploadBps != 0 {
		t.Fatalf("replacement inherited old policy edit: %+v, %v", after, err)
	}
}
