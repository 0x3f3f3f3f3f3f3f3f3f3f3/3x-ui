package service

import (
	"context"
	"errors"
	"testing"
)

// Conflicting effects across shared UUIDs cannot be projected in any valid
// order. These small dependency checks supplement actual clock/hash reversal.
func TestAuthorityRenewalEffectDependenciesRejectConflicts(t *testing.T) {
	operations := []authorityRenewalOperationOrder{{Index: 0, RequestID: "first", ResetAt: 30}, {Index: 1, RequestID: "second", ResetAt: 20}, {Index: 2, RequestID: "third", ResetAt: 10}}
	for _, kind := range []string{"chain", "overlap", "cycle", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			positions := map[string][]authorityRenewalEffectPosition{
				"identity-a": {{Operation: 0, BeforeCount: 0, AfterCount: 1}, {Operation: 1, BeforeCount: 1, AfterCount: 2}},
				"identity-b": {{Operation: 1, BeforeCount: 4, AfterCount: 5}, {Operation: 2, BeforeCount: 5, AfterCount: 6}},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "overlap":
				positions["identity-a"][1].BeforeCount = 0
			case "cycle":
				positions["identity-c"] = []authorityRenewalEffectPosition{{Operation: 2, BeforeCount: 0, AfterCount: 1}, {Operation: 0, BeforeCount: 1, AfterCount: 2}}
			case "cancelled":
				cancel()
			}
			ordered, err := sortAuthorityRenewalOperations(ctx, operations, positions)
			if kind == "chain" {
				if err != nil || len(ordered) != 3 || ordered[0].Index != 0 || ordered[1].Index != 1 || ordered[2].Index != 2 {
					t.Fatalf("clock order overrode shared identity causality: %+v/%v", ordered, err)
				}
			} else {
				want := ErrClientPolicyLedger
				if kind == "cancelled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || len(ordered) != 0 {
					t.Fatalf("conflicting effects accepted: %+v/%v", ordered, err)
				}
			}
		})
	}
}
