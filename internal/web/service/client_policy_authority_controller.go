package service

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type authorityDemandAPI interface {
	authorityCoreAPI
	EnableAuthorityRequests(context.Context, *command.AuthorityBinding) error
	ReadAuthorityRequests(context.Context, *command.AuthorityBinding, uint32) (*command.AuthorityRequests, error)
}

type controllerGrant struct {
	grantID         string
	renewalSequence uint64
}

type authorityController struct {
	mu               sync.Mutex
	execution        *authorityExecution
	api              authorityDemandAPI
	active           map[string]*controllerGrant
	pending          map[string]*command.AuthorityRequest
	suspended        map[string]bool
	retirementCursor map[string]string
	stateMu          sync.Mutex
	cancel           context.CancelFunc
	done             chan struct{}
	stopped          bool
	lastError        error
}

func (c *authorityController) Start() error {
	if c == nil {
		return ErrClientPolicyLedger
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.stopped {
		return ErrClientPolicyLedger
	}
	if c.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	go c.run(ctx, c.done)
	return nil
}

func (c *authorityController) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	for ctx.Err() == nil {
		request, requestCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		requestErr := c.ProcessRequests(request)
		requestCancel()
		settle, settleCancel := context.WithTimeout(ctx, 2*time.Second)
		settleErr := c.SettleAndRenew(settle)
		settleCancel()
		c.stateMu.Lock()
		c.lastError = errors.Join(requestErr, settleErr)
		c.stateMu.Unlock()
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (c *authorityController) join(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	c.stateMu.Lock()
	c.stopped = true
	cancel, done := c.cancel, c.done
	c.stateMu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *authorityController) Stop(ctx context.Context) error {
	if err := c.join(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	grants := make(map[string]bool)
	for _, current := range c.active {
		grants[current.grantID] = true
	}
	var result error
	for _, request := range c.pending {
		grant, err := c.execution.journal.LookupRequest(c.execution.boot.NodeID, request.ClientId, request.RequestId)
		if err != nil {
			if !errors.Is(err, policyauthority.ErrNotFound) {
				result = errors.Join(result, err)
			}
			continue
		}
		if grant.Request.Binding.NodeBoot != c.execution.boot {
			result = errors.Join(result, policyauthority.ErrIncarnation)
			continue
		}
		grants[grant.GrantID] = true
	}
	ids := make([]string, 0, len(grants))
	for id := range grants {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := c.execution.Settle(ctx, id, true, false); err != nil {
			result = errors.Join(result, err)
		}
	}
	for client, current := range c.active {
		if grant, err := c.execution.journal.Grant(current.grantID); err == nil && grant.Sealed {
			delete(c.active, client)
		}
	}
	return result
}

func (c *authorityController) checkNotStopped() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.stopped {
		return ErrClientPolicyLedger
	}
	return nil
}

func newAuthorityController(ctx context.Context, db *gorm.DB, journal *policyauthority.Journal, nodeID string, api authorityDemandAPI) (*authorityController, error) {
	if api == nil || api.Capabilities() == nil || !slices.Contains(api.Capabilities().Capabilities, "on-demand-authority-requests-v1") {
		return nil, ErrClientPolicyLedger
	}
	execution, err := newAuthorityExecution(ctx, db, journal, nodeID, api)
	if err != nil {
		return nil, err
	}
	id := journal.Identity()
	if err := api.EnableAuthorityRequests(ctx, &command.AuthorityBinding{AuthorityId: id.AuthorityID, Generation: id.Generation, NodeId: nodeID}); err != nil {
		return nil, err
	}
	return &authorityController{execution: execution, api: api, active: make(map[string]*controllerGrant), pending: make(map[string]*command.AuthorityRequest), suspended: make(map[string]bool), retirementCursor: make(map[string]string)}, nil
}

func controllerCapacity(account policyauthority.Account) uint64 {
	quota := account.Policy.QuotaBytes
	if account.Policy.QuotaUnlimited {
		quota = math.MaxInt64
	}
	left := quota
	for _, used := range []uint64{account.WindowUsed, account.FrozenBilled, account.HeldCapacity} {
		if used >= left {
			return 0
		}
		left -= used
	}
	fraction := (account.WindowRemainder + account.HeldRemainder + 999999) / 1000000
	if fraction >= left {
		return 0
	}
	return min(left-fraction, 2<<20)
}
func (c *authorityController) ProcessRequests(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	c.mu.Lock()
	pending := &command.AuthorityRequests{InstanceId: c.execution.boot.SourceID, BootId: c.execution.boot.BootID}
	keys := make([]string, 0, len(c.pending))
	for id := range c.pending {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	for _, id := range keys {
		pending.Requests = append(pending.Requests, proto.Clone(c.pending[id]).(*command.AuthorityRequest))
	}
	c.mu.Unlock()
	pendingErr := c.HandleRequests(ctx, pending)
	if err := ctx.Err(); err != nil {
		return errors.Join(pendingErr, err)
	}
	id := c.execution.journal.Identity()
	page, err := c.api.ReadAuthorityRequests(ctx, &command.AuthorityBinding{AuthorityId: id.AuthorityID, Generation: id.Generation, NodeId: c.execution.boot.NodeID}, 128)
	if err != nil {
		return errors.Join(pendingErr, err)
	}
	return errors.Join(pendingErr, c.HandleRequests(ctx, page))
}
func (c *authorityController) HandleRequests(ctx context.Context, page *command.AuthorityRequests) error {
	if c == nil || ctx == nil || page == nil || page.InstanceId != c.execution.boot.SourceID || page.BootId != c.execution.boot.BootID || len(page.Requests) > 128 {
		return ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	clients, ids := make(map[string]bool), make(map[string]bool)
	for _, request := range page.Requests {
		if request == nil || request.ClientId == "" || request.PolicyVersion == 0 || clients[request.ClientId] || ids[request.RequestId] {
			return ErrClientPolicyLedger
		}
		nonce, err := hex.DecodeString(request.RequestId)
		if err != nil || len(nonce) != 16 {
			return ErrClientPolicyLedger
		}
		clients[request.ClientId], ids[request.RequestId] = true, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkNotStopped(); err != nil {
		return err
	}
	var result error
	for _, request := range page.Requests {
		result = errors.Join(result, c.handleRequestLocked(ctx, request))
	}
	return result
}

func (c *authorityController) handleRequestLocked(ctx context.Context, r *command.AuthorityRequest) error {
	if r == nil || len(r.RequestId) != 32 || r.ClientId == "" || r.PolicyVersion == 0 {
		return ErrClientPolicyLedger
	}
	if c.suspended[r.ClientId] {
		return policyauthority.ErrRequest
	}
	account, err := c.execution.journal.Account(r.ClientId)
	if err != nil {
		return err
	}
	if account.Deleted || account.Policy.Version != r.PolicyVersion {
		// An unissued obsolete nonce holds no budget. Issued lost replies stay
		// tracked until an exact seal/settlement or conservative stopped close.
		if c.pending[r.RequestId] != nil {
			if _, err := c.execution.journal.LookupRequest(c.execution.boot.NodeID, r.ClientId, r.RequestId); errors.Is(err, policyauthority.ErrNotFound) {
				delete(c.pending, r.RequestId)
			}
		}
		return policyauthority.ErrRequest
	}
	prior, err := c.execution.journal.LookupRequest(c.execution.boot.NodeID, r.ClientId, r.RequestId)
	var intent authorityAllocation
	if err == nil {
		if prior.Sealed || prior.Request.Binding.NodeBoot != c.execution.boot || prior.Request.Binding.PolicyVersion != r.PolicyVersion {
			return policyauthority.ErrRequest
		}
		intent = authorityAllocation{ClientID: r.ClientId, RequestID: r.RequestId, Capacity: prior.Request.Capacity, Upload: prior.Request.Upload, Download: prior.Request.Download, LeaseDuration: prior.Request.LeaseDuration}
	} else if errors.Is(err, policyauthority.ErrNotFound) {
		if r.PreviousGrantId != "" {
			previous, err := c.execution.journal.Grant(r.PreviousGrantId)
			if err != nil {
				return err
			}
			if previous.Request.Binding.NodeBoot != c.execution.boot || previous.Request.Binding.ClientID != r.ClientId {
				return policyauthority.ErrRequest
			}
			if err := c.rememberPendingLocked(r); err != nil {
				return err
			}
			if err := c.execution.Settle(ctx, previous.GrantID, true, true); err != nil {
				return err
			}
			delete(c.active, r.ClientId)
			account, err = c.execution.journal.Account(r.ClientId)
			if err != nil {
				return err
			}
		}
		if c.active[r.ClientId] == nil && limitedAuthorityRatesHeld(account) {
			if err := c.reconcileRetiredClientRatesLocked(ctx, r.ClientId); err != nil {
				return err
			}
			account, err = c.execution.journal.Account(r.ClientId)
			if err != nil {
				return err
			}
		}
		capacity := controllerCapacity(account)
		if capacity == 0 {
			return policyauthority.ErrCapacity
		}
		intent = authorityAllocation{ClientID: r.ClientId, RequestID: r.RequestId, Capacity: capacity, Upload: account.Policy.Upload, Download: account.Policy.Download, LeaseDuration: policyauthority.MaxLeaseDuration}
	} else {
		return err
	}
	if err := c.rememberPendingLocked(r); err != nil {
		return err
	}
	grant, err := c.execution.Authorize(ctx, intent)
	if err != nil {
		return err
	}
	if current := c.active[r.ClientId]; current == nil || current.grantID != grant.GrantID {
		c.active[r.ClientId] = &controllerGrant{grantID: grant.GrantID}
	}
	delete(c.pending, r.RequestId)
	return nil
}

func (c *authorityController) rememberPendingLocked(request *command.AuthorityRequest) error {
	if prior := c.pending[request.RequestId]; prior != nil {
		if !proto.Equal(prior, request) {
			return policyauthority.ErrRequest
		}
		return nil
	}
	if len(c.pending) >= 128 {
		return policyauthority.ErrCapacity
	}
	c.pending[request.RequestId] = proto.Clone(request).(*command.AuthorityRequest)
	return nil
}
func (c *authorityController) SettleAndRenew(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkNotStopped(); err != nil {
		return err
	}
	clients := make([]string, 0, len(c.active))
	for id := range c.active {
		clients = append(clients, id)
	}
	slices.Sort(clients)
	var result error
	for _, id := range clients {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		result = errors.Join(result, c.settleAndRenewLocked(ctx, id, c.active[id]))
	}
	return result
}

func (c *authorityController) settleAndRenewLocked(ctx context.Context, id string, current *controllerGrant) error {
	if err := c.execution.Settle(ctx, current.grantID, false, false); err != nil {
		return err
	}
	grant, err := c.execution.journal.Grant(current.grantID)
	if err != nil {
		return err
	}
	if grant.Sealed {
		delete(c.active, id)
		return nil
	}
	if current.renewalSequence >= math.MaxInt64 {
		return policyauthority.ErrRequest
	}
	if err := c.execution.Renew(ctx, current.grantID, current.renewalSequence+1, policyauthority.MaxLeaseDuration); err != nil {
		return err
	}
	current.renewalSequence++
	return nil
}
