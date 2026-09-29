//go:build !windows

package xray

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	statscommand "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

func trafficProcessFixture(t *testing.T) *Process {
	t.Helper()
	initProcessTestLogger(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	statscommand.RegisterStatsServiceServer(server, &fakeStatsServer{rounds: [][]*statscommand.Stat{{
		stat("user>>>alice>>>traffic>>>uplink", 5),
		stat("inbound>>>shared>>>traffic>>>uplink", 7),
		stat("outbound>>>shared>>>traffic>>>uplink", 13),
	}}})
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	p := &Process{startProcessHelper(t, "default-term")}
	api, err := json.Marshal(map[string]string{"listen": listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	p.SetConfig(&Config{API: api})
	p.controlReady.Store(true)
	return p
}

func TestProcessTrafficSettlementRetriesAndSerializesPolls(t *testing.T) {
	p := trafficProcessFixture(t)
	injected := errors.New("database commit failed")
	var observed int64
	_, _, err := p.SettleTraffic(func(batch *TrafficBatch) error {
		tags, clients := batch.Traffics, batch.ClientTraffics
		observed = clients[0].Up
		if len(tags) != 2 {
			t.Errorf("same-name inbound and outbound collapsed: %+v", tags)
		}
		for _, tag := range tags {
			if tag.IsInbound && tag.Up != 7 || tag.IsOutbound && tag.Up != 13 {
				t.Errorf("counter direction mixed: %+v", tag)
			}
		}
		return injected
	})
	if !errors.Is(err, injected) || observed != 5 {
		t.Fatalf("initial settlement: observed=%d err=%v", observed, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	values := make(chan int64, 8)
	for range 8 {
		wg.Go(func() {
			_, _, err := p.SettleTraffic(func(batch *TrafficBatch) error {
				clients := batch.ClientTraffics
				values <- clients[0].Up
				return nil
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	close(values)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	for value := range values {
		total += value
	}
	if total != 5 {
		t.Fatalf("concurrent collectors committed %d bytes, want 5", total)
	}
}

func TestProcessTrafficSettlementFinishesBeforeStopAndResetsForNewChild(t *testing.T) {
	p := trafficProcessFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	settled, stopped := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, err := p.SettleTraffic(func(_ *TrafficBatch) error {
			close(entered)
			<-release
			return nil
		})
		settled <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("settlement did not enter transaction")
	}
	go func() { stopped <- p.Stop() }()
	select {
	case err := <-stopped:
		t.Fatalf("stop crossed uncommitted settlement: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if !p.IsRunning() {
		t.Fatal("child stopped before settlement committed")
	}
	unblock()
	if err := <-settled; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestXrayProcessHelper", "--", "default-term")
	cmd.Env = append(os.Environ(), "XRAY_PROCESS_HELPER=1", "XRAY_PROCESS_READY="+readyPath)
	if err := p.startCommand(cmd); err != nil {
		t.Fatal(err)
	}
	waitForProcessHelperReady(t, readyPath)
	p.controlReady.Store(true)
	var committed int64
	if _, _, err := p.SettleTraffic(func(batch *TrafficBatch) error {
		clients := batch.ClientTraffics
		committed += clients[0].Up
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if committed != 5 {
		t.Fatalf("new child inherited old cursor: committed=%d", committed)
	}
}
