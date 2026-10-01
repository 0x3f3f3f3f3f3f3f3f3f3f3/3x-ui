package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func nativeSSHPanelSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func dialNativeSSHPanel(t *testing.T, inbound *model.Inbound, username string, user ssh.Signer) (*ssh.Client, error) {
	t.Helper()
	var host model.NativeSSHHostKey
	if err := database.GetDB().First(&host, "id = ?", inbound.SSHHostKeyID).Error; err != nil {
		t.Fatal(err)
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", inbound.Port), &ssh.ClientConfig{User: username, Auth: []ssh.AuthMethod{ssh.PublicKeys(user)}, HostKeyCallback: ssh.FixedHostKey(public), Timeout: 2 * time.Second})
}

func nativeSSHPanelFlow(t *testing.T, inbound *model.Inbound, username string, user ssh.Signer, targetPort int) net.Conn {
	t.Helper()
	client, err := dialNativeSSHPanel(t, inbound, username, user)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	flow, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flow.Close() })
	return flow
}

func TestSSHPanelPublicCRUDRealCorePreservesSiblingAndSharedLedger(t *testing.T) {
	svc, tunnel, owner, target := setupManagedActivationService(t)
	removeManagedTemplateOptIn(t, svc)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	clients, inbounds := &ClientService{}, &InboundService{}
	user, siblingKey := nativeSSHPanelSigner(t), nativeSSHPanelSigner(t)
	update := owner.ToClient()
	update.SSHAuthorizedKeys = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(user.PublicKey())))
	if _, err := clients.Update(inbounds, owner.Id, *update, 0); err != nil {
		t.Fatalf("prepare canonical SSH authentication: %v", err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	listener, _, err := inbounds.AddInbound(&model.Inbound{Tag: "ssh-live", Protocol: model.SSH, Listen: "127.0.0.1", Port: port, Enable: true, Settings: `{"clients":[]}`})
	if err != nil {
		t.Fatalf("public native SSH listener create: %v", err)
	}
	if _, err := clients.Attach(inbounds, owner.Id, []int{listener.Id}); err != nil {
		t.Fatalf("public native SSH attach: %v", err)
	}
	stored, err := clients.GetRecordByEmail(nil, owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	sibling := model.Client{Email: "ssh-sibling", Enable: true, SSHUsername: "sibling-wire", SSHAuthorizedKeys: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(siblingKey.PublicKey())))}
	if _, err := clients.Create(inbounds, &ClientCreatePayload{Client: sibling, InboundIds: []int{listener.Id}}); err != nil {
		t.Fatalf("public native SSH account create: %v", err)
	}
	first := nativeSSHPanelFlow(t, listener, stored.SSHUsername, user, target)
	other := nativeSSHPanelFlow(t, listener, sibling.SSHUsername, siblingKey, target)
	shared, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	managedActivationEcho(t, first, "first")
	managedActivationEcho(t, other, "other")
	managedActivationEcho(t, shared, "shared")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	before := policyLedgerTotal(t, owner.StableID)
	if before.RawUpload != 111 || before.RawDownload != 211 || before.BilledBytes != 344 {
		t.Fatalf("native SSH and Tunnel do not share canonical usage: %+v", before)
	}
	rotatedKey := nativeSSHPanelSigner(t)
	rotate := model.Client{Email: owner.Email, Enable: true, SSHAuthorizedKeys: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(rotatedKey.PublicKey())))}
	if _, err := clients.Update(inbounds, owner.Id, rotate, 0); err != nil {
		t.Fatalf("public SSH rotation: %v", err)
	}
	managedActivationClosed(t, first)
	stale, err := dialNativeSSHPanel(t, listener, stored.SSHUsername, user)
	if stale != nil {
		_ = stale.Close()
	}
	if err == nil {
		t.Fatal("revoked native SSH key reauthenticated")
	}
	current := nativeSSHPanelFlow(t, listener, stored.SSHUsername, rotatedKey, target)
	managedActivationEcho(t, current, "fresh")
	managedActivationEcho(t, other, "alive")
	managedActivationEcho(t, shared, "stay")
	if currentXrayProcess() != process {
		t.Fatal("SSH account rotation restarted unrelated services")
	}
	rotate.Enable = false
	if _, err := clients.Update(inbounds, owner.Id, rotate, 0); err != nil {
		t.Fatal(err)
	}
	managedActivationClosed(t, current)
	managedActivationClosed(t, shared)
	managedActivationEcho(t, other, "survive")
	rotate.Enable = true
	if _, err := clients.Update(inbounds, owner.Id, rotate, 0); err != nil {
		t.Fatal(err)
	}
	current = nativeSSHPanelFlow(t, listener, stored.SSHUsername, rotatedKey, target)
	managedActivationEcho(t, current, "enabled")
	if _, _, err := svc.GetXrayTraffic(); err != nil {
		t.Fatal(err)
	}
	after := policyLedgerTotal(t, owner.StableID)
	if after.RawUpload != 127 || after.RawDownload != 227 || after.BilledBytes != 408 {
		t.Fatalf("SSH credential and enable lifecycle lost or repriced shared history: %+v", after)
	}
	if _, err := clients.Delete(inbounds, owner.Id, true); err != nil {
		t.Fatal(err)
	}
	managedActivationClosed(t, current)
	managedActivationEcho(t, other, "after-delete")
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	reopened := nativeSSHPanelFlow(t, listener, sibling.SSHUsername, siblingKey, target)
	managedActivationEcho(t, reopened, "restart")
}
