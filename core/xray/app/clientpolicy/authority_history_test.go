package clientpolicy

import (
	"encoding/json"
	"testing"
	"time"
)

func historySnapshot(t *testing.T, e *Engine, id string, expected bool) {
	t.Helper()
	_, snapshot, err := e.GetClient(id)
	if err != nil || snapshot.Usage != (Usage{}) || snapshot.UncertainBytes != 0 || snapshot.FirstUsedAt != 0 {
		t.Fatalf("history proof changed zero business usage: %+v/%v", snapshot, err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var history bool
	if value := fields["authorityGrantHistory"]; value != nil {
		if err := json.Unmarshal(value, &history); err != nil {
			t.Fatal(err)
		}
	}
	if history != expected {
		t.Fatalf("zero-used grant history proof: got %v, want %v", history, expected)
	}
}

func TestClientAuthorityHistorySurvivesSealAndReopen(t *testing.T) {
	p := testPolicy("history-owner")
	e, grant := authorityExecutionFixture(t, p, 40, time.Second)
	path := e.store.(*boltStore).db.Path()
	instance := e.Capabilities().InstanceID
	historySnapshot(t, e, p.ClientID, false)
	if _, err := e.InstallAuthorityGrant(grant); err != nil {
		t.Fatal(err)
	}
	historySnapshot(t, e, p.ClientID, true)
	if _, err := e.SealAuthorityGrant(p.ClientID, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	historySnapshot(t, e, p.ClientID, true)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openPersistentEngine(path, instance, "new-history-boot")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	historySnapshot(t, reopened, p.ClientID, true)
}
