package service

import (
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSnellPanelPostgresConcurrentMembershipUsesResourceLock(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	if db.Name() != "postgres" {
		t.Skip("requires isolated PostgreSQL for independent writer connections")
	}
	listener, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24823, Settings: `{"version":5,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	clients := &ClientService{}
	first := db.Begin()
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	defer first.Rollback()
	if err := clients.ApplyInboundClientDelta(first, listener.Id, []model.Client{{Email: "connection-one", Enable: true, SnellPSK: "first-native-psk"}}, nil); err != nil {
		t.Fatal(err)
	}
	second := db.Begin()
	if second.Error != nil {
		t.Fatal(second.Error)
	}
	defer second.Rollback()
	result := make(chan error, 1)
	go func() {
		result <- clients.ApplyInboundClientDelta(second, listener.Id, []model.Client{{Email: "connection-two", Enable: true, SnellPSK: "second-native-psk"}}, nil)
	}()
	// Let the independently connected writer read while the first transaction's
	// membership is still uncommitted. It must recheck after acquiring the row.
	select {
	case err := <-result:
		t.Fatalf("second writer escaped the locked resource before commit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrSnellOwnerConflict) {
			t.Fatalf("second writer did not reject committed owner: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resource membership writer did not resume after commit")
	}
	if err := second.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var owners int64
	if err := db.Model(&model.ClientInbound{}).Where("inbound_id = ?", listener.Id).Count(&owners).Error; err != nil || owners != 1 {
		t.Fatal("concurrent connections produced multiple native owners")
	}
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", "connection-two").Count(&owners).Error; err != nil || owners != 0 {
		t.Fatal("rejected writer persisted another canonical owner")
	}
}
