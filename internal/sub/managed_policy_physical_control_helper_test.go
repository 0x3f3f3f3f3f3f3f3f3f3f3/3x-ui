package sub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type managedPhysicalControlReply struct {
	Sequence uint64
	Command  string
	Manifest managedPhysicalManifest
}

func managedPhysicalPartitionMiddleware(peer *managedProductHTTPPeer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if peer.partition.Load() && strings.HasPrefix(c.Request.URL.Path, "/panel/api/server/clientPolicyAuthority") {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		c.Next()
	}
}

func managedPhysicalControl(t *testing.T, node *managedPhysicalNode, command string) {
	t.Helper()
	node.controlSequence++
	if _, err := fmt.Fprintf(node.input, "%d %s\n", node.controlSequence, command); err != nil {
		t.Fatal("physical node control write failed", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-node.done:
			t.Fatal("physical node exited during control; private log retained")
		default:
		}
		raw, err := os.ReadFile(node.manifestPath + ".control.json")
		if err == nil {
			var reply managedPhysicalControlReply
			if json.Unmarshal(raw, &reply) != nil {
				t.Fatal("malformed physical control acknowledgement")
			}
			if reply.Sequence == node.controlSequence {
				if reply.Command != command || reply.Manifest.PID != node.manifest.PID || reply.Manifest.SourceID != node.manifest.SourceID || reply.Manifest.LocalClientID != node.manifest.LocalClientID {
					t.Fatal("physical control acknowledgement changed original source")
				}
				node.manifest = reply.Manifest
				return
			}
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("physical node control acknowledgement unavailable")
}

func managedPhysicalWaitAccount(t *testing.T, fixture *managedPhysicalCase, used, held string) service.ManagedPolicyAccountStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var actual service.ManagedPolicyAccountStatus
	for time.Now().Before(deadline) {
		page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: fixture.parent.StableID, Limit: 16})
		if len(page.Accounts) != 1 {
			t.Fatal("physical original account count changed")
		}
		actual = page.Accounts[0]
		if actual.WindowUsed == used && actual.Budget.Allocated == held {
			return actual
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("actual account did not reach used=%s held=%s: %+v", used, held, actual)
	return actual
}

// Connect both real tunnels before sending bytes: a settled first payload may
// legitimately shrink the next member's share. These owners require the full
// initial allocation as their independently asserted crash boundary.
func managedPhysicalFundedFlows(t *testing.T, fixture *managedPhysicalCase, wantCount int) []net.Conn {
	t.Helper()
	var flows []net.Conn
	for _, node := range fixture.nodes {
		flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", node.manifest.TunnelPort), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = flow.Close() })
		flows = append(flows, flow)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: fixture.parent.StableID, Limit: 16})
		funded := len(page.Accounts) == wantCount
		for _, account := range page.Accounts {
			funded = funded && account.WindowUsed == "0" && account.Budget.Allocated == "8192"
		}
		if funded {
			return flows
		}
		if time.Now().After(deadline) {
			t.Fatal("real initial grants did not fund both physical nodes")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runManagedPhysicalControl(t *testing.T, input string, previous uint64, path string, h *sshHTTPHarness, node *service.ClientPolicyNodeService, peer *managedProductHTTPPeer, manifest *managedPhysicalManifest) uint64 {
	t.Helper()
	fields := strings.Fields(input)
	if len(fields) != 2 {
		t.Fatal("malformed private physical control")
	}
	sequence, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || sequence == 0 || sequence != previous+1 {
		t.Fatal("nonsequential private physical control")
	}
	switch fields[1] {
	case "partition", "rejoin":
		peer.partition.Store(fields[1] == "partition")
		peer.server.CloseClientConnections()
	case "restart":
		oldBoot := manifest.BootID
		if err := h.svc.RestartXray(true); err != nil {
			t.Fatal("actual physical core restart failed", err)
		}
		discovery, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{})
		if err != nil || discovery.Capabilities.InstanceId != manifest.SourceID || discovery.Capabilities.BootId == oldBoot {
			t.Fatal("actual restarted core did not retain original source", err)
		}
		endpoint, err := h.svc.GetXrayAPIEndpoint()
		if err != nil {
			t.Fatal(err)
		}
		actual, err := xray.DialClientPolicy(context.Background(), endpoint, manifest.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		state, stateErr := actual.GetClient(context.Background(), manifest.LocalClientID)
		_ = actual.Close()
		if stateErr != nil || state == nil || state.Policy == nil || state.Policy.Version != 1 {
			t.Fatal("actual restarted core lost local version1", stateErr)
		}
		manifest.BootID = discovery.Capabilities.BootId
	default:
		t.Fatal("unknown private physical control")
	}
	raw, err := json.Marshal(managedPhysicalControlReply{Sequence: sequence, Command: fields[1], Manifest: *manifest})
	if err != nil {
		t.Fatal(err)
	}
	target := path + ".control.json"
	if err := os.WriteFile(target+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target+".tmp", target); err != nil {
		t.Fatal(err)
	}
	return sequence
}
