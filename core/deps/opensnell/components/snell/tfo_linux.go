/*
 * This file is part of opensnell.
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

package snell

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// TCP Fast Open (RFC 7413) implementation for Linux.
//
// Server side:
//   setsockopt(IPPROTO_TCP, TCP_FASTOPEN, qlen) on the LISTENING socket
//   BEFORE listen(). The kernel then advertises TFO to clients during
//   the SYN-ACK and accepts SYN-carried payloads.
//
// Client side:
//   setsockopt(IPPROTO_TCP, TCP_FASTOPEN_CONNECT, 1) on the connecting
//   socket BEFORE connect(). The kernel transparently delays the SYN
//   until the first sendmsg(), then emits SYN+payload (TFO if the
//   server granted us a cookie, otherwise normal TCP).
//
// Sysctl gate: /proc/sys/net/ipv4/tcp_fastopen is a bitmask
//   bit 0 (1) — client TFO enabled
//   bit 1 (2) — server TFO enabled
//   bit 2 (4) — allow without cookie (relaxed mode)
// Default on recent kernels is 1 (client only). For server-side TFO
// you typically want `sysctl -w net.ipv4.tcp_fastopen=3`.
//
// Kernel versions:
//   3.6+  server-side TFO
//   3.7+  client-side TFO via raw API
//   4.11+ TCP_FASTOPEN_CONNECT socket option (the API we use)

// tfoListenerQueueLen is the TFO cookie / pending-data queue length the
// kernel uses for this listener. 1024 is generous for our purposes.
const tfoListenerQueueLen = 1024

func tfoSupported() bool { return true }

// applyTFOListen is a net.ListenConfig.Control function that enables
// TCP_FASTOPEN on the listening socket. No-op for non-TCP networks.
func applyTFOListen(network, _ string, c syscall.RawConn) error {
	if !strings.HasPrefix(network, "tcp") {
		return nil
	}
	var sockErr error
	if cerr := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_FASTOPEN, tfoListenerQueueLen)
	}); cerr != nil {
		return cerr
	}
	return sockErr
}

// applyTFODial is a net.Dialer.Control function that enables
// TCP_FASTOPEN_CONNECT on the about-to-connect socket. No-op for
// non-TCP networks.
func applyTFODial(network, _ string, c syscall.RawConn) error {
	if !strings.HasPrefix(network, "tcp") {
		return nil
	}
	var sockErr error
	if cerr := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_FASTOPEN_CONNECT, 1)
	}); cerr != nil {
		return cerr
	}
	return sockErr
}

// tfoListenerReady checks /proc/sys/net/ipv4/tcp_fastopen and tells the
// caller whether the kernel is configured to honor listener-side TFO.
func tfoListenerReady() (bool, string) {
	mode := readTFOSysctlMode()
	if mode < 0 {
		return false, "could not read /proc/sys/net/ipv4/tcp_fastopen; assuming TFO unavailable"
	}
	if mode&2 == 0 {
		return false, "kernel net.ipv4.tcp_fastopen lacks bit 1 (server); run: sysctl -w net.ipv4.tcp_fastopen=3"
	}
	return true, ""
}

// tfoDialerReady checks the same sysctl for client-side TFO.
func tfoDialerReady() (bool, string) {
	mode := readTFOSysctlMode()
	if mode < 0 {
		return false, "could not read /proc/sys/net/ipv4/tcp_fastopen; assuming TFO unavailable"
	}
	if mode&1 == 0 {
		return false, "kernel net.ipv4.tcp_fastopen lacks bit 0 (client); run: sysctl -w net.ipv4.tcp_fastopen=3"
	}
	return true, ""
}

func readTFOSysctlMode() int {
	b, err := os.ReadFile("/proc/sys/net/ipv4/tcp_fastopen")
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}
