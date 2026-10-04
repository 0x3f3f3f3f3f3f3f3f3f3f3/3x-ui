package sub

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Only the physical peer returns; the test never resumes the coordinator after
// rejoin/restart. Real traffic must regain grants without releasing old holds.
func TestManagedPolicyTwoPhysicalNodesActualAutomaticRecovery(t *testing.T) {
	for _, fault := range []string{"core-restart", "unavailable-at-startup"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newManagedPhysicalCase(t, model.ClientPolicyScopeGlobal, "1.5", 32768, 0, 0)
			peer := fixture.nodes[0]
			managedPhysicalExchange(t, peer.manifest.TunnelPort, "tcp4", []byte("w"), true)
			managedPhysicalWaitAccount(t, fixture, "3", "16381")
			if fault == "core-restart" {
				before := peer.manifest
				managedPhysicalControl(t, peer, "restart")
				if before.BootID == peer.manifest.BootID || before.SourceID != peer.manifest.SourceID || before.PID != peer.manifest.PID {
					t.Fatal("physical peer did not independently restart its owned core")
				}
			} else {
				if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
					t.Fatal(err)
				}
				managedPhysicalControl(t, peer, "partition")
				// This is the production startup entry while the peer is absent.
				if err := service.ResumeManagedPolicyCoordinator(context.Background()); err == nil {
					t.Fatal("partitioned startup unexpectedly discovered the peer")
				}
				managedPhysicalControl(t, peer, "rejoin")
			}
			var flow net.Conn
			wantHeld := "16383"
			if fault == "core-restart" {
				wantHeld = "32764"
			}
			for deadline := time.Now().Add(20 * time.Second); ; {
				if flow != nil {
					_ = flow.Close()
				}
				var err error
				flow, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", peer.manifest.TunnelPort), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: fixture.parent.StableID, Limit: 16})
				if len(page.Accounts) == 1 && page.Accounts[0].WindowUsed == "3" && page.Accounts[0].Budget.Allocated == wantHeld {
					// No payload was sent during reconnect polling. A live socket
					// plus original new capacity proves authentic automatic grant.
					_ = flow.SetReadDeadline(time.Now().Add(time.Millisecond))
					var probe [1]byte
					_, probeErr := flow.Read(probe[:])
					if timeout, ok := probeErr.(net.Error); ok && timeout.Timeout() {
						break
					}
				}
				if time.Now().After(deadline) {
					_ = flow.Close()
					t.Fatal("healthy physical peer never automatically obtained a fresh original grant")
				}
				time.Sleep(50 * time.Millisecond)
			}
			defer flow.Close()
			_ = flow.SetDeadline(time.Now().Add(20 * time.Second))
			if _, err := flow.Write([]byte("r")); err != nil {
				t.Fatal(err)
			}
			var reply [1]byte
			if _, err := io.ReadFull(flow, reply[:]); err != nil || reply[0] != 'r' {
				t.Fatalf("healthy physical peer never automatically regained authentic grants: %v", err)
			}
			_ = flow.Close()
			if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
				t.Fatal(err)
			}
			held := "0"
			if fault == "core-restart" {
				held = "16381"
			}
			final := managedPhysicalWaitAccount(t, fixture, "6", held)
			if final.Usage.Upload != "2" || final.Usage.Download != "2" || fault == "core-restart" && (final.Budget.Unallocated == nil || *final.Budget.Unallocated != "16381") {
				t.Fatalf("automatic recovery recreated consumption or old-boot allowance: %+v", final)
			}
		})
	}
}
