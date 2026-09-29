package service

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruPresenceAndStatusFollowAuthenticatedFlows(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, underlay)
			flows := openProductionMieruFlows(t, fixture.client)
			productionMieruStatus(t, fixture.inbound.Id, "running", 2)
			users, tags, err := (&InboundService{}).GetLocalMieruOnlineUsers()
			if err != nil || len(users) != 1 || users[0].Email != fixture.user.Email || !slices.Equal(tags, []string{fixture.inbound.Tag}) {
				t.Fatalf("live native observations lost identity or inbound: users=%+v tags=%v err=%v", users, tags, err)
			}
			if len(users[0].IPs) != 1 || users[0].IPs[0].IP != "127.0.0.1" || users[0].IPs[0].LastSeen < time.Now().Unix()-2 {
				t.Fatalf("native observation lost actual source: %+v", users[0])
			}
			if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", fixture.user.Email).Update("email", "renamed-presence-owner").Error; err != nil {
				t.Fatal(err)
			}
			users, tags, err = (&InboundService{}).GetLocalMieruOnlineUsers()
			if err != nil || len(users) != 0 || len(tags) != 0 {
				t.Fatalf("stale runtime identity was attributed after canonical rename: %+v %v %v", users, tags, err)
			}
			if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "renamed-presence-owner").Update("email", fixture.user.Email).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.StopXray(); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, flows)
			users, tags, err = (&InboundService{}).GetLocalMieruOnlineUsers()
			if err != nil || len(users) != 0 || len(tags) != 0 {
				t.Fatalf("stopped runtime remained online: %+v %v %v", users, tags, err)
			}
			productionMieruStatus(t, fixture.inbound.Id, "pending", 0)
		})
	}
}

func TestMieruStatusColdReadIsOwnerScopedAndStartsNoRuntime(t *testing.T) {
	setupConflictDB(t)
	stopManagedMieru()
	t.Cleanup(stopManagedMieru)
	rows := []*model.Inbound{
		{UserId: 7, Protocol: model.Mieru, Enable: true, Tag: "pending", Settings: `{"password":"private-status-secret","bridgePort":31401}`},
		{UserId: 7, Protocol: model.Mieru, Enable: true, Tag: "disabled"},
		{UserId: 7, Protocol: model.Mieru, Enable: true, Tag: "idle"},
		{UserId: 8, Protocol: model.Mieru, Enable: true, Tag: "other-owner"},
		{UserId: 7, Protocol: model.SSH, Enable: true, Tag: "other-protocol"},
	}
	for _, row := range rows {
		if err := database.GetDB().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.GetDB().Model(rows[1]).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	client := model.ClientRecord{Email: "private-status-owner", Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: client.Id, InboundId: rows[0].Id}).Error; err != nil {
		t.Fatal(err)
	}
	statuses, err := (&InboundService{}).GetMieruRuntimeStatuses(7)
	if err != nil || len(statuses) != 3 {
		t.Fatalf("owner-scoped status count=%d err=%v", len(statuses), err)
	}
	for i, want := range []string{"pending", "disabled", "idle"} {
		if statuses[i].InboundID != rows[i].Id || statuses[i].State != want || statuses[i].AuthenticatedSessions != 0 {
			t.Fatalf("cold status[%d]=%+v, want %s", i, statuses[i], want)
		}
	}
	encoded, err := json.Marshal(statuses)
	if err != nil || strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "bridgePort") {
		t.Fatalf("status exposed stored client or routing data: %s err=%v", encoded, err)
	}
	statuses, err = (&InboundService{}).GetMieruRuntimeStatuses(99)
	encoded, marshalErr := json.Marshal(statuses)
	if err != nil || marshalErr != nil || string(encoded) != "[]" {
		t.Fatalf("empty owner response=%s err=%v/%v", encoded, err, marshalErr)
	}
	users, tags, err := (&InboundService{}).GetLocalMieruOnlineUsers()
	if err != nil || len(users) != 0 || len(tags) != 0 {
		t.Fatalf("cold runtime invented online clients: %+v %v %v", users, tags, err)
	}
	mieruRuntimeState.Lock()
	started := mieruRuntimeState.manager != nil
	mieruRuntimeState.Unlock()
	if started {
		t.Fatal("read-only status or presence created a runtime")
	}
	if err := database.GetDB().Migrator().DropTable(&model.Inbound{}); err != nil {
		t.Fatal(err)
	}
	statuses, err = (&InboundService{}).GetMieruRuntimeStatuses(7)
	if err == nil || statuses != nil || !strings.Contains(err.Error(), "inbounds") {
		t.Fatalf("failed status query appeared successful: %+v %v", statuses, err)
	}
}

func productionMieruStatus(t *testing.T, id int, state string, sessions int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var latest []MieruRuntimeStatus
	for time.Now().Before(deadline) {
		var err error
		latest, err = (&InboundService{}).GetMieruRuntimeStatuses(0)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range latest {
			if status.InboundID == id && status.State == state && status.AuthenticatedSessions == sessions {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("native statuses=%+v, want inbound %d state=%s sessions=%d", latest, id, state, sessions)
}
