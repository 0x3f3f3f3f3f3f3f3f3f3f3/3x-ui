package clientpolicy

import (
	"reflect"
	"testing"
)

func TestLedgerCursorAndConfirmedCountersComeFromOneCommittedRecord(t *testing.T) {
	e, _ := persistentEngine(t)
	if err := e.Apply(testPolicy("ledger")); err != nil {
		t.Fatal(err)
	}
	s := openSession(t, e, "ledger", nil)
	if err := s.Admit(Upload, 1); err != nil {
		t.Fatal(err)
	}
	first, err := e.ReadLedger(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Usage != (Usage{}) || first[0].ReservedBytes != reservationRawBytes {
		t.Fatalf("live uncommitted usage exposed as committed ledger: %+v", first)
	}
	again, err := e.ReadLedger(0, 10)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("read changed ledger: %+v %v", again, err)
	}
	if err := e.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	next, err := e.ReadLedger(first[0].Sequence, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Usage != (Usage{RawUpload: 1, BilledBytes: 1}) || next[0].ReservedBytes != 0 || next[0].Sequence <= first[0].Sequence || next[0].InstanceID != "node-1" || next[0].Epoch != 1 {
		t.Fatalf("inconsistent committed cursor: %+v", next)
	}
	empty, err := e.ReadLedger(next[0].Sequence, 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("replayed already read record: %+v %v", empty, err)
	}
}

func TestLedgerPaginationAndFutureCursorCannotSilentlyReset(t *testing.T) {
	e, _ := persistentEngine(t)
	for _, id := range []string{"z", "a", "m"} {
		if err := e.Apply(testPolicy(id)); err != nil {
			t.Fatal(err)
		}
	}
	var after uint64
	for _, id := range []string{"z", "a", "m"} {
		rows, err := e.ReadLedger(after, 1)
		if err != nil || len(rows) != 1 || rows[0].ClientID != id || rows[0].Sequence <= after {
			t.Fatalf("cursor order: %+v %v", rows, err)
		}
		after = rows[0].Sequence
	}
	if _, err := e.ReadLedger(after+100, 1); err == nil {
		t.Fatal("accepted a cursor from a newer or rolled-back store")
	}
}

func TestClosedEngineDoesNotAdvertiseReadyControlState(t *testing.T) {
	e, _ := persistentEngine(t)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if e.Capabilities().Ready {
		t.Fatal("closed engine advertised ready")
	}
}
