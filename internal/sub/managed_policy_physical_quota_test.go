package sub

import (
	"context"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestManagedPolicyTwoPhysicalNodesActualScopeQuotas(t *testing.T) {
	for _, scope := range []model.ClientPolicyScope{model.ClientPolicyScopeGlobal, model.ClientPolicyScopeNode} {
		t.Run(string(scope), func(t *testing.T) {
			fixture := newManagedPhysicalCase(t, scope, "2", 12, 0, 0)
			var group sync.WaitGroup
			for _, node := range fixture.nodes {
				group.Go(func() { managedPhysicalExchange(t, node.manifest.TunnelPort, "tcp4", []byte("x"), true) })
			}
			group.Wait()
			oneWay := []byte("!")
			if scope == model.ClientPolicyScopeNode {
				oneWay = []byte("!!")
			}
			for _, node := range fixture.nodes {
				group.Go(func() { managedPhysicalExchange(t, node.manifest.TunnelPort, "tcp4", oneWay, false) })
			}
			group.Wait()
			if scope == model.ClientPolicyScopeNode {
				for _, node := range fixture.nodes {
					group.Go(func() { managedPhysicalExchange(t, node.manifest.TunnelPort, "tcp4", []byte("z"), true) })
				}
				group.Wait()
			}
			for _, node := range fixture.nodes {
				managedPhysicalDenied(t, node.manifest.TunnelPort)
			}
			if err := service.StopManagedPolicyCoordinator(context.Background()); err != nil {
				t.Fatal("actual quota grants did not seal", err)
			}
			page := managedProductRPC[service.ManagedPolicyAccountPage](t, fixture.product, "POST", "/accounts", service.ManagedPolicyAccountPageRequest{ParentClientID: fixture.parent.StableID, Limit: 16})
			wantCount := 1
			if scope == model.ClientPolicyScopeNode {
				wantCount = 2
			}
			if len(page.Accounts) != wantCount {
				t.Fatal("scope changed physical account count")
			}
			seen := map[string]bool{}
			for _, account := range page.Accounts {
				if seen[account.ClientID] || account.Usage.Billed != "12" || account.Budget.Allocated != "0" || account.Remaining == nil || *account.Remaining != "0" || account.Budget.Unallocated == nil || *account.Budget.Unallocated != "0" {
					t.Fatalf("actual quota was shared, replenished or overspent: %+v", account)
				}
				seen[account.ClientID] = true
				if scope == model.ClientPolicyScopeNode && account.ClientID == fixture.parent.StableID {
					t.Fatal("node scope reused parent identity")
				}
			}
			t.Logf("physical scope=%s nodes=2 multiplier=2 exact_accounts=%d quota_each=12 denied_after_exhaustion=true", scope, wantCount)
		})
	}
}
