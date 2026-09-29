package service

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func TestMieruInboundQuotaResetKeepsIndependentRestrictions(t *testing.T) {
	testMieruInboundQuotaResetKeepsIndependentRestrictions(t, false)
}

func TestMieruInboundQuotaResetKeepsIndependentRestrictions_Postgres(t *testing.T) {
	testMieruInboundQuotaResetKeepsIndependentRestrictions(t, true)
}

func testMieruInboundQuotaResetKeepsIndependentRestrictions(t *testing.T, postgres bool) {
	t.Helper()
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			fixture := newProductionMieruFixture(t, underlay)
			clients, inbounds := &ClientService{}, &InboundService{}
			live := openProductionMieruFlows(t, fixture.client)
			record := lookupClientRecord(t, fixture.user.Email)
			ledger := database.NewClientUsageLedger(database.GetDB())
			before, err := ledger.Read(t.Context(), record.PolicyID)
			if err != nil {
				t.Fatal(err)
			}
			updated := *record.ToClient()
			past := time.Now().Add(-time.Hour).UnixMilli()
			updated.Enable, updated.ExpiryTime, updated.TotalGB = false, past, before.Billed
			if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, live)
			if _, err := clients.ResetTrafficByEmail(inbounds, updated.Email); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruDenied(t, fixture.client)
			after, err := ledger.Read(t.Context(), record.PolicyID)
			current := lookupClientRecord(t, updated.Email)
			if err != nil || after.Up != 0 || after.Down != 0 || after.Billed != 0 || after.Remainder != 0 || current.Enable || current.ExpiryTime != past {
				t.Fatalf("quota reset changed an independent restriction: usage=%+v client=%+v error=%v", after, current, err)
			}
			updated = *current.ToClient()
			updated.TotalGB = 1 << 20
			if _, err := clients.Update(inbounds, current.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruDenied(t, fixture.client)
			if _, _, err := clients.SetClientEnableByEmail(inbounds, updated.Email, true); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruDenied(t, fixture.client)
			if _, err := clients.ResetClientExpiryTimeByEmail(inbounds, updated.Email, time.Now().Add(time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			live = openProductionMieruFlows(t, fixture.client)
			current = lookupClientRecord(t, updated.Email)
			updated = *current.ToClient()
			updated.Enable, updated.ExpiryTime, updated.Reset = false, past, 1
			if _, err := clients.Update(inbounds, current.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, live)
			if _, _, err := inbounds.AddTraffic(nil, nil); err != nil {
				t.Fatal(err)
			}
			after, err = ledger.Read(t.Context(), record.PolicyID)
			current = lookupClientRecord(t, updated.Email)
			if err != nil || after.Up != 0 || after.Down != 0 || after.Billed != 0 || after.Remainder != 0 || current.Enable || current.ExpiryTime <= time.Now().UnixMilli() {
				t.Fatalf("automatic renewal lost accounting or manual-disable state: usage=%+v client=%+v error=%v", after, current, err)
			}
			requireProductionMieruDenied(t, fixture.client)
			if _, _, err := clients.SetClientEnableByEmail(inbounds, updated.Email, true); err != nil {
				t.Fatal(err)
			}
			openProductionMieruFlows(t, fixture.client)
		})
	}
}
