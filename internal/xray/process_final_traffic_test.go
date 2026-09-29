//go:build linux

package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type finalTrafficSettler interface {
	TrafficDrainBootID() string
	SettleFinalTraffic(context.Context, func(*TrafficBatch) error) error
}

func finalTrafficProcess(t *testing.T, withControl bool) (*Process, finalTrafficSettler, string) {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	dir, err := os.MkdirTemp("", "panel-final-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	listen := freePort(t)
	control := ""
	if withControl {
		control = fmt.Sprintf(`,"trafficControl":{"listen":%q}`, filepath.Join(dir, "drain.sock"))
	}
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"api","listen":%q,"services":["StatsService"]}%s,"stats":{},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}}},"inbounds":[{"tag":"forward","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"email":"alice"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, fmt.Sprintf("127.0.0.1:%d", freePort(t)), control, listen, target.Addr().(*net.TCPAddr).Port)
	var config Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	p := NewTestProcess(&config, filepath.Join(dir, "core.json"))
	t.Cleanup(func() { p.Stop() })
	settler, ok := any(p).(finalTrafficSettler)
	if !ok {
		t.Fatal("process has no boot-owned final settlement")
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	return p, settler, fmt.Sprintf("127.0.0.1:%d", listen)
}

func exchangeFinalTraffic(t *testing.T, address string, payload []byte) net.Conn {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err = net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload: %v", err)
	}
	return conn
}

func TestFinalTrafficReplaysPendingThenSettlesFreshSnapshot(t *testing.T) {
	p, final, address := finalTrafficProcess(t, true)
	boot := final.TrafficDrainBootID()
	if boot == "" {
		t.Fatal("startup did not pin the child boot")
	}
	exchangeFinalTraffic(t, address, []byte("first"))
	receipts := map[string]bool{}
	var upload, download int64
	commit := func(batch *TrafficBatch) error {
		if receipts[batch.ID] {
			return nil
		}
		receipts[batch.ID] = true
		for _, traffic := range batch.ClientTraffics {
			upload += traffic.Up
			download += traffic.Down
		}
		return nil
	}
	lost := errors.New("commit acknowledgement lost")
	if _, _, err := p.SettleTraffic(func(batch *TrafficBatch) error { commit(batch); return lost }); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	conn := exchangeFinalTraffic(t, address, []byte("second!"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	failedID := ""
	rejectFinal := errors.New("final SQL failed")
	err := final.SettleFinalTraffic(ctx, func(batch *TrafficBatch) error {
		if receipts[batch.ID] {
			return nil
		}
		failedID = batch.ID
		return rejectFinal
	})
	if !errors.Is(err, rejectFinal) || failedID == "" {
		t.Fatalf("fresh final batch missing: %v", err)
	}
	if !p.IsRunning() {
		t.Fatal("child stopped before final transaction committed")
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("drained TCP remained active: %v", err)
	}
	if _, _, err := p.SettleTraffic(func(*TrafficBatch) error { t.Error("normal poll crossed final settlement"); return nil }); err == nil {
		t.Fatal("normal poll accepted sealed child")
	}
	if err := final.SettleFinalTraffic(ctx, func(batch *TrafficBatch) error {
		if batch.ID != failedID {
			t.Error("retry replaced final batch identity")
		}
		return commit(batch)
	}); err != nil {
		t.Fatal(err)
	}
	if err := final.SettleFinalTraffic(ctx, func(*TrafficBatch) error { t.Error("completed drain settled twice"); return nil }); err != nil {
		t.Fatal(err)
	}
	if upload != 12 || download != 12 || len(receipts) != 2 {
		t.Fatalf("final totals: up=%d down=%d receipts=%d", upload, download, len(receipts))
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if next := final.TrafficDrainBootID(); next == "" || next == boot {
		t.Fatal("new child reused old boot")
	}
	exchangeFinalTraffic(t, address, []byte("new"))
	if err := final.SettleFinalTraffic(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if upload != 15 || download != 15 || len(receipts) != 3 {
		t.Fatalf("new child inherited settlement: up=%d down=%d receipts=%d", upload, download, len(receipts))
	}
}

func TestFinalTrafficRejectsUnpinnedChildWithoutClosing(t *testing.T) {
	upstream := os.Getenv("XRAY_UPSTREAM_E2E_BINARY")
	if upstream == "" {
		t.Skip("set XRAY_UPSTREAM_E2E_BINARY to the unmodified core")
	}
	t.Setenv("XRAY_E2E_BINARY", upstream)
	_, final, address := finalTrafficProcess(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := final.SettleFinalTraffic(ctx, func(*TrafficBatch) error { t.Error("unsupported child settled"); return nil }); err == nil {
		t.Fatal("unpinned child accepted")
	}
	exchangeFinalTraffic(t, address, []byte("still alive"))
}

func TestFinalTrafficRejectsCancellationAfterPendingReplay(t *testing.T) {
	p, final, address := finalTrafficProcess(t, true)
	exchangeFinalTraffic(t, address, []byte("pending"))
	if _, _, err := p.SettleTraffic(func(*TrafficBatch) error { return errors.New("retry SQL") }); err == nil {
		t.Fatal("pending batch missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := final.SettleFinalTraffic(ctx, func(*TrafficBatch) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled replay: %v", err)
	}
	if _, _, err := p.SettleTraffic(func(*TrafficBatch) error { return nil }); err != nil {
		t.Fatalf("canceled pre-drain blocked ordinary collection: %v", err)
	}
	exchangeFinalTraffic(t, address, []byte("still running"))
}

func TestFinalTrafficRejectsExhaustedSequenceBeforeDrain(t *testing.T) {
	p, final, address := finalTrafficProcess(t, true)
	p.trafficSequence = math.MaxInt64
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := final.SettleFinalTraffic(ctx, func(*TrafficBatch) error { t.Error("exhausted sequence settled"); return nil }); err == nil {
		t.Fatal("exhausted sequence accepted")
	}
	exchangeFinalTraffic(t, address, []byte("still running"))
}

func TestFinalTrafficStartupCannotPinAnotherProcess(t *testing.T) {
	upstream := os.Getenv("XRAY_UPSTREAM_E2E_BINARY")
	if upstream == "" {
		t.Skip("set XRAY_UPSTREAM_E2E_BINARY to the unmodified core")
	}
	owner, _, address := finalTrafficProcess(t, true)
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	if err := os.Symlink(upstream, filepath.Join(dir, GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"trafficControl":{"listen":%q}}`, owner.trafficEndpoint)
	var config Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	other := NewTestProcess(&config, filepath.Join(dir, "foreign.json"))
	defer other.Stop()
	if err := other.Start(); err == nil || other.IsRunning() {
		t.Fatalf("startup pinned another child: err=%v boot=%q", err, other.TrafficDrainBootID())
	}
	exchangeFinalTraffic(t, address, []byte("owner remains healthy"))
}
