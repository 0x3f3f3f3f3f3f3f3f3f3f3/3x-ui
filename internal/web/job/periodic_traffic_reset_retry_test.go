package job

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPeriodicTrafficResetRetryKeepsOriginalMembershipAndNewUsage(t *testing.T) {
	initResetJobDB(t)
	seedClientOnCycle(t, 41008, seededClient{email: "scheduled-original", cycle: "weekly", day: 1, recordEnable: true, quotaEnable: true})
	db := database.GetDB()
	if err := db.Model(&model.Inbound{}).Where("port = ?", 41008).Update("traffic_reset", "weekly").Error; err != nil {
		t.Fatal(err)
	}
	job := NewPeriodicTrafficResetJob("weekly", time.UTC)
	now := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	job.runAt(now)
	if row := trafficFor(t, "scheduled-original"); row.Up != 0 || row.Down != 0 {
		t.Fatalf("initial schedule did not reset due usage: %+v", row)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "scheduled-original").Updates(map[string]any{"up": 111, "down": 222}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Inbound{}).Where("port = ?", 41008).Update("up", 333).Error; err != nil {
		t.Fatal(err)
	}
	seedClientOnCycle(t, 41009, seededClient{email: "scheduled-later", cycle: "weekly", day: 1, recordEnable: true, quotaEnable: true})
	job.runAt(now.Add(time.Hour))
	if row := trafficFor(t, "scheduled-original"); row.Up != 111 || row.Down != 222 {
		t.Fatalf("same calendar window reset newly accrued usage: %+v", row)
	}
	if row := trafficFor(t, "scheduled-later"); row.Up != 500 || row.Down != 700 {
		t.Fatalf("schedule retry reset a client outside the original membership: %+v", row)
	}
	var inbound model.Inbound
	if err := db.First(&inbound, "port = ?", 41008).Error; err != nil || inbound.Up != 333 {
		t.Fatalf("schedule retry reset new inbound usage: %+v, %v", inbound, err)
	}
}
