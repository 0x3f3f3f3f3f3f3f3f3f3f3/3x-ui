package sub

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestManagedPolicyTwoPhysicalNodesActualTunnelBilling(t *testing.T) {
	if configPath := os.Getenv("XUI_MANAGED_PHYSICAL_HELPER_CONFIG"); configPath != "" {
		runManagedPhysicalNode(t, configPath)
		return
	}
	if os.Getenv("XRAY_E2E_BINARY") == "" {
		t.Fatal("actual paired core is required for physical-node acceptance")
	}
	cleanup, err := testpg.IsolatePackage("managed_physical_parent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	seedSubDB(t)
	product := newManagedProductHTTPPeer(t)
	status := managedProductRPC[service.ManagedPolicyCoordinatorStatus](t, product, "GET", "", nil)
	if status.Active {
		t.Fatal("fresh parent acquired an implicit coordinator")
	}
	status = managedProductRPC[service.ManagedPolicyCoordinatorStatus](t, product, "POST", "/activate", map[string]any{})
	if !status.Active || status.Generation != "1" {
		t.Fatal("explicit original coordinator unavailable")
	}
	ctx := context.Background()
	a := startManagedPhysicalNode(t, status, "physical-node-a", "1.5", 8192)
	b := startManagedPhysicalNode(t, status, "physical-node-b", "1.5", 8192)
	t.Cleanup(func() { _ = service.StopManagedPolicyCoordinator(ctx) })
	if a.manifest.PID == b.manifest.PID || a.manifest.PID == os.Getpid() || a.manifest.SourceID == b.manifest.SourceID || a.manifest.RuntimeDir == b.manifest.RuntimeDir || a.manifest.SQLStorage == b.manifest.SQLStorage || a.manifest.LocalClientID == b.manifest.LocalClientID || a.manifest.CoreSHA256 != b.manifest.CoreSHA256 {
		t.Fatal("nodes shared physical process, SQL, runtime, identity or mismatched core")
	}
	scope := model.ClientPolicyScopeGlobal
	parent := model.ClientRecord{Email: "physical-global-parent", Enable: true, TotalGB: 8192, Policy: &model.ClientPolicyOptions{Scope: &scope, Multiplier: "1.5"}}
	if err := database.GetDB().Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	for _, quota := range []int64{8192, 8193, 8194, 8195, 8196, 8197, 8192} {
		if err := database.GetDB().Table("clients").Where("stable_id = ?", parent.StableID).Update("total_gb", quota).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := service.PrepareClientPolicies([]string{parent.StableID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range []*managedPhysicalNode{a, b} {
		request := registerManagedPhysicalInventory(t, node, parent.StableID)
		result := managedProductRPC[service.ManagedPolicyEnrollmentResult](t, product, "POST", "/enroll", request)
		if !result.Connected || result.ClientID != parent.StableID || result.ClientID == node.manifest.LocalClientID || result.PolicyVersion != "7" || result.LocalPolicyVersion != "1" {
			t.Fatal("production enrollment lost independent canonical/global7/local1 proof")
		}
	}
	var group sync.WaitGroup
	for _, node := range []*managedPhysicalNode{a, b} {
		group.Go(func() { managedPhysicalExchange(t, node.manifest.TunnelPort, "tcp4", []byte("x"), true) })
	}
	group.Wait()
	for _, node := range []*managedPhysicalNode{a, b} {
		for _, network := range []string{"tcp4", "udp4"} {
			group.Go(func() { managedPhysicalExchange(t, node.manifest.TunnelPort, network, []byte("y"), true) })
		}
	}
	group.Wait()
	managedPhysicalExchange(t, a.manifest.TunnelPort, "tcp4", []byte("!"), false)
	if err := service.StopManagedPolicyCoordinator(ctx); err != nil {
		t.Fatal("actual physical grants did not seal", err)
	}
	page := managedProductRPC[service.ManagedPolicyAccountPage](t, product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: parent.StableID, Limit: 16})
	if len(page.Accounts) != 1 {
		t.Fatal("global account split across physical nodes")
	}
	account := page.Accounts[0]
	if account.Usage.Upload != "7" || account.Usage.Download != "6" || account.Usage.Billed != "19.5" || account.Budget.Allocated != "0" || account.Remaining == nil || *account.Remaining != "8172.5" || page.PendingEnrollment {
		t.Fatalf("actual TCP/UDP/fraction settlement mismatch: %+v", account)
	}
	// Read authentic cumulative contributions through the protected product route.
	// Reopening the coordinator must not sum seeds or bill these receipts again.
	for attempt := 0; attempt < 2; attempt++ {
		rawUp := map[string]uint64{}
		rawDown := map[string]uint64{}
		billed := map[string]uint64{}
		rem := map[string]uint64{}
		cursor := ""
		for pages := 0; pages < 64; pages++ {
			page := managedProductRPC[struct {
				Contributions []struct {
					NodeID         string `json:"nodeId"`
					SourceID       string `json:"sourceId"`
					BootID         string `json:"bootId"`
					GrantID        string `json:"grantId"`
					GrantSequence  string `json:"grantSequence"`
					ReportSequence string `json:"reportSequence"`
					Sealed         bool   `json:"sealed"`
					Usage          struct {
						Upload   string `json:"upload"`
						Download string `json:"download"`
						Billed   string `json:"billed"`
					} `json:"usage"`
				} `json:"contributions"`
				NextGrant string `json:"nextGrant"`
			}](t, product, "POST", "/contributions", map[string]any{"parentClientId": parent.StableID, "clientId": parent.StableID, "afterGrant": cursor, "limit": 16})
			for _, row := range page.Contributions {
				var node *managedPhysicalNode
				for _, candidate := range []*managedPhysicalNode{a, b} {
					if candidate.manifest.NodeID == row.NodeID {
						node = candidate
					}
				}
				if node == nil || row.SourceID != node.manifest.SourceID || row.BootID == "" || row.GrantID == "" || row.GrantSequence == "" || row.ReportSequence == "0" || !row.Sealed {
					t.Fatalf("original contribution provenance lost: %+v", row)
				}
				up, e := strconv.ParseUint(row.Usage.Upload, 10, 64)
				if e != nil {
					t.Fatal(e)
				}
				down, e := strconv.ParseUint(row.Usage.Download, 10, 64)
				if e != nil {
					t.Fatal(e)
				}
				parts := strings.Split(row.Usage.Billed, ".")
				whole, e := strconv.ParseUint(parts[0], 10, 64)
				if e != nil {
					t.Fatal(e)
				}
				rawUp[row.NodeID] += up
				rawDown[row.NodeID] += down
				billed[row.NodeID] += whole
				if len(parts) == 2 {
					fraction, e := strconv.ParseUint(parts[1]+strings.Repeat("0", 6-len(parts[1])), 10, 64)
					if e != nil {
						t.Fatal(e)
					}
					rem[row.NodeID] += fraction
				}
			}
			if page.NextGrant == "" {
				break
			}
			if page.NextGrant == cursor {
				t.Fatal("contribution cursor stalled")
			}
			cursor = page.NextGrant
		}
		for _, node := range []*managedPhysicalNode{a, b} {
			key := node.manifest.NodeID
			wantUp, wantMicros := uint64(3), uint64(9000000)
			if node == a {
				wantUp, wantMicros = 4, 10500000
			}
			if rawUp[key] != wantUp || rawDown[key] != 3 || billed[key]*1000000+rem[key] != wantMicros {
				t.Fatalf("asymmetric original contribution lost: node=%s raw=%d/%d billed=%d+%d", key, rawUp[key], rawDown[key], billed[key], rem[key])
			}
		}
		if err := service.StopManagedPolicyCoordinator(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("physical managed business backend=%s independent_nodes=2 actual_tcp_udp=true raw=7+6 billed=19.5 global_version=7 local_versions=1,1 core_sha256=%s", database.GetDB().Dialector.Name(), a.manifest.CoreSHA256)
}
