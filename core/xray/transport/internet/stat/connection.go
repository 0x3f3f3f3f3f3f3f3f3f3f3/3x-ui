package stat

import (
	"net"
	"sync"

	"github.com/xtls/xray-core/features/stats"
)

type Connection interface {
	net.Conn
}

type CounterConnection struct {
	Connection
	ReadCounter  stats.Counter
	WriteCounter stats.Counter
	onClose      func()
	closeOnce    sync.Once
	closeErr     error
}

func NewCounterConnection(conn Connection, read, write stats.Counter, onClose func()) *CounterConnection {
	return &CounterConnection{Connection: conn, ReadCounter: read, WriteCounter: write, onClose: onClose}
}

func (c *CounterConnection) Close() error {
	if c.onClose == nil {
		return c.Connection.Close()
	}
	c.closeOnce.Do(func() {
		defer c.onClose()
		c.closeErr = c.Connection.Close()
	})
	return c.closeErr
}

func (c *CounterConnection) Read(b []byte) (int, error) {
	lease, err := stats.BeginIO(c.ReadCounter)
	if err != nil {
		return 0, err
	}
	defer stats.EndIO(lease)
	nBytes, err := c.Connection.Read(b)
	if c.ReadCounter != nil {
		c.ReadCounter.Add(int64(nBytes))
	}

	return nBytes, err
}

func (c *CounterConnection) Write(b []byte) (int, error) {
	lease, err := stats.BeginIO(c.WriteCounter)
	if err != nil {
		return 0, err
	}
	defer stats.EndIO(lease)
	nBytes, err := c.Connection.Write(b)
	if c.WriteCounter != nil {
		c.WriteCounter.Add(int64(nBytes))
	}
	return nBytes, err
}

func TryUnwrapStatsConn(conn net.Conn) net.Conn {
	if conn == nil {
		return conn
	}
	if conn, ok := conn.(*CounterConnection); ok {
		return conn.Connection
	}
	return conn
}
