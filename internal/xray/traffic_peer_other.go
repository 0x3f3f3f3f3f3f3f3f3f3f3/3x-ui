//go:build !linux

package xray

import "net"

func verifyTrafficControlPeer(net.Conn, int) error { return ErrTrafficDrainCapability }
