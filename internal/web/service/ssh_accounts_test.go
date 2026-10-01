package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func nativeSSHTestPublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestSSHPanelExplicitClearIsSeparateFromOmission(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc, inbounds := &ClientService{}, &InboundService{}
	client := sshPanelClient(t, map[string]any{"email": "clear-owner", "enable": true, "password": "unrelated", "sshUsername": "wire", "sshPassword": "secret", "sshAuthorizedKeys": nativeSSHTestPublicKey(t)})
	if _, err := svc.Create(inbounds, &ClientCreatePayload{Client: client}); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	identity := stored.StableID
	for _, command := range []string{"clearSshPassword", "clearSshAuthorizedKeys"} {
		updated := sshPanelClient(t, map[string]any{"email": client.Email, "enable": true, command: true})
		if _, err := svc.Update(inbounds, stored.Id, updated, 0); err != nil {
			t.Fatal(err)
		}
		stored, err = svc.GetRecordByEmail(nil, client.Email)
		if err != nil {
			t.Fatal(err)
		}
		field := "sshPassword"
		if command == "clearSshAuthorizedKeys" {
			field = "sshAuthorizedKeys"
		}
		if value, _ := sshPanelFields(t, stored.ToClient())[field].(string); value != "" {
			t.Fatalf("explicit %s did not revoke authentication", command)
		}
		if stored.Password != "unrelated" || stored.StableID != identity {
			t.Fatal("SSH clear modified another credential or identity")
		}
	}
}

func TestSSHPanelRejectsInvalidNativeAuthenticationBeforeWrites(t *testing.T) {
	for _, fields := range []map[string]any{
		{"sshAuthorizedKeys": "not a public key"},
		{"sshAuthorizedKeys": `command="id" ` + nativeSSHTestPublicKey(t)},
		{"sshAuthorizedKeys": strings.Repeat(nativeSSHTestPublicKey(t)+"\n", 17)},
		{"sshUsername": strings.Repeat("a", 257)},
		{"sshUsername": "line\nbreak"},
		{"sshPassword": strings.Repeat("x", 1025)},
		{"sshPassword": "new", "clearSshPassword": true},
	} {
		t.Run("invalid", func(t *testing.T) {
			setupPolicyLedgerDB(t)
			fields["email"], fields["enable"] = "bad-owner", true
			if _, err := (&ClientService{}).Create(&InboundService{}, &ClientCreatePayload{Client: sshPanelClient(t, fields)}); err == nil {
				t.Fatal("invalid native SSH authentication committed")
			}
			var count int64
			if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("rejected SSH authentication left an SQL record")
			}
		})
	}
}

func TestSSHPanelKeyOnlyPublicInboundUsesCanonicalAuthentication(t *testing.T) {
	setupPolicyLedgerDB(t)
	client := sshPanelClient(t, map[string]any{"email": "key-owner", "enable": true, "sshAuthorizedKeys": nativeSSHTestPublicKey(t) + " comment"})
	raw, err := json.Marshal(map[string]any{"clients": []model.Client{client}})
	if err != nil {
		t.Fatal(err)
	}
	inbound, _, err := (&InboundService{}).AddInbound(&model.Inbound{Tag: "ssh-key-only", Protocol: model.SSH, Port: 24572, Settings: string(raw)})
	if err != nil {
		t.Fatalf("native key-only create: %v", err)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSHUsername == "" || stored.UUID != "" || stored.SSHPassword != "" {
		t.Fatal("native SSH create lacks independent generated username")
	}
	entries, err := (&InboundService{}).GetClients(inbound)
	if err != nil || len(entries) != 1 || entries[0].SSHUsername != stored.SSHUsername || strings.Contains(stored.SSHAuthorizedKeys, " comment") {
		t.Fatal("canonical SSH authentication was not normalized and mirrored")
	}
}

func TestSSHPanelCredentialChangeChecksDisabledLinkedOwnersAndLastMethod(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	first := mkInbound(t, 24573, model.SSH, `{"clients":[],"allowPassword":true}`)
	second := mkInbound(t, 24574, model.SSH, `{"clients":[],"allowPassword":false}`)
	shared := model.Client{Email: "shared", Enable: true, SSHUsername: "original", SSHPassword: "secret", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	sibling := model.Client{Email: "disabled-owner", Enable: false, SSHUsername: "reserved", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	if err := svc.SyncInbound(nil, first.Id, []model.Client{shared}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncInbound(nil, second.Id, []model.Client{shared, sibling}); err != nil {
		t.Fatal(err)
	}
	for _, updated := range []model.Client{
		{Email: shared.Email, Enable: true, SSHUsername: "reserved"},
		sshPanelClient(t, map[string]any{"email": shared.Email, "enable": true, "clearSshAuthorizedKeys": true}),
	} {
		if err := svc.ApplyInboundClientDelta(nil, first.Id, []model.Client{updated}, nil); err == nil {
			t.Fatal("SSH rotation bypassed a linked listener's reserved username or required key authentication")
		}
	}
	stored, err := svc.GetRecordByEmail(nil, shared.Email)
	if err != nil || stored.SSHUsername != "original" || stored.SSHAuthorizedKeys != shared.SSHAuthorizedKeys {
		t.Fatal("rejected native SSH rotation partially committed")
	}
}

func TestSSHPanelOmittedUpdateReadsCredentialsInsideSQLWriter(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	client := model.Client{Email: "concurrent-owner", Enable: true, SSHUsername: "wire", SSHPassword: "old"}
	if _, err := svc.Create(&InboundService{}, &ClientCreatePayload{Client: client}); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	db, rotated := database.GetDB(), false
	const callback = "test:ssh-rotate-before-writer"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "clients" {
			return
		}
		rotated = true
		if err := db.Model(&model.ClientRecord{}).Where("id = ?", stored.Id).UpdateColumn("ssh_password", "new").Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := svc.Update(&InboundService{}, stored.Id, model.Client{Email: client.Email, Enable: true}, 0); err != nil {
		t.Fatal(err)
	}
	stored, err = svc.GetRecordByEmail(nil, client.Email)
	if err != nil || !rotated || stored.SSHPassword != "new" {
		t.Fatal("omitted SSH update restored a stale authentication generation")
	}
}

func TestSSHPanelRejectsUnsupportedServerOptionsBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, settings, stream string }{
		{"raw-users", `{"users":[],"clients":[]}`, `{}`},
		{"raw-key-file", `{"hostKeyFile":"/request/file","clients":[]}`, `{}`},
		{"timeouts", `{"handshakeTimeoutSeconds":121,"clients":[]}`, `{}`},
		{"resource-limit", `{"maxChannels":513,"clients":[]}`, `{}`},
		{"invalid-reverse", `{"reverse":{"enabled":true,"bindAddresses":["0.0.0.0"],"sourceCIDRs":["127.0.0.1/24"],"portFrom":100,"portTo":200},"clients":[]}`, `{}`},
		{"unknown-reverse", `{"reverse":{"enabled":false,"allowEverything":true},"clients":[]}`, `{}`},
		{"tls", `{"clients":[]}`, `{"network":"tcp","security":"tls"}`},
		{"udp-wrapper", `{"clients":[]}`, `{"network":"kcp"}`},
		{"client-id", `{"clients":[{"email":"owner","clientId":"forged-owner","sshPassword":"secret"}],"allowPassword":true}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupPolicyLedgerDB(t)
			if _, _, err := (&InboundService{}).AddInbound(&model.Inbound{Tag: "ssh-invalid-options", Protocol: model.SSH, Port: 24580, Settings: tc.settings, StreamSettings: tc.stream}); err == nil {
				t.Fatal("unsupported SSH listener option was silently committed")
			}
			for _, mdl := range []any{&model.Inbound{}, &model.NativeSSHHostKey{}, &model.ClientRecord{}} {
				var count int64
				if err := database.GetDB().Model(mdl).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("rejected SSH options left listener, host-key or account rows")
				}
			}
		})
	}
}

func TestSSHPanelLocalAccountRejectsRemoteBindingBeforeWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	local := mkInbound(t, 24581, model.SSH, `{"clients":[]}`)
	remote := mkInbound(t, 24582, model.VLESS, `{"decryption":"none","clients":[]}`)
	node := model.Node{Name: "ssh-scope-test"}
	if err := database.GetDB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(remote).UpdateColumn("node_id", node.Id).Error; err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "remote-owner", Enable: true, ID: "existing-vless", SSHUsername: "wire", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	if err := svc.SyncInbound(nil, remote.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncInbound(nil, local.Id, []model.Client{client}); err == nil {
		t.Fatal("local SSH owner acquired an uncoordinated remote binding")
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientInbound{}).Where("inbound_id = ?", local.Id).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected remote SSH scope partially attached")
	}
}

func TestSSHPanelRemoteBindingRejectsLocalOwnerBeforeWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &ClientService{}
	local := mkInbound(t, 24583, model.SSH, `{"clients":[]}`)
	remote := mkInbound(t, 24584, model.VLESS, `{"decryption":"none","clients":[]}`)
	if err := database.GetDB().Model(remote).UpdateColumn("node_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "local-owner", Enable: true, ID: "other-protocol", SSHUsername: "wire", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	if err := svc.SyncInbound(nil, local.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncInbound(nil, remote.Id, []model.Client{client}); err == nil {
		t.Fatal("remote binding bypassed a local native SSH account's scope")
	}
}

func TestSSHPanelAttachReadsCanonicalCredentialsInsideSQLWriter(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc, inbounds := &ClientService{}, &InboundService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "ssh-attach-race", Protocol: model.SSH, Port: 24585, Settings: `{"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	client := model.Client{Email: "attach-owner", Enable: true, SSHUsername: "wire", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	if _, err := svc.Create(inbounds, &ClientCreatePayload{Client: client}); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	newKey := nativeSSHTestPublicKey(t)
	db, rotated := database.GetDB(), false
	const callback = "test:ssh-rotate-before-attach-writer"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "clients" {
			return
		}
		rotated = true
		if err := db.Model(&model.ClientRecord{}).Where("id = ?", stored.Id).UpdateColumn("ssh_authorized_keys", newKey).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := svc.Attach(inbounds, stored.Id, []int{listener.Id}); err != nil {
		t.Fatal(err)
	}
	stored, err = svc.GetRecordByEmail(nil, client.Email)
	if err != nil || !rotated || stored.SSHAuthorizedKeys != newKey {
		t.Fatal("attachment restored a revoked native SSH public key from a pre-write snapshot")
	}
}

func TestSSHPanelClearCommandIsConsumedBeforeListenerReopen(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	client := model.Client{Email: "clear-linked-owner", Enable: true, SSHUsername: "wire", SSHPassword: "old", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	raw, err := json.Marshal(map[string]any{"clients": []model.Client{client}, "allowPassword": true})
	if err != nil {
		t.Fatal(err)
	}
	inbound, _, err := svc.AddInbound(&model.Inbound{Tag: "ssh-clear-reopen", Protocol: model.SSH, Port: 24586, Settings: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	clear := sshPanelClient(t, map[string]any{"email": client.Email, "enable": true, "clearSshPassword": true})
	if _, err := (&ClientService{}).Update(svc, stored.Id, clear, 0); err != nil {
		t.Fatal(err)
	}
	inbound, err = svc.GetInbound(inbound.Id)
	if err != nil || strings.Contains(inbound.Settings, "clearSshPassword") {
		t.Fatal("transient SSH revocation command persisted in listener settings")
	}
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("id = ?", stored.Id).UpdateColumn("ssh_password", "new").Error; err != nil {
		t.Fatal(err)
	}
	inbound.Settings = `{"allowPassword":true,"clients":[{"email":"clear-linked-owner","enable":true}]}`
	if _, _, err := svc.UpdateInbound(inbound); err != nil {
		t.Fatal(err)
	}
	stored, err = (&ClientService{}).GetRecordByEmail(nil, client.Email)
	if err != nil || stored.SSHPassword != "new" {
		t.Fatal("ordinary listener reopen replayed an old native SSH clear command")
	}
}

func TestSSHPanelInboundUpdateResponseDoesNotReplayClearCommand(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	client := model.Client{Email: "response-owner", Enable: true, SSHUsername: "wire", SSHPassword: "secret", SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	raw, err := json.Marshal(map[string]any{"clients": []model.Client{client}, "allowPassword": true})
	if err != nil {
		t.Fatal(err)
	}
	inbound, _, err := svc.AddInbound(&model.Inbound{Tag: "ssh-clear-response", Protocol: model.SSH, Port: 24587, Settings: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	inbound.Settings = `{"allowPassword":true,"clients":[{"email":"response-owner","enable":true,"clearSshPassword":true}]}`
	updated, _, err := svc.UpdateInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updated.Settings, "clearSshPassword") {
		t.Fatal("ordinary SSH update response returns a replayable clear command")
	}
	entries, err := svc.GetClients(updated)
	if err != nil || len(entries) != 1 || entries[0].SSHPassword != "" || entries[0].SSHAuthorizedKeys != client.SSHAuthorizedKeys {
		t.Fatal("SSH update response is not the committed canonical authentication")
	}
}

func sshPanelClient(t *testing.T, fields map[string]any) model.Client {
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

func sshPanelFields(t *testing.T, value any) map[string]any {
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

func TestSSHPanelStandaloneNativeCredentialsPreserveIdentityOnRenameAndRotation(t *testing.T) {
	setupPolicyLedgerDB(t)
	clients, inbounds := &ClientService{}, &InboundService{}
	publicKey := nativeSSHTestPublicKey(t)
	client := sshPanelClient(t, map[string]any{
		"email": "ssh-owner", "enable": true, "password": "other-protocol-secret",
		"mieruUsername": "mieru-wire", "mieruPassword": "mieru-secret",
		"sshUsername": "business-user", "sshAuthorizedKeys": publicKey, "sshPassword": "business-password",
	})
	if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: client}); err != nil {
		t.Fatal(err)
	}
	record, err := clients.GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	identity := record.StableID
	check := func(username, key, password string) {
		t.Helper()
		fields := sshPanelFields(t, record.ToClient())
		if fields["sshUsername"] != username || fields["sshAuthorizedKeys"] != key || fields["sshPassword"] != password {
			t.Fatal("canonical SQL/API projection lost independent native SSH authentication")
		}
		if record.StableID != identity || record.Password != "other-protocol-secret" || record.MieruPassword != "mieru-secret" {
			t.Fatal("native SSH update changed stable identity or another protocol credential")
		}
	}
	check("business-user", publicKey, "business-password")
	if _, err := clients.Update(inbounds, record.Id, model.Client{Email: "renamed-owner", Enable: true}, 0); err != nil {
		t.Fatal(err)
	}
	record, err = clients.GetRecordByEmail(nil, "renamed-owner")
	if err != nil {
		t.Fatal(err)
	}
	check("business-user", publicKey, "business-password")
	newKey := nativeSSHTestPublicKey(t)
	update := sshPanelClient(t, map[string]any{"email": "renamed-owner", "enable": true, "sshAuthorizedKeys": newKey, "sshPassword": "new-business-password"})
	if _, err := clients.Update(inbounds, record.Id, update, 0); err != nil {
		t.Fatal(err)
	}
	record, err = clients.GetRecordByEmail(nil, "renamed-owner")
	if err != nil {
		t.Fatal(err)
	}
	check("business-user", newKey, "new-business-password")
	exported, err := clients.ExportAll()
	if err != nil || len(exported) != 1 {
		t.Fatalf("native SSH portable account export: count=%d error=%v", len(exported), err)
	}
	fields := sshPanelFields(t, exported[0].Client)
	if fields["sshAuthorizedKeys"] != newKey || fields["sshPassword"] != "new-business-password" {
		t.Fatal("portable client export dropped SSH authentication")
	}
}
