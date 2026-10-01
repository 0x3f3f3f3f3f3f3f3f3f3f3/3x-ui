package snell

import (
	"context"
	"errors"
	"maps"
	"net"
	"sync"
	"time"

	S "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv5"
	"github.com/sagernet/sing-snell/snellv6"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/singbridge"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func init() {
	common.Must(common.RegisterConfig((*ServerConfig)(nil), func(ctx context.Context, c any) (any, error) { return NewServer(ctx, c.(*ServerConfig)) }))
}

// Inbound has exactly one independently authenticated PSK owner. The wire client
// ID is supplied by a PSK holder, and is deliberately not a billing identity.
type Inbound struct {
	mu       sync.RWMutex
	config   *ServerConfig
	user     *protocol.MemoryUser
	service  S.Service
	closed   bool
	physical map[*acceptedConnection]struct{}
}

func NewServer(ctx context.Context, c *ServerConfig) (*Inbound, error) {
	if err := ValidateServer(c); err != nil {
		return nil, err
	}
	if err := validateTransport(ctx); err != nil {
		return nil, err
	}
	i := &Inbound{config: c, physical: make(map[*acceptedConnection]struct{})}
	u, err := c.User.ToMemoryUser()
	if err != nil {
		return nil, err
	}
	if err := i.AddUser(ctx, u); err != nil {
		return nil, err
	}
	return i, nil
}

func (i *Inbound) makeService(u *protocol.MemoryUser) (S.Service, error) {
	psk := []byte(u.Account.(*MemoryAccount).PSK)
	if i.config.Version == 6 {
		mode, _ := snellv6.ParseMode(i.config.Mode)
		return snellv6.NewService(snellv6.ServerOptions{PSK: psk, Mode: mode, Handler: i})
	}
	obfs := S.ObfsModeNone
	if i.config.Obfs == "http" {
		obfs = S.ObfsModeHTTP
	}
	return snellv5.NewService(snellv5.ServiceOptions{PSK: psk, ObfsMode: obfs, Handler: i})
}

func (i *Inbound) AddUser(_ context.Context, u *protocol.MemoryUser) error {
	if err := validateUser(u); err != nil {
		return err
	}
	if err := validate(i.config.Version, u.Account.(*MemoryAccount).PSK, i.config.Obfs, i.config.Mode, i.config.Quic); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return net.ErrClosed
	}
	if i.user != nil {
		return errors.New("Snell listener already has an authenticated owner")
	}
	s, err := i.makeService(u)
	if err != nil {
		return err
	}
	i.user = u
	i.service = s
	return nil
}

func (i *Inbound) RemoveUser(_ context.Context, email string) error {
	i.mu.Lock()
	u := i.user
	if u == nil || u.Email != email {
		i.mu.Unlock()
		return errors.New("Snell owner not found")
	}
	i.user = nil
	i.service = nil
	i.mu.Unlock()
	u.RevokeCredential()
	return nil
}

func (i *Inbound) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.user != nil && i.user.Email == email {
		return i.user
	}
	return nil
}

func (i *Inbound) GetUsers(context.Context) []*protocol.MemoryUser {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.user == nil {
		return nil
	}
	return []*protocol.MemoryUser{i.user}
}

func (i *Inbound) GetUsersCount(context.Context) int64 {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.user == nil {
		return 0
	}
	return 1
}

func (i *Inbound) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	u := i.user
	i.user = nil
	i.service = nil
	i.mu.Unlock()
	if u != nil {
		u.RevokeCredential()
	}
	return nil
}

func (i *Inbound) Network() []X.Network {
	if i.config.Version == 5 {
		return []X.Network{X.Network_TCP, X.Network_UDP}
	}
	return []X.Network{X.Network_TCP}
}

// PreserveDatagrams requests the full native UDP listener option. Other Snell
// versions keep the ordinary TCP listener and UDP-over-TCP transport.
func (i *Inbound) PreserveDatagrams() bool { return i.config.Version == 5 }

// QUIC Process bounds both authentication and duplex idle activity itself;
// the generic UDP worker must not replace that policy with its legacy 120s cap.
func (i *Inbound) OwnsDatagramTimeouts() bool { return i.config.Version == 5 }

type (
	acceptedKey struct{}
	accepted    struct {
		dispatcher routing.Dispatcher
		physical   net.Conn
	}
)

type acceptedConnection struct {
	net.Conn
	owner *Inbound
	once  sync.Once
	err   error
}

func (c *acceptedConnection) Close() error {
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.physical, c); c.owner.mu.Unlock(); c.err = c.Conn.Close() })
	return c.err
}

func (i *Inbound) Process(ctx context.Context, network X.Network, c stat.Connection, d routing.Dispatcher) error {
	if network == X.Network_UDP && i.config.Version == 5 {
		return i.processQUIC(ctx, c, d)
	}
	if network != X.Network_TCP {
		return errors.New("Snell uses a native TCP listener")
	}
	i.mu.Lock()
	u, s := i.user, i.service
	if u == nil || s == nil || i.closed {
		i.mu.Unlock()
		return protocol.ErrCredentialRevoked
	}
	if len(i.physical) >= 128 {
		i.mu.Unlock()
		return errors.New("Snell inbound physical connection limit exceeded")
	}
	physical := &acceptedConnection{Conn: c, owner: i}
	i.physical[physical] = struct{}{}
	i.mu.Unlock()
	defer physical.Close()
	// Track the physical transport before decoding, so removing the listener
	// owner also closes stalled handshakes, idle reuse, and idle UDP tunnels.
	untrack, err := u.TrackSession(func() { physical.Close() })
	if err != nil {
		return err
	}
	defer untrack()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { physical.Close() })
	defer stop()
	in := session.InboundFromContext(ctx)
	copyIn := session.Inbound{User: u, Conn: physical, Name: "snell", CanSpliceCopy: 3}
	if in != nil {
		copyIn = *in
		copyIn.User = u
		copyIn.Conn = physical
		copyIn.Name = "snell"
		copyIn.CanSpliceCopy = 3
	}
	ctx = session.ContextWithInbound(ctx, &copyIn)
	ctx = context.WithValue(ctx, acceptedKey{}, accepted{dispatcher: d, physical: physical})
	// Bound unauthenticated reads; an authenticated child renews its own activity.
	if err := physical.SetReadDeadline(time.Now().Add(timeouts(ctx).Handshake)); err != nil {
		return err
	}
	source := M.SocksaddrFromNet(physical.RemoteAddr())
	if copyIn.Source.IsValid() {
		source = singbridge.ToSocksaddr(copyIn.Source)
	}
	return s.NewConnection(ctx, physical, source, nil)
}

func requestContext(ctx context.Context) context.Context {
	// Dispatch writes outbound and sniffing metadata. Logical reuse/UDP flows
	// receive separate metadata objects while retaining the verified owner.
	if in := session.InboundFromContext(ctx); in != nil {
		c := *in
		ctx = session.ContextWithInbound(ctx, &c)
	}
	ctx = session.ContextWithOutbounds(ctx, nil)
	if content := session.ContentFromContext(ctx); content != nil {
		c := *content
		c.Attributes = maps.Clone(content.Attributes)
		ctx = session.ContextWithContent(ctx, &c)
	}
	return ctx
}

func (i *Inbound) NewConnectionEx(ctx context.Context, c net.Conn, _ M.Socksaddr, dest M.Socksaddr, onClose N.CloseHandlerFunc) {
	a := ctx.Value(acceptedKey{}).(accepted)
	_ = c.SetReadDeadline(time.Time{})
	defer func() { _ = c.SetReadDeadline(time.Now().Add(timeouts(ctx).ConnectionIdle)) }()
	destination, err := singbridge.ToDestination(dest, X.Network_TCP)
	if err == nil {
		var linkErr error
		flowCtx := requestContext(ctx)
		content := session.ContentFromContext(flowCtx)
		if content == nil {
			content = new(session.Content)
		}
		content.PreserveTCPHalfClose = true
		flowCtx = session.ContextWithContent(flowCtx, content)
		link, dispatchErr := a.dispatcher.Dispatch(flowCtx, destination)
		if dispatchErr != nil {
			err = dispatchErr
		} else {
			linkErr = copyStream(ctx, link, c, a.physical, true)
			err = linkErr
		}
	}
	if onClose != nil {
		onClose(err)
	}
}
