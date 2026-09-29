package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/mieru"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/sshtunnel"
)

func TestManagedPolicySSHAndMieruShareLiveDuplexRates(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			setupConflictDB(t)
			client := managedPolicyClient(t, 0)
			sshManager := managedSSHRuntime()
			t.Cleanup(stopManagedSSH)
			peer := acquireManagedPolicy(database.GetDB())
			t.Cleanup(peer.Close)
			for _, controller := range []*policyflow.Controller{sshManager.controller, peer.controller} {
				if err := controller.Configure(t.Context(), client.PolicyID, policyflow.Rates{}); err != nil {
					t.Fatal(err)
				}
			}
			target, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			var workers sync.WaitGroup
			var upload, download atomic.Int64
			duplex := func(conn net.Conn, count *atomic.Int64) {
				workers.Go(func() {
					defer conn.Close()
					stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
					defer stop()
					var writer sync.WaitGroup
					writer.Go(func() {
						payload := make([]byte, 32<<10)
						for {
							if _, err := conn.Write(payload); err != nil {
								return
							}
						}
					})
					payload := make([]byte, 32<<10)
					for {
						n, err := conn.Read(payload)
						count.Add(int64(n))
						if err != nil {
							break
						}
					}
					_ = conn.Close()
					writer.Wait()
				})
			}
			workers.Go(func() {
				for {
					conn, err := target.Accept()
					if err != nil {
						return
					}
					duplex(conn, &upload)
				}
			})
			t.Cleanup(func() { cancel(); _ = target.Close(); workers.Wait() })
			key := func() ssh.Signer {
				_, private, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				signer, err := ssh.NewSignerFromKey(private)
				if err != nil {
					t.Fatal(err)
				}
				return signer
			}
			host, identity := key(), key()
			endpoint := netip.MustParseAddrPort(target.Addr().String())
			server, err := sshtunnel.NewServer(sshtunnel.Config{InboundTag: "shared-ssh", HostKey: host, Clients: []sshtunnel.Client{{PolicyID: client.PolicyID, Username: client.Email, PublicKeys: []ssh.PublicKey{identity.PublicKey()}, Targets: []sshtunnel.TargetRule{{Host: "127.0.0.1", Port: endpoint.Port()}}}}}, sshManager.controller, func(ctx context.Context, dest sshtunnel.Destination) (io.ReadWriteCloser, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(dest.Host, strconv.Itoa(int(dest.Port))))
			})
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			served := make(chan error, 1)
			go func() { served <- server.Serve(listener) }()
			t.Cleanup(func() { server.Close(); _ = listener.Close(); <-served })
			sshClient, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: client.Email, Auth: []ssh.AuthMethod{ssh.PublicKeys(identity)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sshClient.Close() })
			stream, err := sshClient.DialContext(ctx, "tcp", target.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			duplex(stream, &download)
			user := mieru.Client{PolicyID: client.PolicyID, Username: client.Email, Password: uuid.NewString()}
			native, err := mieru.New(mieru.Config{InboundTag: "shared-mieru", Bindings: []mieru.Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []mieru.Client{user}}, peer.controller, func(ctx context.Context, dest mieru.Destination) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, dest.Network, net.JoinHostPort(dest.Host, strconv.Itoa(int(dest.Port))))
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = native.Close() })
			if err := native.Start(); err != nil {
				t.Fatal(err)
			}
			address := netip.MustParseAddrPort(native.Addresses()[0].String())
			transport := appctlpb.TransportProtocol_TCP
			if underlay == "udp" {
				transport = appctlpb.TransportProtocol_UDP
			}
			official := clientapi.NewClient()
			if err := official.Store(&clientapi.ClientConfig{Profile: &appctlpb.ClientProfile{ProfileName: proto.String("shared-policy"), User: &appctlpb.User{Name: proto.String(user.Username), Password: proto.String(user.Password)}, Servers: []*appctlpb.ServerEndpoint{{IpAddress: proto.String(address.Addr().String()), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(address.Port())), Protocol: transport.Enum()}}}}}}); err != nil {
				t.Fatal(err)
			}
			if err := official.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = official.Stop() })
			stream, err = official.DialContext(ctx, apimodel.NetAddrSpec{Net: "tcp", AddrSpec: apimodel.AddrSpec{IP: endpoint.Addr().AsSlice(), Port: int(endpoint.Port())}})
			if err != nil {
				t.Fatal(err)
			}
			duplex(stream, &download)
			measure := func(rates policyflow.Rates, duration time.Duration) {
				t.Helper()
				before := [2]int64{upload.Load(), download.Load()}
				start := time.Now()
				time.Sleep(duration)
				elapsed := time.Since(start).Seconds()
				for direction, amount := range []int64{upload.Load() - before[0], download.Load() - before[1]} {
					rate := []int64{rates.Upload, rates.Download}[direction]
					if rate == 0 {
						if float64(amount)/elapsed < 8*131072 {
							t.Fatalf("unlimited baseline too slow: direction=%d %.0f B/s", direction, float64(amount)/elapsed)
						}
					} else if float64(amount) < float64(rate)*elapsed*0.8 || float64(amount) > float64(rate)*elapsed*1.06+float64(rate/10) {
						t.Fatalf("SSH+mieru direction=%d observed=%d duration=%.3f rate=%d: aggregate rate escaped or starved", direction, amount, elapsed, rate)
					}
					t.Logf("SSH+mieru %s direction=%d rate=%d observed=%.0f B/s window=%.3fs", underlay, direction, rate, float64(amount)/elapsed, elapsed)
				}
			}
			time.Sleep(250 * time.Millisecond)
			measure(policyflow.Rates{}, 300*time.Millisecond)
			for _, rates := range []policyflow.Rates{{Upload: 65536, Download: 131072}, {Upload: 131072, Download: 65536}} {
				start := time.Now()
				if err := peer.controller.Configure(t.Context(), client.PolicyID, rates); err != nil {
					t.Fatal(err)
				}
				time.Sleep(250 * time.Millisecond)
				measure(rates, 1200*time.Millisecond)
				if time.Since(start) > 2*time.Second {
					t.Fatal("live aggregate change exceeded 2s")
				}
			}
			server.Close()
			stopManagedSSH()
			time.Sleep(250 * time.Millisecond)
			measure(policyflow.Rates{Upload: 131072, Download: 65536}, 1200*time.Millisecond)
		})
	}
}
