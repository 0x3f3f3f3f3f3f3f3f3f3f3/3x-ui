package clientpolicy

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type tcpShaperProbe struct {
	limiter *Limiter
	bytes   atomic.Int64
	stop    func()
}

func startTCPShaperProbe(t *testing.T, rate int64, connections int) *tcpShaperProbe {
	t.Helper()
	limiter, err := NewLimiter(rate)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	probe := &tcpShaperProbe{limiter: limiter}
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				cancelClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer cancelClose()
				buf := make([]byte, 32<<10)
				for {
					n, err := conn.Read(buf)
					probe.bytes.Add(int64(n))
					if err != nil {
						return
					}
				}
			})
		}
	})
	var once sync.Once
	probe.stop = func() {
		once.Do(func() {
			cancel()
			_ = listener.Close()
			workers.Wait()
		})
	}
	t.Cleanup(probe.stop)
	for range connections {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		workers.Go(func() {
			defer conn.Close()
			cancelClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer cancelClose()
			writer := NewShapedWriter(ctx, conn, limiter)
			buf := make([]byte, 32<<10)
			for {
				if _, err := writer.Write(buf); err != nil {
					if ctx.Err() == nil && !errors.Is(err, io.EOF) {
						t.Errorf("live TCP writer failed: %v", err)
					}
					return
				}
			}
		})
	}
	return probe
}

func checkTCPShaperWindow(t *testing.T, duration time.Duration, probes []*tcpShaperProbe, rates []int64) {
	t.Helper()
	starts := make([]int64, len(probes))
	for i, probe := range probes {
		starts[i] = probe.bytes.Load()
	}
	start := time.Now()
	time.Sleep(duration)
	seconds := time.Since(start).Seconds()
	for i, probe := range probes {
		bytes := probe.bytes.Load() - starts[i]
		rate := float64(rates[i])
		lower := rate * seconds * 0.80
		upper := rate*seconds*1.06 + float64(rateBurst(rates[i]))
		t.Logf("client=%d same-IP=127.0.0.1 connections=4 cap=%d B/s window=%.3fs received=%d throughput=%.0f B/s bounds=[%.0f,%.0f] bytes", i, rates[i], seconds, bytes, float64(bytes)/seconds, lower, upper)
		if float64(bytes) < lower || float64(bytes) > upper {
			t.Errorf("client %d exceeded burst envelope or starved: %d bytes outside [%.0f,%.0f]", i, bytes, lower, upper)
		}
	}
}

func TestLimiterTCPSharedClientsAndLiveChanges(t *testing.T) {
	baseline := startTCPShaperProbe(t, 0, 4)
	time.Sleep(100 * time.Millisecond)
	before, start := baseline.bytes.Load(), time.Now()
	time.Sleep(300 * time.Millisecond)
	throughput := float64(baseline.bytes.Load()-before) / time.Since(start).Seconds()
	baseline.stop()
	t.Logf("unlimited loopback baseline %.0f B/s", throughput)
	if throughput < 8*(128<<10) {
		t.Fatalf("baseline %.0f B/s is too slow for meaningful shaping acceptance", throughput)
	}
	a := startTCPShaperProbe(t, 64<<10, 4)
	b := startTCPShaperProbe(t, 128<<10, 4)
	time.Sleep(300 * time.Millisecond)
	checkTCPShaperWindow(t, 1800*time.Millisecond, []*tcpShaperProbe{a, b}, []int64{64 << 10, 128 << 10})
	if err := a.limiter.SetRate(32 << 10); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	checkTCPShaperWindow(t, 1500*time.Millisecond, []*tcpShaperProbe{a, b}, []int64{32 << 10, 128 << 10})
	if err := a.limiter.SetRate(128 << 10); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	checkTCPShaperWindow(t, 1500*time.Millisecond, []*tcpShaperProbe{a, b}, []int64{128 << 10, 128 << 10})
}
