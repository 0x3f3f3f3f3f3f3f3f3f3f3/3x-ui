package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedPolicyCoordinatorControlIdentityDoesNotBecomeExecutionSource(t *testing.T) {
	setupPolicyLedgerDB(t)
	db, ctx := database.GetDB(), context.Background()
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	var executions int64
	if err := db.Model(&model.ClientPolicySource{}).Count(&executions).Error; err != nil || executions != 0 {
		t.Fatalf("coordinator control identity became a stream/receipt execution source: %d/%v", executions, err)
	}
}

func TestManagedPolicyLiveCoordinatorRejectsReplacedSQLSource(t *testing.T) {
	setupPolicyLedgerDB(t)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed live source admission backend: %s", db.Dialector.Name())
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	scope := model.ClientPolicyScopeGlobal
	parent := model.ClientRecord{Email: "source-replacement-refused", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Scope: &scope}}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	prepared := model.ClientRecord{Email: "existing-source-account", Enable: true, TotalGB: 128, Policy: parent.Policy.Clone()}
	if err := db.Create(&prepared).Error; err != nil {
		t.Fatal(err)
	}
	member := managedAuthorityMember{NodeID: "node-a", SourceID: "source-a"}
	origin, _, err := c.PrepareAccount(ctx, prepared.StableID, member)
	if err != nil {
		t.Fatal(err)
	}
	j := c.state.Journal
	mapping := policyauthority.ClientMapping{Authority: j.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "source-node-original", Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: origin.ClientID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
	if err := j.RecordClientMapping(policyauthority.ClientMappingCoordinator, mapping); err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: member.NodeID, SourceID: member.SourceID, BootID: "boot-a"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	account, err := j.Account(origin.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: origin.ClientID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "after-source-replacement", ChallengeID: "challenge", Capacity: 20, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second}
	if err := db.Model(&model.ClientPolicyCoordinatorSource{}).Where("node_key = ?", managedCoordinatorSourceKey).Update("instance_id", "replacement-coordinator-source").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PrepareAccount(ctx, parent.StableID, managedAuthorityMember{NodeID: "node-a", SourceID: "source-a"}); err == nil {
		t.Fatal("live coordinator accepted replaced original SQL source")
	}
	if _, err := c.state.Journal.Account(parent.StableID); err == nil {
		t.Fatal("source replacement minted original account")
	}
	var current model.ClientRecord
	if err := db.First(&current, "stable_id = ?", parent.StableID).Error; err != nil || current.DesiredPolicyVersion != 0 {
		t.Fatal("failed source admission mutated desired SQL policy", err)
	}
	if _, err := issueClientPolicyAuthority(ctx, db, j, request); err == nil {
		t.Fatal("live managed issuance accepted replaced SQL source")
	}
	account, err = j.Account(origin.ClientID)
	if err != nil || account.HeldCapacity != 0 {
		t.Fatalf("failed source admission held new quota: %+v/%v", account, err)
	}
}
