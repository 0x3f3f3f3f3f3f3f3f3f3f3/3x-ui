//go:build linux

package xray

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

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

func trafficExecutableIdentity(pid int) ([]byte, string, error) {
	path := fmt.Sprintf("/proc/%d/exe", pid)
	target, err := os.Readlink(path)
	if err != nil {
		return nil, "", err
	}
	digest, err := digestTrafficExecutable(path)
	return digest, filepath.Dir(target), err
}
