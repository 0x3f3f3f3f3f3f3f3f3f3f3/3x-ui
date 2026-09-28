package sshtunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type sshDuplexGroup struct {
	up, down atomic.Int64
	conns    []net.Conn
	wg       sync.WaitGroup
}

func (g *sshDuplexGroup) close() {
	for _, conn := range g.conns {
		_ = conn.Close()
	}
	g.wg.Wait()
}

func tunnelDuplex(t *testing.T, username, address, identity string, host ssh.PublicKey) *sshDuplexGroup {
	t.Helper()
	g := &sshDuplexGroup{}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var targets sync.WaitGroup
	targets.Go(func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			targets.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				done := make(chan struct{})
				go func() {
					defer close(done)
					buf := make([]byte, 32<<10)
					for {
						if _, err := conn.Write(buf); err != nil {
							return
						}
					}
				}()
				buf := make([]byte, 32<<10)
				for {
					n, err := conn.Read(buf)
					g.up.Add(int64(n))
					if err != nil {
						break
					}
				}
				_ = conn.Close()
				<-done
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = target.Close(); targets.Wait() })
	for range 2 {
		local := tunnelLocalAddress(t)
		directory, err := os.MkdirTemp("", "xui-ssh-mux-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		control := filepath.Join(directory, "master")
		process := tunnelOpenSSHAs(t, username, address, identity, host, "-M", "-S", control, "-N", "-L", local+":"+target.Addr().String())
		tunnelWaitMaster(t, process, control)
		for range 2 {
			conn, err := net.Dial("tcp", local)
			if err != nil {
				t.Fatal(err)
			}
			g.conns = append(g.conns, conn)
			g.wg.Go(func() {
				buf := make([]byte, 32<<10)
				for {
					if _, err := conn.Write(buf); err != nil {
						return
					}
				}
			})
			g.wg.Go(func() {
				buf := make([]byte, 32<<10)
				for {
					n, err := conn.Read(buf)
					g.down.Add(int64(n))
					if err != nil {
						return
					}
				}
			})
		}
	}
	t.Cleanup(g.close)
	return g
}

func tunnelWaitMaster(t *testing.T, process *sshProcess, control string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-process.done:
			t.Fatalf("OpenSSH master exited before startup: %v", process.err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-S", control, "-O", "check", "127.0.0.1").Run()
		cancel()
		if err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OpenSSH multiplexing control socket did not become ready")
}

func TestOpenSSHDuplexRatesShareAcrossConnectionsAndChangeLive(t *testing.T) {
	db, ledger, controller, a := tunnelDB(t)
	clients := []model.ClientRecord{a}
	for range 2 {
		client := model.ClientRecord{Email: uuid.NewString(), Enable: true}
		if err := db.Create(&client).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true}).Error; err != nil {
			t.Fatal(err)
		}
		if err := controller.Configure(context.Background(), client.PolicyID, policyflow.Rates{}); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	host, _ := tunnelKey(t)
	config := Config{InboundTag: "ssh-live-rates", HostKey: host}
	identities := make([]string, 3)
	for n, client := range clients {
		key, identity := tunnelKey(t)
		identities[n] = identity
		config.Clients = append(config.Clients, Client{PolicyID: client.PolicyID, Username: fmt.Sprintf("client-%d", n), PublicKeys: []ssh.PublicKey{key.PublicKey()}, Targets: []TargetRule{{Host: "127.0.0.1", Port: 0}}})
	}
	server, err := NewServer(config, controller, tcpTestDial)
	if err != nil {
		t.Fatal(err)
	}
	address := tunnelServe(t, server)
	baseline := tunnelDuplex(t, "client-2", address, identities[2], host.PublicKey())
	time.Sleep(100 * time.Millisecond)
	up, down, start := baseline.up.Load(), baseline.down.Load(), time.Now()
	time.Sleep(300 * time.Millisecond)
	elapsed := time.Since(start).Seconds()
	for direction, count := range []int64{baseline.up.Load() - up, baseline.down.Load() - down} {
		bps := float64(count) / elapsed
		if bps < 8*131072 {
			t.Fatalf("OpenSSH unlimited direction %d baseline %.0f B/s is insufficient", direction, bps)
		}
		t.Logf("OpenSSH unlimited direction %d: %.0f B/s", direction, bps)
	}
	baseline.close()
	rates := []policyflow.Rates{{Upload: 65536, Download: 131072}, {Upload: 131072, Download: 65536}}
	groups := make([]*sshDuplexGroup, 2)
	for n := range 2 {
		if err := controller.Configure(context.Background(), clients[n].PolicyID, rates[n]); err != nil {
			t.Fatal(err)
		}
		groups[n] = tunnelDuplex(t, fmt.Sprintf("client-%d", n), address, identities[n], host.PublicKey())
	}
	measure := func(label string, duration time.Duration) {
		t.Helper()
		before := [2][2]int64{}
		for n, group := range groups {
			before[n] = [2]int64{group.up.Load(), group.down.Load()}
		}
		start := time.Now()
		time.Sleep(duration)
		elapsed := time.Since(start).Seconds()
		for n, group := range groups {
			for direction, count := range []int64{group.up.Load() - before[n][0], group.down.Load() - before[n][1]} {
				rate := []int64{rates[n].Upload, rates[n].Download}[direction]
				burst := min(int64(65536), max(int64(1), rate/10))
				lower, upper := float64(rate)*elapsed*0.80, float64(rate)*elapsed*1.06+float64(burst)
				if float64(count) < lower || float64(count) > upper {
					account, err := ledger.Read(context.Background(), clients[n].PolicyID)
					t.Logf("diagnostic client %d: admitted up/down=%d/%d, observed up/down=%d/%d, read=%v", n, account.Up, account.Down, group.up.Load(), group.down.Load(), err)
					t.Fatalf("%s client %d direction %d: %d bytes outside [%.0f,%.0f] in %.3fs", label, n, direction, count, lower, upper, elapsed)
				}
				t.Logf("%s client %d direction %d: %.0f B/s", label, n, direction, float64(count)/elapsed)
			}
		}
	}
	time.Sleep(300 * time.Millisecond)
	measure("initial", 1800*time.Millisecond)
	for _, next := range []policyflow.Rates{{Upload: 32768, Download: 65536}, {Upload: 131072, Download: 131072}} {
		start := time.Now()
		rates[0] = next
		if err := controller.Configure(context.Background(), a.PolicyID, next); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
		measure(fmt.Sprintf("live %d/%d", next.Upload, next.Download), 1500*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("OpenSSH live-update window exceeded 2s: %v", elapsed)
		}
	}
	account, err := ledger.Read(context.Background(), a.PolicyID)
	if err != nil || account.Up == 0 || account.Down == 0 || account.Billed != 2*(account.Up+account.Down) {
		t.Fatalf("raw duplex rate was confused with 2x billing: %+v, %v", account, err)
	}
}
