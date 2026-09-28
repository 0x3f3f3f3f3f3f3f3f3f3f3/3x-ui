package clientpolicy

import (
	"math"
	"sync/atomic"
	"time"
)

const maxPendingAdmissions = 128

type bucket struct {
	rate    uint64
	burst   uint64
	tokens  float64
	updated time.Time
	gate    chan struct{}
	pending atomic.Int32
}

func (b *bucket) refill(now time.Time) {
	if now.After(b.updated) {
		b.tokens = math.Min(float64(b.burst), b.tokens+now.Sub(b.updated).Seconds()*float64(b.rate))
		b.updated = now
	}
}

func (b *bucket) update(rate, burst uint64, now time.Time) {
	b.refill(now)
	if b.gate == nil {
		b.gate = make(chan struct{}, 1)
		b.tokens = float64(burst)
	}
	if b.rate == 0 && rate != 0 {
		b.tokens = float64(burst)
	}
	b.rate = rate
	b.burst = burst
	b.updated = now
	b.tokens = math.Min(b.tokens, float64(burst))
}

func (b *bucket) delay(n uint64, now time.Time) (time.Duration, error) {
	if b.rate == 0 {
		return 0, nil
	}
	if n > b.burst {
		return 0, ErrPacketTooLarge
	}
	b.refill(now)
	if b.tokens >= float64(n) {
		return 0, nil
	}
	return time.Duration(math.Ceil((float64(n) - b.tokens) / float64(b.rate) * float64(time.Second))), nil
}
