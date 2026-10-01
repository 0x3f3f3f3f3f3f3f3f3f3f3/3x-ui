package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSSHPanelEmptyListenerOwnsPersistentBusinessHostKey(t *testing.T) {
	setupPolicyLedgerDB(t)
	inbounds := &InboundService{}
	created, _, err := inbounds.AddInbound(&model.Inbound{Tag: "ssh-business", Protocol: model.Protocol("ssh"), Port: 24571, Enable: false, Settings: `{"clients":[],"allowPassword":false}`})
	if err != nil {
		t.Fatal(err)
	}
	fields := sshPanelFields(t, created)
	id, _ := fields["sshHostKeyId"].(string)
	if _, err := uuid.Parse(id); err != nil {
		t.Fatal("native SSH listener lacks a server-owned host-key UUID")
	}
	var stored struct {
		ID, PrivateKeyPEM, PublicKey, Fingerprint string
	}
	if err := database.GetDB().Table("native_ssh_host_keys").Where("id = ?", id).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(stored.PrivateKeyPEM))
	if err != nil {
		t.Fatal("stored business key is not an actual SSH private key")
	}
	if stored.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) || stored.PublicKey == "" {
		t.Fatal("business host trust metadata does not match the persisted key")
	}
	created.Remark = "renamed display"
	if _, _, err := inbounds.UpdateInbound(created); err != nil {
		t.Fatal(err)
	}
	var again struct{ PrivateKeyPEM string }
	if err := database.GetDB().Table("native_ssh_host_keys").Where("id = ?", id).Take(&again).Error; err != nil || again.PrivateKeyPEM != stored.PrivateKeyPEM {
		t.Fatal("ordinary native SSH service edit rotated its business host key")
	}
}

func TestSSHPanelCandidateUsesCanonicalIdentityWithoutMaterializingPrivateKey(t *testing.T) {
	setupPolicyLedgerDB(t)
	policyConfigTemplate(t)
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	svc := &InboundService{}
	client := model.Client{Email: "candidate-owner", Enable: true, SSHAuthorizedKeys: nativeSSHTestPublicKey(t)}
	raw, err := json.Marshal(map[string]any{"clients": []model.Client{client}})
	if err != nil {
		t.Fatal(err)
	}
	inbound, _, err := svc.AddInbound(&model.Inbound{Tag: "ssh-candidate", Protocol: model.SSH, Port: 24578, Enable: true, Settings: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	config, err := (&XrayService{}).GetManagedXrayConfig(policyConfigState(t))
	if err != nil {
		t.Fatalf("native SSH candidate compilation: %v", err)
	}
	var settings struct {
		HostKeyFile string `json:"hostKeyFile"`
		Users       []struct {
			Username, ClientID string
			PublicKeys         []string
		} `json:"users"`
	}
	if len(config.InboundConfigs) != 1 {
		t.Fatal("native SSH candidate lacks its listener")
	}
	if err := json.Unmarshal(config.InboundConfigs[0].Settings, &settings); err != nil {
		t.Fatal(err)
	}
	stored, err := (&ClientService{}).GetRecordByEmail(nil, client.Email)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Users) != 1 || settings.Users[0].ClientID != stored.StableID || settings.Users[0].Username != stored.SSHUsername || len(settings.Users[0].PublicKeys) != 1 {
		t.Fatal("native SSH runtime lacks SQL-owned authentication and identity")
	}
	wanted := filepath.Join(dir, "native-ssh", "hosts", inbound.SSHHostKeyID+".pem")
	if settings.HostKeyFile != wanted {
		t.Fatal("SSH candidate does not reference its confined business key")
	}
	if _, err := os.Lstat(wanted); !os.IsNotExist(err) {
		t.Fatal("candidate materialized a private key before capability negotiation")
	}
}

func TestSSHPanelInboundOptionsExposeOnlyPublicBusinessTrust(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	inbound, _, err := svc.AddInbound(&model.Inbound{Tag: "ssh-public-trust", Protocol: model.SSH, Port: 24579, Settings: `{"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	var key model.NativeSSHHostKey
	if err := database.GetDB().First(&key, "id = ?", inbound.SSHHostKeyID).Error; err != nil {
		t.Fatal(err)
	}
	options, err := svc.GetInboundOptions(inbound.UserId)
	if err != nil || len(options) != 1 {
		t.Fatalf("native SSH public options: %v", err)
	}
	fields := sshPanelFields(t, options[0])
	if fields["sshHostPublicKey"] != key.PublicKey || fields["sshHostFingerprint"] != key.Fingerprint {
		t.Fatal("SSH public options omitted strict server trust metadata")
	}
	raw, err := json.Marshal(options)
	if err != nil || strings.Contains(string(raw), key.PrivateKeyPEM) || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("SSH public options exposed private host material")
	}
}

func TestSSHPanelInboundOptionsDescribeAllowedAuthentication(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	for i, enabled := range []bool{false, true} {
		settings := `{"clients":[],"allowPassword":false}`
		if enabled {
			settings = `{"clients":[],"allowPassword":true}`
		}
		if _, _, err := svc.AddInbound(&model.Inbound{Tag: fmt.Sprintf("ssh-auth-option-%d", i), Protocol: model.SSH, Port: 24620 + i, Settings: settings}); err != nil {
			t.Fatal(err)
		}
	}
	options, err := svc.GetInboundOptions(0)
	if err != nil || len(options) != 2 {
		t.Fatalf("native SSH authentication options: %v", err)
	}
	for i, option := range options {
		if sshPanelFields(t, option)["sshAllowPassword"] != (i == 1) {
			t.Fatal("public options must describe actual listener authentication")
		}
	}
}

func TestSSHPanelCloneGetsNewTrustAndMissingSQLKeyCannotRotate(t *testing.T) {
	setupPolicyLedgerDB(t)
	svc := &InboundService{}
	first, _, err := svc.AddInbound(&model.Inbound{Tag: "ssh-original", Protocol: model.SSH, Port: 24575, Settings: `{"clients":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	clone := *first
	clone.Id, clone.Port, clone.Tag = 0, 24576, "ssh-clone"
	second, _, err := svc.AddInbound(&clone)
	if err != nil {
		t.Fatal(err)
	}
	if second.SSHHostKeyID == first.SSHHostKeyID {
		t.Fatal("portable listener clone reused another service's trust identity")
	}
	if err := database.GetDB().Where("id = ?", first.SSHHostKeyID).Delete(&model.NativeSSHHostKey{}).Error; err != nil {
		t.Fatal(err)
	}
	first.Remark = "must reject"
	if _, _, err := svc.UpdateInbound(first); err == nil {
		t.Fatal("missing authoritative SSH key silently rotated or committed")
	}
	var stored model.Inbound
	if err := database.GetDB().First(&stored, first.Id).Error; err != nil || stored.Remark != "" || stored.SSHHostKeyID != first.SSHHostKeyID {
		t.Fatal("failed SSH trust validation changed the persisted service")
	}
}
