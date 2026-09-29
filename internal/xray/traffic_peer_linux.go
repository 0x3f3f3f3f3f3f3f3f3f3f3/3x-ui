//go:build linux

package xray

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func verifyTrafficControlPeer(connection net.Conn, expectedPID int) error {
	local, ok := connection.(*net.UnixConn)
	if !ok || expectedPID <= 0 {
		return ErrTrafficDrainCapability
	}
	raw, err := local.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return credentialErr
	}
	if credential == nil || int(credential.Pid) != expectedPID {
		return fmt.Errorf("%w: Unix peer is not the owned child", ErrTrafficDrainCapability)
	}
	return nil
}
