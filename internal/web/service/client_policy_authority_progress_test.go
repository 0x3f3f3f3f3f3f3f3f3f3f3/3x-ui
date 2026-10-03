package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestAuthoritySchema6ProgressPreservesMigrationRecoveryAndFundedState(t *testing.T) {
	previousManual, previousRestart := isManuallyStopped.Load(), isNeedXrayRestart.Load()
	t.Cleanup(func() { isManuallyStopped.Store(previousManual); isNeedXrayRestart.Store(previousRestart) })
	config, operation, protectedTime, before := authorityHistoryMigrationFixture(t)
	dir := filepath.Join(filepath.Dir(config.StateFile), "authority")
	state, err := openAuthorityState(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Journal.Close() })
	journal := state.Journal
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: config.InstanceID, BootID: "schema6-compatibility-boot"}
	if err := journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	grant, err := journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: journal.Identity(), NodeBoot: boot, ClientID: before.Seed.ClientID, WindowID: before.Policy.WindowID, PolicyVersion: before.Policy.Version}, RequestID: "schema6-retained-budget", ChallengeID: "schema6-retained-challenge", Capacity: 40, Upload: before.Policy.Upload, Download: before.Policy.Download, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	funded, err := journal.Account(before.Seed.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	// Opaque storage witnesses exercise opening schema 6, not service execution acknowledgement.
	capture := policyauthority.ResetOperationCapture{Identity: journal.Identity(), SourceID: config.InstanceID, RequestID: "schema6-storage-compatibility", Snapshot: `{"compatibility":"storage-only"}`}
	if err := journal.CaptureResetOperation(capture); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(capture.Snapshot))
	prepared := policyauthority.ResetOperationPreparation{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: hex.EncodeToString(digest[:]), Snapshot: `{"compatibility":"opaque-preparation"}`}
	if err := journal.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	digest = sha256.Sum256([]byte(prepared.Snapshot))
	completed := policyauthority.ResetOperationCompletion{Identity: capture.Identity, SourceID: capture.SourceID, RequestID: capture.RequestID, PreparationDigest: hex.EncodeToString(digest[:])}
	if err := journal.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	if err := db.Where("request_id = ?", operation.RequestID).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", before.Seed.ClientID).Delete(&model.ClientTrafficResetTime{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var recovered model.ClientTrafficResetBatch
	if err := db.First(&recovered, "request_id = ?", operation.RequestID).Error; err != nil || recovered != operation {
		t.Fatalf("schema6 recovery changed original migration batch: %v", err)
	}
	var stamp model.ClientTrafficResetTime
	if err := db.First(&stamp, "client_id = ?", before.Seed.ClientID).Error; err != nil || stamp.EffectiveAt != protectedTime {
		t.Fatalf("schema6 recovery changed original reset time: %v", err)
	}
	state, err = openAuthorityState(dir)
	if err != nil {
		t.Fatal(err)
	}
	journal = state.Journal
	a, err := journal.Account(before.Seed.ClientID)
	if err != nil || a != funded {
		t.Fatalf("schema6 history recovery changed held/spent capacity: exact=%v err=%v", a == funded, err)
	}
	g, err := journal.Grant(grant.GrantID)
	if err != nil || g != grant {
		t.Fatalf("schema6 history recovery changed retained grant: exact=%v err=%v", g == grant, err)
	}
	original, err := journal.LookupResetOperation(capture.RequestID)
	if err != nil || original != capture {
		t.Fatalf("schema6 history recovery changed storage capture: %v", err)
	}
	p, err := journal.LookupResetPreparation(prepared.RequestID)
	if err != nil || p != prepared {
		t.Fatalf("schema6 history recovery changed storage preparation: %v", err)
	}
	c, err := journal.LookupResetCompletion(completed.RequestID)
	if err != nil || c != completed {
		t.Fatalf("schema6 history recovery changed storage completion: %v", err)
	}
}
