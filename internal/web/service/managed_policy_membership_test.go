package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

type managedSealPartition struct {
	authorityCoreAPI
	err error
}

type managedDemandReturnDelay struct {
	authorityDemandAPI
	reached chan struct{}
	release chan struct{}
}

func (a *managedDemandReturnDelay) ReadAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding, limit uint32) (*command.AuthorityRequests, error) {
	close(a.reached)
	page, err := a.authorityDemandAPI.ReadAuthorityRequests(ctx, binding, limit)
	<-a.release // Delay returning the actual RPC outcome; invent no response.
	return page, err
}

func TestManagedPolicyResumeStopTimeoutRetainsTrackedController(t *testing.T) {
	_, client := authorityExecutionFixture(t)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed stop timeout backend: %s", db.Dialector.Name())
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	api, _ := authorityExecutionCore(t, "stop-timeout-source", client)
	delayed := &managedDemandReturnDelay{authorityDemandAPI: api, reached: make(chan struct{}), release: make(chan struct{})}
	controller, err := newAuthorityController(ctx, db, c.state.Journal, "tracked-node", delayed)
	if err != nil {
		t.Fatal(err)
	}
	c.controllers["tracked-node"] = controller
	t.Cleanup(func() { close(delayed.release); _ = c.Close(ctx) })
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delayed.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("actual demand read did not start")
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := c.ResumeNodes(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop timeout was not reported: %v", err)
	}
	if c.controllers["tracked-node"] != controller {
		t.Fatal("timed-out stop discarded a controller that had not joined")
	}
}

func (a *managedSealPartition) SealAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.authorityCoreAPI.SealAuthorityGrant(ctx, client, grant)
}

func TestManagedPolicyMembershipChangeRetainsUncertainActualShares(t *testing.T) {
	j, client := authorityExecutionFixture(t)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed uncertain membership backend: %s", db.Dialector.Name())
	apiA, _ := authorityExecutionCore(t, "membership-source-a", client)
	apiB, address := authorityExecutionCore(t, "membership-source-b", client)
	partition := errors.New("actual peer seal unavailable")
	fault := &managedSealPartition{authorityCoreAPI: apiA, err: partition}
	a, err := newAuthorityExecution(ctx, db, j, "node-a", fault)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newAuthorityExecution(ctx, db, j, "node-b", apiB)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := newManagedAllocationStrategy([]managedAuthorityMember{{NodeID: a.boot.NodeID, SourceID: a.boot.SourceID}, {NodeID: b.boot.NodeID, SourceID: b.boot.SourceID}})
	if err != nil {
		t.Fatal(err)
	}
	var grants []policyauthority.Grant
	for i, execution := range []*authorityExecution{a, b} {
		account, err := j.Account(client)
		if err != nil {
			t.Fatal(err)
		}
		capacity, up, down, err := strategy.Allocate(account, execution.boot)
		if err != nil {
			t.Fatal(err)
		}
		grant, err := execution.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: []string{"0123456789abcdef0123456789abcdef", "1123456789abcdef0123456789abcdef"}[i], Capacity: capacity, Upload: up, Download: down, LeaseDuration: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	if err := b.Settle(ctx, grants[1].GrantID, true, false); err != nil {
		t.Fatal(err)
	}
	before, err := j.Account(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Settle(ctx, grants[0].GrantID, true, false); !errors.Is(err, partition) {
		t.Fatal("partition returned an invented seal", err)
	}
	strategy, err = newManagedAllocationStrategy([]managedAuthorityMember{{NodeID: b.boot.NodeID, SourceID: b.boot.SourceID}})
	if err != nil {
		t.Fatal(err)
	}
	capacity, up, down, err := strategy.Allocate(before, b.boot)
	if err != nil {
		t.Fatal(err)
	}
	intent := authorityAllocation{ClientID: client, RequestID: "2123456789abcdef0123456789abcdef", Capacity: capacity, Upload: up, Download: down, LeaseDuration: time.Second}
	if _, err := b.Authorize(ctx, intent); !errors.Is(err, policyauthority.ErrCapacity) {
		t.Fatal("changed membership overlapped uncertain original rates", err)
	}
	retained, err := j.Account(client)
	if err != nil || retained != before {
		t.Fatal("failed rebalance replenished allowance or fractional seed", err)
	}
	fault.err = nil
	if err := a.Settle(ctx, grants[0].GrantID, true, false); err != nil {
		t.Fatal(err)
	}
	grant, err := b.Authorize(ctx, intent)
	if err != nil {
		t.Fatal("authentic seal did not unblock remaining member", err)
	}
	authorityEndpointExchange(t, address, 5)
	if err := b.Settle(ctx, grant.GrantID, true, false); err != nil {
		t.Fatal(err)
	}
	final, err := j.Account(client)
	if err != nil || final.HeldCapacity != 0 || final.UploadHeld.Rate != 0 || final.DownloadHeld.Rate != 0 || final.Usage.BilledBytes != before.Usage.BilledBytes+15 || final.Usage.Remainder != before.Usage.Remainder {
		t.Fatalf("changed member actual settlement lost conservation: %+v/%v", final, err)
	}
}

func TestManagedPolicyConfiguredConnectionsRejectSQLRetargeting(t *testing.T) {
	setupPolicyLedgerDB(t)
	db, ctx := database.GetDB(), context.Background()
	t.Logf("managed configured identity backend: %s", db.Dialector.Name())
	c, err := openManagedPolicyCoordinator(ctx, db, filepath.Join(t.TempDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	scope := model.ClientPolicyScopeGlobal
	parent := model.ClientRecord{Email: "configured-unit-origin", Enable: true, TotalGB: 100, Policy: &model.ClientPolicyOptions{Scope: &scope}}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	member := managedAuthorityMember{NodeID: "original-configured-node", SourceID: "original-configured-source"}
	origin, _, err := c.PrepareAccount(ctx, parent.StableID, member)
	if err != nil {
		t.Fatal(err)
	}
	mapping := policyauthority.ClientMapping{Authority: c.state.Journal.Identity(), NodeAnchor: policyauthority.Identity{AuthorityID: "unit-original-node-anchor", Generation: 1}, NodeID: member.NodeID, SourceID: member.SourceID, GlobalClientID: origin.ClientID, LocalClientID: uuid.NewString(), GlobalPolicyVersion: origin.InitialPolicyVersion, LocalPolicyVersion: 1, PolicyDigest: origin.PolicyDigest}
	if err := c.state.Journal.RecordClientMapping(policyauthority.ClientMappingCoordinator, mapping); err != nil {
		t.Fatal(err)
	}
	before, _ := c.state.Journal.Account(origin.ClientID)
	inventory := model.Node{Name: "configured unit inventory", Enable: true}
	if err := db.Create(&inventory).Error; err != nil {
		t.Fatal(err)
	}
	row := model.ClientPolicyCoordinatorNode{NodeID: member.NodeID, SourceID: member.SourceID, InventoryID: inventory.Id}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{member.SourceID, "unproven-replacement-source"} {
		if err := db.Model(&row).Update("source_id", source).Error; err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		rows, err := c.configuredConnections(ctx)
		c.mu.Unlock()
		if source == member.SourceID && (err != nil || len(rows) != 1) || source != member.SourceID && (err == nil || rows != nil) {
			t.Fatal("SQL chose configured source without original proof", err)
		}
	}
	if after, err := c.state.Journal.Account(origin.ClientID); err != nil || after != before {
		t.Fatal("connection projection created allocation authority", err)
	}
}
