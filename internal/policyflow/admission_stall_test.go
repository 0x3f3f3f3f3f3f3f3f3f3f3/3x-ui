package policyflow

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

type countedPayload struct{ count atomic.Int64 }

func (w *countedPayload) Write(p []byte) (int, error) { w.count.Add(int64(len(p))); return len(p), nil }

func TestControllerAdmissionDelayDoesNotAccumulateRateGrants(t *testing.T) {
	for _, direction := range []Direction{Upload, Download} {
		for _, datagram := range []bool{false, true} {
			t.Run(fmt.Sprintf("direction-%d/datagram-%t", direction, datagram), func(t *testing.T) { testAdmissionDelay(t, direction, datagram, 65536) })
		}
	}
}

func TestControllerRateChangeShapesAlreadyQueuedAdmissions(t *testing.T) {
	for _, direction := range []Direction{Upload, Download} {
		for _, datagram := range []bool{false, true} {
			t.Run(fmt.Sprintf("direction-%d/datagram-%t", direction, datagram), func(t *testing.T) { testAdmissionDelay(t, direction, datagram, 0) })
		}
	}
}

func testAdmissionDelay(t *testing.T, direction Direction, datagram bool, initialRate int64) {
	db := flowDB(t)
	client := flowClient(t, db, 0)
	controller := flowController(t, database.NewClientUsageLedger(db))
	const rate int64 = 65536
	const burst = rate / 10
	if err := controller.Configure(t.Context(), client.PolicyID, Rates{Upload: initialRate, Download: initialRate}); err != nil {
		t.Fatal(err)
	}
	flows := make([]*Flow, 6)
	for n := range flows {
		var err error
		flows[n], err = controller.Open(t.Context(), client.PolicyID)
		if err != nil {
			t.Fatal(err)
		}
		defer flows[n].Close()
	}
	handle, _ := db.DB()
	handle.SetMaxOpenConns(1)
	held, err := handle.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	before := handle.Stats().WaitCount
	receiver := &countedPayload{}
	done := make(chan error, len(flows))
	for _, f := range flows {
		go func() {
			writer := f.Writer(direction, receiver)
			if datagram {
				writer = f.DatagramWriter(direction, receiver)
			}
			_, err := writer.Write(make([]byte, burst))
			done <- err
		}()
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for handle.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if handle.Stats().WaitCount == before {
		t.Fatal("payload did not reach occupied admission database")
	}
	time.Sleep(250 * time.Millisecond)
	start := time.Now()
	if initialRate == 0 {
		if err := controller.Configure(t.Context(), client.PolicyID, Rates{Upload: rate, Download: rate}); err != nil {
			t.Fatal(err)
		}
	}
	_ = held.Close()
	time.Sleep(60 * time.Millisecond)
	elapsed := time.Since(start).Seconds()
	delivered := receiver.count.Load()
	upper := float64(rate)*elapsed*1.06 + float64(burst)
	if float64(delivered) > upper {
		t.Errorf("admission stall released %d payload bytes above %.0f-byte rate/burst bound in %.3fs", delivered, upper, elapsed)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for range flows {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("rate shaping stranded a flow")
		}
	}
}
