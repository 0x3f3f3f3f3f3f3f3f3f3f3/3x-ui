package sshoutbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	MaxConnections = 128
	DialTimeout    = 10 * time.Second
)

type Connector struct {
	address string
	config  *ssh.ClientConfig
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	active  int
	raw     map[net.Conn]struct{}
	pending sync.WaitGroup
}

func NewConnector(config Config) (*Connector, error) {
	address, client, err := compileConfig(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Connector{address: address, config: client, ctx: ctx, cancel: cancel, raw: make(map[net.Conn]struct{})}, nil
}

func (c *Connector) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, ErrNetwork
	}
	if !validTarget(address) {
		return nil, ErrTarget
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.active >= MaxConnections {
		c.mu.Unlock()
		return nil, ErrCapacity
	}
	c.active++
	c.pending.Add(1)
	c.mu.Unlock()
	defer c.pending.Done()

	dialCtx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()
	stopManager := context.AfterFunc(c.ctx, cancel)
	defer stopManager()
	conn, err := c.dial(dialCtx, address)
	if err != nil {
		c.release(nil)
		if c.ctx.Err() != nil {
			return nil, ErrClosed
		}
		if dialCtx.Err() != nil {
			return nil, dialCtx.Err()
		}
		return nil, err
	}
	return conn, nil
}

func (c *Connector) dial(ctx context.Context, address string) (net.Conn, error) {
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", c.address)
	if err != nil {
		return nil, fmt.Errorf("SSH upstream transport: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	keep := false
	defer func() {
		if !keep {
			_ = raw.Close()
			c.mu.Lock()
			delete(c.raw, raw)
			c.mu.Unlock()
		}
	}()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.raw[raw] = struct{}{}
	c.mu.Unlock()
	deadline, _ := ctx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		return nil, err
	}
	transport, channels, requests, err := ssh.NewClientConn(raw, c.address, c.config)
	if err != nil {
		return nil, fmt.Errorf("SSH upstream handshake: %w", err)
	}
	client := ssh.NewClient(transport, channels, requests)
	channel, err := client.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SSH upstream destination: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !stop() {
		return nil, ctx.Err()
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	keep = true
	return &connection{Conn: channel, raw: raw, client: client, release: func() { c.release(raw) }}, nil
}

func (c *Connector) release(raw net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.raw, raw)
	c.active--
}

func (c *Connector) Close() error {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	owned := make([]net.Conn, 0, len(c.raw))
	for raw := range c.raw {
		owned = append(owned, raw)
	}
	c.mu.Unlock()
	for _, raw := range owned {
		_ = raw.Close()
	}
	c.pending.Wait()
	return nil
}

type connection struct {
	net.Conn
	raw           net.Conn
	client        *ssh.Client
	release       func()
	once          sync.Once
	err           error
	writeMu       sync.Mutex
	readDeadline  operationDeadline
	writeDeadline operationDeadline
}

func (c *connection) Close() error {
	c.once.Do(func() {
		c.err = c.client.Close()
		if errors.Is(c.err, net.ErrClosed) {
			c.err = nil
		}
		c.release()
	})
	return c.err
}

func (c *connection) CloseWrite() error {
	if err := c.writeDeadline.begin(c.raw); err != nil {
		return err
	}
	c.writeMu.Lock()
	err := c.Conn.(interface{ CloseWrite() error }).CloseWrite()
	c.writeMu.Unlock()
	return c.writeDeadline.end(err)
}

func (c *connection) Read(p []byte) (int, error) {
	if err := c.readDeadline.begin(c.raw); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(p)
	return n, c.readDeadline.end(err)
}

func (c *connection) Write(p []byte) (int, error) {
	if err := c.writeDeadline.begin(c.raw); err != nil {
		return 0, err
	}
	c.writeMu.Lock()
	n, err := c.Conn.Write(p)
	c.writeMu.Unlock()
	return n, c.writeDeadline.end(err)
}

func (c *connection) SetDeadline(t time.Time) error {
	c.readDeadline.set(t, c.raw)
	c.writeDeadline.set(t, c.raw)
	return nil
}

func (c *connection) SetReadDeadline(t time.Time) error {
	c.readDeadline.set(t, c.raw)
	return nil
}

func (c *connection) SetWriteDeadline(t time.Time) error {
	c.writeDeadline.set(t, c.raw)
	return nil
}
