//go:build linux

package sshoutbound

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestBridgeRevocationAbortsIdleTCP(t *testing.T) {
	peer := newWirePeer(t)
	manager := bridgeManager(t)
	outbound := Outbound{Tag: "revoke", Settings: peer.config}
	prepared, err := manager.Prepare([]Outbound{outbound})
	if err != nil {
		t.Fatal(err)
	}
	prepared.Commit()
	conn, err := dialBridge(t, renderedBridge(t, manager, outbound), "idle.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	bridgeEcho(t, conn, "fully-drained")
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	result := make(chan error, 1)
	go func() { var one [1]byte; _, err := conn.Read(one[:]); result <- err }()
	prepared, err = manager.Prepare(nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Commit()
	if err := <-result; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("revoked bridge retained graceful half-close semantics: %v", err)
	}
}
