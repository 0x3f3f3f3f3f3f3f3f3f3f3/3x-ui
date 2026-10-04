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

// Unrelated configured nodes must not reserve a client's directional seats or
// last quota byte. Partial overlap still conserves that client's shared limits.
func TestManagedPolicyAllocationUsesCanonicalClientMembership(t *testing.T) {
	setupPolicyLedgerDB(t)
	ctx, db := context.Background(), database.GetDB()
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	members := []managedAuthorityMember{{"node-a", "source-a"}, {"node-b", "source-b"}, {"node-c", "source-c"}}
	strategy, err := newManagedJournalAllocationStrategy(c.state.Journal, members)
	if err != nil {
		t.Fatal(err)
	}
	scope := model.ClientPolicyScopeGlobal
	for _, fixture := range []struct {
		name              string
		nodes             []int
		quota, rate       int64
		capacities, rates []uint64
	}{
		{"only-a", []int{0}, 1, 1, []uint64{1}, []uint64{1}},
		{"only-b", []int{1}, 1, 1, []uint64{1}, []uint64{1}},
		{"overlap-a-b", []int{0, 1}, 3, 3, []uint64{2, 1}, []uint64{2, 1}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			parent := model.ClientRecord{Email: fixture.name, Enable: true, TotalGB: fixture.quota, Policy: &model.ClientPolicyOptions{Scope: &scope, UploadBytesPerSecond: fixture.rate, DownloadBytesPerSecond: fixture.rate}}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			origin, _, err := c.PrepareAccount(ctx, parent.StableID, members[fixture.nodes[0]])
			if err != nil {
				t.Fatal(err)
			}
			j := c.state.Journal
			for _, index := range fixture.nodes {
				member := members[index]
				mapping := policyauthority.ClientMapping{Authority: j.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "anchor-" + member.NodeID, Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: origin.ClientID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
				if err := j.RecordClientMapping(policyauthority.ClientMappingCoordinator, mapping); err != nil {
					t.Fatal(err)
				}
			}
			var grants []policyauthority.Grant
			for position, index := range fixture.nodes {
				member := members[index]
				boot := policyauthority.NodeBoot{NodeID: member.NodeID, SourceID: member.SourceID, BootID: "boot-" + fixture.name + member.NodeID}
				if err := j.RegisterBoot(boot); err != nil {
					t.Fatal(err)
				}
				account, err := j.Account(origin.ClientID)
				if err != nil {
					t.Fatal(err)
				}
				capacity, upload, download, err := strategy.Allocate(account, boot)
				if err != nil || capacity != fixture.capacities[position] || upload.Rate != fixture.rates[position] || download.Rate != fixture.rates[position] {
					t.Fatalf("unrelated configured node stole canonical-client allowance: %d/%+v/%+v/%v", capacity, upload, download, err)
				}
				grant, err := j.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: j.Identity(), NodeBoot: boot, ClientID: origin.ClientID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "last-byte-" + member.NodeID, ChallengeID: "challenge", Capacity: capacity, Upload: upload, Download: download, LeaseDuration: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				grants = append(grants, grant)
			}
			for _, grant := range grants {
				if err := j.Report(policyauthority.Report{Binding: grant.Request.Binding, GrantID: grant.GrantID, Sequence: 1, Usage: policyauthority.Usage{RawUpload: grant.Request.Capacity, BilledBytes: grant.Request.Capacity}, Seal: true}); err != nil {
					t.Fatal(err)
				}
			}
			final, err := j.Account(origin.ClientID)
			if err != nil || final.Usage.BilledBytes != uint64(fixture.quota) || final.HeldCapacity != 0 || final.UploadHeld.Rate != 0 || final.DownloadHeld.Rate != 0 {
				t.Fatalf("canonical member shares lost final quota byte: %+v/%v", final, err)
			}
			if _, _, _, err := strategy.Allocate(final, policyauthority.NodeBoot{NodeID: "node-c", SourceID: "source-c"}); err == nil {
				t.Fatal("unmapped canonical client received an allocation")
			}
		})
	}
}
