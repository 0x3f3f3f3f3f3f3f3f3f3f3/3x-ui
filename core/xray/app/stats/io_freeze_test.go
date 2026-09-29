package stats_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func TestCounterFreezeWaitsForIOAccountingAndSurvivesTimeout(t *testing.T) {
	for _, writing := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[writing], func(t *testing.T) {
			manager, err := appstats.NewManager(context.Background(), &appstats.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sealer, ok := any(manager).(interface {
				SealCounters(context.Context) (map[string]int64, error)
			})
			if !ok {
				t.Fatal("stats manager cannot establish a final IO accounting boundary")
			}
			counter, err := manager.GetOrRegisterCounter("user>>>delayed>>>traffic>>>uplink")
			if err != nil {
				t.Fatal(err)
			}
			left, right := net.Pipe()
			t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
			paused := &pausedAccountingConn{Conn: left, completed: make(chan struct{}), release: make(chan struct{}), writing: writing}
			var release sync.Once
			unblock := func() { release.Do(func() { close(paused.release) }) }
			t.Cleanup(unblock)
			conn := &stat.CounterConnection{Connection: paused, ReadCounter: counter, WriteCounter: counter}
			done := make(chan error, 1)
			go func() {
				var n int
				var err error
				if writing {
					n, err = conn.Write([]byte("abc"))
				} else {
					n, err = io.ReadFull(conn, make([]byte, 3))
				}
				if err == nil && n != 3 {
					err = io.ErrShortWrite
				}
				done <- err
			}()
			_ = right.SetDeadline(time.Now().Add(time.Second))
			if writing {
				if _, err := io.ReadFull(right, make([]byte, 3)); err != nil {
					t.Fatal(err)
				}
			} else if _, err := right.Write([]byte("abc")); err != nil {
				t.Fatal(err)
			}
			<-paused.completed
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err = sealer.SealCounters(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("freeze acknowledged completed IO before its counter: %v", err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			final, err := sealer.SealCounters(ctx)
			if err != nil || final["user>>>delayed>>>traffic>>>uplink"] != 3 {
				t.Fatalf("retry lost late accounting: %+v %v", final, err)
			}
			if previous := counter.Set(0); previous != 3 || counter.Value() != 3 {
				t.Fatal("stats reset changed the frozen accounting boundary")
			}
			final["user>>>delayed>>>traffic>>>uplink"] = -1
			final["injected"] = 1
			repeated, err := sealer.SealCounters(ctx)
			if err != nil || repeated["user>>>delayed>>>traffic>>>uplink"] != 3 || len(repeated) != 1 {
				t.Fatalf("caller altered retry snapshot: %v %v", repeated, err)
			}
			if err := manager.UnregisterCounter("user>>>delayed>>>traffic>>>uplink"); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("registry removal discarded frozen history: %v", err)
			}
			late, err := manager.GetOrRegisterCounter("late-counter")
			if err != nil || late == nil {
				t.Fatalf("late registration selected an unmetered fallback: %v", err)
			}
			fresh, peer := net.Pipe()
			defer fresh.Close()
			defer peer.Close()
			_ = fresh.SetDeadline(time.Now().Add(time.Second))
			blocked := &stat.CounterConnection{Connection: fresh, ReadCounter: late, WriteCounter: counter}
			if n, err := blocked.Write([]byte("x")); n != 0 || !errors.Is(err, net.ErrClosed) {
				t.Fatalf("sealed writer reached underlying IO: %d %v", n, err)
			}
			if n, err := blocked.Read(make([]byte, 1)); n != 0 || !errors.Is(err, net.ErrClosed) {
				t.Fatalf("late counter bypassed sealed reader: %d %v", n, err)
			}
		})
	}
}

type pausedAccountingConn struct {
	net.Conn
	completed chan struct{}
	release   chan struct{}
	writing   bool
}

func (c *pausedAccountingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if !c.writing {
		close(c.completed)
		<-c.release
	}
	return n, err
}

func (c *pausedAccountingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if c.writing {
		close(c.completed)
		<-c.release
	}
	return n, err
}
