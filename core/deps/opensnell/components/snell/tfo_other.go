/*
 * This file is part of opensnell.
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

//go:build !linux

package snell

import (
	"runtime"
	"syscall"
)

// On non-Linux platforms we don't implement the TFO socket-option dance.
// macOS in particular handles TFO largely transparently — modern macOS
// kernels do client-side TFO automatically when `net.inet.tcp.fastopen`
// is enabled, even without per-socket setsockopt — so a snell client
// running on macOS may still benefit from TFO even though these
// functions are no-ops.

func tfoSupported() bool { return false }

func applyTFOListen(network, addr string, c syscall.RawConn) error { return nil }
func applyTFODial(network, addr string, c syscall.RawConn) error   { return nil }

func tfoListenerReady() (bool, string) {
	return false, "TFO socket-option control is only implemented on Linux (current GOOS: " + runtime.GOOS + ")"
}
func tfoDialerReady() (bool, string) {
	return false, "TFO socket-option control is only implemented on Linux (current GOOS: " + runtime.GOOS + ")"
}
