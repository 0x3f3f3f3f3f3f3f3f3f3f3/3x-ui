package snell

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"time"

	S "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv4"
	"github.com/sagernet/sing-snell/snellv6"
	B "github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/singbridge"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

func init() {
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, c any) (any, error) { return NewClient(ctx, c.(*ClientConfig)) }))
}

type Outbound struct {
	server      X.Destination
	method      S.Method
	mu          sync.Mutex
	closed      bool
	reuse       bool
	connections map[*outboundConnection]struct{}
	pending     map[*dialReservation]struct{}
	config      *ClientConfig
	pools       map[poolScope]*pooledClient
}

type (
	poolScope struct {
		user    *protocol.MemoryUser
		tag     string
		dialer  internet.Dialer
		gateway string
		mark    int32
	}
	pooledClient struct {
		method  S.Method
		scope   poolScope
		untrack func()
		once    sync.Once
	}
)

type dialReservation struct {
	cancel context.CancelFunc
	pool   *pooledClient
}

func NewClient(ctx context.Context, c *ClientConfig) (*Outbound, error) {
	if err := ValidateClient(c); err != nil {
		return nil, err
	}
	if err := validateTransport(ctx); err != nil {
		return nil, err
	}
	o := &Outbound{server: X.TCPDestination(c.Address.AsAddress(), X.Port(c.Port)), reuse: c.Reuse, connections: make(map[*outboundConnection]struct{}), pending: make(map[*dialReservation]struct{}), config: c, pools: make(map[poolScope]*pooledClient)}
	var err error
	o.method, err = o.newMethod(false)
	return o, err
}

func (o *Outbound) newMethod(reuse bool) (S.Method, error) {
	c := o.config
	var err error
	var method S.Method
	if c.Version == 6 {
		mode, _ := snellv6.ParseMode(c.Mode)
		method, err = snellv6.NewClient(snellv6.ClientOptions{PSK: []byte(c.Psk), Mode: mode, Reuse: reuse, Dialer: outboundDialer{o}, Server: singbridge.ToSocksaddr(o.server)})
	} else {
		obfs := S.ObfsModeNone
		if c.Obfs == "http" {
			obfs = S.ObfsModeHTTP
		}
		method, err = snellv4.NewClient(snellv4.ClientOptions{PSK: []byte(c.Psk), ObfsMode: obfs, ObfsHost: c.ObfsHost, ObfsURI: c.ObfsUri, Reuse: reuse, Dialer: outboundDialer{o}, Server: singbridge.ToSocksaddr(o.server)})
	}
	return method, err
}

func (o *Outbound) acquirePool(ctx context.Context, d internet.Dialer, ob *session.Outbound) (*pooledClient, error) {
	if typ := reflect.TypeOf(d); typ == nil || !typ.Comparable() {
		return nil, errors.New("Snell reuse requires an identifiable Xray dialer")
	}
	scope := poolScope{dialer: d}
	if in := session.InboundFromContext(ctx); in != nil {
		scope.user = in.User
		scope.tag = in.Tag
	}
	if ob.Gateway != nil {
		scope.gateway = ob.Gateway.String()
	}
	if sock := session.SockoptFromContext(ctx); sock != nil {
		scope.mark = sock.Mark
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, net.ErrClosed
	}
	if p := o.pools[scope]; p != nil {
		o.mu.Unlock()
		return p, nil
	}
	if len(o.pools) >= 64 {
		o.mu.Unlock()
		return nil, errors.New("Snell outbound reuse scope limit exceeded")
	}
	method, err := o.newMethod(true)
	if err != nil {
		o.mu.Unlock()
		return nil, err
	}
	p := &pooledClient{method: method, scope: scope}
	if scope.user != nil {
		p.untrack, err = scope.user.TrackSession(func() { o.closePool(p) })
		if err != nil {
			o.mu.Unlock()
			_ = method.(interface{ Close() error }).Close()
			return nil, err
		}
	}
	o.pools[scope] = p
	o.mu.Unlock()
	return p, nil
}

func (o *Outbound) closePool(p *pooledClient) {
	p.once.Do(func() {
		o.mu.Lock()
		if o.pools[p.scope] == p {
			delete(o.pools, p.scope)
		}
		var conns []*outboundConnection
		for c := range o.connections {
			if c.pool == p {
				conns = append(conns, c)
			}
		}
		var pending []context.CancelFunc
		for r := range o.pending {
			if r.pool == p {
				pending = append(pending, r.cancel)
			}
		}
		o.mu.Unlock()
		for _, cancel := range pending {
			cancel()
		}
		for _, c := range conns {
			c.Close()
		}
		_ = p.method.(interface{ Close() error }).Close()
		if p.untrack != nil {
			p.untrack()
		}
	})
}

func (o *Outbound) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	connections := make([]*outboundConnection, 0, len(o.connections))
	for c := range o.connections {
		connections = append(connections, c)
	}
	pools := make([]*pooledClient, 0, len(o.pools))
	for _, p := range o.pools {
		pools = append(pools, p)
	}
	pending := make([]context.CancelFunc, 0, len(o.pending))
	for r := range o.pending {
		pending = append(pending, r.cancel)
	}
	o.mu.Unlock()
	for _, cancel := range pending {
		cancel()
	}
	for _, c := range connections {
		c.Close()
	}
	for _, p := range pools {
		o.closePool(p)
	}
	return o.method.(interface{ Close() error }).Close()
}

type (
	outboundDialKey struct{}
	outboundDialer  struct{ owner *Outbound }
)

type outboundDialState struct {
	dialer internet.Dialer
	pool   *pooledClient
}

func (d outboundDialer) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("Snell server transport must be TCP")
	}
	injected, ok := ctx.Value(outboundDialKey{}).(outboundDialState)
	if !ok {
		return nil, errors.New("missing Xray Snell dialer")
	}
	return d.owner.open(ctx, injected.dialer)
}

func (d outboundDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("Snell datagrams use the authenticated TCP transport")
}

type outboundConnection struct {
	net.Conn
	owner   *Outbound
	once    sync.Once
	untrack func()
	pool    *pooledClient
	err     error
}

func (c *outboundConnection) Close() error {
	c.once.Do(func() {
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		c.owner.mu.Unlock()
		if c.untrack != nil {
			c.untrack()
		}
		c.err = c.Conn.Close()
	})
	return c.err
}

func (o *Outbound) open(ctx context.Context, d internet.Dialer) (net.Conn, error) {
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &dialReservation{cancel: cancel}
	if state, ok := ctx.Value(outboundDialKey{}).(outboundDialState); ok {
		r.pool = state.pool
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, net.ErrClosed
	}
	if len(o.connections)+len(o.pending) >= 128 {
		o.mu.Unlock()
		return nil, errors.New("Snell outbound physical connection limit exceeded")
	}
	var untrackDial func()
	var err error
	if in := session.InboundFromContext(ctx); in != nil && in.User != nil {
		untrackDial, err = in.User.TrackSession(cancel)
		if err != nil {
			o.mu.Unlock()
			return nil, err
		}
	}
	o.pending[r] = struct{}{}
	o.mu.Unlock()
	raw, err := d.Dial(dialCtx, o.server)
	if untrackDial != nil {
		untrackDial()
	}
	o.mu.Lock()
	delete(o.pending, r)
	if err != nil {
		o.mu.Unlock()
		return nil, err
	}
	if o.closed || dialCtx.Err() != nil {
		o.mu.Unlock()
		raw.Close()
		return nil, net.ErrClosed
	}
	c := &outboundConnection{Conn: raw, owner: o, pool: r.pool}
	if in := session.InboundFromContext(ctx); in != nil && in.User != nil {
		c.untrack, err = in.User.TrackSession(func() { c.Close() })
		if err != nil {
			o.mu.Unlock()
			raw.Close()
			return nil, err
		}
	}
	o.connections[c] = struct{}{}
	o.mu.Unlock()
	return c, nil
}

func (o *Outbound) Process(ctx context.Context, link *transport.Link, d internet.Dialer) error {
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	if closed {
		return errors.New("Snell outbound closed")
	}
	outs := session.OutboundsFromContext(ctx)
	if len(outs) == 0 || !outs[len(outs)-1].Target.IsValid() {
		return errors.New("Snell target not specified")
	}
	ob := outs[len(outs)-1]
	d.SetOutboundGateway(ctx, ob)
	dest := ob.Target
	ob.Name = "snell"
	ob.CanSpliceCopy = 3
	if dest.Network == X.Network_TCP && o.reuse {
		pool, err := o.acquirePool(ctx, d, ob)
		if err != nil {
			return err
		}
		ctx = context.WithValue(ctx, outboundDialKey{}, outboundDialState{dialer: d, pool: pool})
		c, err := pool.method.(interface {
			DialContext(context.Context, M.Socksaddr) (net.Conn, error)
		}).DialContext(ctx, singbridge.ToSocksaddr(dest))
		if err != nil {
			return err
		}
		defer c.Close()
		physical := c.(interface{ Upstream() any }).Upstream().(net.Conn)
		return copyStream(ctx, link, c, physical, false)
	}
	raw, err := o.open(ctx, d)
	if err != nil {
		return err
	}
	defer raw.Close()
	stopHandshake := context.AfterFunc(ctx, func() { raw.Close() })
	defer stopHandshake()
	if dest.Network == X.Network_TCP {
		if err := raw.SetWriteDeadline(time.Now().Add(timeouts(ctx).Handshake)); err != nil {
			return err
		}
		c, err := o.method.DialConn(raw, singbridge.ToSocksaddr(dest))
		if err != nil {
			return err
		}
		if err := raw.SetWriteDeadline(time.Time{}); err != nil {
			return err
		}
		return copyStream(ctx, link, c, raw, false)
	}
	if dest.Network != X.Network_UDP {
		return errors.New("unsupported Snell destination network")
	}
	pc, err := o.method.DialPacketConn(raw)
	if err != nil {
		return err
	}
	defer pc.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { raw.Close(); common.Interrupt(link.Reader); common.Interrupt(link.Writer) })
	defer stop()
	done := make(chan copyResult, 2)
	updates := make(activity, 1)
	go func() {
		for {
			mb, err := link.Reader.ReadMultiBuffer()
			for j, b := range mb {
				target := dest
				if b.UDP != nil {
					target = *b.UDP
				}
				p := packetBuffer(b.Bytes(), pc)
				b.Release()
				mb[j] = nil
				if err := pc.WritePacket(p, singbridge.ToSocksaddr(target)); err != nil {
					buf.ReleaseMulti(mb[j+1:])
					done <- copyResult{upload: true, err: err}
					return
				}
				updates.Update()
			}
			if err != nil {
				done <- copyResult{upload: true, err: err}
				return
			}
		}
	}()
	go func() {
		for {
			p := B.NewSize(maxDatagramSize)
			source, err := pc.ReadPacket(p)
			if err != nil {
				p.Release()
				done <- copyResult{err: err}
				return
			}
			b := buf.NewWithSize(int32(p.Len()))
			_, _ = b.Write(p.Bytes())
			p.Release()
			response, err := singbridge.ToDestination(source, X.Network_UDP)
			if err != nil {
				b.Release()
				done <- copyResult{err: err}
				return
			}
			b.UDP = &response
			if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{b}); err != nil {
				done <- copyResult{err: err}
				return
			}
			updates.Update()
		}
	}()
	return awaitCopies(ctx, cancel, done, updates)
}
