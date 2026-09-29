package inbound

import (
	"context"
	"sync"

	"github.com/xtls/xray-core/transport/internet/stat"
)

type workerConnection struct {
	conn   stat.Connection
	cancel context.CancelFunc
}

type workerConnections struct {
	mu      sync.Mutex
	closed  bool
	active  map[*workerConnection]struct{}
	running sync.WaitGroup
}

func (g *workerConnections) add(parent context.Context, conn stat.Connection) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancel(parent)
	c := &workerConnection{conn: conn, cancel: cancel}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		cancel()
		conn.Close()
		return ctx, nil, false
	}
	if g.active == nil {
		g.active = make(map[*workerConnection]struct{})
	}
	g.active[c] = struct{}{}
	g.running.Add(1)
	g.mu.Unlock()
	return ctx, func() {
		cancel()
		conn.Close()
		g.mu.Lock()
		delete(g.active, c)
		g.mu.Unlock()
		g.running.Done()
	}, true
}

func (g *workerConnections) stop() {
	g.mu.Lock()
	g.closed = true
	active := make([]*workerConnection, 0, len(g.active))
	for c := range g.active {
		active = append(active, c)
	}
	g.mu.Unlock()
	for _, c := range active {
		c.cancel()
		c.conn.Close()
	}
}
