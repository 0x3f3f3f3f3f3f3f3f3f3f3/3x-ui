package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Relative public expiry must propagate its actual first-use receipt to the
// canonical record/stat and ordinary mirrors without parsing password accounts
// as a clients mirror or manufacturing an optional empty Tunnel mirror.
func TestPasswordProxyOwnerFieldsRelativeExpiryFirstUse(t *testing.T) {
	for _, protocol := range []model.Protocol{model.HTTP, model.Mixed} {
		for _, scope := range []string{"password-only", "ordinary", "empty-tunnel", "ambiguous-late-tunnel"} {
			t.Run(string(protocol)+"/"+scope, func(t *testing.T) {
				owner, password, ordinary := passwordUpdateSibling(t)
				if protocol == model.Mixed {
					password.Protocol = protocol
					password.Settings = passwordOwnerSettings(t, protocol, map[string]any{"user": "alice", "pass": "resource-secret", "ownerClientId": owner.StableID})
					if err := preparePasswordProxyOwnerCommand(password); err != nil {
						t.Fatal(err)
					}
					if err := database.GetDB().Model(password).Updates(map[string]any{"protocol": protocol, "settings": password.Settings}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if scope == "password-only" {
					if err := database.GetDB().Where("client_id = ? AND inbound_id = ?", owner.Id, ordinary.Id).Delete(&model.ClientInbound{}).Error; err != nil {
						t.Fatal(err)
					}
				}
				var tunnel *model.Inbound
				if scope == "empty-tunnel" || scope == "ambiguous-late-tunnel" {
					tunnel = mkInbound(t, 24578, model.Tunnel, `{"address":"127.0.0.1","port":80,"network":"tcp"}`)
					if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: tunnel.Id}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if _, err := (&ClientService{}).ResetClientExpiryTimeByEmail(&InboundService{}, owner.Email, -86400000); err != nil {
					t.Fatal(err)
				}
				const source = "fields-relative-first-use"
				if err := BindClientPolicySource("local", source, 1); err != nil {
					t.Fatal(err)
				}
				seed, err := PrepareClientPolicyLedger(source, owner.StableID)
				if err != nil {
					t.Fatal(err)
				}
				policies, err := PrepareClientPolicies([]string{owner.StableID})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "state.db")
				if err := clientpolicy.CreateStore(path, source); err != nil {
					t.Fatal(err)
				}
				engine, err := clientpolicy.OpenPersistentEngine(path, source)
				if err != nil {
					t.Fatal(err)
				}
				defer engine.Close()
				if err := engine.Initialize(policies[0], clientpolicy.Usage{RawUpload: seed.RawUpload, RawDownload: seed.RawDownload, BilledBytes: seed.BilledBytes}); err != nil {
					t.Fatal(err)
				}
				session, err := engine.Open(context.Background(), clientpolicy.Metadata{ClientID: owner.StableID}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				if err := session.Admit(clientpolicy.Upload, 3); err != nil {
					t.Fatal(err)
				}
				if err := engine.Checkpoint(); err != nil {
					t.Fatal(err)
				}
				events, err := engine.ReadLedger(0, 1000)
				if err != nil || len(events) != 1 || events[0].FirstUsedAt <= 0 {
					t.Fatalf("missing actual first-use receipt: %+v/%v", events, err)
				}
				event := events[0]
				page := &command.LedgerPage{NextSequence: event.Sequence, Records: []*command.LedgerRecord{{InstanceId: event.InstanceID, Epoch: event.Epoch, Sequence: event.Sequence, ClientId: event.ClientID, PolicyVersion: event.PolicyVersion, FirstUsedAt: event.FirstUsedAt, Usage: &command.Usage{RawUpload: event.Usage.RawUpload, RawDownload: event.Usage.RawDownload, BilledBytes: event.Usage.BilledBytes, Remainder: event.Usage.Remainder}}}}
				if scope == "ambiguous-late-tunnel" {
					other := passwordOwner(t, "first-use-late-other")
					if err := database.GetDB().Create(&model.ClientInbound{ClientId: other.Id, InboundId: tunnel.Id}).Error; err != nil {
						t.Fatal(err)
					}
					if err := SettleClientPolicyLedger(source, event.Epoch, 0, page); err == nil {
						t.Fatal("late ambiguous empty Tunnel was accepted during first use")
					}
					current, err := (&ClientService{}).GetByID(owner.Id)
					if err != nil || current.ExpiryTime != -86400000 || trafficOf(t, owner.Email).ExpiryTime != -86400000 {
						t.Fatalf("rejected first use committed expiry: %+v/%v", current, err)
					}
					if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 123 || total.RawDownload != 456 || total.BilledBytes != 579 {
						t.Fatalf("rejected first use committed usage: %+v", total)
					}
					return
				}
				for range 2 {
					if err := SettleClientPolicyLedger(source, event.Epoch, 0, page); err != nil {
						t.Fatalf("relative expiry first-use receipt settlement failed: %v", err)
					}
				}
				current, err := (&ClientService{}).GetByID(owner.Id)
				wantExpiry := event.FirstUsedAt + 86400000
				if err != nil || current.ExpiryTime != wantExpiry || !current.Enable || current.Password != owner.Password || current.StableID != owner.StableID {
					t.Fatalf("first use changed canonical restrictions/credentials: %+v/%v", current, err)
				}
				if traffic := trafficOf(t, owner.Email); traffic.ExpiryTime != wantExpiry || traffic.Up != 123 || traffic.Down != 456 || !traffic.Enable {
					t.Fatalf("first use lost expiry/history: %+v", traffic)
				}
				if total := policyLedgerTotal(t, owner.StableID); total.RawUpload != 126 || total.RawDownload != 456 || total.BilledBytes != 583 {
					t.Fatalf("first-use retry replayed billing: %+v", total)
				}
				if saved, err := (&InboundService{}).GetInbound(password.Id); err != nil || saved.Settings != password.Settings {
					t.Fatalf("first use changed password account array: %+v/%v", saved, err)
				}
				if scope != "password-only" {
					saved, err := (&InboundService{}).GetInbound(ordinary.Id)
					if err != nil {
						t.Fatal(err)
					}
					entries, err := ParseInboundSettingsClients(saved.Settings)
					if err != nil || len(entries) != 1 || entries[0].ExpiryTime != wantExpiry || entries[0].ID != owner.UUID {
						t.Fatalf("first use missed ordinary mirror: %+v/%v", entries, err)
					}
				}
				if tunnel != nil {
					if saved, err := (&InboundService{}).GetInbound(tunnel.Id); err != nil || saved.Settings != tunnel.Settings {
						t.Fatalf("first use manufactured Tunnel mirror: %+v/%v", saved, err)
					}
				}
			})
		}
	}
}
