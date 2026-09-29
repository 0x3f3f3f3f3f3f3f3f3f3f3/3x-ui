package mieru

import (
	"errors"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// PeerReadClosed observes a received shutdown without consuming queued wire bytes.
func (c *ownedStream) PeerReadClosed() bool {
	conn, ok := c.Conn.(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return errors.Is(err, net.ErrClosed)
	}
	closed := false
	err = raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLRDHUP}}
		if _, pollErr := unix.Poll(fds, 0); pollErr == nil {
			closed = fds[0].Revents&(unix.POLLRDHUP|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0
		}
	})
	return closed || errors.Is(err, net.ErrClosed)
}
