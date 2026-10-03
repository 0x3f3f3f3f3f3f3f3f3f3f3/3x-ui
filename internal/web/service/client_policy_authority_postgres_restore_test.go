package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/infra/conf"
	"golang.org/x/crypto/ssh"
)

// Removing protected recovery or restoring journal consumption from SQL would
// rewind these literal usage counters after actual old SQL/core import.
func TestManagedAuthorityActualPostgresRestorePreservesConsumption(t *testing.T) {
	dir := setupPrivatePostgresRestoreDatabase(t)
	svc, inbound, client, _ := setupManagedActivationServiceFromDatabase(t)
	hostKey := seedPostgresRestoreNativeIdentity(t, client, inbound)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var cfg conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	oldSQL, err := (&ServerService{}).GetDb()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(oldSQL, []byte("PGDMP")) {
		t.Fatal("not actual PostgreSQL dump")
	}
	if err := os.WriteFile(filepath.Join(dir, "before-consumption.dump"), oldSQL, 0600); err != nil {
		t.Fatal(err)
	}
	oldCore, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "before-consumption-core.db"), oldCore, 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "warm")
	_ = flow.Close()
	assertPostgresRestoreUsageAndAllocation(t, client.StableID, policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316})
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.StateFile, oldCore, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(oldSQL)}, false); err != nil {
		t.Fatal(err)
	}
	assertPostgresRestoreNativeIdentity(t, client, inbound, hostKey)
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil {
		t.Fatal("actual restored PostgreSQL core not owned")
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316}) {
		t.Fatalf("PG restore lost confirmed usage: %+v/%v", account, err)
	}
	flow, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "next")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err = owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 332}) {
		t.Fatalf("PG restore payload billed incorrectly: %+v/%v", account, err)
	}
}

func TestManagedAuthorityActualPostgresRestorePreservesPreparedReset(t *testing.T) {
	dir := setupPrivatePostgresRestoreDatabase(t)
	svc, inbound, client, _ := setupManagedActivationServiceFromDatabase(t)
	hostKey := seedPostgresRestoreNativeIdentity(t, client, inbound)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	var cfg conf.ClientPolicyConfig
	if err := json.Unmarshal(currentXrayProcess().GetConfig().ClientPolicy, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	oldSQL, err := (&ServerService{}).GetDb()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "before-reset.dump"), oldSQL, 0600); err != nil {
		t.Fatal(err)
	}
	oldCore, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "before-reset-core.db"), oldCore, 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "warm")
	_ = flow.Close()
	assertPostgresRestoreUsageAndAllocation(t, client.StableID, policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316})
	db := database.GetDB()
	if err := db.Table("clients").Where("stable_id = ?", client.StableID).Update("policy_multiplier", "0.5").Error; err != nil {
		t.Fatal(err)
	}
	oldSQL, err = (&ServerService{}).GetDb()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "later-policy-before-reset.dump"), oldSQL, 0600); err != nil {
		t.Fatal(err)
	}
	const callback = "test:pg-restore-pending-reset"
	installDeferredCommitFailure(t, db, "create", callback, "client_policy_resets", "pg_restore_pending_parent", "pg_restore_pending_child")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = ResetLocalClientPolicy(ctx, client.StableID, "pg-restore-original-reset")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") || currentXrayProcess().IsRunning() {
		t.Fatalf("not actual interrupted SQL commit: %v", err)
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(cfg.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := state.Journal.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 {
		t.Fatalf("missing original prepared operation: %v", err)
	}
	prepared, err := state.Journal.LookupResetPreparation(page[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Journal.LookupResetCompletion(page[0].RequestID); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("unexecuted reset acknowledged: %v", err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.StateFile, oldCore, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(oldSQL)}, false); err != nil {
		t.Fatal(err)
	}
	assertPostgresRestoreNativeIdentity(t, client, inbound, hostKey)
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil {
		t.Fatal("restored PG core not owned")
	}
	after, err := owner.state.Journal.LookupResetPreparation(prepared.RequestID)
	if err != nil || after != prepared {
		t.Fatalf("original preparation changed: %v", err)
	}
	done, err := owner.state.Journal.LookupResetCompletion(prepared.RequestID)
	if err != nil || done.PreparationDigest != authorityResetSnapshotDigest(prepared.Snapshot) {
		t.Fatalf("actual PG recovery did not complete original reset: %v", err)
	}
	stateAfter, err := owner.api.GetClient(ctx, client.StableID)
	if err != nil || stateAfter.Policy.MultiplierMicros != 500000 {
		t.Fatal("restored PostgreSQL later multiplier lost")
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 104, RawDownload: 204, BilledBytes: 316}) || account.WindowBaseline != 316 || account.WindowUsed != 0 {
		t.Fatalf("prepared PG reset lost boundary: %+v/%v", account, err)
	}
	flow, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "next")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err = owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 320}) || account.WindowBaseline != 316 || account.WindowUsed != 4 {
		t.Fatalf("restored PG reset payload incorrect: %+v/%v", account, err)
	}
}

func TestManagedAuthorityActualPostgresRestoreFailureKeepsOwnedRuntime(t *testing.T) {
	dir := setupPrivatePostgresRestoreDatabase(t)
	svc, inbound, client, _ := setupManagedActivationServiceFromDatabase(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, flow, "warm")
	_ = flow.Close()
	db := database.GetDB()
	if err := db.Create(&model.Setting{Key: "pg-restore-failure-marker", Value: "before-archive"}).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := (&ServerService{}).GetDb()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 1024 {
		t.Fatal("actual archive unexpectedly small")
	}
	damaged := raw[:len(raw)-64]
	archive := filepath.Join(dir, "real-truncated-transaction.dump")
	if err := os.WriteFile(archive, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "pg_restore", "--list", archive).Run(); err != nil {
		t.Fatal("damaged body did not preserve real archive preflight")
	}
	if err := db.Model(&model.Setting{}).Where("key = ?", "pg-restore-failure-marker").Update("value", "later-retained").Error; err != nil {
		t.Fatal(err)
	}
	boot := managedAuthorityForProcess(currentXrayProcess()).socketBoot
	err = (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader([]byte("PGDMPinvalid-real-archive"))}, false)
	if err == nil {
		t.Fatal("invalid archive accepted")
	}
	original := managedAuthorityForProcess(currentXrayProcess())
	if original == nil || !currentXrayProcess().IsRunning() || original.socketBoot != boot {
		t.Fatal("preflight rejection replaced the original owned runtime")
	}
	check, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	managedActivationEcho(t, check, "keep")
	_ = check.Close()
	err = (&ServerService{}).ImportDB(restoreUpload{bytes.NewReader(damaged)}, false)
	if err == nil || !strings.Contains(err.Error(), "pg_restore failed (database left unchanged)") {
		t.Fatalf("real transactional restore failure not reported: %v", err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil || !currentXrayProcess().IsRunning() || owner.socketBoot == boot {
		t.Fatal("actual failed restore did not restart the owned fallback core")
	}
	var marker model.Setting
	if err := database.GetDB().First(&marker, "key = ?", "pg-restore-failure-marker").Error; err != nil || marker.Value != "later-retained" {
		t.Fatalf("failed PG transaction changed current data: %+v/%v", marker, err)
	}
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 108, RawDownload: 208, BilledBytes: 332}) {
		t.Fatalf("failed real restore lost confirmed consumption: %+v/%v", account, err)
	}
	flow, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "next")
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err = owner.state.Journal.Account(client.StableID)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 112, RawDownload: 212, BilledBytes: 348}) {
		t.Fatalf("real fallback payload incorrect: %+v/%v", account, err)
	}
}

// These are synthetic business credentials, independent of Git or fixture DB
// credentials. The Tunnel traffic proves billing; SQL checks prove retention.
func seedPostgresRestoreNativeIdentity(t *testing.T, client *model.ClientRecord, inbound *model.Inbound) model.NativeSSHHostKey {
	t.Helper()
	client.UUID = "67b246eb-1f6e-4f4c-a789-f673ee812ccb"
	client.SnellPSK = "private-PG-restore-雪"
	client.MieruUsername = "pg-native-mieru"
	client.MieruPassword = "synthetic-mieru-restore"
	client.SSHUsername = "pg-native-ssh"
	client.SSHPassword = "synthetic-ssh-restore"
	client.SSHAuthorizedKeys = nativeSSHTestPublicKey(t)
	if err := database.GetDB().Model(client).Updates(map[string]any{"uuid": client.UUID, "snell_psk": client.SnellPSK, "mieru_username": client.MieruUsername, "mieru_password": client.MieruPassword, "ssh_username": client.SSHUsername, "ssh_password": client.SSHPassword, "ssh_authorized_keys": client.SSHAuthorizedKeys}).Error; err != nil {
		t.Fatal("could not seed private native credentials")
	}
	listener, _, err := (&InboundService{}).AddInbound(&model.Inbound{Tag: "pg-restore-private-ssh", Protocol: model.SSH, Port: 34571, Enable: false, Settings: `{"clients":[],"allowPassword":true}`})
	if err != nil {
		t.Fatal(err)
	}
	var key model.NativeSSHHostKey
	if err := database.GetDB().First(&key, "id = ?", listener.SSHHostKeyID).Error; err != nil {
		t.Fatal("missing business SSH host key")
	}
	signer, err := ssh.ParsePrivateKey([]byte(key.PrivateKeyPEM))
	if err != nil || key.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("invalid private fixture SSH host trust")
	}
	return key
}

func assertPostgresRestoreNativeIdentity(t *testing.T, client *model.ClientRecord, inbound *model.Inbound, expectedKey model.NativeSSHHostKey) {
	t.Helper()
	var restored model.ClientRecord
	if err := database.GetDB().First(&restored, "stable_id = ?", client.StableID).Error; err != nil {
		t.Fatal("restored stable client identity missing")
	}
	if restored.Id != client.Id || restored.UUID != client.UUID || restored.Email != client.Email || restored.SubID != client.SubID || restored.SnellPSK != client.SnellPSK || restored.MieruUsername != client.MieruUsername || restored.MieruPassword != client.MieruPassword || restored.SSHUsername != client.SSHUsername || restored.SSHPassword != client.SSHPassword || restored.SSHAuthorizedKeys != client.SSHAuthorizedKeys {
		t.Fatal("actual PostgreSQL restore changed canonical native identity or credentials")
	}
	var bindings []model.ClientInbound
	if err := database.GetDB().Where("client_id = ?", client.Id).Find(&bindings).Error; err != nil || len(bindings) != 1 || bindings[0].InboundId != inbound.Id {
		t.Fatal("actual PostgreSQL restore changed canonical Tunnel membership")
	}
	var restoredInbound model.Inbound
	if err := database.GetDB().First(&restoredInbound, inbound.Id).Error; err != nil || restoredInbound.StableID != inbound.StableID {
		t.Fatal("actual PostgreSQL restore changed resource UUID")
	}
	var key model.NativeSSHHostKey
	if err := database.GetDB().First(&key, "id = ?", expectedKey.ID).Error; err != nil || key.PrivateKeyPEM != expectedKey.PrivateKeyPEM || key.PublicKey != expectedKey.PublicKey || key.Fingerprint != expectedKey.Fingerprint {
		t.Fatal("actual PostgreSQL restore changed private business SSH host trust")
	}
	var listener model.Inbound
	if err := database.GetDB().First(&listener, "tag = ?", "pg-restore-private-ssh").Error; err != nil || listener.SSHHostKeyID != expectedKey.ID {
		t.Fatal("restored SSH listener lost its original host-key binding")
	}
}

func assertPostgresRestoreUsageAndAllocation(t *testing.T, id string, expected policyauthority.Usage) {
	t.Helper()
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil {
		t.Fatal("private actual core has no authority owner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := owner.state.Journal.Account(id)
	if err != nil || account.Usage != expected {
		t.Fatal("actual PostgreSQL fixture payload did not reach the literal billing boundary")
	}
	if account.HeldCapacity == 0 || account.HeldRemainder != 0 || account.FrozenBilled != 0 || account.WindowUsed+account.HeldCapacity > 10000 {
		t.Fatal("private actual grant exceeded retained quota or lost reservation")
	}
}
