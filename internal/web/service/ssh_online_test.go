package service

import (
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func productionSSHOnline(t *testing.T, inbound *model.Inbound, emails ...string) {
	t.Helper()
	slices.Sort(emails)
	deadline := time.Now().Add(2 * time.Second)
	for {
		users, tags, err := (&InboundService{}).GetLocalSSHOnlineUsers()
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(users))
		for _, user := range users {
			got = append(got, user.Email)
			if len(user.IPs) != 1 || user.IPs[0].IP != "127.0.0.1" || user.IPs[0].LastSeen < time.Now().Unix()-2 || user.IPs[0].LastSeen > time.Now().Unix() {
				t.Fatalf("SSH transport source IP observation = %+v, want one fresh 127.0.0.1", user)
			}
		}
		slices.Sort(got)
		var wantTags []string
		if len(emails) > 0 {
			wantTags = []string{inbound.Tag}
		}
		if slices.Equal(got, emails) && slices.Equal(tags, wantTags) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("SSH online users/tags = %+v / %v, want %v / %v", users, tags, emails, wantTags)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSSHOnlineColdReadDoesNotCreateRuntime(t *testing.T) {
	setupBulkDB(t)
	stopManagedSSH()
	users, tags, err := (&InboundService{}).GetLocalSSHOnlineUsers()
	if err != nil || len(users) != 0 || len(tags) != 0 {
		t.Fatalf("cold SSH observations = %+v / %v / %v", users, tags, err)
	}
	sshRuntimeState.Lock()
	defer sshRuntimeState.Unlock()
	if sshRuntimeState.manager != nil {
		t.Fatal("an online read started the managed SSH runtime")
	}
}
