package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyResetEntrypointsRejectReusedEmailWhileQueued(t *testing.T) {
	setupPolicyLedgerDB(t)
	db := database.GetDB()
	owner := model.ClientRecord{Email: "reused-reset", Enable: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Up: 111, Down: 222, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	resetTrafficWriterForTest(t)
	StartTrafficWriter()
	parked, release := make(chan struct{}), make(chan struct{})
	releaseWriter := sync.OnceFunc(func() { close(release) })
	defer releaseWriter()
	go func() { _ = submitTrafficWrite(func() error { close(parked); <-release; return nil }) }()
	<-parked
	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- runSerializedTx(func(tx *gorm.DB) error {
			if err := tx.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Update("email", "renamed-reset").Error; err != nil {
				return err
			}
			if err := tx.Model(&xray.ClientTraffic{}).Where("email = ?", owner.Email).Update("email", "renamed-reset").Error; err != nil {
				return err
			}
			if err := tx.Create(&model.ClientRecord{Email: owner.Email, Enable: true}).Error; err != nil {
				return err
			}
			return tx.Create(&xray.ClientTraffic{Email: owner.Email, Up: 77, Down: 88, Enable: true}).Error
		})
	}()
	waitTrafficWriterQueued(t)
	resetDone := make(chan error, 1)
	go func() {
		_, err := (&ClientService{}).ResetTrafficByEmailWithRequest(context.Background(), &InboundService{}, owner.Email, ClientTrafficResetRequest{ClientID: owner.StableID, RequestID: "old-owner-request"})
		resetDone <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(twQueue) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	queued := len(twQueue)
	releaseWriter()
	if queued != 2 {
		t.Fatalf("reset did not queue behind email replacement: %d", queued)
	}
	if err := <-replaceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-resetDone; err == nil || !strings.Contains(err.Error(), "client identity changed") {
		t.Fatalf("queued reset switched to the replacement identity: %v", err)
	}
	for _, want := range []struct {
		email    string
		up, down int64
	}{{"renamed-reset", 111, 222}, {owner.Email, 77, 88}} {
		var traffic xray.ClientTraffic
		if err := db.First(&traffic, "email = ?", want.email).Error; err != nil {
			t.Fatal(err)
		}
		if traffic.Up != want.up || traffic.Down != want.down {
			t.Fatalf("reset changed the wrong owner's usage: %+v", traffic)
		}
	}
}

func TestClientPolicyResetEntrypointsRecheckAfterQueuedBootstrap(t *testing.T) {
	for _, entrypoint := range []string{"client", "inbound-email", "inbound-client"} {
		t.Run(entrypoint, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			db := database.GetDB()
			owner := model.ClientRecord{Email: "queued-reset", Enable: false}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&owner).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&xray.ClientTraffic{Email: owner.Email, Up: 111, Down: 222, Enable: false}).Error; err != nil {
				t.Fatal(err)
			}
			inbound := mkInbound(t, 24192, model.Tunnel, `{}`)
			if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
				t.Fatal(err)
			}
			if err := BindClientPolicySource("local", "core-a", 1); err != nil {
				t.Fatal(err)
			}
			resetTrafficWriterForTest(t)
			StartTrafficWriter()
			parked, release := make(chan struct{}), make(chan struct{})
			releaseWriter := sync.OnceFunc(func() { close(release) })
			defer releaseWriter()
			go func() {
				_ = submitTrafficWrite(func() error { close(parked); <-release; return nil })
			}()
			<-parked
			prepareDone := make(chan error, 1)
			go func() { _, err := PrepareClientPolicyLedger("core-a", owner.StableID); prepareDone <- err }()
			waitTrafficWriterQueued(t)
			resetDone := make(chan error, 1)
			go func() {
				var err error
				switch entrypoint {
				case "client":
					_, err = (&ClientService{}).ResetTrafficByEmail(&InboundService{}, owner.Email)
				case "inbound-email":
					err = (&InboundService{}).ResetClientTrafficByEmail(owner.Email)
				case "inbound-client":
					_, err = (&InboundService{}).ResetClientTraffic(inbound.Id, owner.Email)
				}
				resetDone <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for len(twQueue) < 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			queued := len(twQueue)
			releaseWriter()
			if queued != 2 {
				t.Fatalf("reset did not queue behind bootstrap: %d", queued)
			}
			if err := <-prepareDone; err != nil {
				t.Fatal(err)
			}
			if err := <-resetDone; !errors.Is(err, ErrClientPolicyLegacyReset) {
				t.Fatalf("legacy reset ignored the newly captured seed: %v", err)
			}
			var traffic xray.ClientTraffic
			if err := db.First(&traffic, "email = ?", owner.Email).Error; err != nil {
				t.Fatal(err)
			}
			if traffic.Up != 111 || traffic.Down != 222 || traffic.Enable {
				t.Fatalf("reset erased a prepared seed: %+v", traffic)
			}
			if err := db.First(&owner, owner.Id).Error; err != nil || owner.Enable {
				t.Fatalf("reset enabled a prepared client: %+v %v", owner, err)
			}
		})
	}
}

func TestClientPolicyResetEntrypointsProtectStoppedAndPreparedClients(t *testing.T) {
	for _, preparedOnly := range []bool{false, true} {
		for _, entrypoint := range []string{"client", "inbound-email", "inbound-client", "bulk", "all", "inbound-all"} {
			t.Run(fmt.Sprintf("%s/prepared-only=%t", entrypoint, preparedOnly), func(t *testing.T) {
				var id string
				if preparedOnly {
					setupPolicyLedgerDB(t)
					if err := BindClientPolicySource("local", "core-a", 1); err != nil {
						t.Fatal(err)
					}
					id = policyLedgerClient(t, "reset-fixture", 111, 222)
				} else {
					id = resetLedgerFixture(t)
				}
				db := database.GetDB()
				if err := db.Model(&model.ClientRecord{}).Where("stable_id = ?", id).Update("enable", false).Error; err != nil {
					t.Fatal(err)
				}
				var owner model.ClientRecord
				if err := db.First(&owner, "stable_id = ?", id).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", owner.Email).Updates(map[string]any{"up": 111, "down": 222, "enable": false}).Error; err != nil {
					t.Fatal(err)
				}
				inbound := mkInbound(t, 24191, model.Tunnel, `{}`)
				if err := db.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
					t.Fatal(err)
				}
				before := policyLedgerTotal(t, id)
				previous := currentXrayProcess()
				xrayState.replace(nil)
				t.Cleanup(func() { xrayState.replace(previous) })
				var resetErr error
				switch entrypoint {
				case "client":
					_, resetErr = (&ClientService{}).ResetTrafficByEmail(&InboundService{}, owner.Email)
				case "inbound-email":
					resetErr = (&InboundService{}).ResetClientTrafficByEmail(owner.Email)
				case "inbound-client":
					_, resetErr = (&InboundService{}).ResetClientTraffic(inbound.Id, owner.Email)
				case "bulk":
					_, resetErr = (&ClientService{}).BulkResetTraffic(&InboundService{}, []string{owner.Email})
				case "all":
					_, resetErr = (&ClientService{}).ResetAllTraffics()
				case "inbound-all":
					resetErr = (&ClientService{}).ResetAllClientTraffics(&InboundService{}, inbound.Id)
				}
				if resetErr == nil || !strings.Contains(resetErr.Error(), "managed core is not ready for reset") {
					t.Fatalf("bound client fell back to a legacy reset: %v", resetErr)
				}
				var traffic xray.ClientTraffic
				if err := db.First(&traffic, "email = ?", owner.Email).Error; err != nil {
					t.Fatal(err)
				}
				if traffic.Up != 111 || traffic.Down != 222 || traffic.Enable {
					t.Fatalf("failed managed reset rewrote raw counters or restrictions: %+v", traffic)
				}
				if err := db.First(&owner, owner.Id).Error; err != nil {
					t.Fatal(err)
				}
				if owner.Enable || !reflect.DeepEqual(before, policyLedgerTotal(t, id)) {
					t.Fatal("failed managed reset changed the lifetime ledger or manual disable")
				}
				var requests int64
				if err := db.Model(&model.ClientPolicyReset{}).Where("client_id = ?", id).Count(&requests).Error; err != nil || requests != 0 {
					t.Fatalf("unavailable core recorded an uncheckpointed reset: %d, %v", requests, err)
				}
			})
		}
	}
}
