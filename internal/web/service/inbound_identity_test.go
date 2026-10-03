package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestInboundStableIdentityPublicCopyAndImport(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}
	original := makeImportInbound("identity-source", 27101, `{"clients":[],"decryption":"none"}`, nil)
	if _, _, err := svc.AddInbound(original); err != nil {
		t.Fatal(err)
	}
	copy := *original
	copy.Port, copy.Tag = 27102, "identity-copy"
	if _, _, err := svc.AddInbound(&copy); err != nil {
		t.Fatalf("new copied resource retained source identity: %v", err)
	}
	if copy.StableID == "" || copy.StableID == original.StableID || copy.Id == original.Id {
		t.Fatal("copy inherited original resource identity")
	}
	raw, err := json.Marshal(original)
	if err != nil || strings.Contains(string(raw), original.StableID) {
		t.Fatal("portable export leaked private identity")
	}
	var imported model.Inbound
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["StableID"], _ = json.Marshal(original.StableID)
	payload["stable_id"] = payload["StableID"]
	raw, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &imported); err != nil {
		t.Fatal(err)
	}
	imported.Port, imported.Tag = 27103, "identity-import"
	if _, _, err := svc.AddInbound(&imported); err != nil {
		t.Fatal(err)
	}
	if imported.StableID == "" || imported.StableID == original.StableID || imported.StableID == copy.StableID {
		t.Fatal("portable import inherited source identity")
	}
}

func TestInboundStableIdentityPublicNativeEdits(t *testing.T) {
	for _, protocol := range []model.Protocol{model.VLESS, model.Snell, model.Mieru, model.SSH, model.Tunnel} {
		t.Run(string(protocol), func(t *testing.T) {
			setupConflictDB(t)
			svc := &InboundService{}
			settings := `{"decryption":"none","clients":[{"email":"identity-owner","enable":true,"id":"00000000-0000-4000-8000-000000000011"}]}`
			if protocol == model.Snell {
				settings = `{"version":5,"clients":[{"email":"identity-owner","enable":true,"snellPsk":"identity-snell-psk"}]}`
			} else if protocol == model.Mieru {
				settings = `{"transport":"TCP","clients":[{"email":"identity-owner","enable":true,"mieruUsername":"identity-user","mieruPassword":"identity-mieru-password"}]}`
			} else if protocol == model.SSH {
				settings = `{"allowPassword":true,"clients":[{"email":"identity-owner","enable":true,"sshUsername":"identity-user","sshPassword":"identity-ssh-password"}]}`
			} else if protocol == model.Tunnel {
				settings = `{"network":"tcp,udp","address":"127.0.0.1","port":27000,"clients":[{"email":"identity-owner","enable":true}]}`
			}
			original := &model.Inbound{UserId: 1, Tag: "identity-native", Port: 27111, Protocol: protocol, Settings: settings}
			if _, _, err := svc.AddInbound(original); err != nil {
				t.Fatal(err)
			}
			var before model.ClientRecord
			if err := database.GetDB().Where("email = ?", "identity-owner").First(&before).Error; err != nil {
				t.Fatal(err)
			}
			identity, hostKey := original.StableID, original.SSHHostKeyID
			if err := database.GetDB().Table("inbounds").Where("id = ?", original.Id).Updates(map[string]any{"up": 111, "down": 222}).Error; err != nil {
				t.Fatal(err)
			}
			update := *original
			update.StableID, update.Port, update.Tag, update.Remark = uuid.NewString(), 27112, "identity-edited", "ordinary edit"
			got, _, err := svc.UpdateInbound(&update)
			if err != nil {
				t.Fatal(err)
			}
			var stored model.Inbound
			if err := database.GetDB().First(&stored, original.Id).Error; err != nil {
				t.Fatal(err)
			}
			if stored.StableID != identity || got.StableID != identity || stored.SSHHostKeyID != hostKey || stored.Port != 27112 || stored.Tag != update.Tag || stored.Up != 111 || stored.Down != 222 {
				t.Fatalf("ordinary edit lost stored/returned identity or native key/statistics: stored=%q returned=%q expected=%q", stored.StableID, got.StableID, identity)
			}
			var after model.ClientRecord
			if err := database.GetDB().First(&after, before.Id).Error; err != nil {
				t.Fatal(err)
			}
			if before.StableID != after.StableID || before.UUID != after.UUID || before.SnellPSK != after.SnellPSK || before.MieruUsername != after.MieruUsername || before.MieruPassword != after.MieruPassword || before.SSHUsername != after.SSHUsername || before.SSHPassword != after.SSHPassword || before.SSHAuthorizedKeys != after.SSHAuthorizedKeys {
				t.Fatal("ordinary resource edit changed canonical owner or native authentication")
			}
			if links := linksOf(t, original.Id); len(links) != 1 || links[before.Id].ClientId != before.Id {
				t.Fatal("ordinary resource edit lost original owner link")
			}
		})
	}
}
