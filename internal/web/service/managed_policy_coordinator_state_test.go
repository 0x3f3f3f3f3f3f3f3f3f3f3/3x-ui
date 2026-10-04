package service

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedPolicyCoordinatorRetainsOriginalAuthority(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	t.Logf("managed coordinator state backend: %s", db.Dialector.Name())
	dir := filepath.Join(t.TempDir(), "coordinator")
	ctx := context.Background()
	owner, err := openManagedPolicyCoordinator(ctx, db, dir)
	if err != nil {
		t.Fatal(err)
	}
	id := owner.state.Journal.Identity()
	source := owner.state.SourceID
	parent := model.ClientRecord{Email: "coordinator-retention", Enable: true}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	allowed := policyauthority.Direction{Unlimited: true}
	seed := policyauthority.Seed{ClientID: parent.StableID, Policy: policyauthority.Policy{WindowID: "original-window", Version: 1, QuotaBytes: 100, Upload: allowed, Download: allowed}}
	if err := owner.state.Journal.AddAccount(seed); err == nil {
		t.Fatal("activated coordinator allowed seed-only provisioning without original scope/parent origin")
	}
	origin := policyauthority.ManagedAccountOrigin{ClientID: parent.StableID, ParentClientID: parent.StableID, Scope: "global", InitialPolicyVersion: 1, PolicyDigest: strings.Repeat("a", 64)}
	if err := owner.state.Journal.AddManagedAccount(seed, origin); err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: "actual-node", SourceID: "actual-node-source", BootID: "original-boot"}
	mapping := policyauthority.ClientMapping{Authority: id, NodeAnchor: policyauthority.Identity{AuthorityID: "actual-node-anchor", Generation: 1}, NodeID: boot.NodeID, SourceID: boot.SourceID, GlobalClientID: parent.StableID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: 1, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
	if err := owner.state.Journal.RecordClientMapping(policyauthority.ClientMappingCoordinator, mapping); err != nil {
		t.Fatal(err)
	}
	if err := owner.state.Journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: id, NodeBoot: boot, ClientID: parent.StableID, WindowID: seed.Policy.WindowID, PolicyVersion: 1}, RequestID: "retained-request", ChallengeID: "original-challenge", Capacity: 60, Upload: allowed, Download: allowed, LeaseDuration: time.Second}
	if _, err := owner.state.Journal.Issue(request); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedPolicyCoordinator(ctx, db, dir); err == nil {
		t.Fatal("busy original journal acquired a second owner")
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err = openManagedPolicyCoordinator(ctx, db, dir)
	if err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(parent.StableID)
	if err != nil || owner.state.Journal.Identity() != id || owner.state.SourceID != source || account.HeldCapacity != 60 {
		t.Fatalf("reopening recreated authority or returned held allowance: %+v/%v", account, err)
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "journal.db"), filepath.Join(dir, "journal-retained.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedPolicyCoordinator(ctx, db, dir); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("activated missing journal recreated empty budget: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "journal.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing authority was recreated")
	}
	if _, err := openManagedPolicyCoordinator(nil, db, dir); err == nil {
		t.Fatal("nil context admitted coordinator")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := openManagedPolicyCoordinator(canceled, db, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
}

func TestManagedPolicyCoordinatorLostSQLAckAndSourceGuards(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	t.Logf("managed coordinator recovery backend: %s", db.Dialector.Name())
	dir := filepath.Join(t.TempDir(), "coordinator")
	ctx := context.Background()
	lost := errors.New("lost coordinator activation SQL acknowledgement")
	callback := "test:coordinator_activation_ack"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_coordinator_sources" {
			tx.AddError(lost)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	if _, err := openManagedPolicyCoordinator(ctx, db, dir); !errors.Is(err, lost) {
		t.Fatalf("lost SQL acknowledgement: %v", err)
	}
	manifest, err := readAuthorityManifest(dir)
	if err != nil || manifest.Phase != "committed" {
		t.Fatalf("original journal commit lost: %+v/%v", manifest, err)
	}
	var pending model.ClientPolicyCoordinatorSource
	if err := db.First(&pending, "node_key = ?", managedCoordinatorSourceKey).Error; err != nil || pending.Activated || pending.InstanceID != manifest.SourceID {
		t.Fatal("failed SQL ack lost pending original source")
	}
	if err := db.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	owner, err := openManagedPolicyCoordinator(ctx, db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if owner.state.Journal.Identity() != manifest.Identity || owner.state.SourceID != pending.InstanceID {
		t.Fatal("retry replaced original authority")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := owner.Close(canceled); !errors.Is(err, context.Canceled) || owner.closed {
		t.Fatal("canceled close discarded live ownership")
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientPolicyCoordinatorSource{}).Where("node_key = ?", managedCoordinatorSourceKey).Update("instance_id", "wrong-original-source").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedPolicyCoordinator(ctx, db, dir); !errors.Is(err, policyauthority.ErrIdentity) {
		t.Fatalf("source replacement was accepted: %v", err)
	}
	if err := db.Where("node_key = ?", managedCoordinatorSourceKey).Delete(&model.ClientPolicyCoordinatorSource{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedPolicyCoordinator(ctx, db, dir); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("missing original SQL source was recreated: %v", err)
	}
	var count int64
	if err := db.Model(&model.ClientPolicyCoordinatorSource{}).Where("node_key = ?", managedCoordinatorSourceKey).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed original-source admission recreated SQL identity")
	}
}

func TestManagedPolicyCoordinatorRestoreAdmission(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "must-not-create")
	if _, err := openManagedPolicyCoordinator(context.Background(), db, dir); !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("coordinator entered SQL restore interval: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("blocked restore admission mutated coordinator state")
	}
}
