package policyflow

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestControllerQueuedDatagramsShareDurableCommits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cancelQueued  bool
		wantDirection int64
		wantBilled    int64
	}{{"duplex", false, 16384, 49152}, {"cancel-half", true, 8192, 24576}} {
		t.Run(tc.name, func(t *testing.T) {
			db := flowDB(t)
			client := flowClient(t, db, 0)
			ledger := database.NewClientUsageLedger(db)
			if _, err := ledger.ChangeMultiplier(t.Context(), client.PolicyID, 1, 1500, nil); err != nil {
				t.Fatal(err)
			}
			controller := flowController(t, ledger)
			if err := controller.Configure(t.Context(), client.PolicyID, Rates{}); err != nil {
				t.Fatal(err)
			}
			var flows [16]*Flow
			queuedCtx, cancelQueued := context.WithCancel(t.Context())
			defer cancelQueued()
			for n := range flows {
				ctx := t.Context()
				if n >= 8 {
					ctx = queuedCtx
				}
				var err error
				flows[n], err = controller.Open(ctx, client.PolicyID)
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
			var received [2]countedPayload
			type result struct {
				index, count int
				err          error
			}
			done := make(chan result, len(flows))
			var started sync.WaitGroup
			write := func(n int) {
				started.Done()
				count, err := flows[n].DatagramWriter(Direction(n%2), &received[n%2]).Write(make([]byte, 2048))
				done <- result{n, count, err}
			}
			before := handle.Stats().WaitCount
			started.Add(1)
			go write(0)
			deadline := time.Now().Add(200 * time.Millisecond)
			for handle.Stats().WaitCount == before && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if handle.Stats().WaitCount == before {
				t.Fatal("first datagram did not reach the occupied database")
			}
			started.Add(15)
			for n := 1; n < len(flows); n++ {
				go write(n)
			}
			started.Wait()
			time.Sleep(100 * time.Millisecond)
			if received[0].count.Load() != 0 || received[1].count.Load() != 0 {
				t.Fatal("payload was delivered before durable admission")
			}
			if tc.cancelQueued {
				cancelQueued()
			}
			_ = held.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			for range flows {
				select {
				case r := <-done:
					if tc.cancelQueued && r.index >= 8 {
						if r.count != 0 || !errors.Is(r.err, context.Canceled) {
							t.Fatalf("canceled queued packet: %+v", r)
						}
					} else if r.count != 2048 || r.err != nil {
						t.Fatalf("queued packet did not complete intact: %+v", r)
					}
				case <-ctx.Done():
					t.Fatal("queued admissions did not finish")
				}
			}
			account, err := ledger.Read(t.Context(), client.PolicyID)
			if err != nil || account.Up != tc.wantDirection || account.Down != tc.wantDirection || account.Billed != tc.wantBilled || account.Remainder != 0 {
				t.Fatalf("queued admission accounting: %+v error=%v", account, err)
			}
			for i := range received {
				if got := received[i].count.Load(); got != tc.wantDirection {
					t.Fatalf("direction %d delivered=%d want=%d", i, got, tc.wantDirection)
				}
			}
			var meter model.ClientUsageMeter
			if err := db.Where("policy_id = ? AND closed = ?", client.PolicyID, false).First(&meter).Error; err != nil {
				t.Fatal(err)
			}
			if meter.Sequence > 3 {
				t.Fatalf("already queued datagrams required %d separate durable cursor writes; want at most 3", meter.Sequence)
			}
		})
	}
}

func TestControllerBatchOverflowPreservesIndividuallyValidAdmissions(t *testing.T) {
	db := flowDB(t)
	client := flowClient(t, db, 0)
	ledger := database.NewClientUsageLedger(db)
	if err := ledger.Ensure(t.Context(), client.PolicyID); err != nil {
		t.Fatal(err)
	}
	const initial int64 = math.MaxInt64 - 10
	if err := db.Model(&model.ClientUsageAccount{}).Where("policy_id = ?", client.PolicyID).Updates(map[string]any{"up": initial, "billed": initial}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("up", initial).Error; err != nil {
		t.Fatal(err)
	}
	controller := flowController(t, ledger)
	if err := controller.Configure(t.Context(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	var flows [3]*Flow
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
	done := make(chan error, 3)
	receiver := &countedPayload{}
	before := handle.Stats().WaitCount
	go func() { _, err := flows[0].DatagramWriter(Upload, receiver).Write([]byte("x")); done <- err }()
	deadline := time.Now().Add(200 * time.Millisecond)
	for handle.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if handle.Stats().WaitCount == before {
		t.Fatal("first packet did not enter the occupied database")
	}
	for n := 1; n < len(flows); n++ {
		go func() { _, err := flows[n].DatagramWriter(Download, receiver).Write([]byte("123456")); done <- err }()
	}
	time.Sleep(100 * time.Millisecond)
	_ = held.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	sawOverflow := false
	for range flows {
		select {
		case err := <-done:
			sawOverflow = sawOverflow || errors.Is(err, clientpolicy.ErrOverflow)
		case <-ctx.Done():
			t.Fatal("overflow left admissions waiting")
		}
	}
	account, err := ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != math.MaxInt64-9 || account.Down != 6 || account.Billed != math.MaxInt64-3 {
		t.Fatalf("aggregate overflow rejected an individually valid packet: %+v error=%v", account, err)
	}
	if !sawOverflow || receiver.count.Load() > 7 {
		t.Fatalf("overflow protection failed: error=%t delivered=%d", sawOverflow, receiver.count.Load())
	}
}
