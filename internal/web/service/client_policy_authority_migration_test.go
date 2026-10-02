package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"
)

func TestAuthorityMigrationPublicEntryClosesAdmissionAndKeepsListenersStopped(t *testing.T) {
	path, client, _ := authorityMigrationFixture(t)
	previous := isManuallyStopped.Load()
	t.Cleanup(func() { isManuallyStopped.Store(previous) })
	identity, err := MigrateLocalClientPolicyAuthority(context.Background(), filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if !isManuallyStopped.Load() || activeDatabaseRestore.Load() != nil {
		t.Fatal("migration retained its temporary admission fence or resumed listeners")
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(path), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client)
	if err != nil || identity != state.Journal.Identity() || a.Usage.BilledBytes != 12 || a.Usage.Remainder != 500000 {
		t.Fatalf("public entry changed initial accounting: %+v/%v", a, err)
	}
}

func TestAuthorityMigrationPublicEntryRejectsActiveStoreAndCompetingOwner(t *testing.T) {
	path, _, _ := authorityMigrationFixture(t)
	previous := isManuallyStopped.Load()
	t.Cleanup(func() { isManuallyStopped.Store(previous) })
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLocalClientPolicyAuthority(context.Background(), filepath.Dir(path))
	owner.release()
	if !errors.Is(err, ErrDatabaseRestoreInProgress) {
		t.Fatalf("migration overlapped another lifecycle owner: %v", err)
	}
	engine, err := clientpolicy.OpenPersistentEngine(path, "migration-source")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := MigrateLocalClientPolicyAuthority(context.Background(), filepath.Dir(path)); err == nil {
		t.Fatal("migration opened an occupied execution store")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(path), "authority")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected live-store capture created authority: %v", err)
	}
	if activeDatabaseRestore.Load() != nil {
		t.Fatal("failed migration retained admission owner")
	}
}

func TestAuthorityMigrationEntryResumesOriginalIdentityAndHeldBudget(t *testing.T) {
	path, client, deleted := authorityMigrationFixture(t)
	dir := filepath.Join(filepath.Dir(path), "authority")
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	state, err := migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	if err != nil {
		t.Fatal(err)
	}
	identity := state.Journal.Identity()
	a, err := state.Journal.Account(client)
	if err != nil || a.Usage.BilledBytes != 12 || a.Usage.Remainder != 500000 || a.WindowUsed != 3 {
		t.Fatalf("migration changed historical boundary: %+v/%v", a, err)
	}
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: "migration-source", BootID: "migration-retry-boot"}
	if err := state.Journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: identity, NodeBoot: boot, ClientID: client, WindowID: a.Policy.WindowID, PolicyVersion: a.Policy.Version}, RequestID: "retained-allocation", ChallengeID: "retained-challenge", Capacity: 40, Upload: a.Policy.Upload, Download: a.Policy.Download, LeaseDuration: time.Second}
	if _, err := state.Journal.Issue(request); err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err = state.Journal.Account(client)
	if err != nil || state.Journal.Identity() != identity || a.HeldCapacity != 40 || a.Usage.BilledBytes != 12 || a.Usage.Remainder != 500000 {
		t.Fatalf("migration retry reset history or held budget: %+v/%v", a, err)
	}
	var rows []model.ClientPolicyAuthorityProjection
	if err := owner.currentDatabase().Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("projection lost live or terminal account: %+v", rows)
	}
	tombstone, err := state.Journal.Account(deleted)
	if err != nil || !tombstone.Deleted || tombstone.Usage.BilledBytes != 20 || tombstone.Usage.Remainder != 200000 {
		t.Fatalf("migration retry lost terminal history: %+v/%v", tombstone, err)
	}
}

func TestAuthorityMigrationEntryProjectionFailureRetainsJournalForExactRetry(t *testing.T) {
	path, client, _ := authorityMigrationFixture(t)
	dir := filepath.Join(filepath.Dir(path), "authority")
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	injected := errors.New("migration projection commit failed")
	callback := "authority-migration-entry-projection-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_authority_projections" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	state, err := migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	if removeErr := db.Callback().Create().Remove(callback); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !errors.Is(err, injected) || state != nil {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("failed projection exposed active migration: %+v/%v", state, err)
	}
	manifest, err := readAuthorityManifest(dir)
	if err != nil || manifest.Phase != "committed" {
		t.Fatalf("failed SQL lost durable journal phase: %+v/%v", manifest, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "journal.db")); err != nil || info.Size() == 0 {
		t.Fatalf("failed SQL lost journal: %v", err)
	}
	var count int64
	if err := owner.currentDatabase().Model(&model.ClientPolicyAuthorityProjection{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed SQL left partial projection: %d/%v", count, err)
	}
	state, err = migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client)
	if err != nil || state.Journal.Identity() != manifest.Identity || a.Usage.BilledBytes != 12 || a.Usage.Remainder != 500000 || a.Revision != 1 {
		t.Fatalf("SQL retry recaptured a fresh baseline: %+v/%v", a, err)
	}
}

func TestAuthorityMigrationEntryFinalMarkerFailureResumesCommittedJournal(t *testing.T) {
	path, client, _ := authorityMigrationFixture(t)
	dir := filepath.Join(filepath.Dir(path), "authority")
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	original := publishAuthorityManifest
	injected := errors.New("migration entry final marker failed")
	publishAuthorityManifest = func(dir string, manifest authorityManifest, create bool) error {
		if manifest.Phase == "committed" {
			return injected
		}
		return original(dir, manifest, create)
	}
	state, err := migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	publishAuthorityManifest = original
	if !errors.Is(err, injected) || state != nil {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("failed marker exposed authority: %+v/%v", state, err)
	}
	prepared, err := readAuthorityManifest(dir)
	if err != nil || prepared.Phase != "preparing" {
		t.Fatalf("failed marker lost prepared identity: %+v/%v", prepared, err)
	}
	state, err = migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client)
	if err != nil || state.Journal.Identity() != prepared.Identity || a.Usage.BilledBytes != 12 || a.Usage.Remainder != 500000 || a.Revision != 1 {
		t.Fatalf("final marker retry changed initial history: %+v/%v", a, err)
	}
}

func TestAuthorityMigrationEntryNeverRecreatesMissingActivatedJournal(t *testing.T) {
	path, _, _ := authorityMigrationFixture(t)
	dir := filepath.Join(filepath.Dir(path), "authority")
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	state, err := migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "journal.db")
	if err := os.Rename(journal, filepath.Join(dir, "preserved-activated-journal.db")); err != nil {
		t.Fatal(err)
	}
	if state, err := migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", dir); !errors.Is(err, ErrAuthorityNotInitialized) || state != nil {
		if state != nil {
			_ = state.Journal.Close()
		}
		t.Fatalf("migration retry recreated missing activated journal: %+v/%v", state, err)
	}
	if _, err := os.Lstat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed retry created journal: %v", err)
	}
}
