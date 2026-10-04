package sub

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestManagedPolicyTwoPhysicalNodesActualNativeProtocols(t *testing.T) {
	fixture, privateKey := newManagedPhysicalNativeCase(t)
	for _, node := range fixture.nodes {
		managedPhysicalExchange(t, node.manifest.TunnelPort, "tcp4", []byte("w"), true)
		for _, protocol := range []string{"snell", "mieru", "ssh"} {
			t.Run(node.manifest.NodeID+"/"+protocol, func(t *testing.T) {
				flow := managedPhysicalNativeFlow(t, node, protocol, privateKey)
				defer flow.Close()
				_ = flow.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := flow.Write([]byte("n")); err != nil {
					t.Fatal("managed native payload failed", err)
				}
				var raw [1]byte
				if _, err := io.ReadFull(flow, raw[:]); err != nil || !bytes.Equal(raw[:], []byte("n")) {
					t.Fatal("managed native echo failed", err)
				}
			})
		}
		client := managedPhysicalSnellClient(t, node)
		packet := client.udp(t)
		snellHTTPSocksPacket(t, packet, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: node.manifest.TargetPort}, []byte("u"))
	}
	if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
		t.Fatal("native actual grants did not seal", err)
	}
	page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: fixture.parent.StableID, Limit: 16})
	if len(page.Accounts) != 1 || page.Accounts[0].Usage.Upload != "10" || page.Accounts[0].Usage.Download != "10" || page.Accounts[0].Usage.Billed != "30" || page.Accounts[0].Budget.Allocated != "0" {
		t.Fatalf("native protocols did not share original exact billing: %+v", page.Accounts)
	}
	t.Log("physical managed native protocols: two independent nodes, Snell TCP/UDP, official mieru client, real OpenSSH, shared Tunnel, exact1.5x billing30, held0")
}
