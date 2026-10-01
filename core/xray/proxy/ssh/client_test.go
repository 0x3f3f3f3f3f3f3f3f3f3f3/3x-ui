package ssh_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	stdnet "net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	coressh "github.com/xtls/xray-core/proxy/ssh"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/pipe"
	gossh "golang.org/x/crypto/ssh"
)

type providedDialer struct {
	connection stat.Connection
	started    chan struct{}
	release    <-chan struct{}
}

func (d providedDialer) Dial(context.Context, net.Destination) (stat.Connection, error) {
	if d.release != nil {
		close(d.started)
		<-d.release
	}
	return d.connection, nil
}
func (providedDialer) DestIpAddress() net.IP                                 { return nil }
func (providedDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func clientFixture(t *testing.T) (*coressh.ClientConfig, gossh.Signer) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := gossh.MarshalPrivateKey(key, "ephemeral SSH client lifecycle test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "business-key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return &coressh.ClientConfig{Address: "supplied-dialer.invalid", Port: 2222, Username: "business", PrivateKeyFile: path, HostKey: string(gossh.MarshalAuthorizedKey(signer.PublicKey())), HandshakeTimeoutSeconds: 1, IdleTimeoutSeconds: 1}, signer
}

func socketPair(t *testing.T) (stdnet.Conn, stdnet.Conn) {
	t.Helper()
	listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := stdnet.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.Accept()
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client, server
}

func processContext(network net.Network) context.Context {
	return session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: net.Destination{Network: network, Address: net.LocalHostIP, Port: 80}}})
}

func TestOutboundUDPReturnsExplicitUnsupportedNetwork(t *testing.T) {
	cfg, _ := clientFixture(t)
	client, err := coressh.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Process(processContext(net.Network_UDP), nil, nil); !errors.Is(err, coressh.ErrUnsupportedNetwork) {
		t.Fatalf("UDP returned %v", err)
	}
}

func TestOutboundHostMismatchReturnsTypedError(t *testing.T) {
	cfg, _ := clientFixture(t)
	_, host := clientFixture(t)
	client, err := coressh.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, peer := socketPair(t)
	defer raw.Close()
	defer peer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server := &gossh.ServerConfig{NoClientAuth: true}
		server.AddHostKey(host)
		c, _, _, err := gossh.NewServerConn(peer, server)
		if err == nil {
			c.Close()
		}
	}()
	err = client.Process(processContext(net.Network_TCP), nil, providedDialer{connection: raw})
	if !errors.Is(err, coressh.ErrHostKey) {
		t.Fatalf("wrong upstream host key returned %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("host mismatch retained SSH transport")
	}
}

func TestOutboundHandshakeCancellationClosesSuppliedTransport(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "handshake", true: "late-dial"}[late], func(t *testing.T) {
			cfg, _ := clientFixture(t)
			client, err := coressh.NewClient(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			raw, peer := socketPair(t)
			defer raw.Close()
			defer peer.Close()
			ctx, cancel := context.WithCancel(processContext(net.Network_TCP))
			defer cancel()
			reader, writer := pipe.New()
			defer writer.Close()
			link := &transport.Link{Reader: reader, Writer: buf.Discard}
			dialer := providedDialer{connection: raw}
			release := make(chan struct{})
			if late {
				dialer.started = make(chan struct{})
				dialer.release = release
			}
			done := make(chan error, 1)
			go func() { done <- client.Process(ctx, link, dialer) }()
			if late {
				<-dialer.started
				cancel()
				close(release)
			} else {
				peer.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := bufio.NewReader(peer).ReadString('\n'); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled SSH handshake succeeded")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("cancelled handshake retained process")
			}
			peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			_, err = peer.Read(make([]byte, 1))
			if err != io.EOF {
				t.Fatalf("cancelled supplied transport read: %v", err)
			}
		})
	}
}

func TestOutboundHandshakeUsesAbsoluteDeadline(t *testing.T) {
	cfg, _ := clientFixture(t)
	client, err := coressh.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, peer := socketPair(t)
	defer raw.Close()
	defer peer.Close()
	go io.Copy(io.Discard, peer)
	begin := time.Now()
	err = client.Process(processContext(net.Network_TCP), nil, providedDialer{connection: raw})
	var timeout stdnet.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("handshake deadline returned %v", err)
	}
	if elapsed := time.Since(begin); elapsed < 900*time.Millisecond || elapsed > 1800*time.Millisecond {
		t.Fatalf("one-second handshake took %s", elapsed)
	}
}

func TestOutboundIdleClosesBothCopyDirectionsAtConfiguredDeadline(t *testing.T) {
	cfg, host := clientFixture(t)
	client, err := coressh.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, peer := socketPair(t)
	defer raw.Close()
	defer peer.Close()
	opened := make(chan struct{})
	go func() {
		server := &gossh.ServerConfig{NoClientAuth: true}
		server.AddHostKey(host)
		conn, channels, requests, err := gossh.NewServerConn(peer, server)
		if err != nil {
			return
		}
		defer conn.Close()
		go gossh.DiscardRequests(requests)
		for in := range channels {
			c, reqs, err := in.Accept()
			if err != nil {
				return
			}
			go gossh.DiscardRequests(reqs)
			close(opened)
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()
	reader, writer := pipe.New()
	defer writer.Close()
	link := &transport.Link{Reader: reader, Writer: buf.Discard}
	done := make(chan error, 1)
	go func() { done <- client.Process(processContext(net.Network_TCP), link, providedDialer{connection: raw}) }()
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("reference SSH did not accept destination")
	}
	time.Sleep(200 * time.Millisecond)
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes([]byte("activity"))}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("one-second idle SSH session retained copy goroutines after its last payload")
	}
}
