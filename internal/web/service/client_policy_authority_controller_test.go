package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestAuthorityControllerRejectsWholeMalformedPageBeforeIssuance(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	api, _ := authorityExecutionCore(t, "malformed-demand-source", client)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", api)
	if err != nil {
		t.Fatal(err)
	}
	valid := &command.AuthorityRequest{RequestId: strings.Repeat("a", 32), ClientId: client, PolicyVersion: 1}
	page := &command.AuthorityRequests{InstanceId: controller.execution.boot.SourceID, BootId: controller.execution.boot.BootID, Requests: []*command.AuthorityRequest{valid, nil}}
	before, err := journal.Account(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.HandleRequests(ctx, page); err == nil {
		t.Fatal("malformed batch accepted")
	}
	after, err := journal.Account(client)
	if err != nil || after != before {
		t.Fatalf("malformed batch committed an earlier grant: %+v/%v", after, err)
	}
	page.Requests = []*command.AuthorityRequest{{RequestId: strings.Repeat("z", 32), ClientId: client, PolicyVersion: 1}}
	if err := controller.HandleRequests(ctx, page); err == nil {
		t.Fatal("non-hex request nonce accepted")
	}
}

func TestAuthorityControllerUnavailableAccountDoesNotBlockAnotherDemand(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	api, address := authorityExecutionCore(t, "independent-demand-source", client)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", api)
	if err != nil {
		t.Fatal(err)
	}
	page := &command.AuthorityRequests{InstanceId: controller.execution.boot.SourceID, BootId: controller.execution.boot.BootID, Requests: []*command.AuthorityRequest{
		{RequestId: strings.Repeat("b", 32), ClientId: "unconfigured-client", PolicyVersion: 1},
		{RequestId: strings.Repeat("a", 32), ClientId: client, PolicyVersion: 1},
	}}
	if err := controller.HandleRequests(ctx, page); !errors.Is(err, policyauthority.ErrJournal) {
		t.Fatalf("missing account result: %v", err)
	}
	if err := demandEndpointExchange(address); err != nil {
		t.Fatalf("unrelated request blocked valid payload: %v", err)
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := journal.Account(client)
	if err != nil || account.Usage.BilledBytes != 25 || account.HeldCapacity != 0 {
		t.Fatalf("independent demand settlement: %+v/%v", account, err)
	}
}

type authorityDemandFault struct {
	authorityDemandAPI
	page         *command.AuthorityRequests
	queued       *command.AuthorityRequests
	installFault error
	pauseFault   error
	settleClient string
	settleFault  error
}

func (f *authorityDemandFault) GetAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	if client == f.settleClient && f.settleFault != nil {
		return nil, f.settleFault
	}
	return f.authorityDemandAPI.GetAuthorityGrant(ctx, client, grant)
}

func (f *authorityDemandFault) PauseAuthorityGrant(ctx context.Context, client, grant string) (*command.ExecutionGrantState, error) {
	state, err := f.authorityDemandAPI.PauseAuthorityGrant(ctx, client, grant)
	if err == nil && f.pauseFault != nil {
		err, f.pauseFault = f.pauseFault, nil
		return nil, err
	}
	return state, err
}

func (f *authorityDemandFault) ReadAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding, limit uint32) (*command.AuthorityRequests, error) {
	if f.queued != nil {
		page := f.queued
		f.queued = nil
		return page, nil
	}
	page, err := f.authorityDemandAPI.ReadAuthorityRequests(ctx, binding, limit)
	if err == nil {
		f.page = proto.Clone(page).(*command.AuthorityRequests)
	}
	return page, err
}

func TestAuthorityControllerStalePendingDoesNotBlockFreshPayload(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	api, address := authorityExecutionCore(t, "pending-demand-source", client)
	fault := &authorityDemandFault{authorityDemandAPI: api}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", fault)
	if err != nil {
		t.Fatal(err)
	}
	stale := &command.AuthorityRequest{RequestId: strings.Repeat("b", 32), ClientId: client, PolicyVersion: 2}
	controller.pending[stale.RequestId] = stale
	fault.queued = &command.AuthorityRequests{InstanceId: controller.execution.boot.SourceID, BootId: controller.execution.boot.BootID, Requests: []*command.AuthorityRequest{
		{RequestId: strings.Repeat("a", 32), ClientId: client, PolicyVersion: 1},
	}}
	if err := controller.ProcessRequests(ctx); !errors.Is(err, policyauthority.ErrRequest) {
		t.Fatalf("stale pending failure was hidden: %v", err)
	}
	if err := demandEndpointExchange(address); err != nil {
		t.Fatalf("stale pending request blocked another valid payload: %v", err)
	}
	if len(controller.pending) != 0 {
		t.Fatal("an unissued stale nonce kept occupying the bounded retry queue")
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := journal.Account(client)
	if err != nil || account.Usage.BilledBytes != 25 || account.HeldCapacity != 0 {
		t.Fatalf("independent demand was not exactly settled: %+v/%v", account, err)
	}
}

func TestAuthorityControllerFailedSettlementDoesNotBlockAnotherRenewal(t *testing.T) {
	journal, first := authorityExecutionFixture(t)
	api, _ := authorityExecutionCore(t, "independent-renewal-source", first)
	second := model.ClientRecord{Email: "independent-renewal", StableID: "ffffffff-ffff-4fff-bfff-ffffffffffff", Enable: true}
	if err := database.GetDB().Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	account, err := journal.Account(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AddAccount(policyauthority.Seed{ClientID: second.StableID, Policy: account.Policy}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	policy := &clientpolicy.PolicyConfig{ClientId: second.StableID, Version: 1, Enabled: true, MultiplierMicros: 1500000, QuotaBytes: 100, UploadBytesPerSecond: 8192, DownloadBytesPerSecond: 8192, BurstBytes: 128}
	if err := api.Initialize(ctx, policy, &command.Usage{}); err != nil {
		t.Fatal(err)
	}
	fault := &authorityDemandFault{authorityDemandAPI: api}
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", fault)
	if err != nil {
		t.Fatal(err)
	}
	page := &command.AuthorityRequests{InstanceId: controller.execution.boot.SourceID, BootId: controller.execution.boot.BootID, Requests: []*command.AuthorityRequest{
		{RequestId: strings.Repeat("a", 32), ClientId: first, PolicyVersion: 1},
		{RequestId: strings.Repeat("b", 32), ClientId: second.StableID, PolicyVersion: 1},
	}}
	if err := controller.HandleRequests(ctx, page); err != nil {
		t.Fatal(err)
	}
	fault.settleClient, fault.settleFault = first, errors.New("first account checkpoint unavailable")
	if err := controller.SettleAndRenew(ctx); !errors.Is(err, fault.settleFault) {
		t.Fatalf("settlement failure was hidden: %v", err)
	}
	if controller.active[first].renewalSequence != 0 || controller.active[second.StableID].renewalSequence != 1 {
		t.Fatal("one unavailable checkpoint blocked an independent actual-core renewal")
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second.StableID} {
		account, err := journal.Account(id)
		if err != nil || account.HeldCapacity != 0 {
			t.Fatalf("independent grant failed to seal: %+v/%v", account, err)
		}
	}
}

func TestAuthorityControllerRetiredRatesResumePayloadWithoutReturningQuota(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	apiA, addressA := authorityExecutionCore(t, "retired-controller-source", client)
	apiB, addressB := authorityExecutionCore(t, "retired-controller-source", client)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	old, err := newAuthorityExecution(ctx, database.GetDB(), journal, "local", apiA)
	if err != nil {
		t.Fatal(err)
	}
	share := policyauthority.Direction{Rate: 8192, Burst: 128}
	grant, err := old.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: "retired-original", Capacity: 40, Upload: share, Download: share, LeaseDuration: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := demandEndpointExchange(addressA); err != nil {
		t.Fatal(err)
	}
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", apiB)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = controller.Stop(stop)
	})
	if err := demandEndpointExchange(addressB); err == nil {
		t.Fatal("replacement core overlapped the retired boot's rate shares")
	}
	timer := time.NewTimer(policyauthority.MaxLeaseDuration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := demandEndpointExchange(addressA); err == nil {
		t.Fatal("retired core still admitted payload after its execution interval")
	}
	if err := demandEndpointExchange(addressB); err != nil {
		t.Fatalf("expired retired shares blocked a replacement payload: %v", err)
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	retired, err := journal.Grant(grant.GrantID)
	if err != nil || retired.Sealed || !retired.RatesReleased {
		t.Fatalf("retired allocation was falsely sealed or kept its rates: %+v/%v", retired, err)
	}
	account, err := journal.Account(client)
	if err != nil || account.HeldCapacity != 40 || account.Usage != (policyauthority.Usage{RawUpload: 8, RawDownload: 7, BilledBytes: 25, Remainder: 100000}) || account.UploadHeld.Rate != 0 || account.DownloadHeld.Rate != 0 {
		t.Fatalf("retired rate recovery credited uncertain quota: %+v/%v", account, err)
	}
}

func TestAuthorityControllerRetiredRateProjectionFailureRetriesCommittedPage(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	apiA, _ := authorityExecutionCore(t, "retired-projection-source", client)
	apiB, address := authorityExecutionCore(t, "retired-projection-source", client)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	old, err := newAuthorityExecution(ctx, database.GetDB(), journal, "local", apiA)
	if err != nil {
		t.Fatal(err)
	}
	share := policyauthority.Direction{Rate: 8192, Burst: 128}
	grant, err := old.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: "retired-projection", Capacity: 40, Upload: share, Download: share, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", apiB)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(policyauthority.MaxLeaseDuration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	db := database.GetDB()
	injected := errors.New("retired rate projection failed after journal commit")
	const callback = "test:retired-rate-projection-fault"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "client_policy_authority_projections" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	if err := controller.reconcileRetiredClientRatesLocked(ctx, client); !errors.Is(err, injected) {
		t.Fatalf("lost projection acknowledgement was hidden: %v", err)
	}
	retired, err := journal.Grant(grant.GrantID)
	if err != nil || !retired.RatesReleased || retired.Sealed || controller.retirementCursor[client] != "" {
		t.Fatalf("failed SQL rolled back rates or advanced the cursor: %+v/%v", retired, err)
	}
	before, err := journal.Account(client)
	if err != nil || before.HeldCapacity != 40 || before.UploadHeld.Rate != 0 || before.DownloadHeld.Rate != 0 {
		t.Fatalf("retired SQL failure credited quota: %+v/%v", before, err)
	}
	if err := db.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := controller.reconcileRetiredClientRatesLocked(ctx, client); err != nil {
		t.Fatal(err)
	}
	_, projection := authorityProjection(t, client)
	after, err := journal.Account(client)
	if err != nil || after != before || projection != after {
		t.Fatalf("rate page retry changed its journal result: %+v/%+v/%v", projection, after, err)
	}
	page := &command.AuthorityRequests{InstanceId: controller.execution.boot.SourceID, BootId: controller.execution.boot.BootID, Requests: []*command.AuthorityRequest{{RequestId: strings.Repeat("a", 32), ClientId: client, PolicyVersion: 1}}}
	if err := controller.HandleRequests(ctx, page); err != nil {
		t.Fatal(err)
	}
	if err := demandEndpointExchange(address); err != nil {
		t.Fatal(err)
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	final, err := journal.Account(client)
	if err != nil || final.HeldCapacity != 40 || final.Usage.BilledBytes != 25 || final.Usage.Remainder != 100000 {
		t.Fatalf("recovered projection rebilled or returned uncertain quota: %+v/%v", final, err)
	}
}

func (f *authorityDemandFault) InstallAuthorityGrant(ctx context.Context, grant *command.ExecutionGrant) (*command.ExecutionGrantState, error) {
	state, err := f.authorityDemandAPI.InstallAuthorityGrant(ctx, grant)
	if err == nil && f.installFault != nil {
		err, f.installFault = f.installFault, nil
		return nil, err
	}
	return state, err
}

func demandEndpointExchange(address string) error {
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	payload := bytes.Repeat([]byte{0x59}, 5)
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if !bytes.Equal(payload, reply) {
		return errors.New("demand endpoint changed payload")
	}
	return nil
}

func TestAuthorityControllerDemandIssuesOnceAndSettlesRealPayload(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "lost-install-reply"}[lost], func(t *testing.T) {
			journal, client := authorityExecutionFixture(t)
			api, address := authorityExecutionCore(t, "demand-source", client)
			fault := &authorityDemandFault{authorityDemandAPI: api}
			injected := errors.New("controller lost grant installation reply")
			if lost {
				fault.installFault = injected
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", fault)
			if err != nil {
				t.Fatal(err)
			}
			delivered := make(chan error, 1)
			go func() { delivered <- demandEndpointExchange(address) }()
			err = controller.ProcessRequests(ctx)
			if lost {
				if !errors.Is(err, injected) {
					t.Fatalf("lost reply was not retained: %v", err)
				}
				if err := controller.HandleRequests(ctx, fault.page); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-delivered:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("demand did not authorize real traffic")
			}
			if err := controller.HandleRequests(ctx, fault.page); err != nil {
				t.Fatal(err)
			}
			if err := controller.SettleAndRenew(ctx); err != nil {
				t.Fatal(err)
			}
			account, err := journal.Account(client)
			if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 8, RawDownload: 7, BilledBytes: 25, Remainder: 100000}) || account.HeldCapacity != 74 || account.UploadHeld.Rate != 8192 || account.DownloadHeld.Rate != 8192 {
				t.Fatalf("controller duplicated allocation or multiplier: %+v/%v", account, err)
			}
		})
	}
}

func TestAuthorityControllerBackgroundRenewsAndStopSealsExactUsage(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	api, address := authorityExecutionCore(t, "background-source", client)
	startup, startupCancel := context.WithTimeout(context.Background(), time.Second)
	controller, err := newAuthorityController(startup, database.GetDB(), journal, "local", api)
	startupCancel()
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = controller.Stop(ctx)
	})
	if err := demandEndpointExchange(address); err != nil {
		t.Fatal(err)
	}
	// This exceeds the original lease while the startup context is cancelled.
	time.Sleep(policyauthority.MaxLeaseDuration + time.Second)
	if err := demandEndpointExchange(address); err != nil {
		t.Fatalf("background controller failed lease renewal: %v", err)
	}
	stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := controller.Stop(stop); err != nil {
		t.Fatal(err)
	}
	account, err := journal.Account(client)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 13, RawDownload: 12, BilledBytes: 40, Remainder: 100000}) || account.HeldCapacity != 0 || account.UploadHeld.Rate != 0 || account.DownloadHeld.Rate != 0 {
		t.Fatalf("stop lost usage or credited unsealed capacity: %+v/%v", account, err)
	}
	if err := controller.Start(); err == nil {
		t.Fatal("stopped controller resumed stale core authority")
	}
}

func TestAuthorityControllerStopRecoversLostInstallWithoutAnotherAllocation(t *testing.T) {
	journal, client := authorityExecutionFixture(t)
	api, address := authorityExecutionCore(t, "lost-stop-source", client)
	injected := errors.New("lost installation before stop")
	fault := &authorityDemandFault{authorityDemandAPI: api, installFault: injected}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", fault)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan error, 1)
	go func() { delivered <- demandEndpointExchange(address) }()
	if err := controller.ProcessRequests(ctx); !errors.Is(err, injected) {
		t.Fatalf("lost install did not fail control call: %v", err)
	}
	if err := <-delivered; err != nil {
		t.Fatal(err)
	}
	before, err := journal.Account(client)
	if err != nil {
		t.Fatal(err)
	}
	next := before.Policy
	next.Version++
	if _, err := journal.ChangePolicy(policyauthority.ChangeRequest{Identity: journal.Identity(), ClientID: client, RequestID: "lost-install-policy-change", ExpectedVersion: before.Policy.Version, Policy: next}); err != nil {
		t.Fatal(err)
	}
	if err := controller.ProcessRequests(ctx); !errors.Is(err, policyauthority.ErrRequest) {
		t.Fatalf("issued obsolete request failure was hidden: %v", err)
	}
	if len(controller.pending) != 1 {
		t.Fatal("an issued lost reply was discarded when its policy became obsolete")
	}
	if err := controller.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := journal.Account(client)
	if err != nil || account.Usage != (policyauthority.Usage{RawUpload: 8, RawDownload: 7, BilledBytes: 25, Remainder: 100000}) || account.HeldCapacity != 0 {
		t.Fatalf("stop failed to settle unacknowledged installation: %+v/%v", account, err)
	}
	if err := controller.HandleRequests(ctx, fault.page); err == nil {
		t.Fatal("stopped controller installed a replay")
	}
	if err := controller.SettleAndRenew(ctx); err == nil {
		t.Fatal("stopped controller renewed a lease")
	}
}

func TestAuthorityControllerLiveRefillSurvivesLostPauseAndInstallReplies(t *testing.T) {
	for _, lost := range []string{"none", "pause", "install", "both"} {
		t.Run(lost, func(t *testing.T) {
			journal, client := authorityExecutionFixture(t)
			api, address := authorityExecutionCore(t, "lost-refill-source", client)
			fault := &authorityDemandFault{authorityDemandAPI: api}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			controller, err := newAuthorityController(ctx, database.GetDB(), journal, "local", fault)
			if err != nil {
				t.Fatal(err)
			}
			share := policyauthority.Direction{Rate: 8192, Burst: 128}
			grant, err := controller.execution.Authorize(ctx, authorityAllocation{ClientID: client, RequestID: "initial-small-allocation", Capacity: 30, Upload: share, Download: share, LeaseDuration: policyauthority.MaxLeaseDuration})
			if err != nil {
				t.Fatal(err)
			}
			controller.active[client] = &controllerGrant{grantID: grant.GrantID}
			if lost == "pause" || lost == "both" {
				fault.pauseFault = errors.New("lost successful pause reply")
			}
			if lost == "install" || lost == "both" {
				fault.installFault = errors.New("lost successful refill install reply")
			}
			if err := controller.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = controller.Stop(stop)
			})
			conn, err := net.DialTimeout("tcp4", address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			for _, size := range []int{8, 5} {
				payload := bytes.Repeat([]byte{0x68}, size)
				if _, err := conn.Write(payload); err != nil {
					t.Fatal(err)
				}
				reply := make([]byte, size)
				if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(payload, reply) {
					t.Fatalf("live refill %s: %v", lost, err)
				}
			}
			if err := controller.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			a, err := journal.Account(client)
			if err != nil || a.Usage != (policyauthority.Usage{RawUpload: 16, RawDownload: 15, BilledBytes: 49, Remainder: 100000}) || a.HeldCapacity != 0 {
				t.Fatalf("lost refill reply changed accounting: %+v/%v", a, err)
			}
		})
	}
}
