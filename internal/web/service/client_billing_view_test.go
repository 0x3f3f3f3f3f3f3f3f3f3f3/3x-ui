package service

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientBillingViewsUseLedgerAndExactFractionalBalances(t *testing.T) {
	setupBulkDB(t)
	db := database.GetDB()
	for _, seed := range []struct {
		email                                               string
		raw, billed, carry, multiplier, quota, trafficQuota int64
	}{
		{"double", 50, 100, 0, 2000, 100, 100},
		{"discount", 120, 60, 0, 500, 100, 100},
		{"fractional", 1, 98, 500, 1000, 100, 100},
		{"whole", 1, 98, 0, 1000, 100, 100},
		{"sub-byte", 1, 99, 500, 1000, 100, 100},
		{"traffic-cap", 10, 40, 0, 4000, 100, 40},
		{"huge", 9007199254740993, 9007199254740993, 500, 1500, 9007199254740994, 9007199254740994},
		{"legacy", 60, 0, 0, 0, 100, 100},
	} {
		client := model.ClientRecord{Email: seed.email, Enable: true, TotalGB: seed.quota}
		if err := db.Create(&client).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&xray.ClientTraffic{Email: seed.email, PolicyID: client.PolicyID, Enable: true, Up: seed.raw, Total: seed.trafficQuota}).Error; err != nil {
			t.Fatal(err)
		}
		if seed.multiplier > 0 {
			if err := db.Create(&model.ClientUsageAccount{PolicyID: client.PolicyID, Up: seed.raw, Billed: seed.billed, Remainder: seed.carry, Multiplier: seed.multiplier, Revision: 1}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	q := newClientQuery(db, time.Now().UnixMilli(), 0, 2)
	for _, test := range []struct {
		filter string
		want   []string
	}{
		{"depleted", []string{"double", "sub-byte", "traffic-cap", "huge"}},
		{"expiring", []string{"fractional"}},
		{"active", []string{"discount", "whole", "legacy"}},
	} {
		rows, err := q.pageRows(ClientPageParams{Filter: test.filter}, nil, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if got := pagedEmails(rows); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s = %v, want %v", test.filter, got, test.want)
		}
	}
	summary, err := q.summary(nil, 8)
	if err != nil || summary.DepletedCount != 4 || summary.ExpiringCount != 1 || summary.Active != 3 {
		t.Fatalf("billing summary = %+v, %v", summary, err)
	}
	for _, test := range []struct {
		params ClientPageParams
		want   []string
	}{
		{ClientPageParams{UsageFrom: 98, UsageTo: 98}, []string{"whole"}},
		{ClientPageParams{UsageFrom: 98, UsageTo: 99, Sort: "traffic"}, []string{"whole", "fractional"}},
		{ClientPageParams{UsageFrom: 98, UsageTo: 99, Sort: "remaining", Order: "descend"}, []string{"whole", "fractional"}},
	} {
		rows, err := q.pageRows(test.params, nil, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if got := pagedEmails(rows); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%+v = %v, want %v", test.params, got, test.want)
		}
	}
	rows, err := q.pageRows(ClientPageParams{Search: "huge"}, nil, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		Billing struct {
			Up, Billed, Remaining, Multiplier string
			Remainder                         int
			Exhausted                         bool
		}
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].Billing.Up != "9007199254740993" || decoded[0].Billing.Billed != "9007199254740993" || decoded[0].Billing.Remaining != "1" || decoded[0].Billing.Remainder != 500 || !decoded[0].Billing.Exhausted || decoded[0].Billing.Multiplier != "1.5" {
		t.Fatalf("exact billing absent or rounded: %s", encoded)
	}
	all, err := (&ClientService{}).List()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		Email   string
		Billing *struct{ Billed string }
	}
	if err := json.Unmarshal(encoded, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 8 || listed[0].Billing == nil || listed[0].Billing.Billed != "100" || listed[7].Billing != nil {
		t.Fatal("unpaged list omitted ledger billing or activated a legacy client")
	}
	if err := db.Create(&model.ClientGlobalTraffic{Email: "discount", MasterGuid: "other-panel", Up: 5000, UpdatedAt: time.Now().UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	q = newClientQuery(db, time.Now().UnixMilli(), 0, 2)
	rows, err = q.pageRows(ClientPageParams{Search: "discount", Filter: "active"}, nil, 0, 10)
	if err != nil || len(rows) != 1 || rows[0].Billing == nil || rows[0].Billing.Up != "120" || rows[0].Billing.Billed != "60" {
		t.Fatalf("legacy global raw overlay replaced local ledger billing: %+v, %v", rows, err)
	}
	var count int64
	if err := db.Model(&model.ClientUsageAccount{}).Count(&count).Error; err != nil || count != 7 {
		t.Fatalf("read activated or removed accounts: %d, %v", count, err)
	}
}

func TestClientBillingViews_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestClientBillingViewsUseLedgerAndExactFractionalBalances(t)
}
