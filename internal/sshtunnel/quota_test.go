package sshtunnel

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
)

func TestOpenSSHQuotaAcrossConnectionsAndAfterServerRestart(t *testing.T) {
	for _, tc := range []struct {
		multiplier  clientpolicy.Multiplier
		raw, billed int64
	}{{500, 2097152, 1048576}, {1000, 1048576, 1048576}, {1500, 699050, 1048575}, {2000, 524288, 1048576}} {
		t.Run(tc.multiplier.String(), func(t *testing.T) {
			db, ledger, controller, client := tunnelPolicyDB(t, tc.multiplier)
			if err := db.Model(&client).Update("total_gb", 1<<20).Error; err != nil {
				t.Fatal(err)
			}
			host, _ := tunnelKey(t)
			key, identity := tunnelKey(t)
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var received atomic.Int64
			var receivers sync.WaitGroup
			ctx, cancel := context.WithCancel(context.Background())
			receivers.Go(func() {
				for {
					conn, err := target.Accept()
					if err != nil {
						return
					}
					receivers.Go(func() {
						defer conn.Close()
						stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
						defer stop()
						buf := make([]byte, 32<<10)
						for {
							n, err := conn.Read(buf)
							received.Add(int64(n))
							if err != nil {
								return
							}
						}
					})
				}
			})
			t.Cleanup(func() { cancel(); _ = target.Close(); receivers.Wait() })
			config := Config{InboundTag: "ssh-quota", HostKey: host, Clients: []Client{{PolicyID: client.PolicyID, Username: "tunnel-user", PublicKeys: []ssh.PublicKey{key.PublicKey()}, Targets: []TargetRule{{Host: "127.0.0.1", Port: 0}}}}}
			dial := func(ctx context.Context, destination Destination) (io.ReadWriteCloser, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))))
			}
			server, err := NewServer(config, controller, dial)
			if err != nil {
				t.Fatal(err)
			}
			address := tunnelServe(t, server)
			bindings := []string{tunnelLocalAddress(t), tunnelLocalAddress(t)}
			for _, binding := range bindings {
				tunnelOpenSSH(t, address, identity, host.PublicKey(), "-N", "-L", binding+":"+target.Addr().String())
				tunnelWaitListener(t, binding)
			}
			var senders sync.WaitGroup
			start := time.Now()
			for n := range 4 {
				conn, err := net.Dial("tcp", bindings[n%2])
				if err != nil {
					t.Fatal(err)
				}
				senders.Go(func() {
					defer conn.Close()
					_ = conn.SetWriteDeadline(time.Now().Add(6 * time.Second))
					buf := make([]byte, 32<<10)
					for {
						if _, err := conn.Write(buf); err != nil {
							return
						}
					}
				})
			}
			senders.Wait()
			if time.Since(start) >= 6*time.Second {
				t.Fatal("SSH quota did not cut the existing long transfers")
			}
			server.Close()
			_ = target.Close()
			receivers.Wait()
			account, err := ledger.Read(context.Background(), client.PolicyID)
			if err != nil || account.Up != tc.raw || account.Down != 0 || account.Billed != tc.billed {
				t.Fatalf("SSH quota/billing mismatch: %+v, expected raw=%d billed=%d, err=%v", account, tc.raw, tc.billed, err)
			}
			if got := received.Load(); got > tc.raw || got < tc.raw-4*policyflow.BufferSize {
				t.Fatalf("target got %d bytes outside allowance [%d,%d]", got, tc.raw-4*policyflow.BufferSize, tc.raw)
			}
			t.Logf("4 channels/2 OpenSSH processes: multiplier=%s admitted=%d target=%d billed=%d elapsed=%v", tc.multiplier, account.Up, received.Load(), account.Billed, time.Since(start))
			controller.Close()
			restarted := policyflow.NewController(ledger, "node-a/shared-dispatch")
			t.Cleanup(restarted.Close)
			if err := restarted.Configure(context.Background(), client.PolicyID, policyflow.Rates{}); err != nil {
				t.Fatal(err)
			}
			second, err := NewServer(config, restarted, dial)
			if err != nil {
				t.Fatal(err)
			}
			process := tunnelOpenSSH(t, tunnelServe(t, second), identity, host.PublicKey(), "-N")
			select {
			case <-process.done:
			case <-time.After(1250 * time.Millisecond):
				t.Fatal("server/controller restart restored an exhausted SSH client's access")
			}
		})
	}
}
