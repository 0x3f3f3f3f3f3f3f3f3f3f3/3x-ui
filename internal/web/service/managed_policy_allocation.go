package service

import (
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

type managedAuthorityMember struct {
	NodeID, SourceID string
}

type managedAllocationStrategy struct {
	members []managedAuthorityMember
	journal *policyauthority.Journal
}

func newManagedJournalAllocationStrategy(journal *policyauthority.Journal, members []managedAuthorityMember) (*managedAllocationStrategy, error) {
	if journal == nil {
		return nil, ErrClientPolicyLedger
	}
	strategy, err := newManagedAllocationStrategy(members)
	if err != nil {
		return nil, err
	}
	strategy.journal = journal
	return strategy, nil
}

func newManagedAllocationStrategy(members []managedAuthorityMember) (*managedAllocationStrategy, error) {
	if len(members) == 0 || len(members) > 1000 {
		return nil, ErrClientPolicyLedger
	}
	copy := slices.Clone(members)
	nodes, sources := make(map[string]bool), make(map[string]bool)
	for _, member := range copy {
		if !validPolicySourceKey(member.NodeID) || !validPolicySourceKey(member.SourceID) || nodes[member.NodeID] || sources[member.SourceID] {
			return nil, ErrClientPolicyLedger
		}
		nodes[member.NodeID], sources[member.SourceID] = true, true
	}
	slices.SortFunc(copy, func(a, b managedAuthorityMember) int {
		if a.NodeID < b.NodeID {
			return -1
		}
		if a.NodeID > b.NodeID {
			return 1
		}
		return 0
	})
	return &managedAllocationStrategy{members: copy}, nil
}

func managedDirectionShare(direction policyauthority.Direction, rank, count uint64) policyauthority.Direction {
	if direction.Unlimited {
		return policyauthority.Direction{Unlimited: true}
	}
	seats := min(count, direction.Rate, direction.Burst)
	if rank >= seats {
		return policyauthority.Direction{}
	}
	share := policyauthority.Direction{Rate: direction.Rate / seats, Burst: direction.Burst / seats}
	if rank < direction.Rate%seats {
		share.Rate++
	}
	if rank < direction.Burst%seats {
		share.Burst++
	}
	return share
}

func (s *managedAllocationStrategy) Allocate(account policyauthority.Account, boot policyauthority.NodeBoot) (uint64, policyauthority.Direction, policyauthority.Direction, error) {
	if s == nil || len(s.members) == 0 {
		return 0, policyauthority.Direction{}, policyauthority.Direction{}, ErrClientPolicyLedger
	}
	rank := slices.Index(s.members, managedAuthorityMember{NodeID: boot.NodeID, SourceID: boot.SourceID})
	if rank < 0 {
		return 0, policyauthority.Direction{}, policyauthority.Direction{}, ErrClientPolicyLedger
	}
	if s.journal != nil {
		origin, err := s.journal.ManagedAccountOrigin(account.Seed.ClientID)
		if err != nil {
			return 0, policyauthority.Direction{}, policyauthority.Direction{}, err
		}
		if origin.Scope == "node" {
			member := managedAuthorityMember{NodeID: origin.NodeID, SourceID: origin.SourceID}
			return (&managedAllocationStrategy{members: []managedAuthorityMember{member}}).Allocate(account, boot)
		}
	}
	count := uint64(len(s.members))
	upload := managedDirectionShare(account.Policy.Upload, uint64(rank), count)
	download := managedDirectionShare(account.Policy.Download, uint64(rank), count)
	if !upload.Unlimited && upload.Rate == 0 && !download.Unlimited && download.Rate == 0 {
		return 0, upload, download, nil
	}
	capacity := controllerCapacity(account)
	if !account.Policy.QuotaUnlimited {
		remaining := account.Policy.QuotaBytes
		for _, used := range []uint64{account.WindowUsed, account.FrozenBilled, (account.WindowRemainder + account.HeldRemainder + 999999) / 1000000} {
			remaining -= min(remaining, used)
		}
		target := remaining / count
		if uint64(rank) < remaining%count {
			target++
		}
		capacity = min(capacity, target)
	}
	return capacity, upload, download, nil
}
