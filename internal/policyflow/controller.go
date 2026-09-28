// Package policyflow owns authenticated clients' shared stream budgets and active connections.
package policyflow

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/clientpolicy"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const (
	BufferSize       = 32 << 10
	MaxFlows         = 128
	checkInterval    = 250 * time.Millisecond
	operationTimeout = 500 * time.Millisecond
	policyLifetime   = time.Second
)

var (
	ErrNotConfigured     = errors.New("client data-path policy is not configured")
	ErrClosed            = errors.New("client data-path controller is closed")
	ErrTooManyFlows      = errors.New("client active-flow limit reached")
	ErrPolicyUnavailable = errors.New("client policy could not be verified within its lifetime")
)

type Direction uint8

const (
	Upload Direction = iota
	Download
)

type Rates struct{ Upload, Download int64 }

type Controller struct {
	ledger      *database.ClientUsageLedger
	source      string
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	configureMu sync.Mutex
	clients     map[string]*clientState
	workers     sync.WaitGroup
}

type flowFault struct{ err error }

type clientState struct {
	controller       *Controller
	policyID         string
	meter            model.ClientUsageMeter
	opMu             sync.Mutex
	upload, download *clientpolicy.Limiter
	flowsMu          sync.Mutex
	flows            map[*Flow]struct{}
	lastGood         atomic.Pointer[time.Time]
	checking         atomic.Bool
	fault            atomic.Pointer[flowFault]
}

func NewController(ledger *database.ClientUsageLedger, source string) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{ledger: ledger, source: source, ctx: ctx, cancel: cancel, clients: make(map[string]*clientState)}
	c.workers.Go(c.watch)
	return c
}

func (c *Controller) Configure(ctx context.Context, policyID string, rates Rates) error {
	up, err := clientpolicy.NewLimiter(rates.Upload)
	if err != nil {
		return err
	}
	down, err := clientpolicy.NewLimiter(rates.Download)
	if err != nil {
		return err
	}
	c.configureMu.Lock()
	defer c.configureMu.Unlock()
	c.mu.Lock()
	old := c.clients[policyID]
	closed := c.ctx.Err() != nil
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if old != nil && old.fault.Load() == nil {
		if err := old.upload.SetRate(rates.Upload); err != nil {
			return err
		}
		return old.download.SetRate(rates.Download)
	}
	meter, err := c.ledger.ClaimAdmissionSource(ctx, policyID, c.source)
	if err != nil {
		return err
	}
	s := &clientState{controller: c, policyID: policyID, meter: meter, upload: up, download: down, flows: make(map[*Flow]struct{})}
	s.checked()
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.clients[policyID] = s
	c.mu.Unlock()
	if old != nil {
		old.closeFlows(ErrClosed)
	}
	return nil
}

func (c *Controller) Open(ctx context.Context, policyID string, closers ...io.Closer) (*Flow, error) {
	c.mu.Lock()
	s := c.clients[policyID]
	c.mu.Unlock()
	if s == nil {
		return nil, ErrNotConfigured
	}
	if fault := s.fault.Load(); fault != nil {
		return nil, fault.err
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	flowCtx, flowCancel := context.WithCancelCause(ctx)
	f := &Flow{state: s, ctx: flowCtx, cancel: flowCancel, closers: closers}
	s.flowsMu.Lock()
	if c.ctx.Err() != nil || ctx.Err() != nil {
		s.flowsMu.Unlock()
		flowCancel(ErrClosed)
		return nil, ErrClosed
	}
	if len(s.flows) >= MaxFlows {
		s.flowsMu.Unlock()
		flowCancel(ErrTooManyFlows)
		return nil, ErrTooManyFlows
	}
	s.flows[f] = struct{}{}
	s.flowsMu.Unlock()
	go func() {
		select {
		case <-c.ctx.Done():
			f.closeWithCause(ErrClosed)
		case <-flowCtx.Done():
			f.closeWithCause(context.Cause(flowCtx))
		}
	}()
	return f, nil
}

func (c *Controller) Close() {
	c.cancel()
	for _, s := range c.snapshot() {
		s.closeFlows(ErrClosed)
	}
	c.workers.Wait()
}

func (c *Controller) snapshot() []*clientState {
	c.mu.Lock()
	defer c.mu.Unlock()
	clients := make([]*clientState, 0, len(c.clients))
	for _, s := range c.clients {
		clients = append(clients, s)
	}
	return clients
}

func (c *Controller) watch() {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			for _, s := range c.snapshot() {
				age := now.Sub(*s.lastGood.Load())
				if age >= policyLifetime {
					s.closeFlows(ErrPolicyUnavailable)
				}
				if age < checkInterval {
					continue
				}
				if !s.checking.CompareAndSwap(false, true) {
					continue
				}
				c.workers.Go(func() {
					defer s.checking.Store(false)
					if err := s.check(c.ctx); err != nil {
						s.closeFlows(err)
					}
				})
			}
		}
	}
}

func (s *clientState) checked() {
	now := time.Now()
	s.lastGood.Store(&now)
}

func (s *clientState) check(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	err := s.controller.ledger.CheckAdmissionSource(ctx, s.meter.ID)
	if err == nil {
		s.checked()
	} else if errors.Is(err, database.ErrUsageClosed) {
		s.fault.Store(&flowFault{err})
	}
	return err
}

func (s *clientState) closeFlows(cause error) {
	s.flowsMu.Lock()
	flows := make([]*Flow, 0, len(s.flows))
	for f := range s.flows {
		flows = append(flows, f)
	}
	s.flowsMu.Unlock()
	for _, f := range flows {
		f.closeWithCause(cause)
	}
}

func (s *clientState) admit(ctx context.Context, direction Direction, requested int) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, context.Cause(ctx)
	}
	if fault := s.fault.Load(); fault != nil {
		return 0, fault.err
	}
	// Finish each committed cursor update even when its originating stream is cancelled.
	ctx, cancel := context.WithTimeout(s.controller.ctx, operationTimeout)
	defer cancel()
	for requested > 0 {
		if s.meter.Sequence == math.MaxInt64 {
			return 0, clientpolicy.ErrOverflow
		}
		r := database.ClientUsageReport{MeterID: s.meter.ID, Sequence: s.meter.Sequence + 1, Up: s.meter.Up, Down: s.meter.Down}
		counter := &r.Up
		if direction == Download {
			counter = &r.Down
		}
		if int64(requested) > math.MaxInt64-*counter {
			return 0, clientpolicy.ErrOverflow
		}
		*counter += int64(requested)
		_, err := s.controller.ledger.Admit(ctx, r)
		if err == nil {
			s.meter.Sequence, s.meter.Up, s.meter.Down = r.Sequence, r.Up, r.Down
			s.checked()
			return requested, nil
		}
		var quota *database.UsageQuotaError
		if errors.As(err, &quota) && quota.RawAllowance > 0 && quota.RawAllowance < int64(requested) {
			requested = int(quota.RawAllowance)
			continue
		}
		if !errors.Is(err, database.ErrUsageQuota) && !errors.Is(err, database.ErrUsageDisabled) && !errors.Is(err, database.ErrUsageExpired) && !errors.Is(err, database.ErrUsageUnready) {
			s.fault.Store(&flowFault{err})
		}
		return 0, err
	}
	return 0, database.ErrUsageQuota
}

type Flow struct {
	state   *clientState
	ctx     context.Context
	cancel  context.CancelCauseFunc
	once    sync.Once
	mu      sync.Mutex
	closers []io.Closer
	closed  bool
}

func (f *Flow) Context() context.Context { return f.ctx }
func (f *Flow) Close()                   { f.closeWithCause(context.Canceled) }

func (f *Flow) closeWithCause(cause error) {
	f.once.Do(func() {
		f.cancel(cause)
		f.mu.Lock()
		f.closed = true
		closers := f.closers
		f.closers = nil
		f.mu.Unlock()
		for _, closer := range closers {
			_ = closer.Close()
		}
		f.state.flowsMu.Lock()
		delete(f.state.flows, f)
		f.state.flowsMu.Unlock()
	})
}

func (f *Flow) bind(closer io.Closer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = closer.Close()
		return context.Cause(f.ctx)
	}
	f.closers = append(f.closers, closer)
	return nil
}

func (f *Flow) Writer(direction Direction, destination io.Writer) io.Writer {
	return &flowWriter{flow: f, direction: direction, destination: destination}
}

type flowWriter struct {
	flow        *Flow
	direction   Direction
	destination io.Writer
}

func (w *flowWriter) Write(p []byte) (int, error) {
	if w.direction != Upload && w.direction != Download {
		return 0, clientpolicy.ErrInvalidGrant
	}
	s := w.flow.state
	limiter := s.upload
	if w.direction == Download {
		limiter = s.download
	}
	written := 0
	for len(p) > 0 {
		grant, err := limiter.Acquire(w.flow.ctx, min(len(p), BufferSize))
		if err != nil {
			return written, err
		}
		grant, err = s.admit(w.flow.ctx, w.direction, grant)
		if err != nil {
			if w.flow.ctx.Err() == nil || s.fault.Load() != nil {
				s.closeFlows(err)
			}
			return written, err
		}
		if err := w.flow.ctx.Err(); err != nil {
			return written, context.Cause(w.flow.ctx)
		}
		n, err := w.destination.Write(p[:grant])
		written += n
		if err != nil {
			return written, err
		}
		if n != grant {
			return written, io.ErrShortWrite
		}
		p = p[n:]
	}
	return written, nil
}

func (c *Controller) Proxy(ctx context.Context, policyID string, client io.ReadWriteCloser, dial func(context.Context) (io.ReadWriteCloser, error)) error {
	f, err := c.Open(ctx, policyID, client)
	if err != nil {
		_ = client.Close()
		return err
	}
	defer f.Close()
	upstream, err := dial(f.ctx)
	if err != nil {
		return err
	}
	if err := f.bind(upstream); err != nil {
		return err
	}
	results := make(chan error, 2)
	copyStream := func(dst io.ReadWriteCloser, src io.Reader, direction Direction) {
		_, err := io.CopyBuffer(f.Writer(direction, dst), src, make([]byte, BufferSize))
		if closer, ok := dst.(interface{ CloseWrite() error }); ok && err == nil {
			_ = closer.CloseWrite()
		}
		if err != nil {
			f.closeWithCause(err)
		}
		results <- err
	}
	go copyStream(upstream, client, Upload)
	go copyStream(client, upstream, Download)
	first, second := <-results, <-results
	if cause := context.Cause(f.ctx); cause != nil {
		return cause
	}
	if first != nil {
		return first
	}
	return second
}
