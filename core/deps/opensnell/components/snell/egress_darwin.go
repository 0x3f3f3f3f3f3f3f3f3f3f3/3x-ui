/*
 * This file is part of opensnell.
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

package snell

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// bindEgressInterface returns a Control function that binds the outgoing
// socket to a specific interface index via IP_BOUND_IF (IPv4) and
// IPV6_BOUND_IF (IPv6). macOS lacks SO_BINDTODEVICE; these socket options
// achieve the same routing effect.
//
// Both setsockopts are attempted because Go's net.Dialer / ListenConfig
// uses a single socket that may be IPv4 or IPv6 depending on dial-time
// address resolution; we don't know which until inside Control, and
// setting the option for the wrong family is a benign EINVAL.
func bindEgressInterface(name string) func(network, addr string, c syscall.RawConn) error {
	return func(network, addr string, c syscall.RawConn) error {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return fmt.Errorf("egress interface %q: %w", name, err)
		}
		var sockErr error
		if cerr := c.Control(func(fd uintptr) {
			// Try IPv4 first; ignore failure since this fd may be IPv6.
			_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, iface.Index)
			// Try IPv6; same rationale.
			_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, iface.Index)
		}); cerr != nil {
			return cerr
		}
		return sockErr
	}
}
