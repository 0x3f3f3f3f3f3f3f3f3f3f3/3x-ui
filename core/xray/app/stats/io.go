package stats

import (
	"context"
	"net"
	"sync"

	featurestats "github.com/xtls/xray-core/features/stats"
)

type counterIOGroup struct {
	mu     sync.Mutex
	sealed bool
	active int64
	done   chan struct{}
}

func (c *Counter) BeginIO() (featurestats.IOLease, error) {
	if c.io == nil {
		return nil, nil
	}
	c.io.mu.Lock()
	defer c.io.mu.Unlock()
	if c.io.sealed {
		return nil, net.ErrClosed
	}
	c.io.active++
	return c.io, nil
}

func (g *counterIOGroup) EndIO() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 && g.done != nil {
		close(g.done)
		g.done = nil
	}
}

func (g *counterIOGroup) seal(ctx context.Context) error {
	g.mu.Lock()
	g.sealed = true
	if g.active == 0 {
		g.mu.Unlock()
		return ctx.Err()
	}
	if g.done == nil {
		g.done = make(chan struct{})
	}
	done := g.done
	g.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SealCounters waits for acquired IO leases; a private drain must first cancel their IO owners.
func (m *Manager) SealCounters(ctx context.Context) (map[string]int64, error) {
	if err := m.io.seal(ctx); err != nil {
		return nil, err
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.snapshot == nil {
		m.snapshot = make(map[string]int64, len(m.counters))
		for name, counter := range m.counters {
			m.snapshot[name] = counter.Value()
		}
	}
	copy := make(map[string]int64, len(m.snapshot))
	for name, value := range m.snapshot {
		copy[name] = value
	}
	return copy, nil
}
