package service

import (
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSSHInboundOptionsExportOnlyActualHostPublicKey(t *testing.T) {
	setupBulkDB(t)
	inbound := &model.Inbound{Protocol: model.SSH, Port: 49121, Tag: "ssh-export", Settings: `{"bridgePort":49122,"clients":[]}`}
	if err := normalizeSSHInbound(inbound, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	var settings sshInboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(settings.HostKey))
	if err != nil {
		t.Fatal("fixture generated an invalid host key")
	}
	wantKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	options, err := (&InboundService{}).GetInboundOptions(0)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		ID         int    `json:"id"`
		SSHHostKey string `json:"sshHostKey"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].ID != inbound.Id || decoded[0].SSHHostKey != wantKey {
		t.Fatal("SSH export metadata must contain the actual listener's public host key")
	}
	if strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), settings.HostKey) {
		t.Fatal("SSH export metadata disclosed a host private key")
	}
	if err := database.GetDB().Model(inbound).Update("settings", `{"hostKey":"invalid","clients":[]}`).Error; err != nil {
		t.Fatal(err)
	}
	options, err = (&InboundService{}).GetInboundOptions(0)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	decoded = nil
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].SSHHostKey != "" {
		t.Fatal("invalid host key must not export a stale trust pin")
	}
}

func TestSSHInboundOptionsExportOnlyActualHostPublicKey_Postgres(t *testing.T) {
	managedUsagePostgresSchema(t)
	TestSSHInboundOptionsExportOnlyActualHostPublicKey(t)
}
