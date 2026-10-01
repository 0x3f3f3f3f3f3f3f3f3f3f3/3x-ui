package mieru

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"

	miCommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	miProtocol "github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"google.golang.org/protobuf/proto"
)

type Client struct {
	config *ClientConfig
	dns    dns.Client
	mu     sync.Mutex
	closed bool
	pools  map[poolKey]*clientPool
	policy policy.Manager
}

// A physical connection retains the authentication and dialer that created it.
// Never reuse it across inbound identities, credential generations or gateways.
type poolKey struct {
	dialer  internet.Dialer
	user    *protocol.MemoryUser
	tag     string
	gateway string
	mark    int32
}

type clientPool struct {
	client         *miProtocol.Mux
	bridge         *outboundDialer
	key            poolKey
	requests       map[*poolRequest]context.CancelFunc
	idle           *time.Timer
	idleGeneration uint64
	untrack        func()
}

type poolRequest struct{ marker byte }

var _ proxy.Outbound = (*Client)(nil)

func NewClient(ctx context.Context, config *ClientConfig) (*Client, error) {
	if config.Address == nil || config.Address.AsAddress() == nil || config.Port == 0 || config.Port > 65535 {
		return nil, errors.New("invalid mieru endpoint")
	}
	if err := ValidateCredentials(config.Username, config.Password); err != nil {
		return nil, err
	}
	if config.Transport != "TCP" && config.Transport != "UDP" {
		return nil, errors.New("invalid mieru transport")
	}
	if _, exists := appctlpb.MultiplexingLevel_value[config.Multiplexing]; !exists {
		return nil, errors.New("invalid mieru multiplexing mode")
	}
	client := &Client{config: proto.Clone(config).(*ClientConfig), pools: make(map[poolKey]*clientPool), policy: policy.DefaultManager{}}
	if core.FromContext(ctx) != nil {
		if err := core.RequireFeatures(ctx, func(manager policy.Manager) error { client.policy = manager; return nil }); err != nil {
			return nil, err
		}
	}
	if config.Address.AsAddress().Family().IsDomain() {
		if err := core.RequireFeatures(ctx, func(resolver dns.Client) error { client.dns = resolver; return nil }); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func (c *Client) createPool(key poolKey) (*clientPool, error) {
	mode := appctlpb.TransportProtocol_TCP
	if c.config.Transport == "UDP" {
		mode = appctlpb.TransportProtocol_UDP
	}
	endpoint := &appctlpb.ServerEndpoint{PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(c.config.Port)), Protocol: mode.Enum()}}}
	address := c.config.Address.AsAddress()
	if address.Family().IsIP() {
		endpoint.IpAddress = proto.String(address.IP().String())
	} else {
		endpoint.DomainName = proto.String(address.Domain())
	}
	level := appctlpb.MultiplexingLevel(appctlpb.MultiplexingLevel_value[c.config.Multiplexing])
	profile := &appctlpb.ClientProfile{
		ProfileName: proto.String("xray-native"), User: &appctlpb.User{Name: proto.String(c.config.Username), Password: proto.String(c.config.Password)},
		Servers: []*appctlpb.ServerEndpoint{endpoint}, Multiplexing: &appctlpb.MultiplexingConfig{Level: level.Enum()},
	}
	if c.config.Mtu != 0 {
		profile.Mtu = proto.Int32(int32(c.config.Mtu))
	}
	bridge := &outboundDialer{dialer: key.dialer}
	if err := appctlcommon.ValidateClientConfigSingleProfile(profile); err != nil {
		return nil, err
	}
	client, err := appctlcommon.NewClientMuxFromProfile(profile, bridge, bridge, &outboundResolver{dns: c.dns}, nil)
	if err != nil {
		return nil, err
	}
	pool := &clientPool{client: client, bridge: bridge, key: key, requests: make(map[*poolRequest]context.CancelFunc)}
	if key.user != nil {
		untrack, err := key.user.TrackSession(func() { c.closePool(pool) })
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		pool.untrack = untrack
	}
	return pool, nil
}

func (c *Client) acquire(ctx context.Context, dialer internet.Dialer, cancel context.CancelFunc) (*clientPool, *poolRequest, error) {
	if dialer == nil || !reflect.TypeOf(dialer).Comparable() {
		return nil, nil, errors.New("mieru requires a comparable outbound dialer identity")
	}
	key := poolKey{dialer: dialer}
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) != 0 {
		outbound := outbounds[len(outbounds)-1]
		dialer.SetOutboundGateway(ctx, outbound)
		if outbound.Gateway != nil {
			key.gateway = outbound.Gateway.String()
		}
	}
	if sockopt := session.SockoptFromContext(ctx); sockopt != nil {
		key.mark = sockopt.Mark
	}
	if inbound := session.InboundFromContext(ctx); inbound != nil {
		key.user, key.tag = inbound.User, inbound.Tag
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, net.ErrClosed
	}
	pool := c.pools[key]
	if pool == nil {
		if len(c.pools) >= 1024 {
			return nil, nil, errors.New("mieru outbound pool capacity reached")
		}
		var err error
		pool, err = c.createPool(key)
		if err != nil {
			return nil, nil, err
		}
		c.pools[key] = pool
	}
	if pool.idle != nil {
		pool.idle.Stop()
		pool.idle = nil
	}
	pool.idleGeneration++
	request := &poolRequest{}
	pool.requests[request] = cancel
	return pool, request, nil
}

func (c *Client) release(pool *clientPool, request *poolRequest) {
	c.mu.Lock()
	delete(pool.requests, request)
	if c.pools[pool.key] != pool || len(pool.requests) != 0 {
		c.mu.Unlock()
		return
	}
	if c.config.Multiplexing == "MULTIPLEXING_OFF" {
		c.detachPool(pool)
		c.mu.Unlock()
		_ = pool.stop()
		if pool.untrack != nil {
			pool.untrack()
		}
		return
	}
	pool.idleGeneration++
	generation := pool.idleGeneration
	pool.idle = time.AfterFunc(30*time.Second, func() { c.closeIdlePool(pool, generation) })
	c.mu.Unlock()
}

func (c *Client) closeIdlePool(pool *clientPool, generation uint64) {
	c.mu.Lock()
	if len(pool.requests) != 0 || c.pools[pool.key] != pool || generation != pool.idleGeneration {
		c.mu.Unlock()
		return
	}
	c.detachPool(pool)
	c.mu.Unlock()
	_ = pool.stop()
	if pool.untrack != nil {
		pool.untrack()
	}
}

// detachPool runs under c.mu; cancellation callbacks perform connection cleanup.
func (c *Client) detachPool(pool *clientPool) {
	delete(c.pools, pool.key)
	if pool.idle != nil {
		pool.idle.Stop()
	}
	for _, cancel := range pool.requests {
		cancel()
	}
}

func (c *Client) closePool(pool *clientPool) {
	c.mu.Lock()
	if c.pools[pool.key] != pool {
		c.mu.Unlock()
		return
	}
	c.detachPool(pool)
	c.mu.Unlock()
	_ = pool.stop()
	if pool.untrack != nil {
		pool.untrack()
	}
}

func (c *Client) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		return errors.New("mieru outbound lacks target")
	}
	target := outbounds[len(outbounds)-1].Target
	if target.Network != xnet.Network_TCP && target.Network != xnet.Network_UDP {
		return errors.New("mieru requires TCP or UDP target")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pool, request, err := c.acquire(ctx, dialer, cancel)
	if err != nil {
		return err
	}
	defer c.release(pool, request)
	p := c.policy.ForLevel(0)
	if inbound := session.InboundFromContext(ctx); inbound != nil && inbound.User != nil {
		p = c.policy.ForLevel(inbound.User.Level)
	}
	timer := signal.NewActivityTimer(cancel)
	defer timer.SetTimeout(0)
	handshakeTimer := time.AfterFunc(p.Timeouts.Handshake, cancel)
	defer handshakeTimer.Stop()
	destination := model.NetAddrSpec{Net: target.Network.SystemString(), AddrSpec: model.AddrSpec{Port: int(target.Port)}}
	if target.Address.Family().IsIP() {
		destination.IP = target.Address.IP()
	} else {
		destination.FQDN = target.Address.Domain()
	}
	conn, err := dialTarget(ctx, pool.client, destination)
	if err != nil {
		return err
	}
	handshakeTimer.Stop()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
	})
	defer stop()
	timer.SetTimeout(p.Timeouts.ConnectionIdle)
	var reader buf.Reader = buf.NewReader(conn)
	var writer buf.Writer = buf.NewWriter(conn)
	if target.Network == xnet.Network_UDP {
		tunnel := miCommon.NewPacketOverStreamTunnel(conn)
		reader, writer = &packetReader{tunnel: tunnel}, &packetWriter{tunnel: tunnel, target: target}
	}
	var copying sync.WaitGroup
	copying.Add(2)
	err = task.Run(ctx,
		func() error {
			defer copying.Done()
			defer timer.SetTimeout(p.Timeouts.DownlinkOnly)
			return buf.Copy(link.Reader, writer, buf.UpdateActivity(timer))
		},
		task.OnSuccess(func() error {
			defer copying.Done()
			defer timer.SetTimeout(p.Timeouts.UplinkOnly)
			return buf.Copy(reader, link.Writer, buf.UpdateActivity(timer))
		}, func() error { return common.Close(link.Writer) }))
	cancel()
	_ = conn.Close()
	common.Interrupt(link.Reader)
	common.Interrupt(link.Writer)
	copying.Wait()
	return err
}

func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	active := make([]*clientPool, 0, len(c.pools))
	for _, pool := range c.pools {
		c.detachPool(pool)
		active = append(active, pool)
	}
	c.mu.Unlock()
	var errs []error
	for _, pool := range active {
		errs = append(errs, pool.stop())
		if pool.untrack != nil {
			pool.untrack()
		}
	}
	return errors.Join(errs...)
}

// Own the raw session during the request/response handshake. The embedding
// API's lazy EarlyConn.Close waits for its in-progress handshake sync.Once.
// A raw session closes immediately even when a peer sends a partial response.
func dialTarget(ctx context.Context, mux *miProtocol.Mux, destination model.NetAddrSpec) (net.Conn, error) {
	conn, err := mux.DialContext(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	command := constant.Socks5ConnectCmd
	if destination.Net == "udp" {
		command = constant.Socks5UDPAssociateCmd
	}
	request := model.Request{Command: command, DstAddr: destination.AddrSpec}
	if err := request.WriteToSocks5(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var response model.Response
	if err := response.ReadFromSocks5(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response.Reply != constant.Socks5ReplySuccess {
		_ = conn.Close()
		return nil, fmt.Errorf("mieru server reply %d", response.Reply)
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (p *clientPool) stop() error {
	// Fence dials and close even sockets not yet appended to the upstream mux.
	return errors.Join(p.bridge.Close(), p.client.Close())
}

type outboundDialer struct {
	dialer      internet.Dialer
	mu          sync.Mutex
	closed      bool
	connections map[*outboundConnection]struct{}
}

type outboundConnection struct {
	net.Conn
	owner *outboundDialer
	once  sync.Once
	err   error
}

func (c *outboundConnection) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		c.owner.mu.Unlock()
	})
	return c.err
}

func (d *outboundDialer) dial(ctx context.Context, target xnet.Destination) (net.Conn, error) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := d.dialer.Dial(ctx, target)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	d.mu.Lock()
	if d.closed || ctx.Err() != nil {
		d.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	if d.connections == nil {
		d.connections = make(map[*outboundConnection]struct{})
	}
	owned := &outboundConnection{Conn: conn, owner: d}
	d.connections[owned] = struct{}{}
	d.mu.Unlock()
	return owned, nil
}

func (d *outboundDialer) Close() error {
	d.mu.Lock()
	d.closed = true
	connections := make([]*outboundConnection, 0, len(d.connections))
	for conn := range d.connections {
		connections = append(connections, conn)
	}
	d.mu.Unlock()
	var errs []error
	for _, conn := range connections {
		errs = append(errs, conn.Close())
	}
	return errors.Join(errs...)
}

func (d *outboundDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, errors.New("mieru stream dial requires TCP")
	}
	destination, err := xnet.ParseDestination("tcp:" + address)
	if err != nil {
		return nil, err
	}
	return d.dial(ctx, destination)
}

func (d *outboundDialer) ListenPacket(ctx context.Context, network, local, remote string) (net.PacketConn, error) {
	if !strings.HasPrefix(network, "udp") || local != "" {
		return nil, errors.New("unsupported mieru UDP bind request")
	}
	destination, err := xnet.ParseDestination("udp:" + remote)
	if err != nil {
		return nil, err
	}
	conn, err := d.dial(ctx, destination)
	if err != nil {
		return nil, err
	}
	peer, err := net.ResolveUDPAddr(network, remote)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &connectedPacketConn{Conn: conn, peer: peer}, nil
}

type connectedPacketConn struct {
	net.Conn
	peer net.Addr
}

func (c *connectedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Read(p)
	return n, c.peer, err
}

func (c *connectedPacketConn) WriteTo(p []byte, peer net.Addr) (int, error) {
	if peer.String() != c.peer.String() {
		return 0, errors.New("mieru packet dialer peer changed")
	}
	return c.Write(p)
}

type outboundResolver struct{ dns dns.Client }

func (r *outboundResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.dns == nil {
		return nil, errors.New("mieru endpoint resolver unavailable")
	}
	ips, _, err := r.dns.LookupIP(host, dns.IPOption{IPv4Enable: network != "ip6", IPv6Enable: network != "ip4"})
	return ips, err
}

func init() {
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, raw interface{}) (interface{}, error) {
		client, err := NewClient(ctx, raw.(*ClientConfig))
		if err != nil {
			return nil, fmt.Errorf("native mieru outbound: %w", err)
		}
		return client, nil
	}))
}
