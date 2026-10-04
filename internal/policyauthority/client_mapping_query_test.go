package policyauthority

import (
	"errors"
	"testing"
)

func TestClientMappingAccountLookupUsesOriginalReverseProof(t *testing.T) {
	j, _ := managedJournalFixture(t, nil, `{"role":"managed-coordinator","schema":1}`, `{"mode":"local"}`)
	if err := j.ActivateManagedCoordinator("managed-source"); err != nil {
		t.Fatal(err)
	}
	seed, origin := managedOriginFixture()
	if err := j.AddManagedAccount(seed, origin); err != nil {
		t.Fatal(err)
	}
	m := ClientMapping{Authority: j.Identity(), NodeAnchor: Identity{"node-original", 1}, NodeID: "node-a", SourceID: "source-a", GlobalClientID: origin.ClientID, LocalClientID: "22222222-2222-4222-8222-222222222222", GlobalPolicyVersion: seed.Policy.Version, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
	if _, err := j.LookupClientMappingAccount(ClientMappingCoordinator, m.SourceID, m.GlobalClientID); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing proof became enrollment", err)
	}
	if err := j.RecordClientMapping(ClientMappingCoordinator, m); err != nil {
		t.Fatal(err)
	}
	got, err := j.LookupClientMappingAccount(ClientMappingCoordinator, m.SourceID, m.GlobalClientID)
	if err != nil || got != m {
		t.Fatal("original reverse proof changed", err)
	}
	if _, err := j.LookupClientMappingAccount(ClientMappingCoordinator, "other-source", m.GlobalClientID); !errors.Is(err, ErrNotFound) {
		t.Fatal("other source inherited enrollment", err)
	}
	if _, err := j.LookupClientMappingAccount("invalid", m.SourceID, m.GlobalClientID); err == nil {
		t.Fatal("invalid side admitted")
	}
}
