package clientpolicy

import "testing"

func TestCheckpointCommitsOnlyChangedUsageAndReservations(t *testing.T) {
	e, path := persistentEngine(t)
	active := testPolicy("active")
	if err := e.ApplyBatch([]Policy{active, testPolicy("idle")}); err != nil {
		t.Fatal(err)
	}
	committed, err := e.ReadLedger(0, 10)
	if err != nil || len(committed) != 2 {
		t.Fatalf("initial ledger: %+v, %v", committed, err)
	}
	after := committed[1].Sequence
	assertIdle := func() {
		t.Helper()
		for range 2 {
			if err := e.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			rows, err := e.ReadLedger(after, 10)
			if err != nil || len(rows) != 0 {
				t.Fatalf("idle checkpoint rewrote committed clients: %+v, %v", rows, err)
			}
		}
	}
	assertIdle()
	session := openSession(t, e, "active", nil)
	if err := session.Admit(Upload, 65536); err != nil {
		t.Fatal(err)
	}
	if err := e.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	rows, err := e.ReadLedger(after, 10)
	if err != nil || len(rows) != 1 || rows[0].ClientID != "active" || rows[0].ReservedBytes != 0 || rows[0].Usage != (Usage{RawUpload: 65536, BilledBytes: 65536}) {
		t.Fatalf("exhausted reservation usage not committed exactly: %+v, %v", rows, err)
	}
	after = rows[0].Sequence
	assertIdle()
	if err := session.Admit(Download, 5); err != nil {
		t.Fatal(err)
	}
	active.Version = 2
	active.Multiplier = 500000
	if err := e.Apply(active); err != nil {
		t.Fatal(err)
	}
	rows, err = e.ReadLedger(after, 10)
	if err != nil || len(rows) != 1 || rows[0].Usage.BilledBytes != 65541 || rows[0].ReservedBytes != 0 {
		t.Fatalf("policy commit lost prior usage: %+v, %v", rows, err)
	}
	after = rows[0].Sequence
	assertIdle()
	if err := session.Admit(Download, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	rows, err = e.ReadLedger(after, 10)
	want := Usage{RawUpload: 65536, RawDownload: 6, BilledBytes: 65541, Remainder: 500000}
	if err != nil || len(rows) != 1 || rows[0].Usage != want || rows[0].ReservedBytes != 0 {
		t.Fatalf("fractional usage or reservation release lost: %+v, %v", rows, err)
	}
	after = rows[0].Sequence
	assertIdle()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPersistentEngine(path, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Snapshot("active")
	if err != nil || got.Usage != want || got.UncertainBytes != 0 {
		t.Fatalf("checkpoint usage changed after recovery: %+v, %v", got, err)
	}
}
