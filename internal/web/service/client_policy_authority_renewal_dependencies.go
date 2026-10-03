package service

import (
	"cmp"
	"container/heap"
	"context"
	"slices"
)

type authorityRenewalEffectPosition struct {
	Operation   int
	BeforeCount int
	AfterCount  int
}

type authorityRenewalReadyHeap []authorityRenewalOperationOrder

func (h authorityRenewalReadyHeap) Len() int { return len(h) }
func (h authorityRenewalReadyHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.ResetAt == 0 && b.ResetAt != 0 {
		return false
	}
	if b.ResetAt == 0 && a.ResetAt != 0 {
		return true
	}
	if a.ResetAt != b.ResetAt {
		return a.ResetAt < b.ResetAt
	}
	if a.ResetAt == 0 && a.CaptureAt != b.CaptureAt {
		return a.CaptureAt < b.CaptureAt
	}
	return a.RequestID < b.RequestID
}
func (h authorityRenewalReadyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *authorityRenewalReadyHeap) Push(v any)   { *h = append(*h, v.(authorityRenewalOperationOrder)) }
func (h *authorityRenewalReadyHeap) Pop() any {
	old := *h
	v := old[len(old)-1]
	old[len(old)-1] = authorityRenewalOperationOrder{}
	*h = old[:len(old)-1]
	return v
}

// A shared UUID's consumed count establishes causality even when clock time
// repeats or moves backwards. Keep only these ordering fields, never payloads.
func sortAuthorityRenewalOperations(ctx context.Context, operations []authorityRenewalOperationOrder, positions map[string][]authorityRenewalEffectPosition) ([]authorityRenewalOperationOrder, error) {
	children := make(map[int]map[int]bool)
	parents := make([]int, len(operations))
	for _, effects := range positions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		slices.SortFunc(effects, func(a, b authorityRenewalEffectPosition) int { return cmp.Compare(a.BeforeCount, b.BeforeCount) })
		for i := 1; i < len(effects); i++ {
			prior, next := effects[i-1], effects[i]
			if prior.AfterCount > next.BeforeCount || prior.Operation == next.Operation {
				return nil, ErrClientPolicyLedger
			}
			if children[prior.Operation] == nil {
				children[prior.Operation] = make(map[int]bool)
			}
			if !children[prior.Operation][next.Operation] {
				children[prior.Operation][next.Operation] = true
				parents[next.Operation]++
			}
		}
	}
	ready := authorityRenewalReadyHeap{}
	for i, operation := range operations {
		if parents[i] == 0 {
			heap.Push(&ready, operation)
		}
	}
	ordered := make([]authorityRenewalOperationOrder, 0, len(operations))
	for ready.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		operation := heap.Pop(&ready).(authorityRenewalOperationOrder)
		ordered = append(ordered, operation)
		for child := range children[operation.Index] {
			parents[child]--
			if parents[child] == 0 {
				heap.Push(&ready, operations[child])
			}
		}
	}
	if len(ordered) != len(operations) {
		return nil, ErrClientPolicyLedger
	}
	return ordered, nil
}
