package xray

import (
	"errors"
	"testing"
)

func TestFinalTrafficCounterNamesPreserveDelimiterEmails(t *testing.T) {
	for _, email := range []string{"a>b", "a>>>traffic>>>uplink", "inbound>>>stolen>>>traffic>>>uplink", "outbound>>>stolen"} {
		t.Run(email, func(t *testing.T) {
			p := NewProcess(&Config{})
			batch, err := p.trafficBatchFromCounters(map[string]int64{"user>>>" + email + ">>>traffic>>>uplink": 7, "user>>>" + email + ">>>traffic>>>downlink": 9}, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.batch.Traffics) != 0 || len(batch.batch.ClientTraffics) != 1 {
				t.Fatalf("counter misclassified: %+v", batch.batch)
			}
			client := batch.batch.ClientTraffics[0]
			if client.Email != email || client.Up != 7 || client.Down != 9 {
				t.Fatalf("counter name changed identity: %+v", client)
			}
		})
	}
}

func TestFinalTrafficOwnersRetainAdditionsBeforeDrain(t *testing.T) {
	p := NewProcess(&Config{})
	if err := p.PinFinalTrafficOwners(map[string]string{"a": "owner-a"}); err != nil {
		t.Fatal(err)
	}
	if err := p.PinFinalTrafficOwners(map[string]string{"a": "owner-a", "b": "owner-b"}); err != nil {
		t.Fatal(err)
	}
	if err := p.PinFinalTrafficOwners(map[string]string{"a": "owner-a", "b": "replacement"}); err == nil {
		t.Fatal("additional owner was not pinned")
	}
}

func TestFinalTrafficOwnersRejectAdditionsAfterDrain(t *testing.T) {
	p := NewProcess(&Config{})
	if err := p.PinFinalTrafficOwners(map[string]string{"a": "owner-a"}); err != nil {
		t.Fatal(err)
	}
	p.trafficDraining = true
	if err := p.PinFinalTrafficOwners(map[string]string{"a": "owner-a", "b": "new-owner"}); err == nil {
		t.Fatal("unknown final owner accepted after drain")
	}
}

func TestFinalTrafficOwnershipBlocksOrdinaryPolling(t *testing.T) {
	p := NewProcess(&Config{})
	if err := p.PinFinalTrafficOwners(map[string]string{"a": "owner-a"}); err != nil {
		t.Fatal(err)
	}
	if !p.FinalTrafficPending() {
		t.Fatal("handoff lost ownership before replayed SQL completed")
	}
	if _, _, err := p.SettleTraffic(func(*TrafficBatch) error { t.Fatal("ordinary writer bypassed owner checks"); return nil }); !errors.Is(err, ErrFinalTrafficPending) {
		t.Fatalf("ordinary poll was not fenced: %v", err)
	}
}
