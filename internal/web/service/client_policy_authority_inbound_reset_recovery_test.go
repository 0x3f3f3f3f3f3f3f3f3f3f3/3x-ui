package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

// Missing protected stamp projection leaves the restored original resource at
// zero; numeric-ID projection would also overwrite the replacement resource.
func TestManagedAuthorityInboundResetColdRecoveryPreservesOriginalResources(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	var extra []model.Inbound
	for i := 0; i < 2; i++ {
		row := *tunnel
		row.Id, row.StableID, row.Enable, row.Port, row.Tag = 0, "", false, tunnel.Port+1+i, fmt.Sprintf("cold-original-%d", i)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		extra = append(extra, row)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const request = "inbound-cold-original-all"
	if err := (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, -1, request); err != nil {
		t.Fatal(err)
	}
	var reset model.ClientPolicyReset
	if err := db.First(&reset, "client_id = ?", client.StableID).Error; err != nil {
		t.Fatal(err)
	}
	if reset.RawUpload != 104 || reset.RawDownload != 204 || reset.BilledBytes != 316 {
		t.Fatalf("wrong original boundary: %+v", reset)
	}
	var original model.Inbound
	if err := db.First(&original, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	stamp := original.LastTrafficResetTime
	if stamp <= 0 {
		t.Fatal("original SQL stamp missing")
	}
	managedActivationEcho(t, flow, "next")
	owner := managedAuthorityForProcess(currentXrayProcess())
	if err := owner.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	f := authorityExecutionRecoveryFixture{config: owner.config, client: *client}
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(f.config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	key := authorityResetRequestKey(request)
	f.capture, err = state.Journal.LookupResetOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	f.prepared, err = state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatal(err)
	}
	f.completed, err = state.Journal.LookupResetCompletion(key)
	if err != nil {
		t.Fatal(err)
	}
	account, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: "local", SourceID: f.config.InstanceID, BootID: "inbound-cold-held-boot"}
	if err := state.Journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	f.grant, err = state.Journal.Issue(policyauthority.Request{Binding: policyauthority.Binding{Identity: state.Journal.Identity(), NodeBoot: boot, ClientID: client.StableID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: "inbound-cold-held40", ChallengeID: "inbound-cold-held-challenge", Capacity: 40, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.account, err = state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate restored SQL acknowledgements, retaining durable execution state.
	if err := db.Where("request_id = ?", request).Delete(&model.ClientTrafficResetBatch{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client_id = ?", client.StableID).Delete(&model.ClientPolicyReset{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(tunnel).Updates(map[string]any{"last_traffic_reset_time": 0, "remark": "later original remark", "up": 701, "down": 907}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&extra[0]).Updates(map[string]any{"last_traffic_reset_time": stamp + 1000, "tag": "later-original-tag", "remark": "later preserved configuration"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&extra[1]).Error; err != nil {
		t.Fatal(err)
	}
	replacement := extra[1]
	replacement.StableID, replacement.LastTrafficResetTime, replacement.Remark = "", 23, "replacement resource"
	if err := db.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	added := replacement
	added.Id, added.StableID, added.Tag, added.LastTrafficResetTime = 0, "", "new-after-original-reset", 29
	if err := db.Create(&added).Error; err != nil {
		t.Fatal(err)
	}
	// Startup/reset legitimately maintains derived embedded client mirrors.
	// Compare persisted configuration bytes immediately before cold projection.
	var beforeOriginal, beforeLater model.Inbound
	if err := db.First(&beforeOriginal, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&beforeLater, extra[0].Id).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := recoverAuthorityDesiredState(ctx, &f.config); err != nil {
			t.Fatal(err)
		}
		current := model.Inbound{}
		if err := db.First(&current, tunnel.Id).Error; err != nil {
			t.Fatal(err)
		}
		if current.LastTrafficResetTime != stamp || current.Up != 701 || current.Down != 907 || current.Remark != "later original remark" || current.Settings != beforeOriginal.Settings {
			t.Fatalf("cold recovery lost original stamp or changed later fields: %+v", current)
		}
		current = model.Inbound{}
		if err := db.First(&current, extra[0].Id).Error; err != nil {
			t.Fatal(err)
		}
		if current.LastTrafficResetTime != stamp+1000 || current.Tag != "later-original-tag" || current.Remark != "later preserved configuration" || current.Settings != beforeLater.Settings {
			t.Fatalf("cold recovery rewound later resource: %+v", current)
		}
		for _, want := range []model.Inbound{replacement, added} {
			current = model.Inbound{}
			if err := db.First(&current, want.Id).Error; err != nil {
				t.Fatal(err)
			}
			if current.StableID != want.StableID || current.LastTrafficResetTime != want.LastTrafficResetTime || current.Settings != want.Settings {
				t.Fatalf("old effects transferred to new resource: %+v", current)
			}
		}
		var restored model.ClientPolicyReset
		if err := db.First(&restored, "client_id = ?", client.StableID).Error; err != nil {
			t.Fatal(err)
		}
		if restored.RawUpload != 104 || restored.RawDownload != 204 || restored.BilledBytes != 316 {
			t.Fatalf("recovery recomputed reset boundary: %+v", restored)
		}
		assertAuthorityExecutionRecoveryUnchanged(t, f)
	}
}

func TestManagedAuthorityInboundResetCommitFailureRecoversOriginalStamp(t *testing.T) {
	svc, tunnel, client, _ := setupManagedActivationService(t)
	db := database.GetDB()
	if db.Dialector.Name() == "sqlite" {
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
		var enabled int
		if err := db.Raw("PRAGMA foreign_keys").Scan(&enabled).Error; err != nil || enabled != 1 {
			t.Fatalf("actual FK enforcement absent: %d/%v", enabled, err)
		}
	}
	const callback = "test:inbound-reset-deferred-commit"
	installDeferredCommitFailure(t, db, "create", callback, "client_policy_resets", "inbound_stamp_commit_parent", "inbound_stamp_commit_child")
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	managedActivationEcho(t, flow, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process := currentXrayProcess()
	config := managedAuthorityForProcess(process).config
	const request = "inbound-stamp-actual-commit-failure"
	err = (&ClientService{}).ResetAllClientTrafficsWithRequest(ctx, &InboundService{}, tunnel.Id, request)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") || process.IsRunning() {
		t.Fatalf("not actual failed commit and stopped core: %v/%t", err, process.IsRunning())
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var current model.Inbound
	if err := db.First(&current, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	if current.LastTrafficResetTime != 0 {
		t.Fatal("failed SQL commit retained stamp")
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	key := authorityResetRequestKey(request)
	capture, err := state.Journal.LookupResetOperation(key)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := state.Journal.LookupResetPreparation(key)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeAuthorityResetPreparation(prepared, capture, state.Journal, state.SourceID)
	if err != nil || len(snapshot.InboundStamps) != 1 || snapshot.InboundStamps[0].StableID != tunnel.StableID || snapshot.Resets[0].BilledBytes != 316 {
		t.Fatalf("lost exact prepared stamp/reset: %+v/%v", snapshot, err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("failed commit gained completion: %v", err)
	}
	before, err := state.Journal.Account(client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverAuthorityDesiredState(ctx, &config); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&current, tunnel.Id).Error; err != nil {
		t.Fatal(err)
	}
	if current.LastTrafficResetTime != snapshot.ResetAt {
		t.Fatalf("failed-commit cold recovery lost original stamp: %d/%d", current.LastTrafficResetTime, snapshot.ResetAt)
	}
	state, err = openAuthorityState(filepath.Join(filepath.Dir(config.StateFile), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	after, err := state.Journal.Account(client.StableID)
	if err != nil || after != before {
		t.Fatalf("metadata recovery changed account: %v", err)
	}
	if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
		t.Fatalf("metadata recovery acknowledged execution: %v", err)
	}
}
