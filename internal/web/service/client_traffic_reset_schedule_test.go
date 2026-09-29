package service

import (
	"context"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestScheduledTrafficResetMonthlySelection(t *testing.T) {
	cases := []struct {
		name     string
		resetDay int
		now      time.Time
		want     bool
	}{
		{"legacy default on first", 0, time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC), true},
		{"configured day", 15, time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC), true},
		{"before configured day", 15, time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC), false},
		{"month end", 31, time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC), true},
		{"short month fallback", 31, time.Date(2026, time.February, 28, 0, 0, 0, 0, time.UTC), true},
		{"leap year fallback", 31, time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC), true},
		{"not before short month end", 31, time.Date(2028, time.February, 28, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			client := model.ClientRecord{Email: "monthly-selection", Enable: true, TrafficReset: "monthly"}
			if err := db.Create(&client).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&client).Update("traffic_reset_day", tc.resetDay).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Up: 11, Down: 22}).Error; err != nil {
				t.Fatal(err)
			}
			if err := (&ClientService{}).RunScheduledTrafficReset(context.Background(), "monthly", tc.now); err != nil {
				t.Fatal(err)
			}
			row := trafficOf(t, client.Email)
			if tc.want && (row.Up != 0 || row.Down != 0) || !tc.want && (row.Up != 11 || row.Down != 22) {
				t.Fatalf("monthly schedule changed the wrong window: due=%v, traffic=%+v", tc.want, row)
			}
		})
	}
}

func TestScheduledTrafficResetOlderWindowKeepsNewUsage(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	client := model.ClientRecord{Email: "calendar-order", Enable: true, TrafficReset: "daily"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Up: 11, Down: 22}).Error; err != nil {
		t.Fatal(err)
	}
	inbound := mkInbound(t, 24215, model.Tunnel, `{}`)
	if err := db.Model(inbound).Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	svc := &ClientService{}
	newer := time.Date(2030, time.January, 2, 0, 0, 0, 0, time.UTC)
	if err := svc.RunScheduledTrafficReset(context.Background(), "daily", newer); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("up", 77).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(inbound).Update("up", 88).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RunScheduledTrafficReset(context.Background(), "daily", newer.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if row := trafficOf(t, client.Email); row.Up != 77 {
		t.Fatalf("delayed older calendar task granted a new allowance: %+v", row)
	}
	if err := db.First(inbound, inbound.Id).Error; err != nil || inbound.Up != 88 || inbound.LastTrafficResetTime != newer.UnixMilli() {
		t.Fatalf("older calendar task reset a newer inbound period: %+v, %v", inbound, err)
	}
}

func TestScheduledTrafficResetPendingWindowCannotUndoManualReset(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	client := model.ClientRecord{Email: "calendar-before-manual", Enable: true, TrafficReset: "daily"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Up: 11}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := captureScheduledTrafficReset(context.Background(), "daily", now); err != nil {
		t.Fatal(err)
	}
	svc := &ClientService{}
	if _, err := svc.ResetTrafficByEmail(&InboundService{}, client.Email); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("up", 77).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RunScheduledTrafficReset(context.Background(), "daily", now); err != nil {
		t.Fatal(err)
	}
	if row := trafficOf(t, client.Email); row.Up != 77 {
		t.Fatalf("pending calendar reset superseded a newer manual window: %+v", row)
	}
}

func TestScheduledTrafficResetPendingWindowKeepsLaterManualDisable(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	svc := &ClientService{}
	inbound := seedLocalDisabledClient(t, svc, 24216, "", "calendar-later-disable", 1000, 0, 11, 22)
	if _, _, err := svc.BulkSetEnable(&InboundService{}, []string{"calendar-later-disable"}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", "calendar-later-disable").Update("traffic_reset", "daily").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := captureScheduledTrafficReset(context.Background(), "daily", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.BulkSetEnable(&InboundService{}, []string{"calendar-later-disable"}, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunScheduledTrafficReset(context.Background(), "daily", now); err != nil {
		t.Fatal(err)
	}
	assertEnableEverywhere(t, svc, &InboundService{}, inbound.Id, "calendar-later-disable", false)
	if row := trafficOf(t, "calendar-later-disable"); row.Up != 11 || row.Down != 22 {
		t.Fatalf("pending calendar task reset an operator-disabled legacy client: %+v", row)
	}
}

func TestScheduledTrafficResetCalendarBoundaries(t *testing.T) {
	cases := []struct {
		name, period, zone, first, second string
		newWindow                         bool
	}{
		{"repeated DST hour", "hourly", "America/New_York", "2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00", true},
		{"long DST day", "daily", "America/New_York", "2026-11-01T00:30:00-04:00", "2026-11-01T23:30:00-05:00", false},
		{"short DST day", "daily", "America/New_York", "2027-03-14T00:30:00-05:00", "2027-03-15T00:30:00-04:00", true},
		{"Sunday boundary", "weekly", "UTC", "2026-09-26T23:59:00Z", "2026-09-27T00:01:00Z", true},
		{"quarter-hour zone", "hourly", "Asia/Kathmandu", "2026-10-01T10:45:00+05:45", "2026-10-01T11:05:00+05:45", true},
		{"local day across UTC midnight", "daily", "Asia/Kathmandu", "2026-10-01T01:00:00+05:45", "2026-10-01T10:00:00+05:45", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			client := model.ClientRecord{Email: "calendar-clock", Enable: true, TrafficReset: tc.period}
			if err := db.Create(&client).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: client.Email, Enable: true, Up: 11}).Error; err != nil {
				t.Fatal(err)
			}
			location, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			svc := &ClientService{}
			for i, stamp := range []string{tc.first, tc.second} {
				now, err := time.Parse(time.RFC3339, stamp)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.RunScheduledTrafficReset(context.Background(), tc.period, now.In(location)); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", client.Email).Update("up", 77).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			wantUsage, wantWindows := int64(77), int64(1)
			if tc.newWindow {
				wantUsage, wantWindows = 0, 2
			}
			if row := trafficOf(t, client.Email); row.Up != wantUsage {
				t.Fatalf("calendar boundary chose the wrong allowance: %+v, want usage %d", row, wantUsage)
			}
			var count int64
			if err := db.Model(&model.ClientTrafficResetBatch{}).Count(&count).Error; err != nil || count != wantWindows {
				t.Fatalf("calendar boundary produced %d operations, want %d: %v", count, wantWindows, err)
			}
		})
	}
}
