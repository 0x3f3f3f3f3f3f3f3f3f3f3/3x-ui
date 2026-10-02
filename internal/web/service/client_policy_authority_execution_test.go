package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func authorityExecutionCore(t *testing.T, node, client string) (*panelxray.ClientPolicyAPI, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "authority-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained %s execution fixture: %s", node, dir)
	state, socket := filepath.Join(dir, "state.db"), filepath.Join(dir, "control.sock")
	if err := clientpolicy.CreateStore(state, node); err != nil {
		t.Fatal(err)
	}
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"stateFile":%q,"instanceId":%q,"policies":[{"clientId":%q,"version":1,"enabled":true,"multiplierMicros":1500000,"quotaBytes":100,"uploadBytesPerSecond":8192,"downloadBytesPerSecond":8192,"burstBytes":128}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, state, node, client, port, echo.Addr().(*net.TCPAddr).Port, client)
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the newly built grant-capable core")
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "core.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var childErr error
	go func() { childErr = cmd.Wait(); _ = logFile.Close(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var api *panelxray.ClientPolicyAPI
	for {
		api, err = panelxray.DialClientPolicy(ctx, socket, node)
		if err == nil {
			break
		}
		select {
		case <-done:
			t.Fatalf("test core exited: %v (log retained in %s)", childErr, dir)
		case <-ctx.Done():
			t.Fatalf("test core unavailable: %v (log retained in %s)", err, dir)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Cleanup(func() { _ = api.Close() })
	return api, address
}

type authorityReplyFault struct {
	authorityCoreAPI
	installFault, renewFault error
	renewals                 []*command.AuthorityRenewalRequest
}

func (f *authorityReplyFault) InstallAuthorityGrant(ctx context.Context, grant *command.ExecutionGrant) (*command.ExecutionGrantState, error) {
	state, err := f.authorityCoreAPI.InstallAuthorityGrant(ctx, grant)
	if err == nil && f.installFault != nil {
		err, f.installFault = f.installFault, nil
		return nil, err
	}
	return state, err
}

func (f *authorityReplyFault) RenewAuthorityGrant(ctx context.Context, request *command.AuthorityRenewalRequest) error {
	f.renewals = append(f.renewals, proto.Clone(request).(*command.AuthorityRenewalRequest))
	err := f.authorityCoreAPI.RenewAuthorityGrant(ctx, request)
	if err == nil && f.renewFault != nil {
		err, f.renewFault = f.renewFault, nil
	}
	return err
}

func TestAuthorityExecutionLostRepliesRetainAllocationAndExactReceipts(t *testing.T) {
	for _, failure := range []string{"issue-projection", "install-reply", "seal-projection", "renew-reply"} {
		t.Run(failure, func(t *testing.T) {
			j, client := authorityExecutionFixture(t)
			api, address := authorityExecutionCore(t, "fault-node", client)
			injected := errors.New("injected lost " + failure)
			fault := &authorityReplyFault{authorityCoreAPI: api}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			a, err := newAuthorityExecution(ctx, database.GetDB(), j, "fault-node", fault)
			if err != nil {
				t.Fatal(err)
			}
			share := policyauthority.Direction{Rate: 4096, Burst: 64}
			intent := authorityAllocation{ClientID: client, RequestID: "retry", Capacity: 40, Upload: share, Download: share, LeaseDuration: 3 * time.Second}
			db := database.GetDB()
			if failure == "issue-projection" {
				name := "authority_execution_issue_fault"
				if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "client_policy_authority_projections" {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
				grant, err := a.Authorize(ctx, intent)
				if !errors.Is(err, injected) || grant != (policyauthority.Grant{}) {
					t.Fatalf("uncertain SQL reply: %+v/%v", grant, err)
				}
				retained, err := j.LookupRequest(a.boot.NodeID, client, intent.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := api.GetAuthorityGrant(ctx, client, retained.GrantID); err == nil {
					t.Fatal("SQL failure admitted an unprojected grant")
				}
				if err := db.Callback().Create().Remove(name); err != nil {
					t.Fatal(err)
				}
			} else if failure == "install-reply" {
				fault.installFault = injected
				grant, err := a.Authorize(ctx, intent)
				if !errors.Is(err, injected) || grant != (policyauthority.Grant{}) {
					t.Fatalf("uncertain core reply: %+v/%v", grant, err)
				}
			}
			grant, err := a.Authorize(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			account, err := j.Account(client)
			if err != nil || account.HeldCapacity != 40 || account.Revision != 2 {
				t.Fatalf("retry allocated twice: %+v/%v", account, err)
			}
			authorityEndpointExchange(t, address, 5)
			if failure == "renew-reply" {
				fault.renewFault = injected
				if err := a.Renew(ctx, grant.GrantID, 1, 3*time.Second); !errors.Is(err, injected) {
					t.Fatalf("lost renewal reply: %v", err)
				}
				if err := a.Renew(ctx, grant.GrantID, 1, 3*time.Second); err != nil {
					t.Fatalf("exact renewal retry failed: %v", err)
				}
				if len(fault.renewals) != 2 || !proto.Equal(fault.renewals[0], fault.renewals[1]) {
					t.Fatal("renewal retry changed its original deadline challenge")
				}
				if err := a.Renew(ctx, grant.GrantID, 1, 2*time.Second); !errors.Is(err, policyauthority.ErrRequest) {
					t.Fatalf("changed renewal intent accepted: %v", err)
				}
			}
			if failure == "seal-projection" {
				name := "authority_execution_seal_fault"
				if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "client_policy_authority_projections" {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
				if err := a.Settle(ctx, grant.GrantID, true, false); !errors.Is(err, injected) {
					t.Fatalf("lost seal projection: %v", err)
				}
				retained, err := j.Grant(grant.GrantID)
				if err != nil || !retained.Sealed || retained.Usage != (policyauthority.Usage{RawUpload: 5, RawDownload: 5, BilledBytes: 15}) {
					t.Fatalf("lost exact committed seal: %+v/%v", retained, err)
				}
				if err := db.Callback().Update().Remove(name); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Settle(ctx, grant.GrantID, true, false); err != nil {
				t.Fatal(err)
			}
			_, projected := authorityProjection(t, client)
			account, err = j.Account(client)
			if err != nil || projected != account || account.HeldCapacity != 0 || account.Usage != (policyauthority.Usage{RawUpload: 8, RawDownload: 7, BilledBytes: 25, Remainder: 100000}) {
				t.Fatalf("retry doubled or lost billed usage: %+v/%+v/%v", projected, account, err)
			}
		})
	}
}

func TestAuthorityExecutionRenewalRejectsContradictorySQLBeforeCoreMutation(t *testing.T) {
	j, client := authorityExecutionFixture(t)
	api, _ := authorityExecutionCore(t, "renew-node", client)
	fault := &authorityReplyFault{authorityCoreAPI: api}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := newAuthorityExecution(ctx, database.GetDB(), j, "renew-node", fault)
	if err != nil {
		t.Fatal(err)
	}
	share := policyauthority.Direction{Rate: 4096, Burst: 64}
	grant, err := a.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: "first", Capacity: 40, Upload: share, Download: share, LeaseDuration: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.ClientPolicyAuthorityProjection{}).Where("client_id = ?", client).Update("revision", 999).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Renew(ctx, grant.GrantID, 1, 3*time.Second); !errors.Is(err, ErrClientPolicyLedger) || len(fault.renewals) != 0 {
		t.Fatalf("contradictory SQL extended an active core grant: %v/%d", err, len(fault.renewals))
	}
}

func authorityExecutionFixture(t *testing.T) (*policyauthority.Journal, string) {
	t.Helper()
	setupPolicyLedgerDB(t)
	client := model.ClientRecord{Email: "actual-authority", Enable: true}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "authority-issuer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained issuance fixture: %s", dir)
	direction := policyauthority.Direction{Rate: 8192, Burst: 128}
	j, _, err := policyauthority.Create(filepath.Join(dir, "journal.db"), []policyauthority.Seed{{ClientID: client.StableID, Policy: policyauthority.Policy{WindowID: "window", Version: 1, QuotaBytes: 100, Upload: direction, Download: direction}, Usage: policyauthority.Usage{RawUpload: 3, RawDownload: 2, BilledBytes: 10, Remainder: 100000}, WindowUsed: 10, WindowRemainder: 100000}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, client.StableID
}

func authorityEndpointExchange(t *testing.T, address string, size int) {
	t.Helper()
	conn, err := net.Dial("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x39}, size)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, size)
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("actual granted endpoint: %v", err)
	}
}

func TestAuthorityExecutionTwoCoresShareQuotaRatesAndExactBilling(t *testing.T) {
	j, client := authorityExecutionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apiA, addressA := authorityExecutionCore(t, "node-a", client)
	apiB, addressB := authorityExecutionCore(t, "node-b", client)
	a, err := newAuthorityExecution(ctx, database.GetDB(), j, "node-a", apiA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newAuthorityExecution(ctx, database.GetDB(), j, "node-b", apiB)
	if err != nil {
		t.Fatal(err)
	}
	share := policyauthority.Direction{Rate: 4096, Burst: 64}
	intent := authorityAllocation{ClientID: client, RequestID: "first", Capacity: 40, Upload: share, Download: share, LeaseDuration: 3 * time.Second}
	ga, err := a.Authorize(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := b.Authorize(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if ga.GrantID == gb.GrantID {
		t.Fatal("two cores received the same allocation")
	}
	authorityEndpointExchange(t, addressA, 7)
	authorityEndpointExchange(t, addressB, 5)
	if err := a.Settle(ctx, ga.GrantID, false, false); err != nil {
		t.Fatal(err)
	}
	if err := b.Settle(ctx, gb.GrantID, false, false); err != nil {
		t.Fatal(err)
	}
	account, err := j.Account(client)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 15, RawDownload: 14, BilledBytes: 46, Remainder: 100000}) || account.HeldCapacity != 44 || account.UploadHeld != (policyauthority.Direction{Rate: 8192, Burst: 128}) {
		t.Fatalf("aggregate execution accounting/bounds: %+v/%v", account, err)
	}
	if err := a.Settle(ctx, ga.GrantID, true, false); err != nil {
		t.Fatal(err)
	}
	intent.RequestID, intent.Capacity = "replacement", 29
	if _, err := a.Authorize(ctx, intent); !errors.Is(err, policyauthority.ErrCapacity) {
		t.Fatalf("two nodes exceeded global budget: %v", err)
	}
	intent.Capacity = 28
	if _, err := a.Authorize(ctx, intent); err != nil {
		t.Fatal(err)
	}
	_, projection := authorityProjection(t, client)
	account, err = j.Account(client)
	if err != nil || projection != account || account.WindowUsed != 46 || account.WindowRemainder != 100000 || account.HeldCapacity != 53 {
		t.Fatalf("SQL projection lost global allocation: %+v/%+v/%v", projection, account, err)
	}
}
