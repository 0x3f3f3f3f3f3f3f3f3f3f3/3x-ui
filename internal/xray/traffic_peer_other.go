//go:build !linux

package xray

import "net"

func verifyTrafficControlPeer(net.Conn, int) error { return ErrTrafficDrainCapability }

func trafficExecutableIdentity(int) ([]byte, string, error) {
	return nil, "", ErrTrafficDrainCapability
}
