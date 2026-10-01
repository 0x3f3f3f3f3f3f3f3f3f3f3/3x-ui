/*
 * This file is part of opensnell.
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

package snell

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// bindEgressInterface returns a net.Dialer.Control / net.ListenConfig.Control
// function that binds the outgoing socket to the named network interface
// via SO_BINDTODEVICE. The kernel will then route packets out via that
// interface regardless of the routing table's default choice.
//
// SO_BINDTODEVICE historically required CAP_NET_RAW. Since Linux 5.7 the
// kernel allows unprivileged callers as well, but most snell-server
// deployments run as root anyway.
func bindEgressInterface(name string) func(network, addr string, c syscall.RawConn) error {
	return func(network, addr string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, name)
		}); err != nil {
			return err
		}
		return sockErr
	}
}
