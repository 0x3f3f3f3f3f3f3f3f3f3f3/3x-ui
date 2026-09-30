package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func passwordUpdateSibling(t *testing.T) (model.ClientRecord, *model.Inbound, *model.Inbound) {
	t.Helper()
	setupPolicyLedgerDB(t)
	owner := passwordOwner(t, "update-review-owner")
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: owner.Email, Enable: true, Up: 123, Down: 456}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	password, _, err := svc.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24561, Settings: passwordOwnerSettings(t, model.HTTP,
		map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := mkInbound(t, 24562, model.VLESS, `{"decryption":"none","clients":[]}`)
	if _, err := (&ClientService{}).Attach(svc, owner.Id, []int{ordinary.Id}); err != nil {
		t.Fatal(err)
	}
	ordinary, err = svc.GetInbound(ordinary.Id)
	if err != nil {
		t.Fatal(err)
	}
	current, err := (&ClientService{}).GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	return *current, password, ordinary
}

func TestPasswordProxyOwnerSingleUpdateFilteredFailurePreservesSelectedCredentials(t *testing.T) {
	owner, _, selected := passwordUpdateSibling(t)
	clients, inbounds := &ClientService{}, &InboundService{}
	excluded := mkInbound(t, 24566, model.VLESS, `{"decryption":"none","clients":[]}`)
	if _, err := clients.Attach(inbounds, owner.Id, []int{excluded.Id}); err != nil {
		t.Fatal(err)
	}
	previous := panelruntime.GetManager()
	t.Cleanup(func() { panelruntime.SetManager(previous) })
	injected := errors.New("selected sibling committed but runtime apply failed")
	selectedApplied := make(chan struct{})
	var attempts atomic.Int32
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) {
		if attempts.Add(1) == 1 {
			close(selectedApplied)
			return true, injected
		}
		return true, nil
	}}))
	var reads atomic.Int32
	const callback = "test-password-update-order-excluded-mirror"
	if err := database.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
		if query.Statement.Table != "inbounds" {
			return
		}
		if _, insideWriter := query.Statement.ConnPool.(gorm.TxCommitter); insideWriter {
			return
		}
		where, ok := query.Statement.Clauses["WHERE"].Expression.(clause.Where)
		if !ok {
			return
		}
		matchesID := false
		for _, expression := range where.Exprs {
			switch condition := expression.(type) {
			case clause.Eq:
				matchesID = fmt.Sprint(condition.Value) == fmt.Sprint(excluded.Id)
			case clause.IN:
				matchesID = len(condition.Values) == 1 && fmt.Sprint(condition.Values[0]) == fmt.Sprint(excluded.Id)
			case clause.Expr:
				matchesID = condition.SQL == "id = ?" && len(condition.Vars) == 1 && fmt.Sprint(condition.Vars[0]) == fmt.Sprint(excluded.Id)
			}
		}
		if !matchesID {
			return
		}
		if reads.Add(1) != 2 {
			return
		}
		select {
		case <-selectedApplied:
		case <-time.After(5 * time.Second):
			query.AddError(errors.New("selected sibling did not reach managed apply"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
	updated := *owner.ToClient()
	updated.Email, updated.ID, updated.Password = "partial-filter-renamed", "960e87bf-a4eb-4b83-a9e6-d90dbe6358ab", "rotated-shared-password"
	if _, err := clients.Update(inbounds, owner.Id, updated, 0, selected.Id); !errors.Is(err, injected) {
		t.Fatalf("selected runtime failure not reported: %v", err)
	}
	if reads.Load() < 2 || attempts.Load() != 2 {
		t.Fatalf("excluded apply ordering did not execute: reads=%d attempts=%d", reads.Load(), attempts.Load())
	}
	current, err := clients.GetByID(owner.Id)
	if err != nil || current.UUID != updated.ID || current.Password != updated.Password {
		t.Fatalf("excluded old wire credentials overwrote committed selected credentials: %+v %v", current, err)
	}
	for _, resource := range []*model.Inbound{selected, excluded} {
		saved, err := inbounds.GetInbound(resource.Id)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := inbounds.GetClients(saved)
		wantID := updated.ID
		if resource.Id == excluded.Id {
			wantID = owner.UUID
		}
		if err != nil || len(entries) != 1 || entries[0].Email != updated.Email || entries[0].ID != wantID {
			t.Fatalf("resource filter changed saved wire credentials: %+v %v", entries, err)
		}
	}
}

func TestPasswordProxyOwnerSingleUpdateFilteredRenameRemainsUsable(t *testing.T) {
	for _, filterKind := range []string{"password", "no-match"} {
		t.Run(filterKind, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			updated := *owner.ToClient()
			updated.Email, updated.Password = "update-filter-renamed", "shared-new-password"
			filter := password.Id
			if filterKind == "no-match" {
				filter = 999999
			}
			clients, inbounds := &ClientService{}, &InboundService{}
			if _, err := clients.Update(inbounds, owner.Id, updated, 0, filter); err != nil {
				t.Fatal(err)
			}
			var saved model.Inbound
			if err := database.GetDB().First(&saved, ordinary.Id).Error; err != nil {
				t.Fatal(err)
			}
			entries, err := inbounds.GetClients(&saved)
			if err != nil || len(entries) != 1 || entries[0].Email != updated.Email || entries[0].ID != owner.UUID {
				t.Fatalf("filtered rename left unusable identity mirror or changed credentials: %+v %v", entries, err)
			}
			current, err := clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			next := *current.ToClient()
			next.Comment = "next ordinary update succeeds"
			if _, err := clients.Update(inbounds, owner.Id, next, 0); err != nil {
				t.Fatalf("successful filtered rename broke subsequent update: %v", err)
			}
		})
	}
}

func TestPasswordProxyOwnerSingleUpdateLateGraphBeforeWrites(t *testing.T) {
	for _, drift := range []string{"remote", "malformed-missing-link", "ambiguous-tunnel"} {
		t.Run(drift, func(t *testing.T) {
			owner, password, ordinary := passwordUpdateSibling(t)
			var changed atomic.Bool
			const callback = "test-password-update-late-owner-graph"
			if err := database.GetDB().Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "clients" || !strings.Contains(query.Statement.SQL.String(), "count(*)") || !strings.Contains(query.Statement.SQL.String(), "email =") || !changed.CompareAndSwap(false, true) {
					return
				}
				query.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					if drift == "malformed-missing-link" {
						settings := fmt.Sprintf(`{"accounts":[{"user":"alice","pass":17,"ownerClientId":"%s"}]}`, owner.StableID)
						if err := tx.Model(password).Update("settings", settings).Error; err != nil {
							return err
						}
						return tx.Where("client_id = ? AND inbound_id = ?", owner.Id, password.Id).Delete(&model.ClientInbound{}).Error
					}
					protocol, settings := model.VLESS, `{"decryption":"none","clients":[]}`
					if drift == "ambiguous-tunnel" {
						protocol, settings = model.Tunnel, `{"address":"127.0.0.1","port":80,"network":"tcp","clients":[]}`
					}
					inbound := model.Inbound{Protocol: protocol, Settings: settings, Port: 24563, Enable: true, Listen: "127.0.0.1"}
					if drift == "remote" {
						nodeID := 99
						inbound.NodeID = &nodeID
					}
					if err := tx.Create(&inbound).Error; err != nil {
						return err
					}
					if err := tx.Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
						return err
					}
					if drift == "ambiguous-tunnel" {
						other := model.ClientRecord{Email: "late-other-tunnel-owner"}
						if err := tx.Create(&other).Error; err != nil {
							return err
						}
						return tx.Create(&model.ClientInbound{ClientId: other.Id, InboundId: inbound.Id}).Error
					}
					return nil
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
			updated := *owner.ToClient()
			updated.Email, updated.Comment = "late-graph-rename", "must-not-save"
			_, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0, ordinary.Id)
			if !changed.Load() || err == nil {
				t.Fatalf("late graph drift escaped rejection: changed=%v err=%v", changed.Load(), err)
			}
			var saved model.Inbound
			if readErr := database.GetDB().First(&saved, ordinary.Id).Error; readErr != nil || saved.Settings != ordinary.Settings {
				t.Fatalf("late graph rejection followed committed ordinary mutation: %+v %v (update error %v)", saved, readErr, err)
			}
			current, readErr := (&ClientService{}).GetByID(owner.Id)
			if readErr != nil || !reflect.DeepEqual(current, &owner) {
				t.Fatalf("late graph rejection committed canonical mutation: %+v %v", current, readErr)
			}
		})
	}
}

func TestPasswordProxyOwnerSingleUpdateAmbiguousEmptyTunnel(t *testing.T) {
	for _, filterKind := range []string{"all", "password"} {
		t.Run(filterKind, func(t *testing.T) {
			owner, password, _ := passwordUpdateSibling(t)
			other := passwordOwner(t, "ambiguous-second-tunnel-owner")
			tunnel := mkInbound(t, 24564, model.Tunnel, `{"address":"127.0.0.1","port":80,"network":"tcp","clients":[]}`)
			for _, id := range []int{owner.Id, other.Id} {
				if err := database.GetDB().Create(&model.ClientInbound{ClientId: id, InboundId: tunnel.Id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			updated := *owner.ToClient()
			updated.Comment = "must-not-save"
			var filter []int
			if filterKind == "password" {
				filter = []int{password.Id}
			}
			if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0, filter...); err == nil {
				t.Fatal("ambiguous empty Tunnel was accepted")
			}
			current, err := (&ClientService{}).GetByID(owner.Id)
			if err != nil || !reflect.DeepEqual(current, &owner) {
				t.Fatalf("ambiguous Tunnel changed owner: %+v %v", current, err)
			}
		})
	}
}

func TestPasswordProxyOwnerSingleUpdatePreservesOmittedTunnelKeys(t *testing.T) {
	owner, _, _ := passwordUpdateSibling(t)
	owner.PreSharedKey, owner.KeepAlive = "stored-peer-psk", 25
	if err := database.GetDB().Model(&owner).Updates(map[string]any{"wg_pre_shared_key": owner.PreSharedKey, "wg_keep_alive": owner.KeepAlive}).Error; err != nil {
		t.Fatal(err)
	}
	peer := *owner.ToClient()
	peer.PrivateKey, peer.PublicKey, peer.AllowedIPs = "stored-private", "stored-public", []string{"10.0.0.2/32"}
	wireguard := mkInbound(t, 24565, model.WireGuard, clientsSettings(t, []model.Client{peer}))
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: wireguard.Id}).Error; err != nil {
		t.Fatal(err)
	}
	updated := *owner.ToClient()
	updated.PreSharedKey, updated.KeepAlive, updated.Comment = "", nil, "metadata only"
	if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0); err != nil {
		t.Fatal(err)
	}
	current, err := (&ClientService{}).GetByID(owner.Id)
	if err != nil || current.PreSharedKey != owner.PreSharedKey || current.KeepAlive != owner.KeepAlive {
		t.Fatalf("shared persistence erased omitted peer credentials: %+v %v", current, err)
	}
	var saved model.Inbound
	if err := database.GetDB().First(&saved, wireguard.Id).Error; err != nil {
		t.Fatal(err)
	}
	entries, err := (&InboundService{}).GetClients(&saved)
	if err != nil || len(entries) != 1 || entries[0].PreSharedKey != owner.PreSharedKey || entries[0].KeepAliveSeconds() != 25 {
		t.Fatalf("resource peer keys changed: %+v %v", entries, err)
	}
}

func TestPasswordProxyOwnerSingleUpdateRenamesAllHistory(t *testing.T) {
	for _, filtered := range []bool{false, true} {
		t.Run(map[bool]string{false: "all", true: "ordinary-filter"}[filtered], func(t *testing.T) {
			owner, _, ordinary := passwordUpdateSibling(t)
			global := model.ClientGlobalTraffic{MasterGuid: "history-master", Email: owner.Email, Up: 111, Down: 222}
			node := model.NodeClientTraffic{NodeId: 7, Email: owner.Email, Up: 333, Down: 444}
			for _, row := range []any{&global, &node} {
				if err := database.GetDB().Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			updated := *owner.ToClient()
			updated.Email = "update-review-renamed"
			var filter []int
			if filtered {
				filter = []int{ordinary.Id}
			}
			if _, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0, filter...); err != nil {
				t.Fatal(err)
			}
			var gotGlobal model.ClientGlobalTraffic
			var gotNode model.NodeClientTraffic
			if err := database.GetDB().First(&gotGlobal, global.Id).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.GetDB().First(&gotNode, node.Id).Error; err != nil {
				t.Fatal(err)
			}
			if gotGlobal.Email != updated.Email || gotGlobal.Up != 111 || gotGlobal.Down != 222 || gotNode.Email != updated.Email || gotNode.Up != 333 || gotNode.Down != 444 {
				t.Fatalf("shared rename stranded history: global=%+v node=%+v", gotGlobal, gotNode)
			}
		})
	}
}

func TestPasswordProxyOwnerSingleUpdateRejectsLateDestination(t *testing.T) {
	for _, field := range []string{"email", "sub_id"} {
		t.Run(field, func(t *testing.T) {
			owner, _, ordinary := passwordUpdateSibling(t)
			replacement := passwordOwner(t, "update-destination-replacement")
			if err := database.GetDB().Create(&xray.ClientTraffic{Email: replacement.Email, Enable: true, Up: 33, Down: 44}).Error; err != nil {
				t.Fatal(err)
			}
			updated := *owner.ToClient()
			updated.Email, updated.SubID, updated.Password, updated.Comment = "update-free-destination", "update-free-subscription", "must-not-save-password", "must-not-save"
			value := updated.Email
			if field == "sub_id" {
				value = updated.SubID
			}
			var changed atomic.Bool
			const callback = "test-password-update-destination-acquisition"
			callbacks := database.GetDB().Callback().Query()
			if field == "email" {
				callbacks = database.GetDB().Callback().Row()
			}
			if err := callbacks.After("gorm:query").After("gorm:row").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table != "clients" || field == "sub_id" && (!strings.Contains(query.Statement.SQL.String(), "count(*)") || !strings.Contains(query.Statement.SQL.String(), field+" =")) || field == "email" && !strings.Contains(query.Statement.SQL.String(), "LOWER(email)") {
					return
				}
				found := false
				for _, variable := range query.Statement.Vars {
					if variable == value {
						found = true
					}
				}
				if !found || !changed.CompareAndSwap(false, true) {
					return
				}
				query.AddError(database.GetDB().Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&model.ClientRecord{}).Where("id = ?", replacement.Id).Update(field, value).Error; err != nil {
						return err
					}
					if field == "email" {
						// Ordinary compatibility permits matching subscription IDs;
						// destination ownership must still remain the captured UUID.
						if err := tx.Model(&model.ClientRecord{}).Where("id = ?", replacement.Id).Update("sub_id", updated.SubID).Error; err != nil {
							return err
						}
						return tx.Model(&xray.ClientTraffic{}).Where("email = ?", replacement.Email).Update("email", value).Error
					}
					return nil
				}))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = callbacks.Remove(callback) })
			_, err := (&ClientService{}).Update(&InboundService{}, owner.Id, updated, 0)
			if !changed.Load() {
				t.Fatal("destination interleaving did not run")
			}
			var saved model.Inbound
			if readErr := database.GetDB().First(&saved, ordinary.Id).Error; readErr != nil || saved.Settings != ordinary.Settings {
				t.Fatalf("late destination collision committed ordinary credentials: %+v %v (update error %v)", saved, readErr, err)
			}
			current, readErr := (&ClientService{}).GetByID(replacement.Id)
			if readErr != nil || current.UUID != replacement.UUID || current.Password != replacement.Password || current.Comment != replacement.Comment {
				t.Fatalf("destination owner changed: %+v %v", current, readErr)
			}
			trafficEmail := replacement.Email
			if field == "email" {
				trafficEmail = value
			}
			if usage := trafficOf(t, trafficEmail); usage.Up != 33 || usage.Down != 44 || usage.Total != 0 {
				t.Fatalf("destination owner's history/quota changed: %+v", usage)
			}
			if current, readErr := (&ClientService{}).GetByID(owner.Id); readErr != nil || !reflect.DeepEqual(current, &owner) {
				t.Fatalf("rejected destination committed original owner: %+v %v", current, readErr)
			}
			if !errors.Is(err, ErrManagedConfigStale) {
				t.Fatalf("late destination was not fenced as stale: %v", err)
			}
		})
	}
}
