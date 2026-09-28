package sshoutbound

import (
	"net"
	"os"
	"sync"
	"time"
)

type operationDeadline struct {
	mu      sync.Mutex
	at      time.Time
	timer   *time.Timer
	active  int
	expired bool
}

func (d *operationDeadline) begin(raw net.Conn) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.expired || (!d.at.IsZero() && !time.Now().Before(d.at)) {
		return os.ErrDeadlineExceeded
	}
	d.active++
	if d.active == 1 {
		d.schedule(raw)
	}
	return nil
}

func (d *operationDeadline) end(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active--
	if d.active == 0 && d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if d.expired {
		return os.ErrDeadlineExceeded
	}
	return err
}

func (d *operationDeadline) set(t time.Time, raw net.Conn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.at = t
	d.schedule(raw)
}

func (d *operationDeadline) schedule(raw net.Conn) {
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if d.active == 0 || d.at.IsZero() || d.expired {
		return
	}
	d.timer = time.AfterFunc(time.Until(d.at), func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.active > 0 && !d.at.IsZero() && !time.Now().Before(d.at) {
			d.expired = true
			// Closing the owned transport also wakes SSH channel window waiters.
			_ = raw.Close()
		}
	})
}
