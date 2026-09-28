package clientpolicy

import (
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"sync"
	"time"
)

const (
	MaxRate    int64 = 1 << 40
	MaxGrant         = 64 << 10
	maxWaiters       = 128
)

var (
	ErrInvalidRate  = errors.New("rate must be between 0 and 1099511627776 raw bytes per second")
	ErrInvalidGrant = errors.New("requested byte grant must be positive")
	ErrShaperBusy   = errors.New("client rate queue is full")
)

type rateWaiter struct {
	requested int
	datagram  bool
}

// One Limiter belongs to one client's direction, shared by every connection and listener.
type Limiter struct {
	mu      sync.Mutex
	rate    int64
	burst   int
	tokens  float64
	last    time.Time
	changed chan struct{}
	waiters []*rateWaiter
}

func NewLimiter(rate int64) (*Limiter, error) {
	if rate < 0 || rate > MaxRate {
		return nil, ErrInvalidRate
	}
	burst := rateBurst(rate)
	return &Limiter{rate: rate, burst: burst, tokens: float64(burst), last: time.Now(), changed: make(chan struct{})}, nil
}

func (l *Limiter) SetRate(rate int64) error {
	if rate < 0 || rate > MaxRate {
		return ErrInvalidRate
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill(time.Now())
	l.rate, l.burst = rate, rateBurst(rate)
	l.tokens = min(l.tokens, float64(l.burst))
	l.wake()
	return nil
}

// Acquire returns a bounded grant; callers forward at most that many bytes before requesting more.
func (l *Limiter) Acquire(ctx context.Context, requested int) (int, error) {
	return l.acquire(ctx, requested, false)
}

// AcquireDatagram keeps packet boundaries while sharing bounded token debt with streams.
func (l *Limiter) AcquireDatagram(ctx context.Context, requested int) (int, error) {
	if requested > MaxGrant {
		return 0, ErrInvalidGrant
	}
	return l.acquire(ctx, requested, true)
}

func (l *Limiter) acquire(ctx context.Context, requested int, datagram bool) (int, error) {
	if requested <= 0 {
		return 0, ErrInvalidGrant
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(l.waiters) >= maxWaiters {
		return 0, ErrShaperBusy
	}
	w := &rateWaiter{requested: min(requested, MaxGrant), datagram: datagram}
	l.waiters = append(l.waiters, w)
	for {
		if err := ctx.Err(); err != nil {
			index := slices.Index(l.waiters, w)
			l.waiters = slices.Delete(l.waiters, index, index+1)
			l.wake()
			return 0, err
		}
		l.refill(time.Now())
		var delay time.Duration
		if l.waiters[0] == w {
			grant := min(w.requested, l.burst)
			if l.rate == 0 || l.tokens >= float64(grant) {
				if w.datagram {
					grant = w.requested
				}
				if l.rate > 0 {
					l.tokens -= float64(grant)
				}
				l.waiters = slices.Delete(l.waiters, 0, 1)
				l.wake()
				return grant, nil
			}
			delay = time.Duration(math.Ceil((float64(grant) - l.tokens) / float64(l.rate) * float64(time.Second)))
		}
		changed := l.changed
		l.mu.Unlock()
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
			case <-changed:
			case <-timer.C:
			}
			timer.Stop()
		} else {
			select {
			case <-ctx.Done():
			case <-changed:
			}
		}
		l.mu.Lock()
	}
}

func (l *Limiter) refill(now time.Time) {
	if l.rate == 0 {
		l.tokens = float64(l.burst)
	} else {
		l.tokens = min(float64(l.burst), l.tokens+now.Sub(l.last).Seconds()*float64(l.rate))
	}
	l.last = now
}

func (l *Limiter) wake() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func rateBurst(rate int64) int {
	if rate == 0 {
		return MaxGrant
	}
	return int(min(int64(MaxGrant), max(int64(1), rate/10)))
}

type ShapedWriter struct {
	ctx     context.Context
	writer  io.Writer
	limiter *Limiter
}

func NewShapedWriter(ctx context.Context, writer io.Writer, limiter *Limiter) *ShapedWriter {
	return &ShapedWriter{ctx: ctx, writer: writer, limiter: limiter}
}

func (w *ShapedWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		grant, err := w.limiter.Acquire(w.ctx, len(p))
		if err != nil {
			return written, err
		}
		n, err := w.writer.Write(p[:grant])
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
