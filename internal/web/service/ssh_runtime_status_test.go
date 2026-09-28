package service

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func productionSSHStatus(t *testing.T, inboundID int, state string, connections int, reason string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var latest []SSHRuntimeStatus
	for time.Now().Before(deadline) {
		statuses, err := (&InboundService{}).GetSSHRuntimeStatuses(0)
		if err != nil {
			t.Fatal(err)
		}
		latest = statuses
		for _, status := range statuses {
			if status.InboundID == inboundID && status.State == state && status.AuthenticatedConnections == connections && status.Reason == reason {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("SSH runtime statuses = %+v, want inbound %d state=%s connections=%d reason=%q", latest, inboundID, state, connections, reason)
}

func TestSSHRuntimeStatusColdReadIsOwnerScopedAndDoesNotStart(t *testing.T) {
	setupBulkDB(t)
	stopManagedSSH()
	t.Cleanup(stopManagedSSH)
	db := database.GetDB()
	address := productionSSHAddress(t)
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	pending := model.Inbound{UserId: 7, Protocol: model.SSH, Enable: true, Tag: "ssh-pending", Listen: "127.0.0.1", Port: port, Settings: `{"clients":[]}`}
	if err := normalizeSSHInbound(&pending, ""); err != nil {
		t.Fatal(err)
	}
	remoteID := 91
	rows := []*model.Inbound{
		&pending,
		{UserId: 7, Protocol: model.SSH, Enable: true, Tag: "ssh-disabled"},
		{UserId: 7, Protocol: model.SSH, Enable: true, Tag: "ssh-idle"},
		{UserId: 7, Protocol: model.SSH, Enable: true, Tag: "ssh-remote", NodeID: &remoteID},
		{UserId: 8, Protocol: model.SSH, Enable: true, Tag: "ssh-other-owner"},
		{UserId: 7, Protocol: model.VMESS, Enable: true, Tag: "native"},
	}
	for _, inbound := range rows {
		if err := db.Create(inbound).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(rows[1]).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	client := model.ClientRecord{Email: "status-private-client", Enable: true}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: pending.Id}).Error; err != nil {
		t.Fatal(err)
	}
	disabledClient := model.ClientRecord{Email: "status-disabled-client", Enable: true}
	if err := db.Create(&disabledClient).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&disabledClient).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: disabledClient.Id, InboundId: rows[2].Id}).Error; err != nil {
		t.Fatal(err)
	}
	statuses, err := (&InboundService{}).GetSSHRuntimeStatuses(7)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []string{"pending", "disabled", "idle", "unsupported"}
	if len(statuses) != len(wantStates) {
		t.Fatalf("owner's SSH status count = %d, want 4", len(statuses))
	}
	for index, status := range statuses {
		if status.InboundID != rows[index].Id || status.State != wantStates[index] || status.AuthenticatedConnections != 0 {
			t.Fatalf("SSH status[%d] = %+v, want inbound %d state %s and zero connections", index, status, rows[index].Id, wantStates[index])
		}
	}
	encoded, err := json.Marshal(statuses)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), client.Email) || strings.Contains(string(encoded), "bridgePort") {
		t.Fatal("runtime status exposed credential or internal routing details")
	}
	sshRuntimeState.Lock()
	started := sshRuntimeState.manager != nil
	sshRuntimeState.Unlock()
	if started {
		t.Fatal("reading runtime status created a manager")
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", address)
	if err != nil {
		t.Fatalf("status read bound the configured listener: %v", err)
	}
	_ = listener.Close()
	for _, owner := range []int{0, 99} {
		empty, err := (&InboundService{}).GetSSHRuntimeStatuses(owner)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err = json.Marshal(empty)
		if err != nil || string(encoded) != "[]" {
			t.Fatalf("empty owner %d response = %s, error=%v; want []", owner, encoded, err)
		}
	}
}

func TestSSHRuntimeStatusUnavailableDatabaseDoesNotReportEmptySuccess(t *testing.T) {
	setupBulkDB(t)
	if err := database.GetDB().Migrator().DropTable(&model.Inbound{}); err != nil {
		t.Fatal(err)
	}
	statuses, err := (&InboundService{}).GetSSHRuntimeStatuses(7)
	if err == nil || statuses != nil || !strings.Contains(err.Error(), "inbounds") {
		t.Fatalf("unavailable table status = %+v, error=%v; want failed query naming inbounds", statuses, err)
	}
}

func TestSSHRuntimeStatus_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	t.Run("cold-read", TestSSHRuntimeStatusColdReadIsOwnerScopedAndDoesNotStart)
	t.Run("database-error", TestSSHRuntimeStatusUnavailableDatabaseDoesNotReportEmptySuccess)
}
