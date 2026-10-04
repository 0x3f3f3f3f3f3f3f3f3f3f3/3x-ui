package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedPolicyDeletionMaintenancePreservesHistoryWithBoundedProgress(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx := context.Background()
	db := database.GetDB()
	c, err := getManagedPolicyCoordinator(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = StopManagedPolicyCoordinator(ctx) })
	scope := model.ClientPolicyScopeGlobal
	ids := []string{}
	for n := 0; n < 64; n++ {
		parent := model.ClientRecord{Email: fmt.Sprintf("history-parent-%03d", n), Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Scope: &scope, Multiplier: "1.5"}}
		if err := db.Create(&parent).Error; err != nil {
			t.Fatal(err)
		}
		member := managedAuthorityMember{NodeID: "history-node", SourceID: "history-source"}
		origin, _, err := c.PrepareAccount(ctx, parent.StableID, member)
		if err != nil {
			t.Fatal(err)
		}
		j := c.state.Journal
		mapping := policyauthority.ClientMapping{Authority: j.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "history-node-anchor", Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: origin.ClientID, LocalClientID: parent.StableID, GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
		if err := j.RecordClientMapping(policyauthority.ClientMappingCoordinator, mapping); err != nil {
			t.Fatal(err)
		}
		boot := policyauthority.NodeBoot{NodeID: member.NodeID, SourceID: member.SourceID, BootID: "history-boot"}
		if err := j.RegisterBoot(boot); err != nil {
			t.Fatal(err)
		}
		account, err := j.Account(origin.ClientID)
		if err != nil {
			t.Fatal(err)
		}
		for k := 0; k < 8; k++ {
			grant, err := j.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: origin.ClientID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: fmt.Sprintf("history-%03d-%02d", n, k), ChallengeID: "history-challenge", Capacity: 1, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Report(policyauthority.Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Seal: true}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, parent.StableID)
		if err := db.Delete(&parent).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientPolicyTombstone{ClientID: parent.StableID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := c.reconcileDeletions(ctx, nil); err != nil {
		t.Fatal(err)
	}
	countDeleted := func() int {
		count := 0
		for _, id := range ids {
			a, err := c.state.Journal.Account(id)
			if err != nil {
				t.Fatal(err)
			}
			if a.Deleted {
				count++
			}
			if a.Usage != (policyauthority.Usage{}) || a.HeldCapacity != 0 {
				t.Fatal("historical maintenance changed original usage or held allowance")
			}
		}
		return count
	}
	if got := countDeleted(); got == 0 || got == 64 {
		t.Fatalf("one maintenance pass did not bound historical work: deleted=%d", got)
	}
	for n := 0; n < 65 && countDeleted() != 64; n++ {
		if err := c.reconcileDeletions(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := countDeleted(); got != 64 {
		t.Fatalf("bounded maintenance lost deletion intent: %d", got)
	}
	t.Logf("managed deletion history backend: %s original_parents=64 sealed_grants=512", db.Dialector.Name())
}
