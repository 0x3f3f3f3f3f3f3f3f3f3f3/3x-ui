package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientPolicyLocalReservationRejectsRemoteMembership(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "remote-before-reservation"
		if prepared {
			name = "reservation-before-remote"
		}
		t.Run(name, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			record := model.ClientRecord{Email: "local-scope", UUID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", SubID: "scope-sub", Enable: true, TotalGB: 1000}
			if err := db.Create(&record).Error; err != nil {
				t.Fatal(err)
			}
			remote := mkInbound(t, 24081, model.VLESS, `{"decryption":"none","clients":[]}`)
			if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
				t.Fatal(err)
			}
			if prepared {
				if _, err := PrepareClientPolicies([]string{record.StableID}); err != nil {
					t.Fatal(err)
				}
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				return (&ClientService{}).SyncInbound(tx, remote.Id, []model.Client{*record.ToClient()})
			})
			if prepared {
				if err == nil {
					t.Error("local budget reservation acquired an uncoordinated remote binding")
				}
				var links int64
				if err := db.Model(&model.ClientInbound{}).Where("client_id = ?", record.Id).Count(&links).Error; err != nil {
					t.Fatal(err)
				}
				if links != 0 {
					t.Errorf("rejected remote attachment committed %d links", links)
				}
			} else {
				if err != nil {
					t.Fatalf("ordinary remote attachment: %v", err)
				}
				policies, err := PrepareClientPolicies([]string{record.StableID})
				if err == nil || len(policies) != 0 {
					t.Errorf("remote client received full local budget: %d policies, err=%v", len(policies), err)
				}
			}
			var current model.ClientRecord
			if err := db.First(&current, record.Id).Error; err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if prepared {
				want = 1
			}
			if current.DesiredPolicyVersion != want {
				t.Errorf("rejected operation changed version: got %d want %d", current.DesiredPolicyVersion, want)
			}
		})
	}
}

func TestClientPolicyLocalReservationSerializesAgainstRemoteLink(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("requires PostgreSQL row locking")
	}
	record := model.ClientRecord{Email: "racing-scope", UUID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", SubID: "racing-sub", Enable: true, TotalGB: 1000}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	remote := mkInbound(t, 24082, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := db.Model(remote).Update("node_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	loaded, release := make(chan struct{}), make(chan struct{})
	var seen, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	const callback = "test:pause-remote-client-read"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "clients" && strings.Contains(tx.Statement.SQL.String(), "email IN") {
			seen.Do(func() { close(loaded); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	attached, prepared := make(chan error, 1), make(chan error, 1)
	go func() {
		attached <- db.Transaction(func(tx *gorm.DB) error {
			return (&ClientService{}).SyncInbound(tx, remote.Id, []model.Client{*record.ToClient()})
		})
	}()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("remote operation did not read client")
	}
	go func() { _, err := PrepareClientPolicies([]string{record.StableID}); prepared <- err }()
	var prepareErr error
	finished := false
	select {
	case prepareErr = <-prepared:
		finished = true
	case <-time.After(150 * time.Millisecond):
	}
	unblock()
	var attachErr error
	select {
	case attachErr = <-attached:
	case <-time.After(5 * time.Second):
		t.Fatal("remote operation did not finish")
	}
	if !finished {
		select {
		case prepareErr = <-prepared:
		case <-time.After(5 * time.Second):
			t.Fatal("policy preparation did not finish")
		}
	}
	if attachErr == nil && prepareErr == nil {
		t.Error("concurrent remote attachment and full local allocation both committed")
	}
	if attachErr != nil && prepareErr != nil {
		t.Fatalf("neither competing operation could commit: attach=%v prepare=%v", attachErr, prepareErr)
	}
	var current model.ClientRecord
	if err := db.First(&current, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ?", record.Id).Count(&links).Error; err != nil {
		t.Fatal(err)
	}
	if current.DesiredPolicyVersion > 0 && links > 0 {
		t.Errorf("stored client has both local budget version %d and %d remote links", current.DesiredPolicyVersion, links)
	}
}
