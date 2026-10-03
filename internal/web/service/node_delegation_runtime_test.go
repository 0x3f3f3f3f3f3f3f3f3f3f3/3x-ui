package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

func setupDelegatedManagedService(t *testing.T) (*XrayService, *model.Inbound, *model.ClientRecord, panelruntime.NodeExecutionRole) {
	t.Helper()
	if os.Getenv("XRAY_E2E_BINARY") == "" {
		t.Skip("set XRAY_E2E_BINARY; mandatory acceptance requires actual core PASS")
	}
	setupPolicyLedgerDB(t)
	svc, inbound, client, _ := setupManagedActivationServiceWithUsage(t, 0, 0)
	request := panelruntime.NodeDelegationRequest{AuthorityID: "coordinator-authority", Generation: 3, NodeID: "delegated-node-a"}
	result, err := (&ClientPolicyNodeService{}).ConfigureDelegation(context.Background(), request)
	if err != nil || result == nil {
		t.Fatalf("fresh delegation setup failed: %+v/%v", result, err)
	}
	return svc, inbound, client, request.Role()
}

func assertDelegatedTunnelHasNoBusinessBytes(t *testing.T, inbound *model.Inbound) {
	t.Helper()
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", inbound.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if err := flow.SetDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, _ = flow.Write([]byte("held-without-grant"))
	buffer := make([]byte, 32)
	if n, err := flow.Read(buffer); n != 0 || err == nil {
		t.Fatalf("delegated node forwarded%d bytes without a grant", n)
	}
}

func TestManagedDelegatedNodeStartsWithoutLocalIssuer(t *testing.T) {
	svc, inbound, client, role := setupDelegatedManagedService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	owner := managedAuthorityForProcess(currentXrayProcess())
	if owner == nil || owner.controller != nil {
		t.Fatal("delegated bootstrap started a local quota issuer")
	}
	if owner.api == nil || owner.state.Role != role {
		t.Fatal("delegated core has no retained owned API/role")
	}
	assertDelegatedTunnelHasNoBusinessBytes(t, inbound)
	account, err := owner.state.Journal.Account(client.StableID)
	if err != nil || account.HeldCapacity != 0 || account.Usage.RawUpload != 0 || account.Usage.RawDownload != 0 {
		t.Fatalf("delegated demand acquired a local quota allocation or usage: %+v/%v", account, err)
	}
	if err := owner.Checkpoint(context.Background()); err != nil {
		t.Fatalf("read-only delegated checkpoint failed: %v", err)
	}
	first, err := (&ClientPolicyNodeService{}).DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || first == nil || first.ExecutionRole == nil || *first.ExecutionRole != role {
		t.Fatalf("actual discovery omitted the delegated role: %+v/%v", first, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	retry, err := (&ClientPolicyNodeService{}).ConfigureDelegation(ctx, panelruntime.NodeDelegationRequest{AuthorityID: role.AuthorityID, Generation: role.Generation, NodeID: role.NodeID})
	if err != nil || retry == nil || retry.InstanceID != first.Capabilities.InstanceId || retry.Role != role {
		t.Fatalf("live exact configuration retry failed: %+v/%v", retry, err)
	}
	first.ExecutionRole.AuthorityID = "response-copy-only"
	copied, err := (&ClientPolicyNodeService{}).DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || copied.ExecutionRole == nil || *copied.ExecutionRole != role {
		t.Fatal("caller changed the retained execution role")
	}
}

func TestManagedDelegatedNodeRestartRetainsRole(t *testing.T) {
	svc, inbound, _, role := setupDelegatedManagedService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	node := &ClientPolicyNodeService{}
	first, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || first == nil || first.ExecutionRole == nil || *first.ExecutionRole != role {
		t.Fatalf("delegated boot discovery failed: %+v/%v", first, err)
	}
	bound := panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: first.Capabilities.InstanceId, ExpectedBootID: first.Capabilities.BootId}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if result, err := node.DiscoverAuthority(context.Background(), bound); err == nil || result != nil {
		t.Fatal("replacement delegated core accepted old boot")
	}
	fresh, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || fresh == nil || fresh.ExecutionRole == nil || *fresh.ExecutionRole != role || fresh.Capabilities.InstanceId != bound.ExpectedInstanceID || fresh.Capabilities.BootId == bound.ExpectedBootID {
		t.Fatalf("restart replaced pinned role/source or retained boot: %+v/%v", fresh, err)
	}
	assertDelegatedTunnelHasNoBusinessBytes(t, inbound)
	t.Run("restore-admission", func(t *testing.T) {
		lease, err := database.BeginRestore()
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		if result, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{}); err == nil || result != nil {
			t.Fatal("delegated discovery crossed SQL restore admission")
		}
	})
	owner := managedAuthorityForProcess(currentXrayProcess())
	t.Run("foreign-process-reference", func(t *testing.T) {
		restore := SetXrayProcessForTest(nil)
		defer restore()
		if result, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{}); err == nil || result != nil {
			t.Fatal("foreign delegated owner produced discovery")
		}
	})
	t.Run("changed-private-socket", func(t *testing.T) {
		retained := owner.socketPath + ".delegated-original"
		if err := os.Rename(owner.socketPath, retained); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(retained, owner.socketPath); err != nil {
				t.Fatal(err)
			}
		}()
		listener, err := net.Listen("unix", owner.socketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if result, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{}); err == nil || result != nil {
			t.Fatal("changed delegated private socket produced discovery")
		}
	})
	t.Run("stopped-core", func(t *testing.T) {
		if err := svc.StopXray(); err != nil {
			t.Fatal(err)
		}
		if result, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{}); err == nil || result != nil {
			t.Fatal("stopped delegated core produced discovery")
		}
		if err := svc.RestartXray(true); err != nil {
			t.Fatal(err)
		}
	})
	owner = managedAuthorityForProcess(currentXrayProcess())
	path := filepath.Join(filepath.Dir(owner.config.StateFile), "authority", "authority.json")
	if err := os.Rename(path, path+".retained-role"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err == nil {
		t.Fatal("missing delegated role silently restarted local allocation")
	}
}

func TestManagedDelegatedNodeRejectsLocalResetBeforeCapture(t *testing.T) {
	svc, inbound, client, _ := setupDelegatedManagedService(t)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	process := currentXrayProcess()
	owner := managedAuthorityForProcess(process)
	if err := ResetLocalClientPolicy(context.Background(), client.StableID, "delegated-local-reset"); err == nil {
		t.Fatal("delegated node accepted an autonomous local reset")
	}
	page, err := owner.state.Journal.ResetOperationPage("", 1)
	if err != nil || len(page) != 0 {
		t.Fatalf("unsupported local reset captured durable work: %d/%v", len(page), err)
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientPolicyReset{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unsupported local reset changed SQL windows: %d/%v", count, err)
	}
	if !process.IsRunning() || managedAuthorityForProcess(process) != owner {
		t.Fatal("unsupported reset interrupted the delegated owner")
	}
	if err := runAuthorityResetCapture(context.Background(), "delegated-batch-reset", "", &model.ClientTrafficResetBatch{}, func(*gorm.DB) error {
		t.Fatal("delegated batch reset reached SQL selection")
		return nil
	}, func(model.ClientTrafficResetBatch) error { return nil }); err == nil {
		t.Fatal("delegated node captured an autonomous batch reset")
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("rejected reset prevented delegated restart: %v", err)
	}
	assertDelegatedTunnelHasNoBusinessBytes(t, inbound)
	t.Run("stopped-node", func(t *testing.T) {
		owner := managedAuthorityForProcess(currentXrayProcess())
		dir := filepath.Join(filepath.Dir(owner.config.StateFile), "authority")
		if err := svc.StopXray(); err != nil {
			t.Fatal(err)
		}
		if err := ResetLocalClientPolicy(context.Background(), client.StableID, "delegated-stopped-reset"); err == nil {
			t.Fatal("stopped delegated node accepted a local reset")
		}
		state, err := openAuthorityState(dir)
		if err != nil {
			t.Fatal(err)
		}
		page, err := state.Journal.ResetOperationPage("", 1)
		closeErr := state.Journal.Close()
		if err != nil || closeErr != nil || len(page) != 0 {
			t.Fatalf("stopped local reset captured work: %d/%v/%v", len(page), err, closeErr)
		}
		if err := svc.RestartXray(true); err != nil {
			t.Fatalf("stopped reset prevented delegated restart: %v", err)
		}
		assertDelegatedTunnelHasNoBusinessBytes(t, inbound)
	})
}
