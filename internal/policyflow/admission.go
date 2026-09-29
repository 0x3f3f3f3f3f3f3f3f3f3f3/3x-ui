package policyflow

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

const (
	maxAdmissionQueue = 2 * MaxFlows
	maxAdmissionBatch = 32
	admissionWindow   = time.Millisecond
)

type admissionResult struct {
	count int
	err   error
}

type admissionRequest struct {
	ctx       context.Context
	direction Direction
	requested int
	partial   bool
	done      chan admissionResult
}

func (s *clientState) admit(ctx context.Context, direction Direction, requested int, partial bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, context.Cause(ctx)
	}
	r := &admissionRequest{ctx: ctx, direction: direction, requested: requested, partial: partial, done: make(chan admissionResult, 1)}
	s.admissionMu.Lock()
	if len(s.admissionQueue) >= maxAdmissionQueue {
		s.admissionMu.Unlock()
		return 0, clientpolicy.ErrShaperBusy
	}
	s.controller.mu.Lock()
	if s.controller.ctx.Err() != nil {
		s.controller.mu.Unlock()
		s.admissionMu.Unlock()
		return 0, ErrClosed
	}
	s.admissionQueue = append(s.admissionQueue, r)
	if !s.admissionRunning {
		s.admissionRunning = true
		// Close takes controller.mu for its snapshot before waiting for workers.
		s.controller.workers.Go(s.drainAdmissions)
	}
	s.controller.mu.Unlock()
	s.admissionMu.Unlock()
	select {
	case result := <-r.done:
		return result.count, result.err
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	case <-s.controller.ctx.Done():
		return 0, ErrClosed
	}
}

func (s *clientState) drainAdmissions() {
	for {
		s.admissionMu.Lock()
		n := min(len(s.admissionQueue), maxAdmissionBatch)
		if n == 0 {
			s.admissionRunning = false
			s.admissionMu.Unlock()
			return
		}
		if n < maxAdmissionBatch {
			s.admissionMu.Unlock()
			timer := time.NewTimer(admissionWindow)
			select {
			case <-timer.C:
			case <-s.controller.ctx.Done():
			}
			timer.Stop()
			s.admissionMu.Lock()
			n = min(len(s.admissionQueue), maxAdmissionBatch)
		}
		batch := s.admissionQueue[:n:n]
		s.admissionQueue = s.admissionQueue[n:]
		if len(s.admissionQueue) == 0 {
			s.admissionQueue = nil
		}
		s.admissionMu.Unlock()
		s.admitBatch(batch)
	}
}

func (s *clientState) admitBatch(batch []*admissionRequest) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	active := batch[:0]
	for _, r := range batch {
		if err := r.ctx.Err(); err != nil {
			r.done <- admissionResult{err: context.Cause(r.ctx)}
			continue
		}
		active = append(active, r)
	}
	if len(active) == 0 {
		return
	}
	var amounts [2]int64
	for _, r := range active {
		if r.requested <= 0 || int64(r.requested) > math.MaxInt64-amounts[r.direction] {
			s.admitIndividually(active)
			return
		}
		amounts[r.direction] += int64(r.requested)
	}
	ctx, cancel := context.WithTimeout(s.controller.ctx, operationTimeout)
	err := s.admitTotalsLocked(ctx, amounts[Upload], amounts[Download])
	cancel()
	if errors.Is(err, database.ErrUsageQuota) || errors.Is(err, clientpolicy.ErrOverflow) {
		s.admitIndividually(active)
		return
	}
	s.recordAdmissionFailure(err)
	for _, r := range active {
		result := admissionResult{err: err}
		if err == nil {
			result.count = r.requested
		}
		r.done <- result
	}
}

func (s *clientState) admitIndividually(requests []*admissionRequest) {
	for _, r := range requests {
		count, err := s.admitOneLocked(r)
		r.done <- admissionResult{count: count, err: err}
	}
}

func (s *clientState) admitOneLocked(r *admissionRequest) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, context.Cause(r.ctx)
	}
	ctx, cancel := context.WithTimeout(s.controller.ctx, operationTimeout)
	defer cancel()
	requested := r.requested
	for requested > 0 {
		var amounts [2]int64
		amounts[r.direction] = int64(requested)
		err := s.admitTotalsLocked(ctx, amounts[Upload], amounts[Download])
		if err == nil {
			return requested, nil
		}
		var quota *database.UsageQuotaError
		if r.partial && errors.As(err, &quota) && quota.RawAllowance > 0 && quota.RawAllowance < int64(requested) {
			requested = int(quota.RawAllowance)
			continue
		}
		s.recordAdmissionFailure(err)
		return 0, err
	}
	return 0, database.ErrUsageQuota
}

// Every byte in the batch remains withheld until its shared cursor is committed.
func (s *clientState) admitTotalsLocked(ctx context.Context, up, down int64) error {
	if fault := s.fault.Load(); fault != nil {
		return fault.err
	}
	if s.meter.Sequence == math.MaxInt64 || up > math.MaxInt64-s.meter.Up || down > math.MaxInt64-s.meter.Down {
		return clientpolicy.ErrOverflow
	}
	r := database.ClientUsageReport{MeterID: s.meter.ID, Sequence: s.meter.Sequence + 1, Up: s.meter.Up + up, Down: s.meter.Down + down}
	_, err := s.controller.ledger.Admit(ctx, r)
	if err == nil {
		s.meter.Sequence, s.meter.Up, s.meter.Down = r.Sequence, r.Up, r.Down
		s.checked()
		return nil
	}
	return err
}

func (s *clientState) recordAdmissionFailure(err error) {
	if err != nil && !errors.Is(err, database.ErrUsageQuota) && !errors.Is(err, database.ErrUsageDisabled) && !errors.Is(err, database.ErrUsageExpired) && !errors.Is(err, database.ErrUsageUnready) {
		s.fault.Store(&flowFault{err})
	}
}
