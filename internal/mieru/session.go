package mieru

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
)

type managedSession struct {
	net.Conn
	server    *Server
	transport *ownedStream
	admitted  bool
	closing   atomic.Bool
	once      sync.Once
	done      chan struct{}
}

func (c *managedSession) UserName() string {
	if identity, ok := c.Conn.(apicommon.UserContext); ok {
		return identity.UserName()
	}
	return ""
}

func (c *managedSession) Close() error {
	c.once.Do(func() {
		c.closing.Store(true)
		c.server.workers.Go(func() {
			defer close(c.done)
			defer func() {
				c.server.mu.Lock()
				delete(c.server.sessions, c)
				c.server.mu.Unlock()
			}()
			done := make(chan struct{})
			go func() { _ = c.Conn.Close(); close(done) }()
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
				// Native Close can wait behind a blocked write; abort only its own TCP underlay.
				if c.transport != nil {
					_ = c.transport.Close()
				}
			}
			<-done
		})
	})
	return nil
}
