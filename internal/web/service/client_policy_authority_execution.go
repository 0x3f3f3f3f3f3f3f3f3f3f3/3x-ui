package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type authorityCoreAPI interface {
	Capabilities() *command.Capabilities
	BindAuthority(context.Context, *command.AuthorityBinding) error
	AuthorityChallenge(context.Context) (*command.AuthorityChallenge, error)
	InstallAuthorityGrant(context.Context, *command.ExecutionGrant) (*command.ExecutionGrantState, error)
	GetAuthorityGrant(context.Context, string, string) (*command.ExecutionGrantState, error)
	SealAuthorityGrant(context.Context, string, string) (*command.ExecutionGrantState, error)
	PauseAuthorityGrant(context.Context, string, string) (*command.ExecutionGrantState, error)
	RenewAuthorityGrant(context.Context, *command.AuthorityRenewalRequest) error
}

type authorityAllocation struct {
	ClientID, RequestID string
	Capacity            uint64
	Upload, Download    policyauthority.Direction
	LeaseDuration       time.Duration
}

type authorityExecution struct {
	mu       sync.Mutex
	db       *gorm.DB
	journal  *policyauthority.Journal
	boot     policyauthority.NodeBoot
	api      authorityCoreAPI
	renewals map[string]*command.AuthorityRenewalRequest
}

func newAuthorityExecution(ctx context.Context, db *gorm.DB, journal *policyauthority.Journal, nodeID string, api authorityCoreAPI) (*authorityExecution, error) {
	if ctx == nil || db == nil || journal == nil || api == nil {
		return nil, ErrClientPolicyLedger
	}
	caps := api.Capabilities()
	if caps == nil || caps.ApiVersion != 1 || caps.BootId == "" || caps.InstanceId == "" {
		return nil, ErrClientPolicyLedger
	}
	for _, feature := range []string{"boot-bound-execution-grants-v1", "monotonic-grant-renewal-v1", "bounded-grant-handoff-v1"} {
		if !slices.Contains(caps.Capabilities, feature) {
			return nil, fmt.Errorf("%w: missing %s", ErrClientPolicyLedger, feature)
		}
	}
	id := journal.Identity()
	if err := api.BindAuthority(ctx, &command.AuthorityBinding{AuthorityId: id.AuthorityID, Generation: id.Generation, NodeId: nodeID}); err != nil {
		return nil, err
	}
	boot := policyauthority.NodeBoot{NodeID: nodeID, SourceID: caps.InstanceId, BootID: caps.BootId}
	if err := runSerializedTxContextForDatabase(ctx, db, func(_ *gorm.DB) error { return journal.RegisterBoot(boot) }); err != nil {
		return nil, err
	}
	return &authorityExecution{db: db, journal: journal, boot: boot, api: api, renewals: make(map[string]*command.AuthorityRenewalRequest)}, nil
}

func executionGrantFromAuthority(grant policyauthority.Grant) *command.ExecutionGrant {
	r := grant.Request
	b := r.Binding
	return &command.ExecutionGrant{Authority: &command.AuthorityBinding{AuthorityId: b.Identity.AuthorityID, Generation: b.Identity.Generation, NodeId: b.NodeBoot.NodeID}, InstanceId: b.NodeBoot.SourceID, BootId: b.NodeBoot.BootID, ClientId: b.ClientID, WindowId: b.WindowID, PolicyVersion: b.PolicyVersion, GrantId: grant.GrantID, Sequence: grant.Sequence, ChallengeId: r.ChallengeID, Capacity: r.Capacity, Upload: &command.AuthorityShare{Unlimited: r.Upload.Unlimited, Rate: r.Upload.Rate, Burst: r.Upload.Burst}, Download: &command.AuthorityShare{Unlimited: r.Download.Unlimited, Rate: r.Download.Rate, Burst: r.Download.Burst}, LeaseDurationMillis: uint64(r.LeaseDuration.Milliseconds())}
}

func (a *authorityExecution) checkChallenge(challenge *command.AuthorityChallenge) error {
	if challenge == nil || challenge.BootId != a.boot.BootID || challenge.InstanceId != a.boot.SourceID || challenge.ChallengeId == "" || challenge.MaxDurationMillis == 0 || challenge.MaxDurationMillis > uint64(policyauthority.MaxLeaseDuration.Milliseconds()) {
		return ErrClientPolicyLedger
	}
	return nil
}

func (a *authorityExecution) Authorize(ctx context.Context, intent authorityAllocation) (policyauthority.Grant, error) {
	if a == nil || ctx == nil || intent.LeaseDuration < time.Millisecond || intent.LeaseDuration > policyauthority.MaxLeaseDuration || intent.LeaseDuration%time.Millisecond != 0 {
		return policyauthority.Grant{}, policyauthority.ErrRequest
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	account, err := a.journal.Account(intent.ClientID)
	if err != nil {
		return policyauthority.Grant{}, err
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: a.journal.Identity(), NodeBoot: a.boot, ClientID: intent.ClientID, WindowID: account.Policy.WindowID, PolicyVersion: account.Policy.Version}, RequestID: intent.RequestID, Capacity: intent.Capacity, Upload: intent.Upload, Download: intent.Download, LeaseDuration: intent.LeaseDuration}
	prior, err := a.journal.LookupRequest(a.boot.NodeID, intent.ClientID, intent.RequestID)
	if err == nil {
		request.ChallengeID = prior.Request.ChallengeID
		if request != prior.Request {
			return policyauthority.Grant{}, policyauthority.ErrRequest
		}
	} else if errors.Is(err, policyauthority.ErrNotFound) {
		challenge, err := a.api.AuthorityChallenge(ctx)
		if err != nil {
			return policyauthority.Grant{}, err
		}
		if err := a.checkChallenge(challenge); err != nil {
			return policyauthority.Grant{}, err
		}
		request.ChallengeID = challenge.ChallengeId
	} else {
		return policyauthority.Grant{}, err
	}
	grant, err := issueClientPolicyAuthority(ctx, a.db, a.journal, request)
	if err != nil {
		return policyauthority.Grant{}, err
	}
	if grant.Sealed {
		return policyauthority.Grant{}, policyauthority.ErrRequest
	}
	expected := executionGrantFromAuthority(grant)
	state, err := a.api.InstallAuthorityGrant(ctx, expected)
	if err != nil {
		return policyauthority.Grant{}, err
	}
	if state == nil || state.Sealed || !proto.Equal(state.Grant, expected) {
		return policyauthority.Grant{}, ErrClientPolicyLedger
	}
	return grant, nil
}

func (a *authorityExecution) Settle(ctx context.Context, grantID string, seal, preserve bool) error {
	if a == nil || ctx == nil || preserve && !seal {
		return ErrClientPolicyLedger
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	grant, err := a.journal.Grant(grantID)
	if err != nil {
		return err
	}
	if grant.Request.Binding.NodeBoot != a.boot || grant.Request.Binding.Identity != a.journal.Identity() {
		return policyauthority.ErrIncarnation
	}
	if grant.Sealed {
		delete(a.renewals, grantID)
		return projectClientPolicyAuthority(ctx, a.db, a.journal, grant.Request.Binding.ClientID)
	}
	var state *command.ExecutionGrantState
	if preserve {
		state, err = a.api.PauseAuthorityGrant(ctx, grant.Request.Binding.ClientID, grantID)
	} else if seal {
		state, err = a.api.SealAuthorityGrant(ctx, grant.Request.Binding.ClientID, grantID)
	} else {
		state, err = a.api.GetAuthorityGrant(ctx, grant.Request.Binding.ClientID, grantID)
	}
	if err != nil {
		return err
	}
	if state == nil || state.Usage == nil || state.Sequence == 0 || !proto.Equal(state.Grant, executionGrantFromAuthority(grant)) || seal && !state.Sealed {
		return ErrClientPolicyLedger
	}
	report := policyauthority.Report{Binding: grant.Request.Binding, GrantID: grantID, Sequence: state.Sequence, Usage: policyauthority.Usage{RawUpload: state.Usage.RawUpload, RawDownload: state.Usage.RawDownload, BilledBytes: state.Usage.BilledBytes, Remainder: state.Usage.Remainder}, Seal: state.Sealed}
	return runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
		account, err := a.journal.Account(report.Binding.ClientID)
		if err != nil {
			return err
		}
		if err := validateAuthorityProjectionClient(tx, account); err != nil {
			return err
		}
		if _, err := checkedAuthorityProjection(tx, a.journal, account); err != nil {
			return err
		}
		if err := a.journal.Report(report); err != nil {
			return err
		}
		if report.Seal {
			delete(a.renewals, grantID)
		}
		return projectClientPolicyAuthorityTx(tx, a.journal, report.Binding.ClientID)
	})
}

func (a *authorityExecution) Renew(ctx context.Context, grantID string, sequence uint64, duration time.Duration) error {
	if a == nil || ctx == nil || sequence == 0 || duration < time.Millisecond || duration > policyauthority.MaxLeaseDuration || duration%time.Millisecond != 0 {
		return policyauthority.ErrRequest
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	grant, err := a.journal.Grant(grantID)
	if err != nil {
		return err
	}
	request := a.renewals[grantID]
	if request != nil && sequence <= request.Sequence {
		if sequence != request.Sequence || uint64(duration.Milliseconds()) != request.LeaseDurationMillis {
			return policyauthority.ErrRequest
		}
	} else {
		challenge, err := a.api.AuthorityChallenge(ctx)
		if err != nil {
			return err
		}
		if err := a.checkChallenge(challenge); err != nil {
			return err
		}
		request = &command.AuthorityRenewalRequest{ExpectedBootId: a.boot.BootID, ClientId: grant.Request.Binding.ClientID, GrantId: grantID, ChallengeId: challenge.ChallengeId, Sequence: sequence, LeaseDurationMillis: uint64(duration.Milliseconds())}
	}
	// Validate after obtaining the core's monotonic challenge: any in-flight
	// renewal for a retired boot was born before its retirement boundary.
	if err := runSerializedTxContextForDatabase(ctx, a.db, func(tx *gorm.DB) error {
		if err := a.journal.CheckActiveGrant(grantID, a.boot); err != nil {
			return err
		}
		account, err := a.journal.Account(grant.Request.Binding.ClientID)
		if err != nil {
			return err
		}
		if err := validateAuthorityProjectionClient(tx, account); err != nil {
			return err
		}
		_, err = checkedAuthorityProjection(tx, a.journal, account)
		return err
	}); err != nil {
		return err
	}
	// Retain the prepared challenge before sending. A lost reply must retry the
	// same monotonic deadline rather than extending it with a new challenge.
	a.renewals[grantID] = request
	return a.api.RenewAuthorityGrant(ctx, proto.Clone(request).(*command.AuthorityRenewalRequest))
}
