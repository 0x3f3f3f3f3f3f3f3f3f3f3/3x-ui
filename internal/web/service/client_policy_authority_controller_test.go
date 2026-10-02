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
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
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
	installFault error
}

func (f *authorityDemandFault) ReadAuthorityRequests(ctx context.Context, binding *command.AuthorityBinding, limit uint32) (*command.AuthorityRequests, error) {
	page, err := f.authorityDemandAPI.ReadAuthorityRequests(ctx, binding, limit)
	if err == nil {
		f.page = proto.Clone(page).(*command.AuthorityRequests)
	}
	return page, err
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
