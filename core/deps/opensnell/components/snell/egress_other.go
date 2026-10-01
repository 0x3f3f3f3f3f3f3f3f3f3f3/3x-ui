/*
 * This file is part of opensnell.
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

//go:build !linux && !darwin

package snell

import (
	"fmt"
	"runtime"
	"syscall"
)

func bindEgressInterface(name string) func(network, addr string, c syscall.RawConn) error {
	return func(network, addr string, c syscall.RawConn) error {
		return fmt.Errorf("egress-interface not supported on %s", runtime.GOOS)
	}
}
