package mieru

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func mieruDB(t *testing.T) (*gorm.DB, *database.ClientUsageLedger, *policyflow.Controller) {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "mieru.db"))
	db := database.GetDB()
	ledger := database.NewClientUsageLedger(db)
	controller := policyflow.NewController(ledger, "local/shared-dispatch")
	t.Cleanup(controller.Close)
	return db, ledger, controller
}

func mieruUser(t *testing.T, db *gorm.DB, ledger *database.ClientUsageLedger, controller *policyflow.Controller, multiplier clientpolicy.Multiplier) (model.ClientRecord, Client) {
	t.Helper()
	c := model.ClientRecord{Email: uuid.NewString(), Enable: true, TotalGB: 1 << 20}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: c.Email, Enable: true, Total: c.TotalGB}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ChangeMultiplier(t.Context(), c.PolicyID, 1, multiplier, nil); err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(t.Context(), c.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	return c, Client{PolicyID: c.PolicyID, Username: uuid.NewString(), Password: uuid.NewString()}
}

func officialClient(t *testing.T, address net.Addr, user Client) clientapi.Client {
	t.Helper()
	return officialClientWithDialer(t, address, user, nil)
}

func officialClientWithDialer(t *testing.T, address net.Addr, user Client, dialer apicommon.Dialer) clientapi.Client {
	t.Helper()
	endpoint := netip.MustParseAddrPort(address.String())
	transport := appctlpb.TransportProtocol_TCP
	if address.Network() == "udp" {
		transport = appctlpb.TransportProtocol_UDP
	}
	c := clientapi.NewClient()
	if err := c.Store(&clientapi.ClientConfig{Dialer: dialer, Profile: &appctlpb.ClientProfile{
		ProfileName: proto.String("owned-loopback-test"),
		User:        &appctlpb.User{Name: proto.String(user.Username), Password: proto.String(user.Password)},
		Servers:     []*appctlpb.ServerEndpoint{{IpAddress: proto.String(endpoint.Addr().String()), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(endpoint.Port())), Protocol: transport.Enum()}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

func nativeEcho(t *testing.T, network string) net.Addr {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			p := make([]byte, 65535)
			for {
				n, addr, err := conn.ReadFrom(p)
				if err != nil {
					return
				}
				_, _ = conn.WriteTo(p[:n], addr)
			}
		}()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		return conn.LocalAddr()
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_, _ = io.Copy(conn, conn)
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr()
}

func wireEcho(t *testing.T, client clientapi.Client, target net.Addr, payload []byte) net.Conn {
	t.Helper()
	_, portText, _ := net.SplitHostPort(target.String())
	port, _ := strconv.Atoi(portText)
	destination := apimodel.NetAddrSpec{Net: target.Network(), AddrSpec: apimodel.AddrSpec{FQDN: "localhost", Port: port}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var got []byte
	if target.Network() == "tcp" {
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		got = make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
	} else {
		packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
		if _, err := packet.WriteTo(payload, destination); err != nil {
			t.Fatal(err)
		}
		p := make([]byte, 65535)
		n, _, err := packet.ReadFrom(p)
		if err != nil {
			t.Fatal(err)
		}
		got = p[:n]
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("official client echo differs: got %d bytes, want %d", len(got), len(payload))
	}
	return conn
}

func TestOfficialClientsKeepIdentityAndPayloadBillingOnEveryTransport(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			first, a := mieruUser(t, db, ledger, controller, 1500)
			second, b := mieruUser(t, db, ledger, controller, 500)
			destinations := make(chan Destination, 4)
			server, err := New(Config{InboundTag: "mieru-test", Bindings: []Binding{{Network: underlay, Address: "127.0.0.1:0"}}, Clients: []Client{a, b}}, controller,
				func(ctx context.Context, d Destination) (net.Conn, error) {
					destinations <- d
					return (&net.Dialer{}).DialContext(ctx, d.Network, net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
				})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			for i, user := range []Client{a, b} {
				client := officialClient(t, server.Addresses()[0], user)
				for _, network := range []string{"tcp", "udp"} {
					target := nativeEcho(t, network)
					payload := bytes.Repeat([]byte{byte(i + 1)}, 4096)
					wireEcho(t, client, target, payload)
					d := <-destinations
					if d.PolicyID != user.PolicyID || d.InboundTag != "mieru-test" || d.Host != "localhost" || d.Network != network || d.Port != netip.MustParseAddrPort(target.String()).Port() || !d.Source.Addr().IsLoopback() || d.Source.Port() == 0 {
						t.Fatalf("authenticated routing context lost: %+v", d)
					}
				}
			}
			for _, want := range []struct {
				id     string
				billed int64
			}{{first.PolicyID, 24576}, {second.PolicyID, 8192}} {
				account, err := ledger.Read(t.Context(), want.id)
				if err != nil || account.Up != 8192 || account.Down != 8192 || account.Billed != want.billed {
					t.Fatalf("native framing counted or same-IP users mixed: %+v / %v; want 8192 each direction, billed %d", account, err, want.billed)
				}
			}
		})
	}
}
