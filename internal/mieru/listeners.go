package mieru

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
)

type ownedListeners struct {
	mu        sync.Mutex
	addresses []net.Addr
	closers   []io.Closer
	conns     map[*ownedStream]struct{}
	closed    bool
	failed    func()
}

func (l *ownedListeners) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		return nil, err
	}
	owned := &ownedListener{Listener: listener, owner: l}
	if !l.keep(listener.Addr(), owned) {
		return nil, ErrClosed
	}
	return owned, nil
}

func (l *ownedListeners) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	owned := &ownedPacket{PacketConn: conn, owner: l}
	if !l.keep(conn.LocalAddr(), owned) {
		return nil, ErrClosed
	}
	return owned, nil
}

func (l *ownedListeners) keep(address net.Addr, closer io.Closer) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = closer.Close()
		return false
	}
	l.addresses = append(l.addresses, address)
	l.closers = append(l.closers, closer)
	return true
}

func (l *ownedListeners) close() {
	l.mu.Lock()
	l.closed = true
	closers := l.closers
	for conn := range l.conns {
		closers = append(closers, conn)
	}
	l.closers = nil
	l.mu.Unlock()
	for _, closer := range closers {
		_ = closer.Close()
	}
}

func (l *ownedListeners) stopAccepting() {
	l.mu.Lock()
	l.closed = true
	var closers []io.Closer
	for _, closer := range l.closers {
		if _, ok := closer.(net.Listener); ok {
			closers = append(closers, closer)
		}
	}
	l.mu.Unlock()
	for _, closer := range closers {
		_ = closer.Close()
	}
}

type ownedListener struct {
	net.Listener
	owner *ownedListeners
	once  sync.Once
	err   error
}

func (l *ownedListener) Close() error {
	l.once.Do(func() {
		l.err = l.Listener.Close()
		l.owner.fail()
	})
	return l.err
}

type ownedPacket struct {
	net.PacketConn
	owner *ownedListeners
	once  sync.Once
	err   error
}

func (c *ownedPacket) Close() error {
	c.once.Do(func() {
		c.err = c.PacketConn.Close()
		c.owner.fail()
	})
	return c.err
}

func (l *ownedListeners) fail() {
	if l != nil && l.failed != nil {
		l.failed()
	}
}

func (c *ownedPacket) ReadFrom(p []byte) (int, net.Addr, error) {
	n, peer, err := c.PacketConn.ReadFrom(p)
	if err != nil {
		var failure net.Error
		if !errors.As(err, &failure) || !failure.Timeout() {
			c.owner.fail()
		}
	}
	return n, peer, err
}

func (l *ownedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			l.owner.fail()
			return nil, err
		}
		l.owner.mu.Lock()
		if l.owner.closed || len(l.owner.conns) >= maxSessions {
			l.owner.mu.Unlock()
			_ = conn.Close()
			continue
		}
		if l.owner.conns == nil {
			l.owner.conns = make(map[*ownedStream]struct{})
		}
		owned := &ownedStream{Conn: conn, owner: l.owner}
		l.owner.conns[owned] = struct{}{}
		l.owner.mu.Unlock()
		return owned, nil
	}
}

type ownedStream struct {
	net.Conn
	owner    *ownedListeners
	identity string
	once     sync.Once
	err      error
}

func (c *ownedStream) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()
	})
	return c.err
}

func (l *ownedListeners) transportFor(session net.Conn) *ownedStream {
	if session.LocalAddr().Network() != "tcp" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for conn := range l.conns {
		if conn.LocalAddr().String() == session.LocalAddr().String() && conn.RemoteAddr().String() == session.RemoteAddr().String() {
			return conn
		}
	}
	return nil
}

func (l *ownedListeners) identify(conn *ownedStream, identity string) {
	if conn == nil {
		return
	}
	l.mu.Lock()
	conn.identity = identity
	l.mu.Unlock()
}

func (l *ownedListeners) retired(clients map[string]*clientGeneration) map[*ownedStream]struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	retired := make(map[*ownedStream]struct{})
	for conn := range l.conns {
		if conn.identity != "" && clients[conn.identity] == nil {
			retired[conn] = struct{}{}
		}
	}
	return retired
}
