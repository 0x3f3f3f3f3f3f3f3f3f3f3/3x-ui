package sub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Ordinary deletion must revoke real global and node accounts without losing
// historical usage, returning uncertain credit, or resurrecting the identity.
func TestManagedPolicyTwoPhysicalNodesActualDeletionClosesOriginalAccounts(t *testing.T) {
	for _, scope := range []model.ClientPolicyScope{model.ClientPolicyScopeGlobal, model.ClientPolicyScopeNode} {
		t.Run(string(scope), func(t *testing.T) {
			fixture := newManagedPhysicalCase(t, scope, "1.5", 8192, 0, 0)
			var flows []net.Conn
			for _, node := range fixture.nodes {
				flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", node.manifest.TunnelPort), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = flow.Close() })
				sshHTTPEcho(t, flow, "w")
				flows = append(flows, flow)
			}
			req, err := http.NewRequest("POST", fixture.product.server.URL+"/panel/api/clients/del/"+url.PathEscape(fixture.parent.Email)+"?keepTraffic=1", bytes.NewBufferString(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+fixture.product.token)
			response, err := fixture.product.server.Client().Do(req)
			if err != nil {
				t.Fatal("actual ordinary delete request failed", err)
			}
			defer response.Body.Close()
			var result struct {
				Success bool `json:"success"`
			}
			if json.NewDecoder(response.Body).Decode(&result) != nil || response.StatusCode != 200 || !result.Success {
				t.Fatalf("actual ordinary delete failed status=%d", response.StatusCode)
			}
			for _, flow := range flows {
				sshHTTPClosed(t, flow)
			}
			page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", map[string]any{"parentClientId": fixture.parent.StableID, "afterNode": "", "limit": 16})
			wantCount := 1
			if scope == model.ClientPolicyScopeNode {
				wantCount = 2
			}
			if len(page.Accounts) != wantCount {
				t.Fatal("deleted original account count changed")
			}
			for _, account := range page.Accounts {
				want := "6"
				if scope == model.ClientPolicyScopeNode {
					want = "3"
				}
				if !account.Deleted || account.Usage.Billed != want || account.Budget.Allocated != "0" || account.Remaining == nil || *account.Remaining != "0" {
					t.Fatalf("deleted original account state lost: %+v", account)
				}
			}
		})
	}
}

func TestManagedPolicyTwoPhysicalNodesActualPartitionedDeletionRecovery(t *testing.T) {
	for _, scope := range []model.ClientPolicyScope{model.ClientPolicyScopeGlobal, model.ClientPolicyScopeNode} {
		t.Run(string(scope), func(t *testing.T) {
			fixture := newManagedPhysicalCase(t, scope, "1.5", 8192, 0, 0)
			read := func() service.ManagedPolicyAccountPage {
				return managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", map[string]any{"parentClientId": fixture.parent.StableID, "afterNode": "", "limit": 16})
			}
			wantCount, used, held := 1, "6", "8186"
			if scope == model.ClientPolicyScopeNode {
				wantCount, used, held = 2, "3", "8189"
			}
			for _, flow := range managedPhysicalFundedFlows(t, fixture, wantCount) {
				sshHTTPEcho(t, flow, "w")
				_ = flow.Close()
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				page := read()
				settled := len(page.Accounts) == wantCount
				for _, account := range page.Accounts {
					settled = settled && account.WindowUsed == used && account.Budget.Allocated == held
				}
				if settled {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("warm real receipts did not settle before partition")
				}
				time.Sleep(20 * time.Millisecond)
			}
			for _, node := range fixture.nodes {
				managedPhysicalControl(t, node, "partition")
			}
			req, err := http.NewRequest("POST", fixture.product.server.URL+"/panel/api/clients/del/"+url.PathEscape(fixture.parent.Email)+"?keepTraffic=1", bytes.NewBufferString(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+fixture.product.token)
			response, err := fixture.product.server.Client().Do(req)
			if err != nil {
				t.Fatal("actual partitioned delete request failed", err)
			}
			var result struct {
				Success bool `json:"success"`
			}
			err = json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || result.Success {
				t.Fatal("unreachable seal was acknowledged")
			}
			if err := service.StopManagedPolicyCoordinator(context.Background()); err == nil {
				t.Fatal("unreachable original close was acknowledged")
			}
			page := read()
			if len(page.Accounts) != wantCount {
				t.Fatal("original deleted membership changed")
			}
			for _, account := range page.Accounts {
				if !account.Deleted || account.WindowUsed != used || account.Budget.Allocated != held || account.Remaining == nil || *account.Remaining != "0" {
					t.Fatalf("partitioned deletion released uncertain allowance: %+v", account)
				}
			}
			for _, node := range fixture.nodes {
				managedPhysicalControl(t, node, "rejoin")
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := service.ResumeManagedPolicyCoordinator(context.Background()); err != nil {
					t.Fatal("deleted original recovery failed", err)
				}
				if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
					t.Fatal("deleted actual seals failed", err)
				}
				for _, account := range read().Accounts {
					if !account.Deleted || account.Usage.Billed != used || account.WindowUsed != used || account.Budget.Allocated != "0" {
						t.Fatalf("original deleted recovery rebilled or recreated credit: %+v", account)
					}
				}
			}
		})
	}
}
