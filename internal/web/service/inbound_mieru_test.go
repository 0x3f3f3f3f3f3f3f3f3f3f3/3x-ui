package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruInboundOwnsCanonicalUsageAndCredentials(t *testing.T) {
	setupConflictDB(t)
	settings, err := json.Marshal(map[string]any{"network": "tcp", "clients": []model.Client{{ID: uuid.NewString(), Email: "mieru-managed", Password: "owned-mieru-password", Enable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Protocol: model.Protocol("mieru"), Listen: "127.0.0.1", Port: 31280, Settings: string(settings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	record := lookupClientRecord(t, "mieru-managed")
	if record.Password != "owned-mieru-password" || record.PolicyID == "" {
		t.Fatal("mieru creation lost the canonical password or stable policy identity")
	}
	account, err := database.NewClientUsageLedger(database.GetDB()).Read(t.Context(), record.PolicyID)
	if err != nil || account.Revision != 1 {
		t.Fatalf("mieru creation did not claim durable accounting ownership: account=%+v err=%v", account, err)
	}
	if _, err := (&ClientService{}).CreateOne(&InboundService{}, inbound.Id, model.Client{Email: "mieru-added", Password: "another-owned-password", Enable: true}); err != nil {
		t.Fatalf("native credential creation incorrectly requires a proxy UUID: %v", err)
	}
	if _, err := (&ClientService{}).CreateOne(&InboundService{}, inbound.Id, model.Client{Email: "missing-mieru-password", Enable: true}); err == nil {
		t.Fatal("mieru client creation accepted missing authentication")
	}
	var invalid int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "missing-mieru-password").Count(&invalid).Error; err != nil || invalid != 0 {
		t.Fatal("failed credential validation persisted a canonical client")
	}
	native := mkInbound(t, 31281, model.VLESS, clientsSettings(t, nil))
	if _, err := (&ClientService{}).Attach(&InboundService{}, record.Id, []int{native.Id}); err == nil {
		t.Fatal("an owned mieru account was attached to an unenforced data path")
	}
	var links int64
	if err := database.GetDB().Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", record.Id, native.Id).Count(&links).Error; err != nil || links != 0 {
		t.Fatal("rejected unmanaged attachment persisted a binding")
	}
}

func TestMieruInboundOwnsCanonicalUsageAndCredentials_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestMieruInboundOwnsCanonicalUsageAndCredentials(t)
}

func TestMieruInboundListenerSettingsFollowNativeTransports(t *testing.T) {
	for network, expected := range map[string]transportBits{"tcp": transportTCP, "udp": transportUDP, "both": transportTCP | transportUDP} {
		t.Run(network, func(t *testing.T) {
			setupConflictDB(t)
			settings, err := json.Marshal(map[string]any{"network": network, "bridgePort": 31290, "clients": []model.Client{}})
			if err != nil {
				t.Fatal(err)
			}
			inbound := &model.Inbound{Protocol: model.Protocol("mieru"), Listen: "127.0.0.1", Port: 31280, Settings: string(settings)}
			if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
				t.Fatal(err)
			}
			if actual := inboundTransports(inbound.Protocol, inbound.StreamSettings, inbound.Settings); actual != expected {
				t.Fatalf("native %s listener claims transport %d, want %d", network, actual, expected)
			}
		})
	}
}

func TestMieruInboundRejectsInvalidListenerSettings(t *testing.T) {
	for name, mutate := range map[string]func(*model.Inbound, map[string]any){
		"invalid transport":   func(_ *model.Inbound, s map[string]any) { s["network"] = "quic" },
		"invalid bridge port": func(_ *model.Inbound, s map[string]any) { s["bridgePort"] = -1 },
		"overlapping bridge":  func(i *model.Inbound, s map[string]any) { s["bridgePort"] = i.Port },
		"domain listener":     func(i *model.Inbound, _ map[string]any) { i.Listen = "localhost" },
		"zero listener port":  func(i *model.Inbound, _ map[string]any) { i.Port = 0 },
		"unknown setting":     func(_ *model.Inbound, s map[string]any) { s["unrecognized"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			setupConflictDB(t)
			settings := map[string]any{"network": "tcp", "bridgePort": 31290, "clients": []model.Client{}}
			inbound := &model.Inbound{Protocol: model.Protocol("mieru"), Listen: "127.0.0.1", Port: 31280}
			mutate(inbound, settings)
			encoded, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			inbound.Settings = string(encoded)
			if _, _, err := (&InboundService{}).AddInbound(inbound); err == nil {
				t.Fatal("invalid native listener configuration was accepted")
			}
			var stored int64
			if err := database.GetDB().Model(&model.Inbound{}).Count(&stored).Error; err != nil || stored != 0 {
				t.Fatal("invalid native listener configuration was persisted")
			}
		})
	}
}
