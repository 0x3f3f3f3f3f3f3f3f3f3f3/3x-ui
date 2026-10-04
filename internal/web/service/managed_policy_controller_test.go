package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedPolicyControllersShareActualCoreGrants(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	t.Logf("managed controller backend: %s", database.GetDB().Dialector.Name())
	apiA, addressA := authorityExecutionCore(t, "managed-source-a", client)
	apiB, addressB := authorityExecutionCore(t, "managed-source-b", client)
	strategy, err := newManagedAllocationStrategy([]managedAuthorityMember{{NodeID: "node-a", SourceID: apiA.Capabilities().InstanceId}, {NodeID: "node-b", SourceID: apiB.Capabilities().InstanceId}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	lost := errors.New("lost successful managed install reply")
	faultA := &authorityDemandFault{authorityDemandAPI: apiA, installFault: lost}
	a, err := newAuthorityControllerWithStrategy(ctx, database.GetDB(), journal, "node-a", faultA, strategy)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newAuthorityControllerWithStrategy(ctx, database.GetDB(), journal, "node-b", apiB, strategy)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan error, 2)
	go func() { delivered <- demandEndpointExchange(addressA) }()
	go func() { delivered <- demandEndpointExchange(addressB) }()
	var wg sync.WaitGroup
	for _, controller := range []*authorityController{a, b} {
		wg.Go(func() {
			for controller.active[client] == nil && ctx.Err() == nil {
				err := controller.ProcessRequests(ctx)
				if err != nil && !errors.Is(err, lost) {
					t.Errorf("actual managed demand: %v", err)
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	wg.Wait()
	for range 2 {
		select {
		case err := <-delivered:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("managed grants did not deliver actual core traffic")
		}
	}
	before, err := journal.Account(client)
	if err != nil || before.HeldCapacity != 89 || before.UploadHeld.Rate != 8192 || before.UploadHeld.Burst != 128 || before.DownloadHeld.Rate != 8192 || before.DownloadHeld.Burst != 128 {
		t.Fatalf("two controllers overlapped or geometrically shrank shares: %+v/%v", before, err)
	}
	for _, controller := range []*authorityController{a, b} {
		if err := controller.SettleAndRenew(ctx); err != nil {
			t.Fatal(err)
		}
	}
	account, err := journal.Account(client)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 13, RawDownload: 12, BilledBytes: 40, Remainder: 100000}) || account.HeldCapacity != 59 {
		t.Fatalf("managed receipts rebilled or changed fractional seed: %+v/%v", account, err)
	}
	for _, controller := range []*authorityController{a, b} {
		if err := controller.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	final, err := journal.Account(client)
	if err != nil || final.HeldCapacity != 0 || final.UploadHeld.Rate != 0 || final.DownloadHeld.Rate != 0 || final.Usage != account.Usage {
		t.Fatalf("actual seal changed usage or retained settled shares: %+v/%v", final, err)
	}
}
