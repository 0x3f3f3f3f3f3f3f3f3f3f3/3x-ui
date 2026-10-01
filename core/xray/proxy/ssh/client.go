package ssh

import (
	"bytes"
	"context"
	"errors"
	stdnet "net"
	"strconv"
	"strings"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	gossh "golang.org/x/crypto/ssh"
)

var (
	ErrHostKey            = errors.New("SSH upstream host key mismatch")
	ErrUnsupportedNetwork = errors.New("SSH supports TCP forwarding only")
)

type Client struct {
	server          net.Destination
	address         string
	config          *gossh.ClientConfig
	handshake, idle time.Duration
}

func NewClient(_ context.Context, c *ClientConfig) (*Client, error) {
	if c.Address == "" || len(c.Address) > 253 || c.Port == 0 || c.Port > 65535 || c.Username == "" || len(c.Username) > 256 || len(c.Password) > 1024 || c.PrivateKeyFile == "" && c.Password == "" || len(c.HostKey) > 16384 {
		return nil, ErrConfiguration
	}
	key, _, options, rest, err := gossh.ParseAuthorizedKey([]byte(c.HostKey))
	if err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, ErrConfiguration
	}
	if _, cert := key.(*gossh.Certificate); cert {
		return nil, ErrConfiguration
	}
	var auth []gossh.AuthMethod
	if c.PrivateKeyFile != "" {
		signer, err := privateKeyFile(c.PrivateKeyFile)
		if err != nil {
			return nil, err
		}
		auth = append(auth, gossh.PublicKeys(signer))
	}
	if c.Password != "" {
		auth = append(auth, gossh.Password(c.Password))
	}
	handshake, err := bounded(c.HandshakeTimeoutSeconds, 10, 120)
	if err != nil {
		return nil, err
	}
	idle, err := bounded(c.IdleTimeoutSeconds, 300, 86400)
	if err != nil {
		return nil, err
	}
	pin := key.Marshal()
	config := &gossh.ClientConfig{User: c.Username, Auth: auth, HostKeyCallback: func(_ string, _ stdnet.Addr, key gossh.PublicKey) error {
		if !bytes.Equal(pin, key.Marshal()) {
			return ErrHostKey
		}
		return nil
	}}
	return &Client{server: net.TCPDestination(net.ParseAddress(c.Address), net.Port(c.Port)), address: stdnet.JoinHostPort(c.Address, strconv.Itoa(int(c.Port))), config: config, handshake: time.Duration(handshake) * time.Second, idle: time.Duration(idle) * time.Second}, nil
}

func (c *Client) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 || !outbounds[len(outbounds)-1].Target.IsValid() {
		return ErrConfiguration
	}
	ob := outbounds[len(outbounds)-1]
	if ob.Target.Network != net.Network_TCP {
		return ErrUnsupportedNetwork
	}
	ob.Name = "ssh"
	ob.CanSpliceCopy = 3
	raw, err := dialer.Dial(ctx, c.server)
	if err != nil {
		return err
	}
	defer raw.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	closeConnection := func() {
		raw.Close()
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
		if in := session.InboundFromContext(ctx); in != nil && in.Conn != nil {
			in.Conn.Close()
		}
	}
	stop := context.AfterFunc(ctx, closeConnection)
	defer stop()
	if err := raw.SetDeadline(time.Now().Add(c.handshake)); err != nil {
		return err
	}
	conn, channels, requests, err := gossh.NewClientConn(raw, c.address, c.config)
	if err != nil {
		return err
	}
	client := gossh.NewClient(conn, channels, requests)
	defer client.Close()
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return err
	}
	openCtx, cancel := context.WithTimeout(ctx, c.handshake)
	stream, err := client.DialContext(openCtx, "tcp", ob.Target.NetAddr())
	cancel()
	if err != nil {
		return err
	}
	defer stream.Close()
	copyCtx, cancelCopy := context.WithCancel(ctx)
	defer cancelCopy()
	timer := newIdleTimer(c.idle, func() { cancelCopy(); closeConnection() })
	defer timer.Stop()
	return duplex(copyCtx, func() error {
		err := buf.Copy(link.Reader, buf.NewWriter(stream), buf.UpdateActivity(timer))
		if cw, ok := stream.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		return err
	}, func() error {
		err := buf.Copy(buf.NewReader(stream), link.Writer, buf.UpdateActivity(timer))
		common.Close(link.Writer)
		return err
	}, closeConnection)
}

func init() {
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, raw interface{}) (interface{}, error) {
		return NewClient(ctx, raw.(*ClientConfig))
	}))
}
