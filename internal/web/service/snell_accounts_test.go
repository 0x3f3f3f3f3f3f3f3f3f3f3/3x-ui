package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func nativeSnellPanelClient(t *testing.T, fields map[string]any) model.Client {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var client model.Client
	if err := json.Unmarshal(raw, &client); err != nil {
		t.Fatal(err)
	}
	return client
}

func nativeSnellPanelFields(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestSnellPanelCanonicalIndependentPSK(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			inbounds, clients := &InboundService{}, &ClientService{}
			payload := map[string]any{"version": version, "clients": []map[string]any{{"email": "native-owner", "enable": true, "snellPsk": "independent-native-snell-key", "password": "ordinary-secret", "sshUsername": "ssh-wire", "sshPassword": "ssh-secret", "mieruUsername": "mieru-wire", "mieruPassword": "mieru-secret"}}}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Protocol("snell"), Port: 24800 + version, Settings: string(raw)})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := clients.GetRecordByEmail(nil, "native-owner")
			if err != nil {
				t.Fatal(err)
			}
			fields := nativeSnellPanelFields(t, owner)
			if fields["snellPsk"] != "independent-native-snell-key" || owner.Password != "ordinary-secret" || owner.SSHPassword != "ssh-secret" || owner.MieruPassword != "mieru-secret" || owner.StableID == "" {
				t.Fatal("native Snell PSK lost independence or canonical SQL identity")
			}
			update := nativeSnellPanelClient(t, map[string]any{"email": owner.Email, "enable": true, "comment": "metadata-only"})
			if _, err := clients.Update(inbounds, owner.Id, update, 0, listener.Id); err != nil {
				t.Fatal(err)
			}
			owner, err = clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			if nativeSnellPanelFields(t, owner)["snellPsk"] != "independent-native-snell-key" {
				t.Fatal("omitted native PSK rotated during ordinary metadata update")
			}
		})
	}
}

func TestSnellPanelExclusiveOwnerAndLastDetach(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Protocol("snell"), Port: 24810, Settings: `{"version":5,"clients":[{"email":"exclusive-owner","subId":"snell-owner-sub","enable":true,"snellPsk":"exclusive-native-snell-key"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := clients.GetRecordByEmail(nil, "exclusive-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.AddInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: `{"clients":[{"email":"second-owner","enable":true,"snellPsk":"second-native-snell-key"}]}`}); err == nil {
		t.Fatal("second Snell owner was accepted on an occupied resource")
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", "second-owner").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected exclusive attach persisted another owner")
	}
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", listener.Id).Update("enable", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Detach(inbounds, owner.Id, []int{listener.Id}); err != nil {
		t.Fatal(err)
	}
	listener, err = inbounds.GetInbound(listener.Id)
	if err != nil {
		t.Fatal(err)
	}
	if listener.Enable {
		t.Fatal("empty Snell listener remained enabled after final detach")
	}
	if err := database.GetDB().Model(&model.ClientInbound{}).Where("inbound_id = ?", listener.Id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("detached Snell owner retained exclusive membership")
	}
}

func TestSnellPanelManagedConfigUsesSQLIdentityAndIndependentPSK(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	clients := &ClientService{}
	listener := mkInbound(t, 24811, model.Protocol("snell"), `{"version":5,"quic":true,"psk":"forged","clientId":"forged","email":"forged","clients":[]}`)
	owner := nativeSnellPanelClient(t, map[string]any{"email": "sql-snell-owner", "enable": false, "snellPsk": "canonical-native-psk", "password": "ordinary-password"})
	if err := clients.SyncInbound(nil, listener.Id, []model.Client{owner}); err != nil {
		t.Fatal(err)
	}
	stored, err := clients.GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag != listener.Tag {
			continue
		}
		fields := nativeSnellPanelFields(t, json.RawMessage(inbound.Settings))
		if fields["psk"] != "canonical-native-psk" || fields["clientId"] != stored.StableID || fields["email"] != stored.Email || fields["quic"] != true || fields["version"] != float64(5) {
			t.Fatal("native configuration lost SQL-bound disabled owner or options")
		}
		if _, ok := fields["clients"]; ok {
			t.Fatal("native runtime retained panel clients")
		}
		if _, ok := fields["users"]; ok {
			t.Fatal("native runtime retained unsupported user array")
		}
		return
	}
	t.Fatal("native Snell listener absent from managed configuration")
}

func TestSnellPanelRejectsUnsupportedOptionsBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, settings, stream string }{
		{"version", `{"version":3,"clients":[]}`, `{}`},
		{"missing-version", `{"clients":[]}`, `{}`},
		{"v6-obfs", `{"version":6,"obfs":"http","clients":[]}`, `{}`},
		{"v6-quic", `{"version":6,"quic":true,"clients":[]}`, `{}`},
		{"unsafe-v6", `{"version":6,"mode":"unsafe-raw","clients":[]}`, `{}`},
		{"v4-mode", `{"version":4,"mode":"default","clients":[]}`, `{}`},
		{"raw-id", `{"version":5,"clientId":"forged","clients":[]}`, `{}`},
		{"raw-psk", `{"version":5,"psk":"forged","clients":[]}`, `{}`},
		{"raw-client-id", `{"version":5,"clients":[{"email":"owner","clientId":"forged"}]}`, `{}`},
		{"two-owners", `{"version":5,"clients":[{"email":"one"},{"email":"two"}]}`, `{}`},
		{"v6-short-key", `{"version":6,"clients":[{"email":"owner","snellPsk":"short"}]}`, `{}`},
		{"tls", `{"version":5,"clients":[]}`, `{"network":"tcp","security":"tls"}`},
		{"wrapper", `{"version":5,"clients":[]}`, `{"network":"kcp"}`},
		{"raw-settings", `{"version":5,"users":[],"clients":[]}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			if _, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.Protocol("snell"), Port: 24812, Settings: tc.settings, StreamSettings: tc.stream}); err == nil {
				t.Fatal("unsupported Snell settings committed")
			}
			for _, table := range []any{&model.Inbound{}, &model.ClientRecord{}, &model.ClientInbound{}} {
				var count int64
				if err := database.GetDB().Model(table).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("invalid native request wrote SQL rows: count=%d error=%v", count, err)
				}
			}
		})
	}
}

func TestSnellPanelVersionFiveReservesUDP(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			listener := mkInbound(t, 24813, model.Protocol("snell"), fmt.Sprintf(`{"version":%d,"quic":false,"clients":[]}`, version))
			udp := &model.Inbound{Protocol: model.Tunnel, Port: listener.Port, Enable: true, Settings: `{"allowedNetwork":"udp","clients":[]}`}
			conflict, err := checkPortConflictTx(database.GetDB(), udp, 0)
			if err != nil {
				t.Fatal(err)
			}
			if (conflict != nil) != (version == 5) {
				t.Fatal("native Snell listener reserved incorrect physical ports")
			}
		})
	}
}

func TestSnellPanelGeneratedPSKOwnerCommandAndDisabledReservation(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	owner := passwordOwner(t, "selected-snell-owner")
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24814, OwnerClientID: &owner.StableID, Settings: `{"version":6,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := clients.GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	original := stored.SnellPSK
	if len(original) < 32 || stored.StableID != owner.StableID {
		t.Fatal("existing-owner command lost identity or native PSK generation")
	}
	if _, _, err := clients.SetClientEnableByEmail(inbounds, owner.Email, false); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.AddInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: `{"clients":[{"email":"occupied-other","enable":true}]}`}); err == nil {
		t.Fatal("disabled owner lost exclusive reservation")
	}
	current, err := inbounds.GetInbound(listener.Id)
	if err != nil {
		t.Fatal(err)
	}
	current.OwnerClientID = nil
	current.Settings = `{"version":6,"clients":[{"email":"selected-snell-owner","enable":false,"comment":"metadata-only"}]}`
	if _, _, err := inbounds.UpdateInbound(current); err != nil {
		t.Fatal(err)
	}
	stored, err = clients.GetByID(owner.Id)
	if err != nil || stored.SnellPSK != original {
		t.Fatal("omitted listener update rotated native PSK")
	}
	if _, err := clients.Detach(inbounds, owner.Id, []int{listener.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.AddInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: `{"clients":[{"email":"new-owner","enable":true}]}`}); err != nil {
		t.Fatal(err)
	}
	next, err := clients.GetRecordByEmail(nil, "new-owner")
	if err != nil || next.StableID == owner.StableID || next.SnellPSK == original || next.SnellPSK == "" {
		t.Fatal("new owner inherited previous canonical identity or PSK")
	}
}

func TestSnellPanelSharedVersionSixChecksAllCredentialUpdatePaths(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24815, Settings: `{"version":6,"clients":[{"email":"shared-native-owner","enable":true,"snellPsk":"native-original-psk"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := clients.GetRecordByEmail(nil, "shared-native-owner")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := mkInbound(t, 24816, model.VLESS, `{"clients":[]}`)
	if _, err := clients.Attach(inbounds, owner.Id, []int{ordinary.Id}); err != nil {
		t.Fatal(err)
	}
	http, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24817, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "ordinary-user", "pass": "ordinary-secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range []int{listener.Id, ordinary.Id, http.Id} {
		if _, err := clients.Update(inbounds, owner.Id, model.Client{Email: owner.Email, Enable: true, SnellPSK: "short"}, 0, filter); err == nil {
			t.Fatal("short PSK committed through a linked protocol")
		}
		got, err := clients.GetByID(owner.Id)
		if err != nil || got.SnellPSK != "native-original-psk" {
			t.Fatal("rejected credential change partially committed")
		}
	}
	if _, err := clients.Update(inbounds, owner.Id, model.Client{Email: owner.Email, Enable: true, SnellPSK: "native-rotated-psk"}, 0, http.Id); err != nil {
		t.Fatal(err)
	}
	got, err := clients.GetByID(owner.Id)
	if err != nil || got.SnellPSK != "native-rotated-psk" || got.StableID != owner.StableID {
		t.Fatal("HTTP owner update ignored native PSK or identity")
	}
	listener, err = inbounds.GetInbound(listener.Id)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := inbounds.GetClients(listener)
	if err != nil || len(mirror) != 1 || mirror[0].SnellPSK != got.SnellPSK {
		t.Fatal("excluded Snell mirror retained stale authentication")
	}
}

func TestSnellPanelClientMutationRejectsRuntimeIdentityAndRemoteScope(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24818, Settings: `{"version":5,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clientId", "client_id"} {
		raw, err := json.Marshal(map[string]any{"clients": []map[string]any{{"email": "forged-owner", "enable": true, name: "forged-runtime-id"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clients.AddInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: string(raw)}); err == nil {
			t.Fatal("forged runtime owner was accepted")
		}
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected runtime identity persisted canonical rows")
	}
	node := &model.Node{Name: "remote-snell-test", Address: "https://127.0.0.1:1"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, NodeID: &node.Id, Port: 24819, Settings: `{"version":5,"clients":[]}`}); err == nil {
		t.Fatal("remote Snell resource accepted without coordinated ownership")
	}
}

func TestSnellPanelInvalidSocketOptionsRejectBeforeWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	if _, _, err := (&InboundService{}).AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24820, Settings: `{"version":5,"clients":[{"email":"socket-owner","enable":true}]}`, StreamSettings: `{"network":"tcp","security":"none","sockopt":{"mark":"invalid-type"}}`}); err == nil {
		t.Fatal("invalid native socket options accepted")
	}
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid socket options persisted the listener")
	}
	if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid socket options persisted an owner")
	}
}

func TestSnellPanelBulkCreateRejectsMultipleOwnersBeforeWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24821, Settings: `{"version":5,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := clients.BulkCreate(inbounds, []ClientCreatePayload{
		{Client: model.Client{Email: "bulk-one", Enable: true}, InboundIds: []int{listener.Id}},
		{Client: model.Client{Email: "bulk-two", Enable: true}, InboundIds: []int{listener.Id}},
	})
	if result.Created != 0 || err == nil && len(result.Skipped) != 2 {
		t.Fatal("bulk create accepted multiple Snell owners")
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected bulk create persisted native owners")
	}
	if err := database.GetDB().Model(&model.ClientInbound{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected bulk create persisted native memberships")
	}
}

func TestSnellPanelBulkAttachReadsCurrentPSKInsideWriter(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	owner := passwordOwner(t, "bulk-attach-native-owner")
	db := database.GetDB()
	if err := db.Model(owner).Update("snell_psk", "before-native-psk").Error; err != nil {
		t.Fatal(err)
	}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24822, Settings: `{"version":6,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	rotated := false
	const callback = "test:snell-bulk-attach-rotate-before-writer"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "clients" {
			return
		}
		rotated = true
		if err := db.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Update("snell_psk", "latest-native-psk").Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	result, _, err := clients.BulkAttach(inbounds, []string{owner.Email}, []int{listener.Id})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("native bulk attachment failed: %+v %v", result, err)
	}
	got, err := clients.GetByID(owner.Id)
	if err != nil || !rotated || got.SnellPSK != "latest-native-psk" {
		t.Fatal("membership command replayed stale native authentication")
	}
}

func TestSnellPanelFilteredOrdinaryUpdateRefusesReusedCanonicalLabel(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.VLESS, Port: 24824, Settings: `{"decryption":"none","clients":[{"email":"selected-native-owner","subId":"canonical-native-race-sub","id":"00000000-0000-4000-8000-000000000011","enable":true,"snellPsk":"original-native-psk"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.Snell, Port: 24825, Settings: `{"version":5,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := clients.GetRecordByEmail(nil, "selected-native-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Attach(inbounds, selected.Id, []int{other.Id}); err != nil {
		t.Fatal(err)
	}
	var acquired *model.ClientRecord
	reads := 0
	changed := false
	db := database.GetDB()
	const callback = "test:snell-reuse-after-ordinary-inbound-read"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if changed || tx.Statement.Table != "inbounds" {
			return
		}
		captured, ok := tx.Statement.Dest.(*model.Inbound)
		if !ok || captured.Id != listener.Id {
			return
		}
		reads++
		if reads != 2 {
			return
		}
		changed = true
		if _, err := clients.Update(inbounds, selected.Id, model.Client{Email: "selected-native-renamed", Enable: true}, 0, other.Id); err != nil {
			tx.AddError(err)
			return
		}
		if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: model.Client{Email: "selected-native-owner", Enable: true, SnellPSK: "unrelated-native-psk"}}); err != nil {
			tx.AddError(err)
			return
		}
		var err error
		acquired, err = clients.GetRecordByEmail(nil, "selected-native-owner")
		if err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, err = clients.Update(inbounds, selected.Id, model.Client{Email: "selected-native-owner", Enable: true, SnellPSK: "requested-native-psk"}, 0, listener.Id)
	if !changed || acquired == nil {
		t.Fatalf("public interleaving hook did not run: reads=%d changed=%t error=%v", reads, changed, err)
	}
	got, readErr := clients.GetByID(acquired.Id)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var links int64
	if readErr := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", acquired.Id, listener.Id).Count(&links).Error; readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil || got.SnellPSK != "unrelated-native-psk" || links != 0 {
		t.Fatal("ordinary update changed the new holder of a reused canonical label")
	}
}

func TestSnellPanelStandalonePortableRoundTripPreservesIndependentPSK(t *testing.T) {
	setupPolicyLedgerDB(t)
	clients, inbounds := &ClientService{}, &InboundService{}
	owner := model.Client{Email: "portable-native-owner", Enable: true, SnellPSK: "portable-native-psk", Password: "ordinary-kept", SSHPassword: "ssh-kept", MieruPassword: "mieru-kept"}
	if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: owner}); err != nil {
		t.Fatal(err)
	}
	stored, err := clients.GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Update(inbounds, stored.Id, model.Client{Email: "portable-native-renamed", Enable: true}, 0); err != nil {
		t.Fatal(err)
	}
	exported, err := clients.ExportAll()
	if err != nil || len(exported) != 1 {
		t.Fatal("portable export did not retain standalone owner")
	}
	client := exported[0].Client
	if client.SnellPSK != owner.SnellPSK || client.Password != owner.Password || client.SSHPassword != owner.SSHPassword || client.MieruPassword != owner.MieruPassword {
		t.Fatal("portable export conflated or discarded native authentication")
	}
	copied := exported[0]
	copied.Client.Email = "portable-native-imported"
	copied.Client.SubID = "portable-native-imported-sub"
	result, _, err := clients.ImportClients(inbounds, []ClientCreatePayload{copied})
	if err != nil || result.Created != 1 {
		t.Fatal("portable import failed to create independent native account")
	}
	imported, err := clients.GetRecordByEmail(nil, copied.Client.Email)
	if err != nil || imported.SnellPSK != owner.SnellPSK || imported.StableID == stored.StableID {
		t.Fatal("portable import lost PSK or reused canonical identity")
	}
}

func TestSnellPanelStandaloneRejectsInvalidPSKBeforeWrites(t *testing.T) {
	for _, psk := range []string{strings.Repeat("x", 256), string([]byte{0xff, 0xfe})} {
		t.Run(fmt.Sprint(len(psk)), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			if _, err := (&ClientService{}).Create(&InboundService{}, &ClientCreatePayload{Client: model.Client{Email: "invalid-native-owner", Enable: true, SnellPSK: psk}}); err == nil {
				t.Fatal("standalone account accepted invalid native PSK")
			}
			var count int64
			if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("invalid independent native authentication persisted")
			}
		})
	}
}
