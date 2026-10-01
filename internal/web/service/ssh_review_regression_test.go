package service

import (
	"encoding/json"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSSHPanelReviewCredentialsUpdatePasswordProxyOwner(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	owner := passwordOwner(t, "ssh-http-owner")
	oldKey, newKey := nativeSSHTestPublicKey(t), nativeSSHTestPublicKey(t)
	if err := database.GetDB().Model(&owner).Updates(map[string]any{"ssh_username": "original", "ssh_authorized_keys": oldKey, "ssh_password": "old-password"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24691, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "http-user", "pass": "http-secret", "ownerClientId": owner.StableID})}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Update(inbounds, owner.Id, model.Client{Email: owner.Email, Enable: true, SSHUsername: "rotated", SSHAuthorizedKeys: newKey, ClearSSHPassword: true}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := clients.GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSHUsername != "rotated" || got.SSHAuthorizedKeys != newKey || got.SSHPassword != "" {
		t.Fatalf("successful public update ignored SSH authentication: username=%q newKeyRetained=%t passwordCleared=%t", got.SSHUsername, got.SSHAuthorizedKeys == newKey, got.SSHPassword == "")
	}
}

func TestSSHPanelReviewAddClientRejectsForgedRuntimeOwnerBeforeWrites(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.SSH, Port: 24692, Settings: `{"allowPassword":true,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clientId", "client_id"} {
		raw, err := json.Marshal(map[string]any{"clients": []map[string]any{{"email": "forged-client", "enable": true, "sshPassword": "secret", name: "forged-runtime-owner"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clients.AddInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: string(raw)}); err == nil {
			t.Fatalf("AddInboundClient accepted forged %s", name)
		}
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected forged account left canonical rows")
	}
	if _, err := clients.AddInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: `{"clients":[{"email":"real-owner","enable":true,"sshPassword":"original"}]}`}); err != nil {
		t.Fatal(err)
	}
	owner, err := clients.GetRecordByEmail(nil, "real-owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clientId", "client_id"} {
		raw, err := json.Marshal(map[string]any{"clients": []map[string]any{{"email": owner.Email, "enable": true, "sshPassword": "changed", name: "forged-runtime-owner"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clients.UpdateInboundClient(inbounds, &model.Inbound{Id: listener.Id, Settings: string(raw)}, owner.Email); err == nil {
			t.Fatalf("UpdateInboundClient accepted forged %s", name)
		}
	}
	got, err := clients.GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.StableID != owner.StableID || got.SSHPassword != "original" {
		t.Fatal("rejected forged update changed canonical authentication")
	}
}

func TestSSHPanelReviewClientUpdateRefusesReusedCanonicalLabel(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	key := nativeSSHTestPublicKey(t)
	payload, err := json.Marshal(map[string]any{"allowPassword": true, "clients": []model.Client{{Email: "selected-owner", SubID: "canonical-race-sub", Enable: true, SSHUsername: "original-wire", SSHAuthorizedKeys: key}}})
	if err != nil {
		t.Fatal(err)
	}
	listener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "ssh-owner-race", Protocol: model.SSH, Port: 24693, Settings: string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	otherListener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "ssh-other-owner-race", Protocol: model.SSH, Port: 24694, Settings: `{"allowPassword":true,"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := clients.GetRecordByEmail(nil, "selected-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Attach(inbounds, selected.Id, []int{otherListener.Id}); err != nil {
		t.Fatal(err)
	}
	var acquired *model.ClientRecord
	reads := 0
	changed := false
	db := database.GetDB()
	const callback = "test:ssh-reuse-after-inbound-read"
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
		if _, err := clients.Update(inbounds, selected.Id, model.Client{Email: "selected-renamed", Enable: true}, 0, otherListener.Id); err != nil {
			tx.AddError(err)
			return
		}
		if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: model.Client{Email: "selected-owner", Enable: true, SSHUsername: "other-wire", SSHPassword: "other-password"}}); err != nil {
			tx.AddError(err)
			return
		}
		var err error
		acquired, err = clients.GetRecordByEmail(nil, "selected-owner")
		if err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, err = clients.Update(inbounds, selected.Id, model.Client{Email: "selected-owner", Enable: true, SSHUsername: "requested-wire", SSHPassword: "requested-password"}, 0, listener.Id)
	if !changed || acquired == nil {
		t.Fatalf("public interleaving hook did not run: reads=%d changed=%t outerErr=%v", reads, changed, err)
	}
	var current model.ClientRecord
	if e := db.First(&current, acquired.Id).Error; e != nil {
		t.Fatal(e)
	}
	var links int64
	if e := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", acquired.Id, listener.Id).Count(&links).Error; e != nil {
		t.Fatal(e)
	}
	if err == nil || current.SSHUsername != "other-wire" || current.SSHPassword != "other-password" || links != 0 {
		t.Fatalf("selected stable owner was not fenced against PUBLIC rename + label reuse: error=%v unrelatedUsername=%q unrelatedPasswordChanged=%t unrelatedListenerLinks=%d", err, current.SSHUsername, current.SSHPassword != "other-password", links)
	}
}

func TestSSHPanelReviewPasswordOwnerFilterValidatesAllNativeListeners(t *testing.T) {
	for _, protocol := range []model.Protocol{model.HTTP, model.Mixed} {
		t.Run(string(protocol), func(t *testing.T) {
			setupPolicyLedgerDB(t)
			inbounds, clients := &InboundService{}, &ClientService{}
			owner := passwordOwner(t, "filtered-owner")
			oldKey, newKey := nativeSSHTestPublicKey(t), nativeSSHTestPublicKey(t)
			auth := map[string]any{"ssh_username": "original", "ssh_authorized_keys": oldKey, "ssh_password": "old-password", "mieru_username": "mieru-original", "mieru_password": "mieru-old"}
			if err := database.GetDB().Model(&owner).Updates(auth).Error; err != nil {
				t.Fatal(err)
			}
			shared := model.Client{Email: owner.Email, SubID: owner.SubID, Enable: true, SSHUsername: "original", SSHAuthorizedKeys: oldKey, SSHPassword: "old-password", MieruUsername: "mieru-original", MieruPassword: "mieru-old"}
			sibling := model.Client{Email: "reserved-owner", SubID: "reserved-sub", Enable: false, SSHUsername: "reserved", SSHAuthorizedKeys: nativeSSHTestPublicKey(t), MieruUsername: "mieru-reserved", MieruPassword: "reserved-password"}
			var sshListener *model.Inbound
			for _, native := range []model.Protocol{model.SSH, model.Mieru} {
				raw, err := json.Marshal(map[string]any{"clients": []model.Client{shared, sibling}})
				if err != nil {
					t.Fatal(err)
				}
				port := 24701
				if native == model.Mieru {
					port = 24702
				}
				listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: native, Port: port, Settings: string(raw)})
				if err != nil {
					t.Fatal(err)
				}
				if native == model.SSH {
					sshListener = listener
				}
			}
			passwordListener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: protocol, Port: 24703, Settings: passwordOwnerSettings(t, protocol, map[string]any{"user": "local-user", "pass": "local-password", "ownerClientId": owner.StableID})})
			if err != nil {
				t.Fatal(err)
			}
			for _, update := range []model.Client{
				{Email: owner.Email, Enable: true, SSHUsername: "reserved"},
				{Email: owner.Email, Enable: true, ClearSSHAuthorizedKeys: true},
				{Email: owner.Email, Enable: true, MieruUsername: "mieru-reserved"},
			} {
				if _, err := clients.Update(inbounds, owner.Id, update, 0, passwordListener.Id); err == nil {
					t.Fatal("password-only filter bypassed a linked native authentication constraint")
				}
			}
			got, err := clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			if got.SSHUsername != "original" || got.SSHAuthorizedKeys != oldKey || got.MieruUsername != "mieru-original" {
				t.Fatal("rejected update partially committed native credentials")
			}
			if _, err := clients.Update(inbounds, owner.Id, model.Client{Email: owner.Email, Enable: true, SSHUsername: "rotated", SSHAuthorizedKeys: newKey, ClearSSHPassword: true, MieruUsername: "mieru-rotated", MieruPassword: "mieru-new"}, 0, passwordListener.Id); err != nil {
				t.Fatal(err)
			}
			got, err = clients.GetByID(owner.Id)
			if err != nil {
				t.Fatal(err)
			}
			if got.StableID != owner.StableID || got.UUID != owner.UUID || got.Password != owner.Password || got.SSHUsername != "rotated" || got.SSHAuthorizedKeys != newKey || got.SSHPassword != "" || got.MieruUsername != "mieru-rotated" || got.MieruPassword != "mieru-new" {
				t.Fatal("filtered shared owner update lost independent credentials or canonical identity")
			}
			sshListener, err = inbounds.GetInbound(sshListener.Id)
			if err != nil {
				t.Fatal(err)
			}
			mirrors, err := inbounds.GetClients(sshListener)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, mirror := range mirrors {
				if mirror.Email == owner.Email {
					found = true
					if mirror.SSHUsername != got.SSHUsername || mirror.SSHAuthorizedKeys != newKey || mirror.SSHPassword != "" || mirror.ClearSSHPassword {
						t.Fatal("SSH mirror retained old authentication or replayable clear command")
					}
				}
			}
			if !found {
				t.Fatal("shared owner disappeared from its SSH mirror")
			}
		})
	}
}

func TestSSHPanelReviewPasswordOwnerOmissionsReadCurrentNativeCredentials(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	owner := passwordOwner(t, "omitted-native-owner")
	if err := database.GetDB().Model(&owner).Updates(map[string]any{"ssh_username": "original", "ssh_password": "old-ssh", "mieru_username": "original-mieru", "mieru_password": "old-mieru"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24704, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "local-user", "pass": "local-password", "ownerClientId": owner.StableID})}); err != nil {
		t.Fatal(err)
	}
	db, rotated := database.GetDB(), false
	const callback = "test:ssh-password-owner-rotate-before-writer"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "clients" {
			return
		}
		rotated = true
		if err := db.Model(&model.ClientRecord{}).Where("id = ?", owner.Id).Updates(map[string]any{"ssh_username": "latest-ssh", "ssh_password": "latest-ssh-password", "mieru_username": "latest-mieru", "mieru_password": "latest-mieru-password"}).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := clients.Update(inbounds, owner.Id, model.Client{Email: owner.Email, Enable: true, Comment: "metadata only"}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := clients.GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated || got.SSHUsername != "latest-ssh" || got.SSHPassword != "latest-ssh-password" || got.MieruUsername != "latest-mieru" || got.MieruPassword != "latest-mieru-password" {
		t.Fatal("omitted shared native fields reverted a concurrent rotation")
	}
}

func TestSSHPanelReviewFilteredRenameConsumesClearInOrdinaryMirrors(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds, clients := &InboundService{}, &ClientService{}
	owner := passwordOwner(t, "mirror-owner")
	key := nativeSSHTestPublicKey(t)
	if err := database.GetDB().Model(&owner).Updates(map[string]any{"ssh_username": "mirror-wire", "ssh_authorized_keys": key, "ssh_password": "old-password"}).Error; err != nil {
		t.Fatal(err)
	}
	shared := model.Client{Email: owner.Email, SubID: owner.SubID, ID: owner.UUID, Enable: true, SSHUsername: "mirror-wire", SSHAuthorizedKeys: key, SSHPassword: "old-password"}
	var ordinary *model.Inbound
	for _, protocol := range []model.Protocol{model.SSH, model.VLESS} {
		raw, err := json.Marshal(map[string]any{"clients": []model.Client{shared}})
		if err != nil {
			t.Fatal(err)
		}
		port := 24705
		if protocol == model.VLESS {
			port = 24706
		}
		listener, _, err := inbounds.AddInbound(&model.Inbound{Protocol: protocol, Port: port, Settings: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		if protocol == model.VLESS {
			ordinary = listener
		}
	}
	http, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Port: 24707, Settings: passwordOwnerSettings(t, model.HTTP, map[string]any{"user": "local-user", "pass": "local-secret", "ownerClientId": owner.StableID})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Update(inbounds, owner.Id, model.Client{Email: "mirror-renamed", Enable: true, ClearSSHPassword: true}, 0, http.Id); err != nil {
		t.Fatal(err)
	}
	ordinary, err = inbounds.GetInbound(ordinary.Id)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal([]byte(ordinary.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	for _, mirror := range settings.Clients {
		if _, ok := mirror["clearSshPassword"]; ok {
			t.Fatal("excluded ordinary mirror retained a replayable SSH revocation command")
		}
	}
	got, err := clients.GetByID(owner.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "mirror-renamed" || got.SSHPassword != "" || got.SSHAuthorizedKeys != key || got.StableID != owner.StableID {
		t.Fatal("filtered rename lost canonical authentication or identity")
	}
}
