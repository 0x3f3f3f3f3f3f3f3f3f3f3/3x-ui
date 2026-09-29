package policyflow

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestControllerQueuedAdmissionRollbackStopsOnlyAffectedClient(t *testing.T) {
	db := flowDB(t)
	client, other := flowClient(t, db, 0), flowClient(t, db, 0)
	ledger := database.NewClientUsageLedger(db)
	controller := flowController(t, ledger)
	for _, id := range []string{client.PolicyID, other.PolicyID} {
		if err := controller.Configure(t.Context(), id, Rates{}); err != nil {
			t.Fatal(err)
		}
	}
	var flows [16]*Flow
	for n := range flows {
		var err error
		flows[n], err = controller.Open(t.Context(), client.PolicyID)
		if err != nil {
			t.Fatal(err)
		}
		defer flows[n].Close()
	}
	healthy, err := controller.Open(t.Context(), other.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Close()
	injected := errors.New("fixture rejects meter commit")
	var hit atomic.Bool
	callback := "test:reject-admission-meter"
	if err := db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		meter, ok := tx.Statement.Dest.(*model.ClientUsageMeter)
		if ok && meter.PolicyID == client.PolicyID {
			hit.Store(true)
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	received := &countedPayload{}
	done := make(chan error, len(flows))
	for _, flow := range flows {
		go func() {
			n, e := flow.DatagramWriter(Upload, received).Write([]byte("rollback"))
			if n != 0 && e == nil {
				e = errors.New("failed commit delivered a packet")
			}
			done <- e
		}()
	}
	for range flows {
		select {
		case err := <-done:
			if !errors.Is(err, injected) {
				t.Fatalf("failed admission did not propagate transaction error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("failed transaction stranded a queued admission")
		}
	}
	account, err := ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 || !hit.Load() || received.count.Load() != 0 {
		t.Fatalf("failed commit leaked payload or accounting: account=%+v err=%v injected=%t payload=%d", account, err, hit.Load(), received.count.Load())
	}
	var meter model.ClientUsageMeter
	if err := db.Where("policy_id = ? AND closed = ?", client.PolicyID, false).First(&meter).Error; err != nil || meter.Sequence != 0 || meter.Up != 0 {
		t.Fatalf("failed transaction advanced cursor: %+v error=%v", meter, err)
	}
	if n, err := healthy.DatagramWriter(Download, received).Write([]byte("healthy")); n != 7 || err != nil {
		t.Fatalf("another client was poisoned: %d %v", n, err)
	}
	account, err = ledger.Read(t.Context(), other.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 7 || account.Billed != 7 || received.count.Load() != 7 {
		t.Fatalf("unaffected client lost accounting: %+v error=%v payload=%d", account, err, received.count.Load())
	}
}

func TestControllerCloseDrainsWaitingAdmissionsBeforeReplacement(t *testing.T) {
	db := flowDB(t)
	client := flowClient(t, db, 0)
	ledger := database.NewClientUsageLedger(db)
	controller := flowController(t, ledger)
	if err := controller.Configure(t.Context(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	var flows [32]*Flow
	for n := range flows {
		var err error
		flows[n], err = controller.Open(t.Context(), client.PolicyID)
		if err != nil {
			t.Fatal(err)
		}
	}
	handle, _ := db.DB()
	handle.SetMaxOpenConns(1)
	held, err := handle.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	received := &countedPayload{}
	done := make(chan error, len(flows))
	before := handle.Stats().WaitCount
	for _, flow := range flows {
		go func() { _, err := flow.DatagramWriter(Upload, received).Write([]byte("pending")); done <- err }()
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for handle.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if handle.Stats().WaitCount == before {
		t.Fatal("admission did not reach the occupied database")
	}
	closed := make(chan struct{})
	go func() { controller.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("controller close stranded a database waiter")
	}
	for range flows {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("closed controller acknowledged uncommitted data")
			}
		case <-time.After(time.Second):
			t.Fatal("controller close stranded a queued writer")
		}
	}
	_ = held.Close()
	account, err := ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 || received.count.Load() != 0 {
		t.Fatalf("closed controller leaked an admission: %+v error=%v received=%d", account, err, received.count.Load())
	}
	replacement := flowController(t, ledger)
	if err := replacement.Configure(t.Context(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	flow, err := replacement.Open(t.Context(), client.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if n, err := flow.DatagramWriter(Download, received).Write([]byte("fresh")); n != 5 || err != nil {
		t.Fatalf("closed source poisoned replacement: %d %v", n, err)
	}
	account, err = ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 5 || account.Billed != 5 || received.count.Load() != 5 {
		t.Fatalf("replacement inherited pending bytes: %+v error=%v received=%d", account, err, received.count.Load())
	}
}
