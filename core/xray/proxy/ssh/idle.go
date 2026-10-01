package ssh

import (
	"sync"
	"time"
)

type idleTimer struct {
	mu       sync.Mutex
	timer    *time.Timer
	duration time.Duration
	deadline time.Time
	closed   bool
	onIdle   func()
}

func newIdleTimer(duration time.Duration, onIdle func()) *idleTimer {
	t := &idleTimer{duration: duration, deadline: time.Now().Add(duration), onIdle: onIdle}
	t.timer = time.AfterFunc(duration, t.expire)
	return t
}

func (t *idleTimer) expire() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	if remaining := time.Until(t.deadline); remaining > 0 {
		t.timer.Reset(remaining)
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	t.onIdle()
}

func (t *idleTimer) Update() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.deadline = time.Now().Add(t.duration)
		t.timer.Reset(t.duration)
	}
}

func (t *idleTimer) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.timer.Stop()
}
