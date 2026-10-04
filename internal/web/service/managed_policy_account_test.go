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

func TestManagedPolicyIssuanceRejectsPendingSQLPolicyAndRetargetedParent(t *testing.T) {
	for _, fault := range []string{"pending-policy", "retargeted-parent"} {
		t.Run(fault, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db, ctx := database.GetDB(), context.Background()
			t.Logf("managed issuance admission backend: %s", db.Dialector.Name())
			c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close(ctx) })
			scope := model.ClientPolicyScopeGlobal
			if fault == "retargeted-parent" {
				scope = model.ClientPolicyScopeNode
			}
			parent := model.ClientRecord{Email: "managed-admission-parent", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Scope: &scope, Multiplier: "1.5"}}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			member := managedAuthorityMember{NodeID: "node-a", SourceID: "source-a"}
			origin, _, err := c.PrepareAccount(ctx, parent.StableID, member)
			if err != nil {
				t.Fatal(err)
			}
			j := c.state.Journal
			m := policyauthority.ClientMapping{Authority: j.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "original-node-a", Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: origin.ClientID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
			if err := j.RecordClientMapping(policyauthority.ClientMappingCoordinator, m); err != nil {
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
			request := policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: origin.ClientID, WindowID: a.Policy.WindowID, PolicyVersion: a.Policy.Version}, RequestID: "before-edit", ChallengeID: "challenge", Capacity: 20, Upload: a.Policy.Upload, Download: a.Policy.Download, LeaseDuration: time.Second}
			if _, err := issueClientPolicyAuthority(ctx, db, j, request); err != nil {
				t.Fatal(err)
			}
			if fault == "pending-policy" {
				if err := db.Model(&parent).Update("policy_multiplier", "2").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				other := model.ClientRecord{Email: "different-parent", Enable: true, TotalGB: 128, Policy: parent.Policy.Clone()}
				if err := db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Table("client_policy_node_accounts").Where("client_id = ?", origin.ClientID).Update("parent_client_id", other.StableID).Error; err != nil {
					t.Fatal(err)
				}
				if err := projectClientPolicyAuthority(ctx, db, j, origin.ClientID); err == nil {
					t.Fatal("SQL projection retargeted original parent")
				}
			}
			request.RequestID = "after-edit"
			if _, err := issueClientPolicyAuthority(ctx, db, j, request); err == nil {
				t.Fatal("SQL edit bypassed original managed admission")
			}
			after, err := j.Account(origin.ClientID)
			if err != nil || after.HeldCapacity != 20 {
				t.Fatalf("refused issuance changed original held budget: %+v/%v", after, err)
			}
		})
	}
}

func TestManagedPolicyAccountProvisioningUsesOriginalScope(t *testing.T) {
	setupPolicyLedgerDB(t)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed account provisioning backend: %s", db.Dialector.Name())
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	scope := model.ClientPolicyScopeGlobal
	global := model.ClientRecord{Email: "managed-global-parent", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Scope: &scope, Multiplier: "1.5", UploadBytesPerSecond: 8192, DownloadBytesPerSecond: 4096}}
	node := model.ClientRecord{Email: "managed-node-parent", Enable: true, TotalGB: 128}
	if err := db.Create(&global).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	a := managedAuthorityMember{NodeID: "actual-node-a", SourceID: "original-source-a"}
	b := managedAuthorityMember{NodeID: "actual-node-b", SourceID: "original-source-b"}
	ga, gp, err := c.PrepareAccount(ctx, global.StableID, a)
	if err != nil {
		t.Fatal(err)
	}
	gb, bp, err := c.PrepareAccount(ctx, global.StableID, b)
	if err != nil || ga != gb || ga.ClientID != global.StableID || gp.ClientId != global.StableID || gp.MultiplierMicros != 1500000 || bp.Version != gp.Version || ga.Scope != "global" {
		t.Fatalf("global account not shared: %+v/%+v/%v", ga, gb, err)
	}
	na, np, err := c.PrepareAccount(ctx, node.StableID, a)
	if err != nil {
		t.Fatal(err)
	}
	nb, _, err := c.PrepareAccount(ctx, node.StableID, b)
	if err != nil || na.ClientID == nb.ClientID || na.ClientID == node.StableID || na.ParentClientID != node.StableID || na.Scope != "node" || np.ClientId != na.ClientID {
		t.Fatalf("node accounts overlap: %+v/%+v/%v", na, nb, err)
	}
	for _, o := range []policyauthority.ManagedAccountOrigin{ga, na, nb} {
		retained, err := c.state.Journal.ManagedAccountOrigin(o.ClientID)
		if err != nil || retained != o {
			t.Fatalf("SQL replaced original origin: %+v/%v", retained, err)
		}
		account, err := c.state.Journal.Account(o.ClientID)
		if err != nil || account.Usage != (policyauthority.Usage{}) || account.HeldCapacity != 0 || account.Policy.QuotaBytes != 128 {
			t.Fatalf("provisioning minted grants/history: %+v/%v", account, err)
		}
		if o.Scope == "global" {
			for _, member := range []managedAuthorityMember{a, b} {
				proof := policyauthority.ClientMapping{Authority: c.state.Journal.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "original-" + member.NodeID, Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: o.ClientID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: o.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: o.PolicyDigest}
				if err := c.state.Journal.RecordClientMapping(policyauthority.ClientMappingCoordinator, proof); err != nil {
					t.Fatal(err)
				}
			}
		}
		strategy, err := newManagedJournalAllocationStrategy(c.state.Journal, []managedAuthorityMember{a, b})
		if err != nil {
			t.Fatal(err)
		}
		member := a
		if o.NodeID == b.NodeID {
			member = b
		}
		capacity, _, _, err := strategy.Allocate(account, policyauthority.NodeBoot{NodeID: member.NodeID, SourceID: member.SourceID, BootID: "allocation-boot"})
		wanted := uint64(64)
		if o.Scope == "node" {
			wanted = 128
		}
		if err != nil || capacity != wanted {
			t.Fatalf("scope %s quota share=%d want=%d: %v", o.Scope, capacity, wanted, err)
		}
	}
	if _, _, err := c.PrepareAccount(nil, global.StableID, a); err == nil {
		t.Fatal("nil context admitted provisioning")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := c.PrepareAccount(canceled, global.StableID, a); err == nil {
		t.Fatal("canceled context admitted provisioning")
	}
	if err := db.Model(&global).Update("policy_multiplier", "2").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PrepareAccount(ctx, global.StableID, a); err == nil {
		t.Fatal("changed full policy bypassed append-only proof")
	}
	consumed := model.ClientRecord{Email: "managed-consumed-parent", Enable: true, TotalGB: 128, Policy: &model.ClientPolicyOptions{Scope: &scope}}
	if err := db.Create(&consumed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientPolicyTotal{ClientID: consumed.StableID, RawUpload: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PrepareAccount(ctx, consumed.StableID, a); err == nil {
		t.Fatal("consumed SQL account acquired fresh original seed")
	}
	if _, err := c.state.Journal.Account(consumed.StableID); err == nil {
		t.Fatal("refused consumed account left a seed")
	}
}
