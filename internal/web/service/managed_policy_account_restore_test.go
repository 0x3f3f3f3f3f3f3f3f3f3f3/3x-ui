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

func TestManagedPolicyNodeAccountRestoresOriginalUUIDAndHeldAllowance(t *testing.T) {
	setupPolicyLedgerDB(t)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed original account reconstruction backend: %s", db.Dialector.Name())
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	parent := model.ClientRecord{Email: "managed-restored-node", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Multiplier: "1.5"}}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	member := managedAuthorityMember{NodeID: "node-a", SourceID: "source-a"}
	origin, policy, err := c.PrepareAccount(ctx, parent.StableID, member)
	if err != nil {
		t.Fatal(err)
	}
	j := c.state.Journal
	mapping := policyauthority.ClientMapping{Authority: j.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "node-original-a", Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: origin.ClientID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
	if err := j.RecordClientMapping(policyauthority.ClientMappingCoordinator, mapping); err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: member.NodeID, SourceID: member.SourceID, BootID: "boot-a"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	a, err := j.Account(origin.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: origin.ClientID, WindowID: a.Policy.WindowID, PolicyVersion: a.Policy.Version}, RequestID: "original-held", ChallengeID: "original-challenge", Capacity: 60, Upload: a.Policy.Upload, Download: a.Policy.Download, LeaseDuration: time.Second}
	if _, err := issueClientPolicyAuthority(ctx, db, j, request); err != nil {
		t.Fatal(err)
	}
	before, err := j.Account(origin.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", origin.ClientID).Delete(&model.ClientPolicyNodeAccount{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", origin.ClientID).Delete(&model.ClientPolicyAuthorityProjection{}).Error; err != nil {
		t.Fatal(err)
	}
	restored, current, err := c.PrepareAccount(ctx, parent.StableID, member)
	if err != nil || restored != origin || current.ClientId != policy.ClientId || current.Version != policy.Version {
		t.Fatalf("SQL restoration failed to recover original canonical identity: %+v/%v", restored, err)
	}
	after, err := j.Account(origin.ClientID)
	if err != nil || after != before || after.HeldCapacity != 60 {
		t.Fatalf("SQL restoration replenished held allowance: %+v/%v", after, err)
	}
	var row model.ClientPolicyNodeAccount
	if err := db.First(&row, "client_id = ?", origin.ClientID).Error; err != nil || row.ParentClientID != parent.StableID || row.NodeID != member.NodeID || row.SourceID != member.SourceID {
		t.Fatal("SQL reconstruction lost original account tuple", err)
	}
	_, projection := authorityProjection(t, origin.ClientID)
	if projection != before {
		t.Fatal("SQL reconstruction created an empty balance")
	}
}
