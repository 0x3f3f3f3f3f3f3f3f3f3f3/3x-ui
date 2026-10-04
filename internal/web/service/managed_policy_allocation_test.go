package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedPolicyAllocationConservesQuotaAndDirectionalShares(t *testing.T) {
	members := []managedAuthorityMember{{NodeID: "node-b", SourceID: "source-b"}, {NodeID: "node-a", SourceID: "source-a"}}
	strategy, err := newManagedAllocationStrategy(members)
	if err != nil {
		t.Fatal(err)
	}
	members[0].NodeID = "caller-mutated-node"
	for _, count := range []uint64{1, 2, 3, 7, 128} {
		account := policyauthority.Account{Policy: policyauthority.Policy{QuotaBytes: 128, Upload: policyauthority.Direction{Rate: count, Burst: 3}, Download: policyauthority.Direction{Rate: 5, Burst: count}}}
		var held, upRate, upBurst, downRate, downBurst uint64
		for _, member := range []managedAuthorityMember{{NodeID: "node-a", SourceID: "source-a"}, {NodeID: "node-b", SourceID: "source-b"}} {
			capacity, up, down, err := strategy.Allocate(account, policyauthority.NodeBoot{NodeID: member.NodeID, SourceID: member.SourceID})
			wanted := uint64(64)
			if count == 1 && member.NodeID == "node-b" {
				wanted = 0
			}
			if err != nil || capacity != wanted || up.Unlimited || down.Unlimited || (up.Rate == 0) != (up.Burst == 0) || (down.Rate == 0) != (down.Burst == 0) {
				t.Fatalf("invalid or geometrically shrinking member share: %d/%+v/%+v/%v", capacity, up, down, err)
			}
			held += capacity
			account.HeldCapacity = held
			upRate += up.Rate
			upBurst += up.Burst
			downRate += down.Rate
			downBurst += down.Burst
		}
		wantedHeld := uint64(128)
		if count == 1 {
			wantedHeld = 64
		}
		if held != wantedHeld || upRate != count || upBurst != 3 || downRate != 5 || downBurst != count {
			t.Fatal("managed shares exceeded or discarded aggregate allowance")
		}
	}
	account := policyauthority.Account{Policy: policyauthority.Policy{QuotaBytes: 128, Upload: policyauthority.Direction{Unlimited: true}, Download: policyauthority.Direction{Unlimited: true}}, WindowUsed: 3, WindowRemainder: 500000, HeldRemainder: 500000, FrozenBilled: 4, HeldCapacity: 100}
	capacity, up, down, err := strategy.Allocate(account, policyauthority.NodeBoot{NodeID: "node-b", SourceID: "source-b"})
	if err != nil || capacity != 20 || !up.Unlimited || !down.Unlimited {
		t.Fatalf("fraction/frozen/held conservation: %d/%v", capacity, err)
	}
	if _, _, _, err := strategy.Allocate(account, policyauthority.NodeBoot{NodeID: "node-a", SourceID: "other-source"}); err == nil {
		t.Fatal("unregistered source received allowance")
	}
	for _, bad := range [][]managedAuthorityMember{nil, {{NodeID: "a", SourceID: "s"}, {NodeID: "a", SourceID: "t"}}, {{NodeID: "a", SourceID: "s"}, {NodeID: "b", SourceID: "s"}}} {
		if _, err := newManagedAllocationStrategy(bad); err == nil {
			t.Fatal("invalid membership admitted")
		}
	}
}

func TestManagedPolicyAllocationDoesNotHoldUnusableQuota(t *testing.T) {
	strategy, err := newManagedAllocationStrategy([]managedAuthorityMember{{NodeID: "a", SourceID: "source-a"}, {NodeID: "b", SourceID: "source-b"}})
	if err != nil {
		t.Fatal(err)
	}
	account := policyauthority.Account{Policy: policyauthority.Policy{QuotaBytes: 128, Upload: policyauthority.Direction{Rate: 1, Burst: 1}, Download: policyauthority.Direction{Rate: 1, Burst: 1}}}
	capacity, up, down, err := strategy.Allocate(account, policyauthority.NodeBoot{NodeID: "b", SourceID: "source-b"})
	if err != nil || capacity != 0 || up != (policyauthority.Direction{}) || down != (policyauthority.Direction{}) {
		t.Fatalf("a node denied in both directions reserved unusable quota: %d/%v", capacity, err)
	}
}
