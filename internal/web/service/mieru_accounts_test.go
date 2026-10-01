package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestMieruPublicInboundCreateAndOmittedUpdatePreserveGeneratedCredentials(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	listener := &model.Inbound{Tag: "native-create", Protocol: model.Mieru, Port: 24238, Settings: `{"transport":"UDP","mtu":1400,"clients":[{"email":"owner","enable":true}]}`}
	created, _, err := svc.AddInbound(listener)
	if err != nil {
		t.Fatalf("native create wrongly requires another protocol credential: %v", err)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MieruUsername == "" || stored.MieruPassword == "" || stored.UUID != "" {
		t.Fatal("native inbound did not persist independent generated authentication")
	}
	username, password, identity := stored.MieruUsername, stored.MieruPassword, stored.StableID
	created.Settings = `{"transport":"TCP","clients":[{"email":"owner","enable":true,"comment":"omitted native pair"}]}`
	if _, _, err := svc.UpdateInbound(created); err != nil {
		t.Fatal(err)
	}
	stored, err = (&ClientService{}).GetRecordByEmail(nil, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MieruUsername != username || stored.MieruPassword != password || stored.StableID != identity {
		t.Fatal("omitted inbound update rotated authentication or owner")
	}
	entries, err := svc.GetClients(created)
	if err != nil || len(entries) != 1 {
		t.Fatalf("persisted native mirror: count=%d error=%v", len(entries), err)
	}
	requireMieruPair(t, entries[0], username, password)
}

func TestMieruPublicClientCreateAttachAndUpdateUseNativeCredentials(t *testing.T) {
	setupPolicyLedgerDB(t)
	clients, inbounds := &ClientService{}, &InboundService{}
	first := mkInbound(t, 24243, model.Mieru, `{"transport":"TCP","clients":[]}`)
	second := mkInbound(t, 24244, model.Mieru, `{"transport":"UDP","clients":[]}`)
	if err := database.GetDB().Model(&model.Inbound{}).Where("id IN ?", []int{first.Id, second.Id}).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "public-owner", Enable: true}
	if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: client, InboundIds: []int{first.Id}}); err != nil {
		t.Fatalf("public native client create: %v", err)
	}
	stored, err := clients.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	username, password, identity := stored.MieruUsername, stored.MieruPassword, stored.StableID
	if username == "" || password == "" || stored.UUID != "" {
		t.Fatal("native public create reused another protocol identity")
	}
	if _, err := clients.Attach(inbounds, stored.Id, []int{second.Id}); err != nil {
		t.Fatalf("public native attach: %v", err)
	}
	if _, err := clients.Update(inbounds, stored.Id, model.Client{Email: "public-renamed", Enable: true}, 0); err != nil {
		t.Fatalf("public native update: %v", err)
	}
	stored, err = clients.GetRecordByEmail(nil, "public-renamed")
	if err != nil || stored.StableID != identity || stored.MieruUsername != username || stored.MieruPassword != password {
		t.Fatal("public rename/attach rotated native authentication")
	}
}

func TestMieruPublicInboundRejectsUnsupportedOptionsBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, settings, stream string }{
		{"transport", `{"transport":"QUIC","clients":[]}`, `{}`},
		{"mtu", `{"transport":"UDP","mtu":900,"clients":[]}`, `{}`},
		{"tls", `{"transport":"TCP","clients":[]}`, `{"network":"tcp","security":"tls"}`},
		{"wrapper", `{"transport":"UDP","clients":[]}`, `{"network":"kcp"}`},
		{"unmapped-native-users", `{"transport":"TCP","users":[{"username":"wire","password":"wire-secret","email":"owner"}]}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			listener := &model.Inbound{Tag: "bad-native", Protocol: model.Mieru, Port: 24239, Settings: tc.settings, StreamSettings: tc.stream}
			if _, _, err := (&InboundService{}).AddInbound(listener); err == nil {
				t.Fatal("unsupported native option silently committed")
			}
			var count int64
			if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("invalid native listener persisted: count=%d error=%v", count, err)
			}
		})
	}
}

func TestMieruCredentialChangeChecksAllLinkedListenersBeforeWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	first := mkInbound(t, 24240, model.Mieru, `{"transport":"TCP","clients":[]}`)
	second := mkInbound(t, 24241, model.Mieru, `{"transport":"TCP","clients":[]}`)
	shared := model.Client{Email: "shared", Enable: true, MieruUsername: "original", MieruPassword: "shared-secret"}
	sibling := model.Client{Email: "sibling", Enable: false, MieruUsername: "reserved", MieruPassword: "sibling-secret"}
	if err := svc.SyncInbound(nil, first.Id, []model.Client{shared}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncInbound(nil, second.Id, []model.Client{shared, sibling}); err != nil {
		t.Fatal(err)
	}
	shared.MieruUsername = "reserved"
	if err := svc.ApplyInboundClientDelta(nil, first.Id, []model.Client{shared}, nil); err == nil {
		t.Fatal("credential rotation collided with another linked listener's disabled owner")
	}
	stored, err := svc.GetRecordByEmail(nil, shared.Email)
	if err != nil || stored.MieruUsername != "original" {
		t.Fatal("rejected shared rotation partially committed canonical authentication")
	}
}

func TestMieruConfigRejectsConcurrentCredentialRotation(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	svc := &ClientService{}
	client := model.Client{Email: "compile-owner", Enable: true, MieruUsername: "wire", MieruPassword: "before"}
	listener := mkInbound(t, 24242, model.Mieru, `{"transport":"TCP","clients":[]}`)
	if err := svc.SyncInbound(nil, listener.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	db, reads, rotated := database.GetDB(), 0, false
	const callback = "test:mieru-rotate-compilation"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "clients" || !strings.Contains(tx.Statement.SQL.String(), "flow_override AS flow_override") {
			return
		}
		reads++
		if reads != 2 {
			return
		}
		rotated = true
		if err := db.Model(&model.ClientRecord{}).Where("email = ?", client.Email).Update("mieru_password", "after").Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t)); !errors.Is(err, ErrManagedConfigStale) {
		t.Fatalf("stale native authentication compiled: rotated=%t error=%v", rotated, err)
	}
	if !rotated {
		t.Fatal("fixture did not rotate after runtime credential read")
	}
	if _, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t)); err != nil {
		t.Fatal(err)
	}
}

func mieruRequestClient(t *testing.T, raw string) model.Client {
	t.Helper()
	var client model.Client
	if err := json.Unmarshal([]byte(raw), &client); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestMieruEmptyLocalListenerRequestsManagedActivation(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	mkInbound(t, 24235, model.Mieru, `{"transport":"TCP","clients":[]}`)
	requested, err := (&XrayService{}).managedPolicyRequested()
	if err != nil || !requested {
		t.Fatalf("empty native listener did not request capability-negotiated activation: %t %v", requested, err)
	}
}

func TestMieruLocalAttachmentRejectsUncoordinatedRemoteIdentity(t *testing.T) {
	setupPolicyLedgerDB(t)
	remote := mkInbound(t, 24236, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := database.GetDB().Model(remote).Update("node_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "shared-owner", ID: "936997e1-3b0c-4de9-9eea-047ee5829d3e", Enable: true, MieruUsername: "wire", MieruPassword: "wire-password"}
	clients := &ClientService{}
	if err := clients.SyncInbound(nil, remote.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	local := mkInbound(t, 24237, model.Mieru, `{"transport":"TCP","clients":[]}`)
	if err := clients.SyncInbound(nil, local.Id, []model.Client{client}); err == nil {
		t.Fatal("native mieru acquired full local budget for a remote shared identity")
	}
	bound, err := clients.ListForInbound(nil, local.Id)
	if err != nil || len(bound) != 0 {
		t.Fatalf("rejected remote scope committed local membership: count=%d error=%v", len(bound), err)
	}
}

func TestMieruPortOwnershipFollowsNativePhysicalTransport(t *testing.T) {
	for _, tc := range []struct {
		settings, stream string
		want             transportBits
	}{
		{`{"transport":"TCP"}`, `{"network":"kcp"}`, transportTCP},
		{`{"transport":"UDP"}`, `{}`, transportUDP},
		{`{}`, `{}`, transportTCP},
	} {
		if got := inboundTransports(model.Mieru, tc.stream, tc.settings); got != tc.want {
			t.Fatalf("native physical socket classification: got=%d want=%d", got, tc.want)
		}
	}
}

func requireMieruPair(t *testing.T, value any, username, password string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["mieruUsername"] != username || fields["mieruPassword"] != password {
		t.Fatal("API/SQL projection lost the requested mieru credential pair")
	}
}

func TestMieruStandaloneCRUDAndPortableExportPreserveCredentials(t *testing.T) {
	setupPolicyLedgerDB(t)
	clients, inbounds := &ClientService{}, &InboundService{}
	client := mieruRequestClient(t, `{"email":"mieru-owner","enable":true,"password":"other-protocol","mieruUsername":"wire-user","mieruPassword":"wire-secret"}`)
	if restart, err := clients.Create(inbounds, &ClientCreatePayload{Client: client}); err != nil || restart {
		t.Fatalf("standalone create: restart=%t error=%v", restart, err)
	}
	stored, err := clients.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	requireMieruPair(t, stored, "wire-user", "wire-secret")
	stableID := stored.StableID
	update := mieruRequestClient(t, `{"email":"renamed-label","enable":true,"comment":"no credential rotation"}`)
	if _, err := clients.Update(inbounds, stored.Id, update, 0); err != nil {
		t.Fatal(err)
	}
	stored, err = clients.GetRecordByEmail(nil, update.Email)
	if err != nil {
		t.Fatal(err)
	}
	requireMieruPair(t, stored, "wire-user", "wire-secret")
	if stored.StableID != stableID || stored.Password != "other-protocol" {
		t.Fatal("label rename changed stable identity or other credential")
	}
	update = mieruRequestClient(t, `{"email":"renamed-label","enable":true,"mieruUsername":"new-wire-user","mieruPassword":"new-wire-secret"}`)
	if _, err := clients.Update(inbounds, stored.Id, update, 0); err != nil {
		t.Fatal(err)
	}
	exported, err := clients.ExportAll()
	if err != nil || len(exported) != 1 {
		t.Fatalf("portable export: count=%d error=%v", len(exported), err)
	}
	requireMieruPair(t, exported[0].Client, "new-wire-user", "new-wire-secret")
}

func TestMieruDefaultsAreIndependentAndGeneratedOnce(t *testing.T) {
	client := model.Client{Email: "display-label", Password: "other-protocol"}
	listener := &model.Inbound{Protocol: model.Mieru}
	clients := &ClientService{}
	if err := clients.fillProtocolDefaults(&client, listener); err != nil {
		t.Fatal(err)
	}
	if client.MieruUsername == "" || client.MieruPassword == "" || client.MieruUsername == client.Email || client.MieruPassword == client.Password {
		t.Fatal("mieru defaults missing or reused labels/unrelated credentials")
	}
	username, password := client.MieruUsername, client.MieruPassword
	if err := clients.fillProtocolDefaults(&client, listener); err != nil {
		t.Fatal(err)
	}
	if client.MieruUsername != username || client.MieruPassword != password || client.Password != "other-protocol" {
		t.Fatal("second attachment rotated a shared mieru credential")
	}
}

func TestMieruCredentialByteLimitsRejectBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, username, password string }{
		{"long-ascii-name", strings.Repeat("u", 65), "valid"},
		{"long-unicode-name", strings.Repeat("界", 22), "valid"},
		{"long-unicode-password", "valid", strings.Repeat("界", 22)},
		{"invalid-utf8-name", string([]byte{0xff}), "valid"},
		{"invalid-utf8-password", "valid", string([]byte{0xff})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := model.Client{Email: "owner", MieruUsername: tc.username, MieruPassword: tc.password}
			if err := validateClientSettings(client); err == nil {
				t.Fatal("invalid mieru credential accepted before SQL write")
			}
		})
	}
}

func TestMieruListenerDuplicateUsernameIncludesDisabledSibling(t *testing.T) {
	setupPolicyLedgerDB(t)
	listener := mkInbound(t, 24233, model.Mieru, `{"transport":"TCP","clients":[]}`)
	clients := &ClientService{}
	first := model.Client{Email: "first", Enable: false, MieruUsername: "same-user", MieruPassword: "first-secret"}
	if err := clients.SyncInbound(nil, listener.Id, []model.Client{first}); err != nil {
		t.Fatal(err)
	}
	second := model.Client{Email: "second", Enable: true, MieruUsername: "same-user", MieruPassword: "second-secret"}
	if err := clients.ApplyInboundClientDelta(nil, listener.Id, []model.Client{second}, nil); err == nil {
		t.Fatal("delta added username already reserved by a disabled sibling")
	}
	bound, err := clients.ListForInbound(nil, listener.Id)
	if err != nil || len(bound) != 1 || bound[0].Email != first.Email {
		t.Fatalf("rejected duplicate committed membership: count=%d error=%v", len(bound), err)
	}
}

func TestMieruManagedConfigUsesStoredIdentityAndIndependentAuthentication(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	clients := &ClientService{}
	client := mieruRequestClient(t, `{"email":"panel-label","enable":true,"password":"other-protocol","mieruUsername":"wire-user","mieruPassword":"wire-secret","policy":{"multiplier":"1.5"}}`)
	listener := mkInbound(t, 24231, model.Protocol("mieru"), `{"transport":"UDP","mtu":1400,"clients":[],"users":[{"username":"forged","password":"forged","email":"forged","clientId":"forged"}]}`)
	tunnel := mkInbound(t, 24232, model.Tunnel, `{"network":"tcp,udp","address":"127.0.0.1","port":9,"clients":[]}`)
	for _, inbound := range []*model.Inbound{listener, tunnel} {
		if err := clients.SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := clients.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t))
	if err != nil {
		t.Fatalf("native mieru panel config unavailable: %v", err)
	}
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag != listener.Tag {
			continue
		}
		var settings struct {
			Transport string          `json:"transport"`
			MTU       int             `json:"mtu"`
			Clients   json.RawMessage `json:"clients"`
			Users     []struct {
				Username, Password, Email string
				ClientID                  string `json:"clientId"`
			} `json:"users"`
		}
		if err := json.Unmarshal(inbound.Settings, &settings); err != nil {
			t.Fatal(err)
		}
		if len(settings.Clients) != 0 || len(settings.Users) != 1 {
			t.Fatal("native runtime retained panel clients or untrusted saved users")
		}
		user := settings.Users[0]
		if user.Username != "wire-user" || user.Password != "wire-secret" || user.Email != stored.Email || user.ClientID != stored.StableID || settings.Transport != "UDP" || settings.MTU != 1400 {
			t.Fatal("native authentication/transport was not bound to current SQL owner")
		}
		return
	}
	t.Fatal("native mieru listener absent from generated config")
}
