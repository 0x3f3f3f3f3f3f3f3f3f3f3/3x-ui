package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestAWGForwardOwnershipRechecksConcurrentPeer_Postgres(t *testing.T) {
	for _, scope := range []string{"same-inbound", "cross-inbound"} {
		t.Run(scope, func(t *testing.T) {
			managedUsagePostgresSchema(t)
			setupConflictDB(t)
			db := database.GetDB()
			inboundSvc, clientSvc := &InboundService{}, &ClientService{}
			owner, first := awgForwardOwnershipFixture(t, "pending-peer", 51992, 82, 34412)
			if _, _, err := inboundSvc.AddInbound(owner); err != nil {
				t.Fatal(err)
			}
			target := owner
			if scope == "cross-inbound" {
				target, _ = awgForwardOwnershipFixture(t, "waiting-inbound", 51993, 83, 34413)
				var empty amneziawg.InboundSettings
				if err := json.Unmarshal([]byte(target.Settings), &empty); err != nil {
					t.Fatal(err)
				}
				empty.Clients = nil
				raw, err := json.Marshal(empty)
				if err != nil {
					t.Fatal(err)
				}
				target.Settings = string(raw)
				if _, _, err := inboundSvc.AddInbound(target); err != nil {
					t.Fatal(err)
				}
			}
			before := target.Settings
			counts := map[string]int64{}
			for _, table := range []string{"inbounds", "clients", "client_traffics", "client_inbounds"} {
				var count int64
				if err := db.Table(table).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				counts[table] = count
			}
			holder := db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			t.Cleanup(func() { _ = holder.Rollback().Error })
			if err := lockListenerReservationsTx(holder); err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				t.Fatal(err)
			}
			var pending amneziawg.InboundSettings
			if err := json.Unmarshal([]byte(owner.Settings), &pending); err != nil {
				t.Fatal(err)
			}
			pending.Clients[0].ForwardedPorts = "34410"
			encoded, err := json.Marshal(pending)
			if err != nil {
				t.Fatal(err)
			}
			if err := holder.Model(owner).Update("settings", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}
			result, finished := make(chan error, 1), make(chan struct{})
			input := &model.Inbound{Id: target.Id, Settings: clientsSettings(t, []model.Client{{Email: "waiting-peer", Enable: true, ForwardedPorts: "34410"}})}
			go func() {
				defer close(finished)
				_, err := clientSvc.AddInboundClient(inboundSvc, input)
				result <- err
			}()
			t.Cleanup(func() {
				_ = holder.Rollback().Error
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("peer claim survived holder cleanup")
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				select {
				case err := <-result:
					t.Fatalf("peer claim did not wait for reservation transaction: %v", err)
				default:
				}
				var waiting int64
				if err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid)) AND wait_event = 'advisory'", holderPID).Scan(&waiting).Error; err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("did not observe actual peer reservation wait")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := holder.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), first.Email) || !strings.Contains(err.Error(), "34410") {
					t.Fatalf("concurrent committed peer claim was not rejected: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("peer mutation did not finish after owner commit")
			}
			if scope == "same-inbound" {
				before = string(encoded)
			}
			var saved model.Inbound
			if err := db.First(&saved, target.Id).Error; err != nil || saved.Settings != before {
				t.Fatalf("rejected peer claim changed saved settings: %v", err)
			}
			for table, before := range counts {
				var after int64
				if err := db.Table(table).Count(&after).Error; err != nil || after != before {
					t.Fatalf("rejected peer claim changed %s: before=%d after=%d err=%v", table, before, after, err)
				}
			}
		})
	}
}
